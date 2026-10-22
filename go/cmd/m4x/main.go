// Command m4x isolates KiwiSDR retune/sequential-record behaviour (QA probe).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

func main() {
	server := flag.String("server", "", "KiwiSDR host[:port]")
	freqA := flag.Float64("freqA", 198.0, "first dial frequency kHz")
	modeA := flag.String("modeA", "am", "mode A")
	freqB := flag.Float64("freqB", 0, "second dial frequency kHz (0 = stay on A)")
	modeB := flag.String("modeB", "usb", "mode B")
	secs := flag.Float64("secs", 4, "seconds per record")
	gap := flag.Duration("gap", 0, "sleep between records")
	flag.Parse()
	ctx := context.Background()
	c, err := kiwisdr.New(*server, func() kiwisdr.Options { o := kiwisdr.DefaultOptions(); o.AllowPrivate = true; return o }())
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	mustTune := func(f float64, m string) {
		if err := c.Tune(ctx, int64(f*1000), m); err != nil {
			log.Fatalf("tune %.1f: %v", f, err)
		}
	}
	mustTune(*freqA, *modeA)
	s1, r1, err := c.Record(ctx, time.Duration(*secs*float64(time.Second)))
	fmt.Printf("rec1: %d samples @%d err=%v\n", len(s1), r1, err)
	if *gap > 0 {
		time.Sleep(*gap)
	}
	if *freqB > 0 {
		mustTune(*freqB, *modeB)
	}
	s2, r2, err := c.Record(ctx, time.Duration(*secs*float64(time.Second)))
	fmt.Printf("rec2: %d samples @%d err=%v\n", len(s2), r2, err)
}
