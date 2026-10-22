// Command kiwiscan loads the public KiwiSDR directory mirror (one cached
// fetch per hour, deduped by host:port) and, with -capture N, records a
// short clip from the first N receivers that have a free slot. Receivers are
// visited one at a time, one connection each, closed straight after.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

func main() {
	cache := flag.String("cache", filepath.Join(os.TempDir(), "sdrstream-kiwi"), "cache directory for the listing")
	capture := flag.Int("capture", 0, "record from this many receivers with a free slot")
	freqKHz := flag.Float64("freq", 14074, "dial frequency in kHz")
	mode := flag.String("mode", "usb", "demodulator mode")
	secs := flag.Float64("secs", 8, "seconds to record per receiver")
	out := flag.String("out", ".", "directory for WAV files")
	maxTries := flag.Int("max-tries", 8, "give up after this many connection attempts")
	flag.Parse()

	ctx := context.Background()
	loader := kiwisdr.NewLoader(*cache)
	recs, raw, err := loader.Load(ctx)
	if err != nil {
		log.Fatal(err)
	}
	usable := 0
	for _, r := range recs {
		if r.Usable() {
			usable++
		}
	}
	fmt.Printf("listing: %d raw entries, %d unique host:port, %d with a free slot (network fetches this run: %d)\n", raw, len(recs), usable, loader.Fetches)
	if *capture == 0 {
		return
	}
	done, tries := 0, 0
	for _, r := range recs {
		if done >= *capture || tries >= *maxTries {
			break
		}
		if !r.Usable() || r.Secure || r.Version == "" {
			continue
		}
		tries++
		fmt.Printf("\n[%d] %s  %s  slots %d/%d\n", tries, r.Key(), r.Name, r.Users, r.UsersMax)
		samples, rate, info, err := record(ctx, r, int64(*freqKHz*1000), *mode, time.Duration(*secs*float64(time.Second)))
		switch {
		case errors.Is(err, kiwisdr.ErrBusy):
			fmt.Println("  busy, skipped:", err)
			continue
		case err != nil:
			fmt.Println("  failed:", err)
			continue
		case len(samples) == 0:
			fmt.Println("  no audio")
			continue
		}
		path := filepath.Join(*out, fmt.Sprintf("kiwi-%s-%dkHz.wav", sanitize(r.Key()), int(*freqKHz)))
		f := make([]float64, len(samples))
		var sum float64
		peak := 0
		for i, s := range samples {
			f[i] = float64(s) / 32768
			sum += f[i] * f[i]
			if a := abs(int(s)); a > peak {
				peak = a
			}
		}
		if err := voice.WriteWAV(path, f, rate); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  OK %d samples @ %d Hz = %.1fs  rms %.4f  peak %d  server %s ch %d\n  wrote %s\n",
			len(samples), rate, float64(len(samples))/float64(rate), math.Sqrt(sum/float64(len(f))), peak, info.Version, info.RXChannels, path)
		done++
		time.Sleep(2 * time.Second) // be polite between hosts
	}
	fmt.Printf("\ncaptured %d/%d requested\n", done, *capture)
	if done < *capture {
		os.Exit(1)
	}
}

func record(ctx context.Context, r kiwisdr.Receiver, hz int64, mode string, d time.Duration) ([]int16, int, kiwisdr.Info, error) {
	opts := kiwisdr.DefaultOptions()
	opts.Reconnect = false
	c, err := r.Client(opts)
	if err != nil {
		return nil, 0, kiwisdr.Info{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, d+20*time.Second)
	defer cancel()
	if err := c.Connect(cctx); err != nil {
		return nil, 0, kiwisdr.Info{}, err
	}
	defer c.Close()
	if err := c.Tune(cctx, hz, mode); err != nil {
		return nil, 0, kiwisdr.Info{}, err
	}
	samples, rate, err := c.Record(cctx, d)
	return samples, rate, c.InfoSnapshot(), err
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}
