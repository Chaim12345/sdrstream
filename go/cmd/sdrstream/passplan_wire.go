package main

import (
	"context"
	"log"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/passplan"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// passplanRefreshEvery is how often the plan is recomputed from the cached
// element sets. The cache itself is filled by the existing pass table; this
// never fetches.
const passplanRefreshEvery = 10 * time.Minute

// startPassplan wires the read-only multi-pass planner into the web server:
// station location from the existing -feeds-lat/-feeds-lon, cached TLEs only,
// state under web.StateFile. It plans only; it never starts a capture or
// contacts a receiver.
func startPassplan(ctx context.Context, server *web.Server, lat, lon float64) (*passplan.Service, <-chan struct{}) {
	svc := passplan.NewService(passplan.Config{
		Lat: lat, Lon: lon,
		StatePath: web.StateFile("passplan_state.json"),
		Strategy:  passplan.StrategyPriority,
		Logf:      log.Printf,
	})
	server.SetPassplan(svc)
	tick := time.NewTicker(passplanRefreshEvery)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer tick.Stop()
		svc.Run(ctx, tick.C)
	}()
	return svc, done
}
