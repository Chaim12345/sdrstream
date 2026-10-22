package feeds

// Raw AIS NMEA sentences from openwaters.io, an open community AIS network.
// Anonymous read access is allowed ("read without an account"); every event
// carries the raw NMEA sentence, the decoded fields and the upstream source
// attribution. Data is re-served under each upstream source's own terms,
// which the event names, so we keep the attribution on every item.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// AISRawSentence is one raw AIS NMEA sentence event.
type AISRawSentence struct {
	Sentence    string    `json:"sentence"`
	Source      string    `json:"source"`      // upstream network, e.g. aishub
	Station     string    `json:"station"`     // receiving station id
	Attribution string    `json:"attribution"` // required upstream attribution
	License     string    `json:"license"`
	Synthesized bool      `json:"synthesized"` // sentence regenerated upstream from decoded data
	Time        time.Time `json:"time"`
}

type openwatersEvent struct {
	Type        string   `json:"type"`
	NMEA        []string `json:"nmea"`
	Source      string   `json:"source"`
	Station     string   `json:"station"`
	Attribution string   `json:"attribution"`
	License     string   `json:"license"`
	Synthesized bool     `json:"synthesized"`
	Time        string   `json:"time"`
}

// OpenWaters streams raw AIS NMEA sentences from openwaters.io.
type OpenWaters struct {
	URL   string     // default wss://ais.openwaters.io/v1/stream
	Token string     // optional personal token; empty = anonymous
	BBox  [4]float64 // lat1, lon1, lat2, lon2; zero = Eastern Mediterranean
	Dial  func(ctx context.Context, url string) (*websocket.Conn, error)
	// OnSentence, when set, receives every raw sentence event in addition
	// to the Item stream.
	OnSentence func(AISRawSentence)
}

func (o *OpenWaters) Name() string { return "openwaters-ais-raw" }
func (o *OpenWaters) Kind() string { return "ais-raw" }

func (o *OpenWaters) url() string {
	if o.URL != "" {
		return o.URL
	}
	return "wss://ais.openwaters.io/v1/stream"
}

func (o *OpenWaters) bbox() [4]float64 {
	if o.BBox != [4]float64{} {
		return o.BBox
	}
	return [4]float64{32.0, 33.5, 33.5, 36.0} // Eastern Mediterranean
}

// Run streams raw AIS sentences until ctx is cancelled.
func (o *OpenWaters) Run(ctx context.Context, emit func(Item)) error {
	dial := o.Dial
	if dial == nil {
		dial = func(ctx context.Context, url string) (*websocket.Conn, error) {
			c, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
			return c, err
		}
	}
	c, err := dial(ctx, o.url())
	if err != nil {
		return fmt.Errorf("openwaters dial: %w", err)
	}
	defer c.Close()
	b := o.bbox()
	sub := map[string]any{"type": "subscribe", "bbox": [][]float64{{b[0], b[1], b[2], b[3]}}}
	if o.Token != "" {
		sub["token"] = o.Token
	}
	if err := c.WriteJSON(sub); err != nil {
		return fmt.Errorf("openwaters subscribe: %w", err)
	}
	go func() {
		<-ctx.Done()
		c.Close()
	}()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return err
		}
		var ev openwatersEvent
		if json.Unmarshal(data, &ev) != nil || ev.Type != "event" {
			continue
		}
		t, _ := time.Parse(time.RFC3339, ev.Time)
		for _, s := range ev.NMEA {
			rs := AISRawSentence{Sentence: s, Source: ev.Source, Station: ev.Station,
				Attribution: ev.Attribution, License: ev.License, Synthesized: ev.Synthesized, Time: t}
			if o.OnSentence != nil {
				o.OnSentence(rs)
			}
			emit(Item{
				ID:     "aisraw:" + ev.Source + ":" + s,
				Source: o.Name(),
				Kind:   o.Kind(),
				Time:   t,
				Label:  "AIS raw sentence",
				Detail: fmt.Sprintf("%s [%s/%s]", s, ev.Source, ev.Station),
			})
		}
	}
}

// AttributionNotice is the notice openwaters.io asks re-users to keep.
const OpenWatersAttributionNotice = "AIS data via openwaters.io; upstream source named per event."
