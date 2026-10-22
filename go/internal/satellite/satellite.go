// Package satellite plans and records satellite passes through public SDR
// receivers.
//
// Tuning a remote receiver is a request, not a guarantee: it takes effect
// after a moment and a busy server may be slow. So the loop retunes only once
// the Doppler correction has moved past a threshold, and it reconnects per
// segment so a dropped connection costs one segment of audio, not the pass.
package satellite

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/openwebrx"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

// SegmentSeconds is how long one connection stays open before reconnecting
// and retuning. Short enough that a missed retune costs little audio, long
// enough to be polite.
const SegmentSeconds = 20.0

// ChasePlan describes what a pass-chasing recording will do.
type ChasePlan struct {
	Satellite   satellites.Sat
	Receiver    catalogue.Receiver
	Passes      []tracking.Pass
	Start       time.Time
	End         time.Time
	StartFreqHz float64
	DopplerLow  int64
	DopplerHigh int64
	TLEText     string
}

// Summary renders the plan the way the Python CLI does.
func (p ChasePlan) Summary() string {
	lines := []string{
		fmt.Sprintf("%s via %s (%.2f, %.2f)", p.Satellite.Name, p.Receiver.URL,
			p.Receiver.Latitude, p.Receiver.Longitude),
		fmt.Sprintf("  chasing %.4f MHz%s, mode %s",
			chaseFrequencyHz(p.Satellite)/1e6,
			chaseSuffix(p.Satellite),
			p.Satellite.Mode),
		fmt.Sprintf("  recording %s to %s UTC (%.1f min)",
			p.Start.Format("15:04:05"), p.End.Format("15:04:05"),
			p.End.Sub(p.Start).Minutes()),
		fmt.Sprintf("  Doppler range %+d to %+d Hz", p.DopplerLow, p.DopplerHigh),
	}
	for _, pass := range p.Passes {
		lines = append(lines, "  "+pass.Summary())
	}
	return strings.Join(lines, "\n")
}

// loadTracker builds the propagation tracker for a satellite, preferring
// General-Perturbations records and falling back to TLE text. GP records
// are the modern element format: since the NORAD catalog passed 99,999 in
// July 2026, new satellites are published only there, never as TLE text.
func loadTracker(name, group, tleText string, offline bool) (*tracking.Tracker, error) {
	if tleText != "" {
		return tracking.FromTLEText(tleText, name)
	}
	if group == "" {
		group = "amateur"
	}
	gp, err := tle.LoadGP(group, 24, offline, 60*time.Second)
	if err == nil {
		if tracker, gpErr := tracking.FromGPText(gp, name); gpErr == nil {
			return tracker, nil
		}
	}
	text, err := tle.Load(group, 24, offline, 60*time.Second)
	if err != nil {
		return nil, err
	}
	return tracking.FromTLEText(text, name)
}

