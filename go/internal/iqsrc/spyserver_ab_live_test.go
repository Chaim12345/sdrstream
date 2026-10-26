package iqsrc

// Manually-invoked bounded A/B probe against one named public SpyServer:
//   SDRSTREAM_LIVE_AB="host:port,freq_hz[,seconds[,mode]]" (mode: nbfm default, am, usb, lsb)
// Pass A streams the decimated channel path (2.5 Msps request, digital
// gain) exactly like the earlier carrier proof; pass B streams full-rate
// IQ (decimation 0). Both demodulate NBFM locally with the same code and
// write the audio to /tmp as private evidence. It reports per-pass
// negotiated rate, wall-clock coverage, sequence discontinuities and
// audio statistics. It claims nothing beyond the numbers it prints.
// Sequential passes are NOT identical RF time and shared frequency /
// geography is not causal equivalence. Skipped unless the env var is set.

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
)

func writeABWav(t *testing.T, path string, samples []float64, rate int) {
	t.Helper()
	data := make([]byte, 44+2*len(samples))
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1)
	binary.LittleEndian.PutUint16(data[22:24], 1)
	binary.LittleEndian.PutUint32(data[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(2*len(samples)))
	for i, v := range samples {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(int16(v*32767)))
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func abPass(t *testing.T, addr string, freq uint32, wantRate uint32, secs int, mode iqdemod.Mode, tag string) (audio []float64, rate int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+30)*time.Second)
	defer cancel()
	c, err := DialSpy(ctx, SpyConfig{Addr: addr, Frequency: freq, SampleRate: wantRate, DigitalGain: true, AllowPrivate: true})
	if err != nil {
		t.Fatalf("[%s] DialSpy: %v", tag, err)
	}
	defer c.Close()
	rate = int(c.SampleRate())
	t.Logf("[%s] handshake: negotiated=%dHz max=%d CanControl=%d DeviceCenter=%d IQCenter=%d (requested %d) ForcedIQFormat=%d DeviceType=%d",
		tag, rate, c.Info.MaximumSampleRate, c.Sync.CanControl, c.Sync.DeviceCenterFrequency, c.Sync.IQCenterFrequency, freq, c.Info.ForcedIQFormat, c.Info.DeviceType)
	dem := iqdemod.New(rate, 48000, 0, mode)
	buf := make([]complex64, 65536)
	out := make([]float64, 0, 8192)
	var total int64
	start := time.Now()
	deadline := start.Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		n, err := c.ReadSamples(buf)
		if err != nil {
			t.Logf("[%s] read ended early: %v", tag, err)
			break
		}
		if n > 0 {
			chunk := dem.Process(buf[:n], out[:0])
			audio = append(audio, chunk...)
			total += int64(n)
		}
	}
	wall := time.Since(start)
	st := c.Stats()
	coverage := float64(total) / (wall.Seconds() * float64(rate)) * 100
	t.Logf("[%s] samples=%d wall=%.1fs nominal=%.0f coverage=%.1f%% IQmsgs=%d seqGaps=%d skippedNonIQ=%d",
		tag, total, wall.Seconds(), wall.Seconds()*float64(rate), coverage, st.IQMessages, st.IQSeqGaps, st.SkippedNonIQ)
	return audio, rate
}

// audioStats reports RMS plus a crude voiced-activity figure: the share of
// 20 ms frames louder than three times the quietest-decile frame energy.
// It says "something amplitude-varying was present", never that speech was
// intelligible.
func audioStats(samples []float64) (rms, activeFrac float64) {
	if len(samples) == 0 {
		return 0, 0
	}
	var sum float64
	frame := 960 // 20 ms at 48 kHz
	var frames []float64
	for i := 0; i+frame <= len(samples); i += frame {
		var e float64
		for _, v := range samples[i : i+frame] {
			e += v * v
			sum += v * v
		}
		frames = append(frames, e/float64(frame))
	}
	rms = math.Sqrt(sum / float64(len(samples)))
	if len(frames) == 0 {
		return rms, 0
	}
	sorted := append([]float64(nil), frames...)
	for i := 1; i < len(sorted); i++ { // insertion sort, frames are few
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	floor := sorted[len(sorted)/10] * 9
	active := 0
	for _, f := range frames {
		if f > floor {
			active++
		}
	}
	return rms, float64(active) / float64(len(frames))
}

func TestLiveSpyAB(t *testing.T) {
	spec := os.Getenv("SDRSTREAM_LIVE_AB")
	if spec == "" {
		t.Skip("set SDRSTREAM_LIVE_AB=host:port,freq_hz[,seconds] for a bounded live A/B probe")
	}
	parts := strings.Split(spec, ",")
	addr := parts[0]
	f64, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		t.Fatalf("bad freq: %v", err)
	}
	freq := uint32(f64)
	secs := 20
	if len(parts) > 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 && n <= 30 {
			secs = n
		}
	}
	mode := iqdemod.NBFM
	if len(parts) > 3 {
		switch parts[3] {
		case "am":
			mode = iqdemod.AM
		case "usb":
			mode = iqdemod.USB
		case "lsb":
			mode = iqdemod.LSB
		case "nbfm":
		default:
			t.Fatalf("unknown mode %q", parts[3])
		}
	}
	host := strings.ReplaceAll(addr, ":", "_")

	audioA, _ := abPass(t, addr, freq, 2500000, secs, mode, "A-decimated-2.5M")
	writeABWav(t, "/tmp/ab-"+host+"-A.wav", audioA, 48000)
	rmsA, afA := audioStats(audioA)
	t.Logf("[A] audio: n=%d rms=%.4f voiced-frames=%.2f file=/tmp/ab-%s-A.wav", len(audioA), rmsA, afA, host)

	audioB, _ := abPass(t, addr, freq, ^uint32(0), secs, mode, "B-fullrate")
	writeABWav(t, "/tmp/ab-"+host+"-B.wav", audioB, 48000)
	rmsB, afB := audioStats(audioB)
	t.Logf("[B] audio: n=%d rms=%.4f voiced-frames=%.2f file=/tmp/ab-%s-B.wav", len(audioB), rmsB, afB, host)
}
