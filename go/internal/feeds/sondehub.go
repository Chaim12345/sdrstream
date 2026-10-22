package feeds

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// SondeHub bootstraps history once, then uses the provider's MQTT WebSocket
// stream. The API explicitly forbids regular telemetry polling.
type SondeHub struct {
	Bootstrap      *PublicJSON
	BrokerEndpoint string
	Client         *http.Client
	once           sync.Once
}

func (s *SondeHub) Name() string { return "sondehub" }
func (s *SondeHub) Kind() string { return "radiosonde" }
func (s *SondeHub) Run(ctx context.Context, emit func(Item)) error {
	return s.RunReady(ctx, emit, func() {})
}
func (s *SondeHub) RunReady(ctx context.Context, emit func(Item), ready func()) error {
	s.once.Do(func() {
		p := s.Bootstrap
		if p == nil {
			p = &PublicJSON{Source: "sondehub", Client: s.Client}
		}
		if items, err := p.Poll(ctx); err == nil {
			for _, it := range items {
				emit(it)
			}
		}
	})
	endpoint := s.BrokerEndpoint
	if endpoint == "" {
		endpoint = "https://api.v2.sondehub.org/sondes/websocket"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "sdrstream/1.0")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("sondehub broker discovery: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16384))
	if err != nil {
		return err
	}
	broker := strings.TrimSpace(string(b))
	u, err := url.Parse(broker)
	if err != nil || u.Scheme != "wss" {
		return fmt.Errorf("sondehub: invalid secure websocket URL")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second, Subprotocols: []string{"mqtt"}}
	ws, _, err := dialer.DialContext(ctx, broker, nil)
	if err != nil {
		return fmt.Errorf("sondehub websocket connection failed")
	}
	defer ws.Close()
	ws.SetReadLimit(4 << 20)
	adapter := &mqttWebSocket{Conn: ws.UnderlyingConn(), ws: ws}
	c := &mqttClient{conn: adapter, r: bufio.NewReader(adapter)}
	// Closing on cancellation unblocks any read, including the handshake.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ws.Close()
		case <-done:
		}
	}()
	ws.SetReadDeadline(time.Now().Add(25 * time.Second))
	id := fmt.Sprintf("sdrstream-%d", time.Now().UnixNano())
	pkt := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 2, 0, 60, 0, byte(len(id))}
	pkt = append(pkt, id...)
	if err := c.write(0x10, pkt); err != nil {
		return err
	}
	typ, payload, err := c.read()
	if err != nil {
		return err
	}
	if typ != 0x20 || len(payload) != 2 || payload[1] != 0 {
		return fmt.Errorf("sondehub: MQTT CONNECT refused")
	}
	if err := c.subscribe("sondes/#"); err != nil {
		return err
	}
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ready()
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		t := time.NewTicker(40 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if c.ping() != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	for {
		_, payload, err := c.nextPublish()
		if err != nil {
			return err
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		for _, it := range parseSondeMessage(payload) {
			emit(it)
		}
	}
}

// MQTT uses a byte stream, while the broker carries it in binary WebSocket
// messages. Only the MQTT reader goroutine reads, and only keepalive writes
// after subscription, satisfying gorilla's one-reader/one-writer rule.
type mqttWebSocket struct {
	net.Conn
	ws     *websocket.Conn
	reader io.Reader
}

func (w *mqttWebSocket) Read(p []byte) (int, error) {
	for {
		if w.reader == nil {
			_, r, e := w.ws.NextReader()
			if e != nil {
				return 0, e
			}
			w.reader = r
		}
		n, e := w.reader.Read(p)
		if e == io.EOF {
			w.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, e
	}
}
func (w *mqttWebSocket) Write(p []byte) (int, error) {
	if e := w.ws.WriteMessage(websocket.BinaryMessage, p); e != nil {
		return 0, e
	}
	return len(p), nil
}
func (w *mqttWebSocket) SetWriteDeadline(t time.Time) error { return w.ws.SetWriteDeadline(t) }
func parseSondeMessage(b []byte) []Item {
	var rows []json.RawMessage
	if json.Unmarshal(b, &rows) != nil {
		rows = []json.RawMessage{b}
	}
	var out []Item
	for _, row := range rows {
		var r struct {
			Serial string `json:"serial"`
			Time   string `json:"datetime"`
		}
		if json.Unmarshal(row, &r) != nil || r.Serial == "" {
			continue
		}
		wrapped := map[string]map[string]json.RawMessage{r.Serial: {r.Time: row}}
		j, _ := json.Marshal(wrapped)
		items, e := (&PublicJSON{Source: "sondehub"}).parse(j)
		if e == nil {
			out = append(out, items...)
		}
	}
	return out
}
