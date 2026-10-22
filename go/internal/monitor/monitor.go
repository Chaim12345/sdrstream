// Package monitor watches a receiver for activity of any kind.
//
// The satellite tools ask "is a bird overhead right now". This package asks
// the broader question: what is on the air at all? A receiver's own FFT
// gives a live picture of the band; peaks that rise above the noise floor
// are transmissions, whoever sent them. Each transmission is optionally
// recorded, classified (voice, data, CW, carrier) and then decoded: data
// goes through the packet and paging decoders, voice goes through speech
// recognition.
package monitor

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/verify"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

// Source is a receiver the monitor can watch and listen to.
type Source interface {
	// Spectrum returns the latest FFT bins plus the centre frequency and
	// the span they cover. ok is false when the receiver sends no spectrum.
	Spectrum() (bins []float64, centreHz, spanHz float64, ok bool)
	// Listen retunes to hz and records dur of audio.
	Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error)
	// Close releases the connection.
	Close() error
}

// Activity is one detected transmission.
type Activity struct {
	ID                 int        `json:"id"`
	Started            time.Time  `json:"started"`
	Ended              *time.Time `json:"ended,omitempty"` // nil when the transmission was not recorded
	FrequencyHz        float64    `json:"frequency_hz"`
	BandwidthHz        float64    `json:"bandwidth_hz"`
	LevelDB            float64    `json:"level_db"`
	Kind               string     `json:"kind"`             // voice, data, cw, carrier, noise, unknown
	Mode               string     `json:"mode,omitempty"`   // decoder or modulation that produced the detail
	Detail             string     `json:"detail,omitempty"` // decoded payload or transcript
	VerificationAnchor string     `json:"-"`                // expected callsign/phrase for the live quality gate
	Label              string     `json:"label,omitempty"`
	Source             string     `json:"source"`
	DurationSec        float64    `json:"duration_sec"`
	AudioPath          string     `json:"audio_path,omitempty"`
	Confidence         float64    `json:"confidence,omitempty"`
	Language           string     `json:"language,omitempty"`
	ASRModel           string     `json:"asr_model,omitempty"`
	ASRFallback        bool       `json:"asr_fallback,omitempty"`
}

// Options configures a monitor.
type Options struct {
	Source Source
	// Strategy is sweep, spectrum, or broadcast (fixed-frequency voice).
	Strategy string
	// StartHz, EndHz and StepHz define the sweep range.
	StartHz float64
	EndHz   float64
	StepHz  float64
	// Dwell is how long the sweep listens at each step.
	Dwell time.Duration
	// ThresholdDB is how far above the noise floor a bin must sit.
	ThresholdDB float64
	// DwellTime is how long a peak must persist before it is reported.
	DwellTime time.Duration
	// Cooldown is how long a frequency is not reported again after it
	// has been seen.
	Cooldown time.Duration
	// Listen is how much audio to record per transmission; 0 skips it.
	Listen time.Duration
	// Mode is the modulation used when listening ("auto" picks per signal).
	Mode string
	// Voice enables speech recognition for voice transmissions.
	Voice bool
	// VoiceOptions configures the speech backend.
	VoiceOptions voice.Options
	// VoiceNIMOptions configures the hosted NVIDIA speech backend, which is
	// tried first when an API key is set (empty disables it).
	VoiceNIMOptions voice.NIMOptions
	// MultimonBinary and DirewolfBinary opt in to local external decoders.
	MultimonBinary string
	DirewolfBinary string
	// OutDir saves a WAV of every transmission.
	OutDir string
	// Channels, when set with the broadcast strategy, is the rotation of
	// fixed voice channels to visit (see DefaultVoiceChannels).
	Channels []Channel
	// ChannelPause is the rest between channel visits; it keeps the
	// duty cycle inside the load budget. Default 2s.
	ChannelPause time.Duration
	// Name identifies the receiver in reports.
	Name  string
	Label string
	// OnActivity is called for every new or updated activity.
	OnActivity func(Activity)
	// OnImage receives verified reconstructed images with their activity metadata.
	OnImage func(Activity, []byte)
	// Logf receives progress messages.
	Logf func(format string, args ...interface{})
}

// job is a transmission that has been found and now has to be recorded,
// classified and decoded.
type job struct {
	frequency  float64
	bandwidth  float64
	levelDB    float64
	kind       string
	modes      []string
	activityID int
}

// Monitor watches a source and keeps the activity log.
type Monitor struct {
	opts Options
	jobs chan job

	mu         sync.Mutex
	activities []Activity
	nextID     int
	active     map[string]*candidate
	reported   map[string]time.Time
	spectrum   []float64
	centreHz   float64
	spanHz     float64
	spectrumOK bool
	tunedHz    float64
	bc         broadcastState
}

// candidate is a peak that is currently present in the spectrum.
type candidate struct {
	key        string
	frequency  float64
	bandwidth  float64
	levelDB    float64
	firstSeen  time.Time
	lastSeen   time.Time
	reported   bool
	activityID int
}

