package feeds

import (
	"context"
	"time"
)

// Per-source staleness windows used by Start.
var maxAges = map[string]time.Duration{
	"adsb.lol":    2 * time.Minute,
	"aprs-is":     time.Hour,
	"pskreporter": 30 * time.Minute,
	"rbn":         30 * time.Minute,
	"aisstream":   10 * time.Minute,
	"wsprnet":     24 * time.Hour,
	"noaa-swpc":   12 * time.Hour,
	"wspr.live":   30 * time.Minute,
	"sondehub":    2 * time.Hour,
	"digitraffic": 30 * time.Minute,
}

// Start launches every feed against the manager until ctx ends.
func Start(ctx context.Context, m *Manager, lat, lon float64, aisKey string) {
	pollers := []Poller{&ADSB{Lat: lat, Lon: lon, Radius: 250},
		&PublicJSON{Source: "noaa-swpc"}, &PublicJSON{Source: "wspr.live"},
		&PublicJSON{Source: "digitraffic"}}
	streams := []StreamSource{
		&SondeHub{},
		&APRSIS{Lat: lat, Lon: lon, Radius: 400},
		&PSKReporter{},
		&RBN{},
	}
	if aisKey == "" {
		m.Disable("aisstream", "ais", "needs a free aisstream.io API key (OAuth signup at aisstream.io), then set SDRSTREAM_AIS_KEY (or pass --ais-key) and restart; optional, all other feeds run without it")
	} else {
		streams = append(streams, &AIS{Key: aisKey})
	}
	for _, p := range pollers {
		age := maxAges[p.Name()]
		go m.RunPoller(ctx, p, age)
	}
	for _, s := range streams {
		age := maxAges[s.Name()]
		go m.RunStream(ctx, s, age)
	}
	go m.PruneLoop(ctx)
}
