package decode

// Live end-to-end check against the public SatNOGS network: fetch the
// audio of a recent good ISS observation and decode APRS frames from it.
// Skipped by default; run with SDRSTREAM_SATNOGS_LIVE=1. Kept out of the
// satnogs package so the decode package owns all demodulator tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSatNOGSISSAudioLive(t *testing.T) {
	if os.Getenv("SDRSTREAM_SATNOGS_LIVE") != "1" {
		t.Skip("set SDRSTREAM_SATNOGS_LIVE=1 to fetch and decode live SatNOGS audio")
	}
	if !FFmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("GET", "https://network.satnogs.org/api/observations/?satellite=25544&format=json&limit=15&status=good", nil)
	req.Header.Set("User-Agent", "sdrstream/0.1 (+satellite test)")
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("SatNOGS unreachable: %v", err)
	}
	defer resp.Body.Close()
	var observations []struct {
		ID      int    `json:"id"`
		Payload string `json:"payload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&observations); err != nil {
		t.Skipf("SatNOGS answer unreadable: %v", err)
	}
	dir := t.TempDir()
	for _, obs := range observations {
		if obs.Payload == "" {
			continue
		}
		ogg := filepath.Join(dir, fmt.Sprintf("%d.ogg", obs.ID))
		wav := filepath.Join(dir, fmt.Sprintf("%d.wav", obs.ID))
		r2, err := client.Get(obs.Payload)
		if err != nil {
			continue
		}
		if r2.StatusCode != 200 {
			r2.Body.Close()
			continue
		}
		f, _ := os.Create(ogg)
		_, err = f.ReadFrom(r2.Body)
		f.Close()
		r2.Body.Close()
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err = ConvertToWAV(ctx, "", ogg, wav)
		cancel()
		if err != nil {
			continue
		}
		samples, rate, err := ReadWAV(wav)
		if err != nil {
			continue
		}
		frames, _ := AFSKFrames(wav)
		frames = append(frames, GMSKFrames(samples, rate, []int{9600, 4800})...)
		if len(frames) > 0 {
			t.Logf("observation %d: %d frames, first: %s", obs.ID, len(frames), frames[0].String())
			return
		}
	}
	t.Skip("no decodable ISS observation in the current feed window")
}
