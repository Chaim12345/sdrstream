// satprobe sweeps the public OpenWebRX directory and reports, as JSON, which
// receivers answered their /status.json probe and what each one can hear.
// Unlike `sdrstream discover` (a quick human-readable listing), this keeps
// every published band so satellite coverage questions can be answered
// offline without re-hammering the receivers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/directory"
)

type bandJSON struct {
	Name          string  `json:"name"`
	CenterKHz     float64 `json:"center_khz"`
	SampleRateKHz float64 `json:"sample_rate_khz"`
	LowMHz        float64 `json:"low_mhz"`
	HighMHz       float64 `json:"high_mhz"`
}

type receiverJSON struct {
	URL            string     `json:"url"`
	Latitude       float64    `json:"latitude"`
	Longitude      float64    `json:"longitude"`
	Bands          []bandJSON `json:"bands"`
	SatelliteBands []string   `json:"satellite_bands"`
}

func main() {
	timeout := flag.Float64("timeout", 20, "per-request timeout in seconds")
	limit := flag.Int("limit", 0, "probe only the first N servers")
	out := flag.String("out", "", "write JSON here instead of stdout")
	verify := flag.String("verify", "", "skip the sweep; verify this OpenWebRX URL tunes -freq by recording real audio")
	freq := flag.Float64("freq", 137620000, "frequency in Hz for -verify")
	mode := flag.String("mode", "nfm", "modulation for -verify")
	seconds := flag.Float64("seconds", 4, "audio seconds to capture for -verify")
	profile := flag.String("profile", "", "profile display name to select before tuning (optional)")
	flag.Parse()

	if *verify != "" {
		ctx, cancel := context.WithTimeout(context.Background(),
			time.Duration((*seconds+40)*float64(time.Second)))
		defer cancel()
		result := verifyReceiver(ctx, *verify, int64(*freq), *mode, *seconds, *profile)
		payload, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(payload))
		if !result.Verified {
			os.Exit(1)
		}
		return
	}

	results, err := directory.Discover(context.Background(),
		time.Duration(*timeout*float64(time.Second)), *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "satprobe:", err)
		os.Exit(1)
	}
	receivers := make([]receiverJSON, 0, len(results))
	for _, r := range results {
		entry := receiverJSON{URL: r.URL, Latitude: r.Latitude,
			Longitude: r.Longitude, SatelliteBands: r.SatelliteBands}
		for _, b := range r.Bands {
			entry.Bands = append(entry.Bands, bandJSON{
				Name: b.Name, CenterKHz: b.CenterKHz, SampleRateKHz: b.SampleRateKHz,
				LowMHz: b.LowHz() / 1e6, HighMHz: b.HighHz() / 1e6})
		}
		receivers = append(receivers, entry)
	}
	payload, err := json.MarshalIndent(receivers, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "satprobe:", err)
		os.Exit(1)
	}
	if *out != "" {
		if err := os.WriteFile(*out, payload, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "satprobe:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "satprobe: %d receivers answered, wrote %s\n", len(receivers), *out)
		return
	}
	fmt.Println(string(payload))
}
