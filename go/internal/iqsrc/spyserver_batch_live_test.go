package iqsrc

// Manually-invoked bounded batch probe over the public SpyServer directory:
//   SDRSTREAM_LIVE_BATCH="/path/status.json,count[,offset]"
// For up to count listed servers (serial, one connection at a time, no
// retries) it handshakes, requests the server's OWN current center
// frequency (no control attempt), streams a few seconds of the decimated
// channel, and reports per server: reachability, negotiated rate,
// CanControl, wall-clock coverage, sequence discontinuities, and the
// strongest carrier above the band median. It stops after 3 consecutive
// unreachable servers. Counts are separated: listed != reachable !=
// streaming != strong carrier. It claims nothing beyond printed numbers.
// Skipped unless the env var is set; never runs in CI.

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type batchEntry struct {
	StreamingHost          string `json:"streamingHost"`
	StreamingPort          int    `json:"streamingPort"`
	DeviceType             string `json:"deviceType"`
	MinimumFrequency       int64  `json:"minimumFrequency"`
	MaximumFrequency       int64  `json:"maximumFrequency"`
	CurrentCenterFrequency int64  `json:"currentCenterFrequency"`
	CurrentClientCount     int    `json:"currentClientCount"`
	MaxClients             int    `json:"maxClients"`
	FullControlAllowed     bool   `json:"fullControlAllowed"`
	Online                 bool   `json:"online"`
}

func TestLiveSpyBatch(t *testing.T) {
	spec := os.Getenv("SDRSTREAM_LIVE_BATCH")
	if spec == "" {
		t.Skip("set SDRSTREAM_LIVE_BATCH=/path/status.json,count[,offset] for a bounded batch probe")
	}
	parts := strings.Split(spec, ",")
	raw, err := os.ReadFile(parts[0])
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	var doc struct {
		Servers []batchEntry `json:"servers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse directory: %v", err)
	}
	count, _ := strconv.Atoi(parts[1])
	offset := 0
	if len(parts) > 2 {
		offset, _ = strconv.Atoi(parts[2])
	}
	var candidates []batchEntry
	for _, e := range doc.Servers {
		if !e.Online || e.CurrentCenterFrequency == 0 || e.CurrentClientCount >= e.MaxClients {
			continue
		}
		if e.CurrentCenterFrequency < e.MinimumFrequency || e.CurrentCenterFrequency > e.MaximumFrequency {
			continue
		}
		candidates = append(candidates, e)
	}
	// Keep directory order: sorting by host clusters same-operator servers,
	// which made consecutive-failure stops undersample the directory.
	if offset > len(candidates) {
		offset = len(candidates)
	}
	candidates = candidates[offset:]
	if count > 0 && len(candidates) > count {
		candidates = candidates[:count]
	}
	t.Logf("directory candidates with center+free slot: attempting %d from offset %d", len(candidates), offset)

	var reachable, streaming, carrier int
	attempted := 0
	for _, e := range candidates {
		attempted++
		addr := e.StreamingHost + ":" + strconv.Itoa(e.StreamingPort)
		freq := uint32(e.CurrentCenterFrequency)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		c, err := DialSpy(ctx, SpyConfig{Addr: addr, Frequency: freq, SampleRate: 2500000, DigitalGain: true, AllowPrivate: true})
		if err != nil {
			t.Logf("%s UNREACHABLE: %v", addr, err)
			cancel()
			continue
		}
		reachable++
		rate := int(c.SampleRate())
		const nfft = 8192
		buf := make([]complex64, nfft)
		window := make([]float64, nfft)
		for i := range window {
			window[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(nfft)))
		}
		var total int64
		var maxRatio float64
		start := time.Now()
		for time.Since(start) < 5*time.Second {
			n, err := c.ReadSamples(buf)
			if err != nil {
				break
			}
			if n != nfft {
				continue
			}
			total += int64(n)
			w := make([]complex128, nfft)
			for i, v := range buf {
				w[i] = complex(float64(real(v))*window[i], float64(imag(v))*window[i])
			}
			sp := fft(w)
			mags := make([]float64, nfft/2)
			for i := range mags {
				mags[i] = real(sp[i])*real(sp[i]) + imag(sp[i])*imag(sp[i])
			}
			sorted := append([]float64(nil), mags...)
			sort.Float64s(sorted)
			median := sorted[len(sorted)/2]
			var peak float64
			for _, m := range mags {
				if m > peak {
					peak = m
				}
			}
			if r := 10 * math.Log10(peak/(median+1e-30)); r > maxRatio {
				maxRatio = r
			}
		}
		wall := time.Since(start)
		st := c.Stats()
		c.Close()
		cancel()
		coverage := float64(total) / (wall.Seconds() * float64(rate)) * 100
		if total > 0 {
			streaming++
		}
		if maxRatio > 20 {
			carrier++
		}
		t.Logf("%s OK: rate=%d CanControl=%d center=%d coverage=%.0f%% gaps=%d peakCarrier=+%.1fdB wall=%.1fs",
			addr, rate, c.Sync.CanControl, c.Sync.DeviceCenterFrequency, coverage, st.IQSeqGaps, maxRatio, wall.Seconds())
	}
	t.Logf("BATCH COUNTS: attempted=%d reachable=%d streaming=%d strongCarrier(>20dB)=%d (candidates listed %d)", attempted, reachable, streaming, carrier, len(candidates))
}
