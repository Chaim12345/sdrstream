package feeds

// ACARS, VDL2, HFDL and L-band SATCOM messages from the airframes.io
// community network. The public REST endpoints work anonymously (an API key
// only raises rate limits) and the realtime socket.io firehose offers a
// sampled global stream without credentials. Polling stays polite: the
// default interval is 60 s.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// AirframesMessage is one message object from the airframes.io API.
type AirframesMessage struct {
	ID           int64   `json:"id"`
	Source       string  `json:"source"`     // dumpvdl2, dumphfdl, acarsdec, vdlm2dec, jaero-...
	SourceType   string  `json:"sourceType"` // acars, vdl, hfdl, ...
	Timestamp    string  `json:"timestamp"`
	Tail         string  `json:"tail"`
	FlightNumber string  `json:"flightNumber"`
	Frequency    float64 `json:"frequency"`
	Mode         string  `json:"mode"`
	Label        string  `json:"label"`
	BlockID      string  `json:"blockId"`
	Text         string  `json:"text"`
	StationIdent string  `json:"-"`
}

// Airframes reads recent messages and/or the live firehose.
type Airframes struct {
	Base   string // default https://api.airframes.io
	Client *http.Client
	Every  time.Duration // REST poll interval; default 60 s (polite)
	Limit  int           // messages per REST poll; default 25
}

func (a *Airframes) Name() string { return "airframes" }
func (a *Airframes) Kind() string { return "acars" }

func (a *Airframes) base() string {
	if a.Base != "" {
		return strings.TrimRight(a.Base, "/")
	}
	return "https://api.airframes.io"
}

func (a *Airframes) Interval() time.Duration {
	if a.Every > 0 {
		return a.Every
	}
	return 60 * time.Second
}

// FetchRecent returns the newest messages from the public REST endpoint.
func (a *Airframes) FetchRecent(ctx context.Context) ([]AirframesMessage, error) {
	hc := a.Client
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 25
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/v1/messages?limit=%d", a.base(), limit), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream-research (github.com/Chaim12345/sdrstream)")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("airframes: HTTP %d", resp.StatusCode)
	}
	var raw []struct {
		AirframesMessage
		Station struct {
			Ident string `json:"ident"`
		} `json:"station"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]AirframesMessage, 0, len(raw))
	for _, m := range raw {
		m.StationIdent = m.Station.Ident
		out = append(out, m.AirframesMessage)
	}
	return out, nil
}

// Poll implements Poller over the REST endpoint.
func (a *Airframes) Poll(ctx context.Context) ([]Item, error) {
	msgs, err := a.FetchRecent(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(msgs))
	for _, m := range msgs {
		t, _ := time.Parse(time.RFC3339Nano, m.Timestamp)
		items = append(items, Item{
			ID:     fmt.Sprintf("airframes:%d", m.ID),
			Source: a.Name(),
			Kind:   a.Kind(),
			Time:   t,
			Label:  fmt.Sprintf("%s %s", m.SourceType, m.FlightNumber),
			Detail: fmt.Sprintf("label=%s %s", m.Label, firstLine(m.Text)),
		})
	}
	return items, nil
}

// RunSniff connects to the anonymous global firehose over socket.io and
// streams messages until ctx is cancelled. Each message is passed to onMsg.
func (a *Airframes) RunSniff(ctx context.Context, onMsg func(AirframesMessage)) error {
	url := "wss://ws.airframes.io/socket.io/?EIO=4&transport=websocket"
	c, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("airframes ws dial: %w", err)
	}
	defer c.Close()
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	// Engine.IO open packet ("0{...}") arrives first; answer with the
	// socket.io connect packet "40", then join the global firehose.
	connected := false
	joined := false
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return err
		}
		s := string(data)
		switch {
		case strings.HasPrefix(s, "0{"):
			if err := c.WriteMessage(websocket.TextMessage, []byte("40")); err != nil {
				return err
			}
		case strings.HasPrefix(s, "40"):
			connected = true
			if !joined {
				if err := c.WriteMessage(websocket.TextMessage, []byte(`42["messages:sniff"]`)); err != nil {
					return err
				}
				joined = true
			}
		case s == "2": // engine.io ping
			if err := c.WriteMessage(websocket.TextMessage, []byte("3")); err != nil {
				return err
			}
		case strings.HasPrefix(s, "42"):
			if connected && !joined {
				if err := c.WriteMessage(websocket.TextMessage, []byte(`42["messages:sniff"]`)); err != nil {
					return err
				}
				joined = true
			}
			var ev []json.RawMessage
			if json.Unmarshal([]byte(s[2:]), &ev) != nil || len(ev) < 2 {
				continue
			}
			var name string
			if json.Unmarshal(ev[0], &name) != nil || name != "message" {
				continue
			}
			var m AirframesMessage
			if json.Unmarshal(ev[1], &m) == nil && onMsg != nil {
				onMsg(m)
			}
		}
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
