package feeds

// AIS vessel positions from aisstream.io. The service is free but requires
// an API key from an OAuth signup on their site, so without a key this
// source stays disabled and the UI says what is missing.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type AIS struct {
	URL  string // default wss://stream.aisstream.io/v0/stream
	Key  string // API key; empty = disabled
	BBox [4]float64
	Now  func() time.Time
	dial func(ctx context.Context, url string) (aisConn, error)
}

type aisConn interface {
	WriteJSON(v any) error
	ReadMessage() (int, []byte, error)
	Close() error
}

func (a *AIS) Name() string { return "aisstream" }
func (a *AIS) Kind() string { return "ais" }

func (a *AIS) url() string {
	if a.URL != "" {
		return a.URL
	}
	return "wss://stream.aisstream.io/v0/stream"
}

func (a *AIS) bbox() [4]float64 {
	if a.BBox != [4]float64{} {
		return a.BBox
	}
	return [4]float64{27, 30, 37, 40} // Eastern Mediterranean
}

func (a *AIS) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// subscribeMessage is the first frame aisstream.io expects.
type aisSubscribe struct {
	APIKey             string         `json:"APIKey"`
	BoundingBoxes      [][][2]float64 `json:"BoundingBoxes"`
	FilterMessageTypes []string       `json:"FilterMessageTypes,omitempty"`
}

func (a *AIS) subscribe() aisSubscribe {
	b := a.bbox()
	return aisSubscribe{
		APIKey:             a.Key,
		BoundingBoxes:      [][][2]float64{{{b[0], b[1]}, {b[2], b[3]}}},
		FilterMessageTypes: []string{"PositionReport", "ShipStaticData"},
	}
}

type aisMessage struct {
	MessageType string `json:"MessageType"`
	MetaData    struct {
		MMSI     int64   `json:"MMSI"`
		ShipName string  `json:"ShipName"`
		Lat      float64 `json:"latitude"`
		Lon      float64 `json:"longitude"`
		TimeUTC  string  `json:"time_utc"`
	} `json:"MetaData"`
	Message json.RawMessage `json:"Message"`
}

// ParseAIS converts one aisstream.io frame into an Item.
func ParseAIS(payload []byte, now time.Time) (Item, bool) {
	var m aisMessage
	if err := json.Unmarshal(payload, &m); err != nil || m.MetaData.MMSI == 0 {
		return Item{}, false
	}
	label := strings.TrimSpace(m.MetaData.ShipName)
	if label == "" {
		label = fmt.Sprintf("MMSI %d", m.MetaData.MMSI)
	}
	when := now
	if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", m.MetaData.TimeUTC); err == nil {
		when = t
	}
	it := Item{
		ID:    fmt.Sprintf("ais|%d", m.MetaData.MMSI),
		Label: label,
		Time:  when,
		Ref:   fmt.Sprintf("https://www.vesselfinder.com/vessels/details/%d", m.MetaData.MMSI),
	}
	var detail []string
	if m.MetaData.Lat != 0 || m.MetaData.Lon != 0 {
		it.Lat, it.Lon, it.HasPos = m.MetaData.Lat, m.MetaData.Lon, true
	}
	if m.MessageType == "PositionReport" {
		var pr struct {
			PositionReport struct {
				Sog float64 `json:"Sog"`
				Cog float64 `json:"Cog"`
			} `json:"PositionReport"`
		}
		if json.Unmarshal(m.Message, &pr) == nil {
			detail = append(detail, fmt.Sprintf("%.1f kt", pr.PositionReport.Sog))
			detail = append(detail, fmt.Sprintf("course %.0f°", pr.PositionReport.Cog))
		}
	} else {
		detail = append(detail, strings.ToLower(m.MessageType))
	}
	it.Detail = strings.Join(detail, " · ")
	return it, true
}

func (a *AIS) Run(ctx context.Context, emit func(Item)) error {
	if a.Key == "" {
		return fmt.Errorf("aisstream.io needs an API key (free OAuth signup at aisstream.io)")
	}
	dial := a.dial
	if dial == nil {
		dial = func(ctx context.Context, url string) (aisConn, error) {
			c, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
			return c, err
		}
	}
	conn, err := dial(ctx, a.url())
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.WriteJSON(a.subscribe()); err != nil {
		return err
	}
	go func() { <-ctx.Done(); conn.Close() }()
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if it, ok := ParseAIS(payload, a.now()); ok {
			emit(it)
		}
	}
}
