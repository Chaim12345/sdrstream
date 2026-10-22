// Command feedtest runs the live data feeds for a short time and prints
// what arrives. It is a development smoke test for the feeds package: real
// connections to the named public sources, nothing fabricated.
package main

import (
	"context"
	"flag"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

func main() {
	secs := flag.Int("secs", 45, "seconds to listen")
	only := flag.String("only", "", "comma list of sources (default: pskreporter,rbn,dxcluster,pota,sota,wspr.live,aprs-is)")
	flag.Parse()
	want := map[string]bool{}
	if *only != "" {
		for _, s := range splitComma(*only) {
			want[s] = true
		}
	} else {
		for _, s := range []string{"pskreporter", "rbn", "dxcluster", "pota", "sota", "wspr.live", "aprs-is"} {
			want[s] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*secs)*time.Second)
	defer cancel()
	var count atomic.Int64
	emit := func(it feeds.Item) {
		n := count.Add(1)
		if n <= 40 {
			fmt.Printf("[%s] %s | %s | %s\n", it.Source, it.Time.UTC().Format("15:04:05"), it.Label, it.Detail)
		}
	}
	pollers := []feeds.Poller{&feeds.POTA{}, &feeds.SOTA{}, &feeds.WSPRLive{}}
	streams := []feeds.StreamSource{&feeds.PSKReporter{}, &feeds.RBN{}, &feeds.DXCluster{}, &feeds.APRSIS{Lat: 32, Lon: 35, Radius: 400}}
	for _, p := range pollers {
		if !want[p.Name()] {
			continue
		}
		go func(p feeds.Poller) {
			items, err := p.Poll(ctx)
			if err != nil {
				fmt.Printf("[%s] poll error: %v\n", p.Name(), err)
				return
			}
			fmt.Printf("[%s] poll ok: %d items\n", p.Name(), len(items))
			for _, it := range items {
				if it.Source == p.Name() {
					emit(it)
				}
			}
		}(p)
	}
	for _, s := range streams {
		if !want[s.Name()] {
			continue
		}
		go func(s feeds.StreamSource) {
			err := s.Run(ctx, emit)
			fmt.Printf("[%s] stream ended: %v\n", s.Name(), err)
		}(s)
	}
	<-ctx.Done()
	time.Sleep(500 * time.Millisecond)
	fmt.Printf("total items: %d\n", count.Load())
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