// New builds a monitor.
func New(options Options) *Monitor {
	if options.ThresholdDB <= 0 {
		options.ThresholdDB = 8
	}
	if options.DwellTime <= 0 {
		options.DwellTime = 1500 * time.Millisecond
	}
	if options.Cooldown <= 0 {
		options.Cooldown = 60 * time.Second
	}
	if options.Dwell <= 0 {
		options.Dwell = 400 * time.Millisecond
	}
	if options.StepHz <= 0 {
		options.StepHz = 25000
	}
	if options.Strategy == "" {
		options.Strategy = "sweep"
	}
	if options.Mode == "" {
		options.Mode = "auto"
	}
	if options.Logf == nil {
		options.Logf = func(string, ...interface{}) {}
	}
	if options.ChannelPause <= 0 {
		options.ChannelPause = 2 * time.Second
	}
	return &Monitor{opts: options, active: map[string]*candidate{},
		reported: map[string]time.Time{}, jobs: make(chan job, 16)}
}

// Activities returns the activity log, newest first.
func (m *Monitor) Activities() []Activity {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Activity, 0, len(m.activities))
	for i := len(m.activities) - 1; i >= 0; i-- {
		out = append(out, m.activities[i])
	}
	return out
}

// Spectrum returns the latest spectrum frame.
func (m *Monitor) Spectrum() ([]float64, float64, float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spectrum, m.centreHz, m.spanHz, m.spectrumOK
}

// OnAirPeak is a transmission currently visible in the spectrum.
type OnAirPeak struct {
	FrequencyHz float64 `json:"frequency_hz"`
	BandwidthHz float64 `json:"bandwidth_hz"`
	LevelDB     float64 `json:"level_db"`
}