// PlanPass works out when and where to record sat. With receiver nil and no
// coordinates, the first receiver that covers the downlink is used.
func PlanPass(sat satellites.Sat, receiver *catalogue.Receiver,
	latitude, longitude *float64, start time.Time, hours, minElevation float64,
	leadSeconds, tailSeconds float64, tleText string, offline bool,
) (ChasePlan, error) {
	if receiver == nil && (latitude == nil || longitude == nil) {
		candidates := catalogue.ForFrequency(float64(sat.DownlinkHz))
		if len(candidates) == 0 {
			return ChasePlan{}, fmt.Errorf(
				"no known receiver covers %.4f MHz; pass a receiver or observer coordinates",
				float64(sat.DownlinkHz)/1e6)
		}
		chosen := candidates[0]
		receiver = &chosen
	}

	if receiver == nil {
		receiver = &catalogue.Receiver{URL: "(observer)", Kind: "observer",
			Latitude: *latitude, Longitude: *longitude}
	} else if latitude == nil || longitude == nil {
		lat, lon := receiver.Latitude, receiver.Longitude
		latitude, longitude = &lat, &lon
	}

	if len(receiver.Bands) > 0 && !receiver.CanHear(float64(sat.DownlinkHz)) {
		return ChasePlan{}, fmt.Errorf(
			"%s cannot hear %.4f MHz (bands: %s)", receiver.URL,
			float64(sat.DownlinkHz)/1e6, bandNames(receiver.Bands))
	}

	text := tleText
	tracker, err := loadTracker(sat.Name, sat.Group, text, offline)
	if err != nil {
		return ChasePlan{}, err
	}
	passes, err := tracker.FindPasses(*latitude, *longitude, start, hours, minElevation)
	if err != nil {
		return ChasePlan{}, err
	}
	if len(passes) == 0 {
		return ChasePlan{}, fmt.Errorf(
			"no %s passes above %.0f deg in the next %.0f h from %.3f,%.3f",
			sat.Name, minElevation, hours, *latitude, *longitude)
	}
	chosen := passes[0]
	begin := chosen.Rise.Add(-time.Duration(leadSeconds * float64(time.Second)))
	end := chosen.Set.Add(time.Duration(tailSeconds * float64(time.Second)))

	var low, high float64
	first := true
	var startFreq float64
	for t := begin; !t.After(end); t = t.Add(10 * time.Second) {
		look, err := tracker.LookAngle(t, *latitude, *longitude, 0)
		if err != nil {
			continue
		}
		shift := look.DopplerShift(chaseFrequencyHz(sat))
		if first {
			low, high = shift, shift
			startFreq = dialFrequency(chaseFrequencyHz(sat), shift, sat.Mode)
			first = false
		}
		low = math.Min(low, shift)
		high = math.Max(high, shift)
	}
	return ChasePlan{
		Satellite: sat, Receiver: *receiver, Passes: passes,
		Start: begin, End: end, StartFreqHz: startFreq,
		DopplerLow:  int64(low),
		DopplerHigh: int64(high),
		TLEText:     text,
	}, nil
}

