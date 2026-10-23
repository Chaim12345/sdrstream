package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/openwebrx"
)

// verifyResult is the proof one receiver really tunes a frequency.
type verifyResult struct {
	URL          string          `json:"url"`
	FrequencyHz  int64           `json:"frequency_hz"`
	Mode         string          `json:"mode"`
	Profiles     json.RawMessage `json:"profiles"`
	ProfileUsed  string          `json:"profile_used,omitempty"`
	Seconds      float64         `json:"seconds"`
	Chunks       int             `json:"chunks"`
	Samples      int             `json:"samples"`
	SampleRate   int             `json:"sample_rate"`
	RMS          float64         `json:"rms"`
	Peak         int             `json:"peak"`
	NonZeroRatio float64         `json:"nonzero_ratio"`
	Verified     bool            `json:"verified"`
	Note         string          `json:"note,omitempty"`
}

// verifyReceiver connects to one OpenWebRX server, tunes frequencyHz and
// records seconds of audio. Verified means real samples flowed at a sane
// rate; it says nothing about whether a satellite was audible.
func verifyReceiver(ctx context.Context, rawURL string, frequencyHz int64, mode string, seconds float64, profileName string) verifyResult {
	result := verifyResult{URL: rawURL, FrequencyHz: frequencyHz, Mode: mode}
	client, err := openwebrx.New(rawURL, openwebrx.DefaultOptions())
	if err != nil {
		result.Note = "client: " + err.Error()
		return result
	}
	defer client.Close()
	if _, err := client.Connect(ctx); err != nil {
		result.Note = "connect: " + err.Error()
		return result
	}
	if raw, err := json.Marshal(client.Profiles); err == nil {
		result.Profiles = raw
	}
	if profileName != "" {
		for _, profile := range client.ProfileList() {
			if strings.Contains(strings.ToLower(profile.Name), strings.ToLower(profileName)) {
				if err := client.SelectProfile(ctx, profile.Key); err != nil {
					result.Note = "select profile: " + err.Error()
					return result
				}
				result.ProfileUsed = profile.Key
				break
			}
		}
		if result.ProfileUsed == "" && profileName != "" {
			result.Note = "profile " + profileName + " not found"
			return result
		}
	}
	freq := frequencyHz
	if err := client.Tune(ctx, &freq, mode, nil); err != nil {
		result.Note = "tune: " + err.Error()
		return result
	}
	chunks, err := client.Record(ctx, time.Duration(seconds*float64(time.Second)))
	if err != nil {
		result.Note = "record: " + err.Error()
		return result
	}
	var sumSquares float64
	nonzero := 0
	for _, chunk := range chunks {
		if chunk.Samplerate > 0 {
			result.SampleRate = chunk.Samplerate
		}
		for _, sample := range chunk.Samples {
			sumSquares += float64(sample) * float64(sample)
			if sample != 0 {
				nonzero++
			}
			if int(math.Abs(float64(sample))) > result.Peak {
				result.Peak = int(math.Abs(float64(sample)))
			}
		}
		result.Samples += len(chunk.Samples)
	}
	result.Chunks = len(chunks)
	if result.Samples > 0 {
		result.RMS = math.Sqrt(sumSquares / float64(result.Samples))
		result.NonZeroRatio = float64(nonzero) / float64(result.Samples)
		result.Seconds = float64(result.Samples) / float64(result.SampleRate)
	}
	// A dead tuner returns nothing or a flat line; both fail verification.
	result.Verified = result.Samples > 0 && result.NonZeroRatio > 0.5 &&
		result.Seconds >= seconds*0.5
	if !result.Verified && result.Note == "" {
		result.Note = fmt.Sprintf("audio did not flow: %d samples in %d chunks", result.Samples, result.Chunks)
	}
	fmt.Fprintf(os.Stderr, "satprobe verify %s @ %.4f MHz: verified=%v samples=%d rms=%.0f note=%q\n",
		rawURL, float64(frequencyHz)/1e6, result.Verified, result.Samples, result.RMS, result.Note)
	return result
}