// LiveInfo is a point-in-time snapshot of what the monitor sees right now:
// the receiver identity, the noise floor and the peaks above it.
type LiveInfo struct {
	Available    bool        `json:"available"`
	Name         string      `json:"name"`
	Label        string      `json:"label,omitempty"`
	Strategy     string      `json:"strategy"`
	SpectrumOK   bool        `json:"spectrum_ok"`
	CentreHz     float64     `json:"centre_hz"`
	SpanHz       float64     `json:"span_hz"`
	NoiseFloorDB float64     `json:"noise_floor_db"`
	PeakDB       float64     `json:"peak_db"`
	ThresholdDB  float64     `json:"threshold_db"`
	TunedHz      float64     `json:"tuned_hz,omitempty"`
	OnAir        []OnAirPeak `json:"on_air"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// Live snapshots the monitor's current view for the live UI: receiver
// identity, noise floor, peak level and the peaks above the threshold.
func (m *Monitor) Live() LiveInfo {
	m.mu.Lock()
	bins := append([]float64(nil), m.spectrum...)
	centre, span, ok := m.centreHz, m.spanHz, m.spectrumOK
	tuned := m.tunedHz
	m.mu.Unlock()
	info := LiveInfo{
		Name: m.opts.Name, Label: m.opts.Label, Strategy: m.opts.Strategy,
		SpectrumOK: ok, CentreHz: centre, SpanHz: span,
		ThresholdDB: m.opts.ThresholdDB, UpdatedAt: time.Now(),
		TunedHz: tuned, OnAir: []OnAirPeak{},
	}
	if !ok || len(bins) == 0 {
		return info
	}
	ordered := append([]float64(nil), bins...)
	sort.Float64s(ordered)
	info.NoiseFloorDB = ordered[len(ordered)/2]
	info.PeakDB = ordered[len(ordered)-1]
	for _, p := range m.detect(bins, centre, span) {
		info.OnAir = append(info.OnAir, OnAirPeak{FrequencyHz: p.frequency, BandwidthHz: p.bandwidth, LevelDB: p.levelDB})
	}
	return info
}

// RequestListen parks the monitor on a frequency the user clicked in the
// web waterfall and queues an immediate listen there, ahead of whatever
// the detector would find on its own. The tuned frequency shows up in
// Live snapshots as tuned_hz. queued is false when the worker is busy or
// listening is disabled; the tuned marker still moves.
func (m *Monitor) RequestListen(hz float64, mode string) (queued bool) {
	if hz <= 0 {
		return false
	}
	m.mu.Lock()
	m.tunedHz = hz
	m.nextID++
	activity := Activity{
		ID: m.nextID, Started: time.Now(), FrequencyHz: hz,
		BandwidthHz: 12500, Kind: "unknown",
		Source: m.opts.Name, Label: m.opts.Label,
	}
	m.activities = append(m.activities, activity)
	if len(m.activities) > 200 {
		m.activities = m.activities[len(m.activities)-200:]
	}
	m.mu.Unlock()
	if m.opts.Listen <= 0 {
		return false
	}
	if mode == "" {
		mode = ChooseMode(m.opts.Mode, activity.BandwidthHz)
	}
	select {
	case m.jobs <- job{frequency: hz, bandwidth: activity.BandwidthHz, modes: []string{mode}, activityID: activity.ID}:
		return true
	default:
		return false
	}
}

// Run watches the source until the context is cancelled.
func (m *Monitor) Run(ctx context.Context) error {
	// Recording and decoding a transmission takes seconds, sometimes a
	// minute when whisper is involved, so it happens in a worker: the
	// detector has to keep watching while that runs.
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		m.worker(ctx)
	}()
	defer func() { <-workerDone }()
	if m.opts.Strategy == "spectrum" {
		return m.runSpectrum(ctx)
	}
	if m.opts.Strategy == "broadcast" {
		return m.runBroadcast(ctx)
	}
	return m.runSweep(ctx)
}

// worker records and decodes the transmissions the detector finds.
func (m *Monitor) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-m.jobs:
			m.handleJob(ctx, job)
		}
	}
}

// runSweep walks the band, listening at every step. This works with any
// receiver, including the ones that send no spectrum, and it is the only
// way to be sure that what was heard was really there.
//
// The sweep runs in two passes. The first surveys the band and notes the
// level at every step; a step only counts as traffic when it stands out
// against the median of the band, because every receiver has a noise floor
// and the floor itself looks like a signal to a per-file classifier. The
// second pass listens longer at the frequencies that stood out and decodes
// them.
// runBroadcast records one fixed-frequency broadcast clip at a time.
func (m *Monitor) runBroadcast(ctx context.Context) error {
	if len(m.opts.Channels) > 0 {
		return m.runChannels(ctx)
	}
	if m.opts.StartHz <= 0 {
		return fmt.Errorf("monitor: broadcast strategy needs --start or channels")
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.report(ctx, &candidate{frequency: m.opts.StartHz, firstSeen: time.Now()})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.opts.Listen + 2*time.Second):
		}
	}
}

func (m *Monitor) runSweep(ctx context.Context) error {
	start := m.opts.StartHz
	end := m.opts.EndHz
	if end <= start {
		return fmt.Errorf("monitor: the sweep range is empty (%.0f-%.0f Hz)", start, end)
	}
	for {
		if err := m.sweepOnce(ctx, start, end); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// sweepOnce surveys the band once and investigates what stands out.
func (m *Monitor) sweepOnce(ctx context.Context, start, end float64) error {
	m.opts.Logf("sweeping %.4f-%.4f MHz in %.0f kHz steps, %.0f ms per step",
		start/1e6, end/1e6, m.opts.StepHz/1e3, float64(m.opts.Dwell)/float64(time.Millisecond))

	var survey []step
	// The step is counted rather than accumulated: adding a step in
	// floating point drifts, and the last step of the band would be
	// missed.
	for index := 0; ; index++ {
		frequency := math.Round(start + float64(index)*m.opts.StepHz)
		if frequency > end {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		m.updateSpectrum()
		// "auto" is not a modulation any receiver understands, so the
		// survey always asks for a real one.
		samples, rate, err := m.opts.Source.Listen(ctx, frequency, m.surveyMode(), m.opts.Dwell)
		if err != nil || len(samples) < rate/20 {
			continue
		}
		hit := decode.Classify(samples, rate)
		modulated := voice.Modulated(samples, rate)
		survey = append(survey, step{frequency: frequency, levelDB: hit.LevelDB, modulated: modulated,
			concentrated: hit.Kind == "data" || hit.Kind == "cw"})
	}
	if len(survey) == 0 {
		return ctx.Err()
	}
	levels := make([]float64, len(survey))
	for i, s := range survey {
		levels[i] = s.levelDB
	}
	floor := median(levels)
	limit := floor + m.opts.ThresholdDB
	// A receiver with AGC gives every step the same level, which makes
	// the level useless for telling traffic from noise. Only when the
	// levels actually differ across the band does the threshold mean
	// something.
	normalised := spread(levels) < 6
	m.opts.Logf("band surveyed: %d steps, noise floor %.0f dB, traffic above %.0f dB%s",
		len(survey), floor, limit, map[bool]string{true: " (levels are normalised, using modulation instead)"}[normalised])

	// Group the steps that stand out into frequency ranges. A step counts
	// when the audio is modulated or when the spectrum is concentrated on
	// one tone, and, where the receiver does not normalise the level, when
	// it is louder than the band.
	var ranges [][2]float64
	for _, s := range survey {
		if !s.modulated && !s.concentrated {
			continue
		}
		if !normalised && s.levelDB < limit {
			continue
		}
		if len(ranges) > 0 && s.frequency-ranges[len(ranges)-1][1] <= m.opts.StepHz*1.5 {
			ranges[len(ranges)-1][1] = s.frequency
			continue
		}
		ranges = append(ranges, [2]float64{s.frequency, s.frequency})
	}
	for _, r := range ranges {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		centre := (r[0] + r[1]) / 2
		m.opts.Logf("hit at %.4f MHz (%.0f kHz wide)", centre/1e6, (r[1]-r[0]+m.opts.StepHz)/1e3)
		m.handleHit(ctx, centre, decode.BandHit{Kind: "unknown", LevelDB: levelOf(survey, centre)})
	}
	return ctx.Err()
}

// step is one measurement of the sweep: the level at a frequency and
// whether the audio there looked like a signal.
type step struct {
	frequency    float64
	levelDB      float64
	modulated    bool
	concentrated bool
}

// levelOf returns the loudest level measured at or near a frequency.
func levelOf(survey []step, frequency float64) float64 {
	best := -100.0
	for _, s := range survey {
		if math.Abs(s.frequency-frequency) <= m_stepTolerance {
			if s.levelDB > best {
				best = s.levelDB
			}
		}
	}
	return best
}

// m_stepTolerance is how far a measurement may be from a reported centre
// frequency and still count as its level.
const m_stepTolerance = 25000

// spread returns the difference between the largest and the smallest
// value.
func spread(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	low, high := values[0], values[0]
	for _, v := range values {
		if v < low {
			low = v
		}
		if v > high {
			high = v
		}
	}
	return high - low
}

// median returns the middle value of a sorted copy of the values.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}

// handleHit records a transmission found by the sweep and hands it to the
// worker, which listens to it properly and decides what it is.
func (m *Monitor) handleHit(ctx context.Context, frequency float64, hit decode.BandHit) {
	activity := Activity{
		Started:     time.Now(),
		FrequencyHz: frequency,
		LevelDB:     hit.LevelDB,
		Kind:        "unknown",
		Source:      m.opts.Name,
		Label:       m.opts.Label,
	}
	m.mu.Lock()
	m.nextID++
	activity.ID = m.nextID
	m.activities = append(m.activities, activity)
	if len(m.activities) > 200 {
		m.activities = m.activities[len(m.activities)-200:]
	}
	m.mu.Unlock()

	// A wideband signal is FM voice, a narrow one is a sideband or a
	// beacon; try both and keep whichever gives the better answer.
	modes := []string{"usb", "fm"}
	if m.opts.Mode != "auto" && m.opts.Mode != "" {
		modes = []string{m.opts.Mode}
	}
	select {
	case m.jobs <- job{
		frequency:  frequency,
		levelDB:    hit.LevelDB,
		modes:      modes,
		activityID: activity.ID,
	}:
	default:
		m.publish(activity)
	}
}

// runSpectrum watches the receiver's own FFT and reports the peaks.
func (m *Monitor) runSpectrum(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.step(ctx)
		}
	}
}

// step looks at the current spectrum and updates the activity log.
func (m *Monitor) step(ctx context.Context) {
	bins, centre, span, ok := m.updateSpectrum()
	if !ok || len(bins) < 8 || span <= 0 {
		return
	}
	peaks := m.detect(bins, centre, span)
	now := time.Now()
	for _, peak := range peaks {
		key := peakKey(peak.frequency)
		existing, found := m.active[key]
		if !found {
			existing = &candidate{key: key, firstSeen: now}
			m.mu.Lock()
			m.active[key] = existing
			m.mu.Unlock()
		}
		existing.frequency = peak.frequency
		existing.bandwidth = peak.bandwidth
		if peak.levelDB > existing.levelDB {
			existing.levelDB = peak.levelDB
		}
		existing.lastSeen = now
		if existing.reported || now.Sub(existing.firstSeen) < m.opts.DwellTime {
			continue
		}
		// One transmission, one entry: a transmitter that stays on the
		// air is reported again only after the cooldown.
		m.mu.Lock()
		last := m.reported[key]
		m.mu.Unlock()
		if !last.IsZero() && now.Sub(last) < m.opts.Cooldown {
			continue
		}
		existing.reported = true
		m.mu.Lock()
		m.reported[key] = now
		m.mu.Unlock()
		m.report(ctx, existing)
	}
	// Retire peaks that are gone.
	m.mu.Lock()
	for key, candidate := range m.active {
		if now.Sub(candidate.lastSeen) > 3*time.Second {
			delete(m.active, key)
		}
	}
	m.mu.Unlock()
}

// updateSpectrum stores the receiver's latest spectrum frame so the web UI
// can draw it, whichever strategy is running.
func (m *Monitor) updateSpectrum() ([]float64, float64, float64, bool) {
	bins, centre, span, ok := m.opts.Source.Spectrum()
	m.mu.Lock()
	m.spectrum, m.centreHz, m.spanHz, m.spectrumOK = bins, centre, span, ok
	m.mu.Unlock()
	return bins, centre, span, ok
}

// peak is a run of bins above the noise floor.
type peak struct {
	frequency float64
	bandwidth float64
	levelDB   float64
}

// detect finds the transmissions in one spectrum frame.
//
// OpenWebRX sends a calibrated dB spectrum, so the median bin is the noise
// floor (most of the band is empty) and every bin above the threshold is
// real energy. A per-bin noise tracker would be wrong here: a carrier that
// never goes away would be absorbed into its own floor.
func (m *Monitor) detect(bins []float64, centre, span float64) []peak {
	if len(bins) < 8 || span <= 0 {
		return nil
	}
	// A noise-floor window must be wider than a real channel. With the
	// old 24-bin radius, a 25 kHz FM channel filled the whole local median
	// window at 125 Hz/bin and raised its own floor until it disappeared.
	// Estimate a slowly varying local floor every 512 bins from a 2048-bin
	// window, then interpolate between estimates. A lower quartile resists
	// occupied channels while still tracking receiver roll-off across the
	// profile. This keeps sorting work bounded instead of sorting per bin.
	const floorWindow, floorStride = 2048, 512
	type floorPoint struct {
		bin   int
		value float64
	}
	points := make([]floorPoint, 0, len(bins)/floorStride+2)
	for center := 0; center < len(bins); center += floorStride {
		low := center - floorWindow/2
		if low < 0 {
			low = 0
		}
		high := low + floorWindow
		if high > len(bins) {
			high = len(bins)
			low = max(0, high-floorWindow)
		}
		window := append([]float64(nil), bins[low:high]...)
		sort.Float64s(window)
		points = append(points, floorPoint{bin: center, value: window[len(window)/4]})
	}
	if points[len(points)-1].bin != len(bins)-1 {
		center := len(bins) - 1
		low := max(0, center-floorWindow/2)
		high := min(len(bins), low+floorWindow)
		low = max(0, high-floorWindow)
		window := append([]float64(nil), bins[low:high]...)
		sort.Float64s(window)
		points = append(points, floorPoint{bin: center, value: window[len(window)/4]})
	}
	binHz := span / float64(len(bins))
	floors := make([]float64, len(bins))
	point := 0
	for i := range bins {
		for point+1 < len(points)-1 && i > points[point+1].bin {
			point++
		}
		left, right := points[point], points[min(point+1, len(points)-1)]
		fraction := 0.0
		if right.bin > left.bin {
			fraction = float64(i-left.bin) / float64(right.bin-left.bin)
		}
		floors[i] = left.value + fraction*(right.value-left.value)
	}
	var peaks []peak
	start := -1
	for i := 0; i <= len(bins); i++ {
		above := false
		if i < len(bins) {
			above = bins[i] > floors[i]+m.opts.ThresholdDB
		}
		if above && start < 0 {
			start = i
		}
		if !above && start >= 0 {
			end := i - 1
			best, bestLevel := start, bins[start]
			// The carrier of an FM signal sits in the middle of the run,
			// not under the loudest bin, so the frequency estimate is the
			// power-weighted centroid of the run.
			var weight, weighted float64
			for j := start; j <= end; j++ {
				power := math.Pow(10, bins[j]/10)
				weight += power
				weighted += power * float64(j)
			}
			centroid := start
			if weight > 0 {
				centroid = int(math.Round(weighted / weight))
			}
			for j := start; j <= end; j++ {
				if bins[j] > bestLevel {
					best, bestLevel = j, bins[j]
				}
			}
			_ = best
			peaks = append(peaks, peak{
				frequency: centre - span/2 + (float64(centroid)+0.5)*binHz,
				bandwidth: float64(end-start+1) * binHz,
				levelDB:   bestLevel - floors[best],
			})
			start = -1
		}
	}
	// The bins at the centre of the profile hold the server's own
	// demodulated channel, which shows up as a permanent spike whose
	// centroid can sit a few kHz off centre on a high-resolution FFT.
	// Drop the whole neighbourhood, not only the exact bin: a quiet band
	// with one real signal otherwise reports the demodulator forever.
	// The guard is never narrower than 3 kHz, the width of the narrowest
	// demodulator the server can run.
	guard := math.Max(4*binHz, 3000)
	kept := peaks[:0]
	for _, p := range peaks {
		if math.Abs(p.frequency-centre) < guard {
			continue
		}
		kept = append(kept, p)
	}
	peaks = kept
	// The first and last bins sit on the edge of the profile, where the
	// server's filter rolls off and produces phantom peaks.
	if len(peaks) > 0 && peaks[0].frequency-centre+span/2 < 2*binHz {
		peaks = peaks[1:]
	}
	if len(peaks) > 0 && centre+span/2-peaks[len(peaks)-1].frequency < 2*binHz {
		peaks = peaks[:len(peaks)-1]
	}
	return peaks
}

// ChooseMode picks a modulation for a transmission: a wide signal is
// almost always FM voice, a narrow one is a sideband or a data burst.
func ChooseMode(mode string, bandwidthHz float64) string {
	if mode != "auto" && mode != "" {
		return mode
	}
	if bandwidthHz > 12000 {
		return "fm"
	}
	return "usb"
}

// surveyMode is the modulation used while sweeping. The sweep does not
// know the signal's bandwidth yet, so it uses a sideband mode, which also
// passes packet signals through well enough to decode them.
func (m *Monitor) surveyMode() string {
	if m.opts.Mode != "auto" && m.opts.Mode != "" {
		return m.opts.Mode
	}
	return "usb"
}

// peakKey buckets a frequency so the same transmission keeps its identity.
func peakKey(frequency float64) string {
	return fmt.Sprintf("%.0f", math.Round(frequency/2000)*2000)
}

// report records a new activity and, when listening is enabled, decodes it.
// report records a new activity and hands it to the worker, which does the
// recording and the decoding.
func (m *Monitor) report(ctx context.Context, candidate *candidate) {
	// Recording and decoding take much longer than spotting a signal, so
	// when the worker is behind the new detection is dropped rather than
	// queued: a queue of noise would keep the worker busy for minutes.
	if len(m.jobs) >= cap(m.jobs) {
		m.opts.Logf("worker is busy, skipping %.4f MHz", candidate.frequency/1e6)
		return
	}
	activity := Activity{
		Started:     candidate.firstSeen,
		FrequencyHz: candidate.frequency,
		BandwidthHz: candidate.bandwidth,
		LevelDB:     candidate.levelDB,
		Kind:        "unknown",
		Source:      m.opts.Name,
		Label:       m.opts.Label,
	}
	m.mu.Lock()
	m.nextID++
	activity.ID = m.nextID
	m.activities = append(m.activities, activity)
	if len(m.activities) > 200 {
		m.activities = m.activities[len(m.activities)-200:]
	}
	m.mu.Unlock()
	candidate.activityID = activity.ID
	m.opts.Logf("activity #%d %.4f MHz (%.0f kHz wide, %.1f dB above the floor)",
		activity.ID, activity.FrequencyHz/1e6, activity.BandwidthHz/1e3, activity.LevelDB)

	if m.opts.Listen <= 0 {
		m.publish(activity)
		return
	}
	select {
	case m.jobs <- job{
		frequency:  candidate.frequency,
		bandwidth:  candidate.bandwidth,
		levelDB:    candidate.levelDB,
		modes:      []string{ChooseMode(m.opts.Mode, candidate.bandwidth)},
		activityID: activity.ID,
	}:
	default:
		// The worker is busy; report the detection without a recording.
		m.publish(activity)
	}
}

// handleJob records a transmission and works out what it is.
func (m *Monitor) handleJob(ctx context.Context, work job) {
	activity := Activity{
		ID: work.activityID, Started: time.Now(), FrequencyHz: work.frequency,
		BandwidthHz: work.bandwidth, LevelDB: work.levelDB, Kind: "unknown",
		Source: m.opts.Name, Label: m.opts.Label,
	}
	modes := work.modes
	if len(modes) == 0 {
		modes = []string{m.surveyMode()}
	}
	best := activity
	found := false
	var bestImage []byte
	for _, mode := range modes {
		samples, rate, err := m.opts.Source.Listen(ctx, work.frequency, mode, m.opts.Listen)
		// The transmission ended when the receiver stopped hearing it,
		// not when decoding finished: whisper can take a minute, and
		// that time is not part of the transmission.
		listenEnd := time.Now()
		if err != nil || len(samples) == 0 {
			if err != nil {
				m.opts.Logf("listen at %.4f MHz failed: %v", work.frequency/1e6, err)
			} else {
				m.opts.Logf("listen at %.4f MHz returned no audio", work.frequency/1e6)
			}
			continue
		}
		kind, modeName, detail, asr := m.classify(ctx, samples, rate, work.bandwidth)
		var imageData []byte
		if kind == "sstv" && bestImage == nil {
			imageData, _ = decode.SSTVImage(samples, rate)
			if len(imageData) > 0 && verify.Live("sstv", "sstv", "", imageData) != nil {
				imageData = nil
			}
		}
		// Mode is the modulation or decoder, never a verdict; when no
		// decoder produced a name, the modulation that was listened to
		// is the honest answer.
		if modeName == "" {
			modeName = mode
		}
		candidate := Activity{
			ID: activity.ID, Started: activity.Started, Ended: &listenEnd,
			FrequencyHz: work.frequency, BandwidthHz: work.bandwidth, LevelDB: work.levelDB,
			Kind: kind, Mode: modeName, Detail: detail, Source: m.opts.Name, Label: m.opts.Label,
			DurationSec: float64(len(samples)) / float64(rate),
			Confidence:  asr.Confidence, Language: asr.Language, ASRModel: asr.Model, ASRFallback: asr.Fallback, VerificationAnchor: activity.VerificationAnchor,
		}
		if kind == "noise" || kind == "silence" || kind == "unknown" {
			continue
		}
		if m.opts.OutDir != "" {
			path := filepath.Join(m.opts.OutDir, fmt.Sprintf("activity-%03d-%d-%s.wav",
				activity.ID, int(work.frequency), modeName))
			if err := voice.WriteWAV(path, samples, rate); err == nil {
				candidate.AudioPath = path
			}
		}
		// A verdict with content beats one without; otherwise the first
		// classification stands, so the activity never stays "unknown".
		if !found || (detail != "" && best.Detail == "") {
			best = candidate
			bestImage = imageData
			found = true
		}
	}
	m.mu.Lock()
	for i := range m.activities {
		if m.activities[i].ID == activity.ID {
			activity.Started = m.activities[i].Started
			if best.Kind == "noise" || best.Kind == "silence" || best.Kind == "unknown" {
				// Do not flood the feed with detector false positives. Keep
				// genuine decoded payloads and voice, not background noise.
				m.activities = append(m.activities[:i], m.activities[i+1:]...)
				break
			}
			m.activities[i] = best
		}
	}
	m.mu.Unlock()
	if best.Kind != "noise" && best.Kind != "silence" && best.Kind != "unknown" {
		m.publish(best)
		if best.Kind == "sstv" && len(bestImage) > 0 && m.opts.OnImage != nil && verify.Live(best.Kind, best.Mode, "", bestImage) == nil {
			m.opts.OnImage(best, bestImage)
		}
	}
}

// publish hands an activity to the callback.
func (m *Monitor) publish(activity Activity) {
	if m.opts.OnActivity != nil {
		m.opts.OnActivity(activity)
	}
}

// classify decides what a recording contains and decodes it.
//
// The data decoders run first: a successful AX.25, PSK31 or paging decode
// is proof that the signal was data, while a "voice" verdict from the
// spectrum alone is only a guess.
func (m *Monitor) classify(ctx context.Context, samples []float64, rate int, bandwidthHz float64) (kind, mode, detail string, asr voice.Result) {
	// The sweep path never measures the channel width, so the narrow
	// decoders would otherwise fire on every loud signal. Measure the
	// signal itself instead of trusting a zero that means "unknown".
	if bandwidthHz <= 0 {
		bandwidthHz = decode.OccupiedBandwidth(samples, rate)
	}
	hit := decode.Classify(samples, rate)
	// Two shapes of real signal: a modulated one, whose level swings with
	// the speech or the data, and a concentrated one, whose energy sits in
	// a narrow band (a beacon, a packet burst). A receiver's noise floor
	// is neither, and handing it to the decoders only invents traffic.
	modulated := voice.Modulated(samples, rate)
	concentrated := hit.Kind == "data" || hit.Kind == "cw"
	if !modulated && !concentrated {
		if hit.Kind == "silence" {
			return "silence", "", "", asr
		}
		return "noise", "", "no signal above the noise floor", asr
	}
	if m.opts.Strategy != "broadcast" {
		if mode, detail, ok := m.decodeData(ctx, samples, rate, bandwidthHz); ok {
			if mode == "morse" {
				return "cw", "cw", detail, asr
			}
			return "data", mode, detail, asr
		}
	}
	if !modulated {
		return "carrier", "", "steady signal, no modulation", asr
	}
	// A very narrow signal is a carrier or a keyed tone, not a voice.
	if bandwidthHz > 0 && bandwidthHz < 2500 && hit.Kind != "silence" {
		return "carrier", "", fmt.Sprintf("%.0f kHz wide", bandwidthHz/1e3), asr
	}
	switch hit.Kind {
	case "voice", "data", "cw":
		segments := voice.DetectSpeech(samples, rate, voice.VADOptions{})
		if len(segments) == 0 {
			m.opts.Logf("voice: signal present but no speech detected")
			return hit.Kind, "", "no speech detected", asr
		}
		if m.opts.Voice {
			var transcript voice.Transcript
			var err error
			if voice.NIMAvailable(m.opts.VoiceNIMOptions) || voice.Available(m.opts.VoiceOptions) {
				transcript, err = voice.TranscribeBest(ctx, samples, rate, m.opts.VoiceOptions, m.opts.VoiceNIMOptions)
			}
			if err == nil && transcript.Text != "" {
				verdict := voice.GuardTranscript(samples, rate, transcript.Text)
				if verdict.Rejected {
					m.opts.Logf("voice: transcript dropped: %s", verdict.Reason)
					return "voice", "speech", "", asr
				}
				return "voice", "speech", verdict.Text, asr
			}
		}
		m.opts.Logf("voice: no backend or empty transcript; %d speech segment(s), %.1f s", len(segments), segments[0].End-segments[0].Start)
		return "voice", "speech", "", asr
	case "silence":
		return "silence", "", "", asr
	default:
		return hit.Kind, "", "", asr
	}
}

// ClassifyForScan exposes the monitor's existing receive-only classifiers to the bounded multi-station scanner.
func (m *Monitor) ClassifyForScan(ctx context.Context, samples []float64, rate int, bandwidthHz float64) (kind, mode, detail string, ok bool) {
	kind, mode, detail, _ = m.classify(ctx, samples, rate, bandwidthHz)
	return kind, mode, detail, detail != "" || kind == "voice" || kind == "sstv"
}

// decodeData tries every data decoder and returns the first result.
//
// bandwidthHz is the width of the channel the transmission was heard in, or
// 0 when it is not known. The narrowband decoders (CW, RTTY, PSK31, NAVTEX,
// SSTV) are only tried on a narrow channel: they will happily return
// plausible-looking text from any loud narrowband-looking audio, and on an
// AM aviation voice channel that turned every call into "RTTY" instead of
// letting the speech detector and whisper look at it. A POCSAG pager
// channel is 12.5 kHz wide and packet data is a few kHz, so those are not
// gated.
func (m *Monitor) decodeData(ctx context.Context, samples []float64, rate int, bandwidthHz float64) (mode, detail string, ok bool) {
	narrowChannel := bandwidthHz <= 0 || bandwidthHz <= 4000
	// APRS and Morse need a file, so the samples go to a temporary WAV.
	path, err := writeTempWAV(samples, rate)
	if err == nil {
		defer os.Remove(path)
		packets, _ := decode.AFSKFrames(path)
		if len(packets) > 0 {
			lines := make([]string, 0, len(packets))
			for _, packet := range packets {
				lines = append(lines, packet.String())
			}
			return "aprs", strings.Join(lines, " | "), true
		}
		if m.opts.MultimonBinary != "" && ctx.Err() == nil {
			lines, err := decode.MultimonDecodeWithBinary(ctx, m.opts.MultimonBinary, path, "AFSK1200")
			if err == nil && len(lines) > 0 {
				valid := make([]string, 0, len(lines))
				for _, line := range lines {
					if strings.Contains(line, ">") && strings.Contains(line, ":") {
						valid = append(valid, line)
					}
				}
				if len(valid) > 0 {
					return "aprs", strings.Join(valid, " | "), true
				}
			}
		}
		if m.opts.DirewolfBinary != "" && ctx.Err() == nil {
			lines, err := decode.DirewolfDecode(ctx, m.opts.DirewolfBinary, samples, rate)
			if err == nil && len(lines) > 0 {
				return "aprs", strings.Join(lines, " | "), true
			}
		}
		if narrowChannel {
			if result, err := decode.MorseDecode(path); err == nil && result.Text != "" {
				return "morse", result.Text, true
			}
			if m.opts.MultimonBinary != "" && ctx.Err() == nil {
				lines, err := decode.MultimonDecodeWithBinary(ctx, m.opts.MultimonBinary, path, "MORSE_CW")
				if err == nil {
					for _, line := range lines {
						text := strings.TrimSpace(line)
						if validExternalCW(text) {
							return "morse", text, true
						}
					}
				}
			}
		}
	}
	if text := decode.DTMFDecode(samples, rate); text != "" {
		return "dtmf", text, true
	}
	for _, baud := range []int{1200, 2400} {
		if pages := decode.POCSAGDecode(samples, rate, baud); len(pages) > 0 {
			lines := make([]string, 0, len(pages))
			for _, page := range pages {
				text := page.Alpha
				if text == "" {
					text = page.Numeric
				}
				lines = append(lines, fmt.Sprintf("%d: %s", page.Address, text))
			}
			return "pocsag", strings.Join(lines, " | "), true
		}
	}
	if alerts := decode.EASDecode(samples, rate); len(alerts) > 0 {
		lines := make([]string, 0, len(alerts))
		for _, alert := range alerts {
			lines = append(lines, alert.Event+" "+alert.Origin+" "+strings.Join(alert.Locations, " ")+" "+alert.Duration)
		}
		return "eas", strings.Join(lines, " | "), true
	}
	if narrowChannel {
		if messages := decode.NAVTEXDecode(samples, rate, 0); len(messages) > 0 {
			lines := make([]string, 0, len(messages))
			for _, message := range messages {
				lines = append(lines, message.Raw)
			}
			return "navtex", strings.Join(lines, " | "), true
		}
		if text := decode.PSK31Decode(samples, rate, 0); text != "" {
			return "psk31", text, true
		}
		if text := decode.RTTYDecode(samples, rate, 0, 0, 0); text != "" {
			return "rtty", text, true
		}
		// SSTV is intentionally disabled in live classification until the
		// independent per-line sync decoder passes real-frame review.
	}
	return "", "", false
}

// writeTempWAV stores samples in a temporary WAV file.
func writeTempWAV(samples []float64, rate int) (string, error) {
	file, err := os.CreateTemp("", "sdrstream-monitor-*.wav")
	if err != nil {
		return "", err
	}
	path := file.Name()
	file.Close()
	if err := voice.WriteWAV(path, samples, rate); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// Kinds lists the activity kinds, for help text and documentation.
func Kinds() []string {
	kinds := []string{"voice", "data", "cw", "carrier", "noise", "silence", "unknown"}
	sort.Strings(kinds)
	return kinds
}

func validExternalCW(text string) bool {
	if len(text) < 5 || strings.Contains(strings.ToUpper(text), "ERR") || strings.ContainsAny(text, "<>_") {
		return false
	}
	letters, words := 0, 0
	for _, r := range text {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			letters++
		}
		if r == ' ' {
			words++
		}
	}
	return letters >= 5 && words >= 1
}
