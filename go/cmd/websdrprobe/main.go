// Probe a WebSDR server: connect, tune, record a few seconds.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

func main() {
	server := flag.String("server", "http://eshail.batc.org.uk/nb/", "WebSDR URL")
	freqKHz := flag.Float64("freq", 10489550, "dial kHz")
	out := flag.String("out", "", "optional WAV output")
	mode := flag.String("mode", "usb", "mode")
	secs := flag.Int("secs", 12, "seconds")
	flag.Parse()
	ctx := context.Background()
	c, err := websdr.New(*server, websdr.DefaultOptions())
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	if err := c.Tune(ctx, *freqKHz, *mode, nil); err != nil {
		log.Fatal(err)
	}
	samples, rate, err := c.Record(ctx, time.Duration(*secs)*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	if *out != "" {
		if err := os.WriteFile(*out, decode.WriteWAV16(samples, rate), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	peak := 0
	for _, s := range samples {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	fmt.Printf("websdr: %d samples at %d Hz, peak %d\n", len(samples), rate, peak)
}
