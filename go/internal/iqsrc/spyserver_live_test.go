package iqsrc

import (
	"context"
	"fmt"
	"math"
	"math/cmplx"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveSpyCarrierSurvey is a manually-invoked live probe against one
// named public SpyServer: SDRSTREAM_LIVE_SPY="host:port,freq_hz" (optionally
// ,seconds). It streams IQ for a few seconds, averages a power spectrum,
// and reports band powers relative to the median so a human can see
// carriers above the floor. It stores a small raw IQ snippet under /tmp as
// private evidence. It does not decode anything and claims nothing beyond
// the numbers it prints. Skipped unless the env var is set; never runs in
// CI.
func TestLiveSpyCarrierSurvey(t *testing.T) {
	spec := os.Getenv("SDRSTREAM_LIVE_SPY")
	if spec == "" {
		t.Skip("set SDRSTREAM_LIVE_SPY=host:port,freq_hz[,seconds] for a bounded live probe")
	}
	parts := strings.Split(spec, ",")
	addr := parts[0]
	freq, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		t.Fatalf("bad freq: %v", err)
	}
	secs := 8
	if len(parts) > 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 && n <= 20 {
			secs = n
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+20)*time.Second)
	defer cancel()
	c, err := DialSpy(ctx, SpyConfig{Addr: addr, Frequency: uint32(freq), SampleRate: 2500000, DigitalGain: true, AllowPrivate: true})
	if err != nil {
		t.Fatalf("DialSpy: %v", err)
	}
	defer c.Close()
	rate := int(c.SampleRate())
	t.Logf("negotiated rate %d Hz (max %d), device type %d, sync CanControl=%d",
		rate, c.Info.MaximumSampleRate, c.Info.DeviceType, c.Sync.CanControl)

	const nfft = 8192
	bands := 100 // ~25 kHz bands at 2.5 MHz
	power := make([]float64, bands)
	var chunks int
	buf := make([]complex64, nfft)
	window := make([]float64, nfft)
	for i := range window { // Hann
		window[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(nfft)))
	}
	var snippet []complex64
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		n, err := c.ReadSamples(buf)
		if n > 0 {
			if len(snippet) < 65536 {
				snippet = append(snippet, buf[:min(n, 65536-len(snippet))]...)
			}
			if n == nfft {
				w := make([]complex128, nfft)
				for i, v := range buf {
					w[i] = complex(float64(real(v))*window[i], float64(imag(v))*window[i])
				}
				spectrum := fft(w)
				for b := 0; b < bands; b++ {
					var p float64
					lo := b * nfft / bands
					hi := (b + 1) * nfft / bands
					for i := lo; i < hi; i++ {
						p += real(spectrum[i])*real(spectrum[i]) + imag(spectrum[i])*imag(spectrum[i])
					}
					power[b] += p / float64(hi-lo)
				}
				chunks++
			}
		}
		if err != nil {
			t.Fatalf("ReadSamples: %v", err)
		}
	}
	if chunks == 0 {
		t.Fatal("no complete FFT chunks streamed")
	}
	// Save private raw evidence snippet as interleaved float32.
	f, err := os.Create("/tmp/spy-live-iq-snippet.f32")
	if err == nil {
		for _, v := range snippet {
			var b [8]byte
			u := math.Float32bits(real(v))
			b[0], b[1], b[2], b[3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
			u = math.Float32bits(imag(v))
			b[4], b[5], b[6], b[7] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
			f.Write(b[:])
		}
		f.Close()
		t.Logf("raw IQ snippet: %d samples -> /tmp/spy-live-iq-snippet.f32 (PRIVATE evidence)", len(snippet))
	}
	sorted := make([]float64, bands)
	for b := range power {
		power[b] /= float64(chunks)
		sorted[b] = power[b]
	}
	sort.Float64s(sorted)
	median := sorted[bands/2]
	bw := float64(rate) / float64(bands)
	t.Logf("chunks=%d rate=%d band=%.0fHz median=%.3e", chunks, rate, bw, median)
	over := 0
	for b, p := range power {
		db := 10 * math.Log10(p/median)
		mhz := (float64(uint32(freq)) + (float64(b)+0.5-float64(bands)/2)*bw) / 1e6
		if db > 8 {
			over++
			t.Logf("band %3d  %8.3f MHz  %+6.1f dB over median", b, mhz, db)
		}
	}
	t.Logf("bands >8dB over floor: %d of %d (DC near center is expected on direct-conversion hardware)", over, bands)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fft is a radix-2 Cooley-Tukey transform for the live probe; the shipped
// code has no FFT dependency and this test file must not add one.
func fft(x []complex128) []complex128 {
	n := len(x)
	if n <= 1 {
		return x
	}
	even := make([]complex128, n/2)
	odd := make([]complex128, n/2)
	for i := 0; i < n/2; i++ {
		even[i] = x[2*i]
		odd[i] = x[2*i+1]
	}
	fe, fo := fft(even), fft(odd)
	out := make([]complex128, n)
	for k := 0; k < n/2; k++ {
		t := cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n))) * fo[k]
		out[k] = fe[k] + t
		out[k+n/2] = fe[k] - t
	}
	return out
}

var _ = fmt.Sprintf // keep fmt import if logging shrinks
