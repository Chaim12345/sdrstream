// xcheck prints the predicted elevation/azimuth of one satellite at one UTC
// instant, for cross-checking the SGP4 port against a second implementation.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

func main() {
	text, err := tle.LoadCatnr(25338, 24, false, 60*time.Second)
	if err != nil || text == "" {
		fmt.Println("tle fetch failed:", err)
		os.Exit(1)
	}
	tracker, err := tracking.FromTLEText(text, "NOAA 15")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	for _, stamp := range os.Args[1:] {
		when, _ := time.Parse(time.RFC3339, stamp)
		look, err := tracker.LookAngle(when, 51.80, 5.71, 0)
		if err != nil {
			fmt.Println(stamp, err)
			continue
		}
		fmt.Printf("%s el=%.2f az=%.2f range=%.0f km rrate=%.3f km/s\n",
			stamp, look.Elevation, look.Azimuth, look.RangeKm, look.RangeRateKmS)
	}
}