func bandNames(bands []catalogue.Band) string {
	names := make([]string, 0, len(bands))
	for _, band := range bands {
		names = append(names, band.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}

// chaseFrequencyHz is the RF frequency the chase actually listens on: the
// first published beacon when one exists (a transponder's DownlinkHz is the
// passband centre, which usually carries nothing by itself), otherwise the
// downlink itself.
func chaseFrequencyHz(sat satellites.Sat) float64 {
	if len(sat.Beacons) > 0 && sat.Beacons[0] > 0 {
		return float64(sat.Beacons[0])
	}
	return float64(sat.DownlinkHz)
}

// chaseSuffix marks whether the chase frequency is a beacon, so plan logs
// state what will actually be recorded rather than the transponder centre.
func chaseSuffix(sat satellites.Sat) string {
	if chaseFrequencyHz(sat) != float64(sat.DownlinkHz) {
		return " (beacon)"
	}
	return ""
}

// toneOffsetHz is the audio offset the chase aims a beacon at in SSB modes.
// A WebSDR/OpenWebRX dial frequency is the LO, i.e. where 0 Hz audio sits.
// Tuning the LO to nominal+Doppler parks a USB beacon right at 0 Hz where
// the demodulator filter removes it, so for sideband modes the dial is
// offset to place the beacon at a comfortable audible tone instead. FM is
// unaffected: the dial is the carrier centre and the demodulator has capture
// range, so no offset applies there.
const toneOffsetHz = 700.0

// dialFrequency is the receiver LO to command for a signal at rfHz given the
// current Doppler shift, so the signal lands at +toneOffsetHz (SSB) or at the
// centre of the channel (FM).
func dialFrequency(rfHz float64, dopplerHz float64, mode string) float64 {
	received := rfHz + dopplerHz
	switch strings.ToUpper(mode) {
	case "USB", "CW":
		return received - toneOffsetHz
	case "LSB":
		return received + toneOffsetHz
	default: // FM: centre the channel on the dial
		return received
	}
}

// modeFor translates a satellite mode into the receiver's own vocabulary.
func modeFor(kind, mode string) string {
	if kind == "openwebrx" {
		switch strings.ToUpper(mode) {
		case "FM":
			return "nfm"
		case "USB":
			return "usb"
		case "LSB":
			return "lsb"
		case "CW":
			return "cw"
		}
	}
	return strings.ToUpper(mode)
}

// segment opens a fresh connection, tunes, records one segment, closes.
func segment(ctx context.Context, plan ChasePlan, frequencyHz float64,
	window time.Duration) ([]int16, int, error) {
	kind := plan.Receiver.Kind
	mode := modeFor(kind, plan.Satellite.Mode)
	if kind == "openwebrx" {
		client, err := openwebrx.New(plan.Receiver.URL, openwebrx.DefaultOptions())
		if err != nil {
			return nil, 0, err
		}
		if _, err := client.Connect(ctx); err != nil {
			return nil, 0, err
		}
		defer client.Close()
		freq := int64(frequencyHz)
		if err := client.Tune(ctx, &freq, mode, nil); err != nil {
			return nil, 0, err
		}
		return client.RecordSamples(ctx, window)
	}
	client, err := websdr.New(plan.Receiver.URL, websdr.Options{
		Reconnect: false, ReconnectInitial: time.Second, ReconnectMax: 30 * time.Second,
		OpenTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second})
	if err != nil {
		return nil, 0, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, 0, err
	}
	defer client.Close()
	kHz := frequencyHz / 1000.0
	if err := client.Tune(ctx, kHz, mode, nil); err != nil {
		return nil, 0, err
	}
	return client.Record(ctx, window)
}

// Chase runs plan to completion, writing audio to outPath. say is called with
// progress lines; nil discards them. Returns the number of samples written.
func Chase(ctx context.Context, plan ChasePlan, outPath string,
	retuneThresholdHz float64, minRetuneInterval time.Duration,
	segmentSeconds float64, offline bool, say func(string),
) (int, error) {
	if say == nil {
		say = func(string) {}
	}
	text := plan.TLEText
	tracker, err := loadTracker(plan.Satellite.Name, plan.Satellite.Group, text, offline)
	if err != nil {
		return 0, err
	}
	latitude, longitude := plan.Receiver.Latitude, plan.Receiver.Longitude
	nominal := chaseFrequencyHz(plan.Satellite)

	wait := time.Until(plan.Start)
	if wait > 0 {
		say(fmt.Sprintf("waiting %.0f s for the pass to begin", wait.Seconds()))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	var collected []int16
	rate := 12000
	current := plan.StartFreqHz
	var lastRetune time.Time
	failures := 0

	for time.Now().Before(plan.End) {
		now := time.Now()
		look, err := tracker.LookAngle(now, latitude, longitude, 0)
		if err != nil {
			look = tracking.LookAngle{Time: now}
		}
		wanted := nominal
		if err == nil {
			wanted = dialFrequency(nominal, look.DopplerShift(nominal), plan.Satellite.Mode)
		}
		if math.Abs(wanted-current) >= retuneThresholdHz && time.Since(lastRetune) >= minRetuneInterval {
			say(fmt.Sprintf("  el %5.1f deg  Doppler %+6d Hz  tune %.4f MHz",
				look.Elevation, int64(wanted-nominal), wanted/1e6))
			current = wanted
			lastRetune = now
		}

		remaining := time.Until(plan.End).Seconds()
		if remaining <= 0 {
			break
		}
		window := math.Min(segmentSeconds, remaining)
		samples, segmentRate, err := segment(ctx, plan, current, time.Duration(window*float64(time.Second)))
		if err != nil {
			failures++
			say(fmt.Sprintf("  segment failed (%s); retrying in %.0f s",
				err, math.Min(float64(failures), 10)))
			select {
			case <-time.After(time.Duration(math.Min(float64(failures), 10)) * time.Second):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			continue
		}
		failures = 0
		if len(samples) > 0 {
			collected = append(collected, samples...)
			if segmentRate > 0 {
				rate = segmentRate
			}
			say(fmt.Sprintf("  +%d samples (%.1f s of audio, total %.1f s)",
				len(samples), float64(len(samples))/float64(rate), float64(len(collected))/float64(rate)))
		}
	}
	if len(collected) == 0 {
		return 0, fmt.Errorf("no audio captured during the pass")
	}
	file, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	if _, err := common.WriteWAV(file, collected, rate, 1); err != nil {
		return 0, err
	}
	say(fmt.Sprintf("wrote %s: %d samples at %d Hz (%.1f s)",
		outPath, len(collected), rate, float64(len(collected))/float64(rate)))
	return len(collected), nil
}
