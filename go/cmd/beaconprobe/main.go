// Command beaconprobe measures a time-signal beacon's SNR on a real KiwiSDR
// (QA probe for the receiver health table).
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
	host := flag.String("server", "", "KiwiSDR host[:port]")
	flag.Parse()
	if *host == "" {
		log.Fatal("-server required")
	}
	ctx := context.Background()
	opts := kiwisdr.DefaultOptions()
	opts.AllowPrivate = true
	for _, b := range kiwisdr.DefaultBeacons {
		snr, err := kiwisdr.MeasureBeaconSNR(ctx, *host, b, 5*time.Second, opts)
		if err != nil {
			fmt.Printf("%s: error %v\n", b.Name, err)
			continue
		}
		fmt.Printf("%s: SNR %.1f dB\n", b.Name, snr)
	}
}
