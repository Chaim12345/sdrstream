package feeds

// PSK Reporter live spot feed over MQTT (mqtt.pskreporter.info). The feed
// is public and needs no account; we subscribe with a topic filter and keep
// spots in the shared buffer, which also makes them available as a
// cross-check for our own digimode decodes.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type PSKReporter struct {
	Server string // default mqtt.pskreporter.info:1883
	Topic  string // default: spots received by stations in Israel (DXCC 336)
	Now    func() time.Time
	dial   func(ctx context.Context, addr, clientID string, keepalive time.Duration) (mqttConn, error)
}

type mqttConn interface {
	subscribe(topic string) error
	ping() error
	nextPublish() (string, []byte, error)
	Close() error
}

func (p *PSKReporter) Name() string { return "pskreporter" }
func (p *PSKReporter) Kind() string { return "digimode" }

func (p *PSKReporter) server() string {
	if p.Server != "" {
		return p.Server
	}
	return "mqtt.pskreporter.info:1883"
}

func (p *PSKReporter) topic() string {
	if p.Topic != "" {
		return p.Topic
	}
	// pskr/filter/v2/{band}/{mode}/{tx}/{rx}/{txloc}/{rxloc}/{txdxcc}/{rxdxcc};
	// DXCC 336 is Israel, so this follows what receivers in Israel hear -
	// the right corroboration for decodes we make there.
	return "pskr/filter/v2/+/+/+/+/+/+/+/336"
}

func (p *PSKReporter) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

type pskSpot struct {
	SenderCall   string `json:"sc"`
	ReceiverCall string `json:"rc"`
	SenderLoc    string `json:"sl"`
	ReceiverLoc  string `json:"rl"`
	Freq         int64  `json:"f"`
	Mode         string `json:"md"`
	Report       int    `json:"rp"`
	UnixTime     int64  `json:"t"`
	Band         string `json:"b"`
}

// ParsePSKSpot converts one MQTT payload into an Item.
func ParsePSKSpot(payload []byte, now time.Time) (Item, bool) {
	var s pskSpot
	if err := json.Unmarshal(payload, &s); err != nil || s.SenderCall == "" {
		return Item{}, false
	}
	when := now
	if s.UnixTime > 0 {
		when = time.Unix(s.UnixTime, 0)
	}
	var parts []string
	if s.Mode != "" {
		parts = append(parts, s.Mode)
	}
	if s.Band != "" {
		parts = append(parts, s.Band)
	}
	if s.Freq > 0 {
		parts = append(parts, fmt.Sprintf("%.3f MHz", float64(s.Freq)/1e6))
	}
	parts = append(parts, fmt.Sprintf("heard by %s", s.ReceiverCall))
	if s.ReceiverLoc != "" {
		parts = append(parts, s.ReceiverLoc)
	}
	it := Item{
		ID:     fmt.Sprintf("psk|%s|%s|%d", s.SenderCall, s.ReceiverCall, s.UnixTime),
		Label:  strings.ToUpper(s.SenderCall),
		Detail: strings.Join(parts, " · "),
		Time:   when,
	}
	if lat, lon, ok := maidenheadToLatLon(s.SenderLoc); ok {
		it.Lat, it.Lon, it.HasPos = lat, lon, true
	}
	return it, true
}

// maidenheadToLatLon resolves a 4 or 6 character locator to its centre.
func maidenheadToLatLon(loc string) (float64, float64, bool) {
	if len(loc) < 4 {
		return 0, 0, false
	}
	loc = strings.ToUpper(loc[:min(6, len(loc))])
	lon := float64(int(loc[0]-'A')*20 - 180)
	lat := float64(int(loc[1]-'A')*10 - 90)
	lon += float64(loc[2]-'0') * 2
	lat += float64(loc[3] - '0')
	lonC, latC := lon+1, lat+0.5
	if len(loc) == 6 {
		lonC = lon + float64(int(loc[4]-'A'))*(2.0/24) + 1.0/24
		latC = lat + float64(int(loc[5]-'A'))*(1.0/24) + 1.0/48
	}
	return latC, lonC, true
}

// Run connects and streams spots until the connection drops.
func (p *PSKReporter) Run(ctx context.Context, emit func(Item)) error {
	return p.RunReady(ctx, emit, func() {})
}

// RunReady also fires ready once the MQTT subscription is acknowledged.
func (p *PSKReporter) RunReady(ctx context.Context, emit func(Item), ready func()) error {
	dial := p.dial
	if dial == nil {
		dial = func(ctx context.Context, addr, clientID string, keepalive time.Duration) (mqttConn, error) {
			return mqttDial(ctx, addr, clientID, keepalive)
		}
	}
	client, err := dial(ctx, p.server(), fmt.Sprintf("sdrstream-%d", p.now().UnixNano()%1e6), 60*time.Second)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.subscribe(p.topic()); err != nil {
		return err
	}
	ready()

	type publish struct {
		payload []byte
		err     error
	}
	ch := make(chan publish, 256)
	go func() {
		defer close(ch)
		for {
			_, payload, err := client.nextPublish()
			if err != nil {
				ch <- publish{err: err}
				return
			}
			select {
			case ch <- publish{payload: payload}:
			default: // drop under overload rather than block the reader
			}
		}
	}()

	ping := time.NewTicker(45 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok || msg.err != nil {
				if msg.err != nil {
					return msg.err
				}
				return fmt.Errorf("mqtt reader stopped")
			}
			if it, ok := ParsePSKSpot(msg.payload, p.now()); ok {
				emit(it)
			}
		case <-ping.C:
			if err := client.ping(); err != nil {
				return err
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
