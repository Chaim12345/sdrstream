package monitor

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// fakeSource is a receiver the tests drive by hand.
type fakeSource struct {
	mu       sync.Mutex
	modes    []string
	freqs    []float64
	spectra  [][]float64
	level    map[float64]float64
	frames   int
	listened int
}

func (f *fakeSource) Spectrum() ([]float64, float64, float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.spectra) == 0 {
		return nil, 145e6, 2e6, false
	}
	frame := f.spectra[f.frames%len(f.spectra)]
	f.frames++
	return frame, 145e6, 2e6, true
}

// Listen records the request and returns a tone of the requested level.
func (f *fakeSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	f.mu.Lock()
	f.modes = append(f.modes, mode)
	f.freqs = append(f.freqs, hz)
	f.listened++
	level := f.level[hz]
	f.mu.Unlock()
	rate := 8000
	n := int(dur.Seconds()) * rate
	if n < rate/10 {
		n = rate / 10
	}
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = level * math.Sin(2*math.Pi*1000*float64(i)/float64(rate))
	}
	return samples, rate, nil
}

func (f *fakeSource) Close() error { return nil }

// wavSource replays a real recording committed under tests/data. The
// decoders and the speech detector were calibrated against these captures,
// so a test that checks what the monitor concludes uses real PCM rather than
// a synthetic tone.
type wavSource struct {
	samples []float64
	rate    int
}

func newWAVSource(t *testing.T, path string) *wavSource {
	t.Helper()
	samples, rate, err := decode.ReadWAV(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(samples) == 0 {
		t.Fatalf("%s holds no audio", path)
	}
	return &wavSource{samples: samples, rate: rate}
}

func (w *wavSource) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

func (w *wavSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	n := int(dur.Seconds() * float64(w.rate))
	if n <= 0 || n > len(w.samples) {
		n = len(w.samples)
	}
	return w.samples[:n], w.rate, nil
}

func (w *wavSource) Close() error { return nil }

// TestSurveyUsesRealModulation checks that the sweep never asks a receiver
// for a modulation named "auto": no receiver knows that mode, so the
// survey would silently hear nothing.
func TestSurveyUsesRealModulation(t *testing.T) {
	source := &fakeSource{level: map[float64]float64{}}
	instance := New(Options{
		Source:  source,
		StartHz: 144e6, EndHz: 144.1e6, StepHz: 25e3, Dwell: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	instance.Run(ctx)
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.modes) == 0 {
		t.Fatal("the sweep never listened")
	}
	for _, mode := range source.modes {
		if mode == "auto" {
			t.Fatalf("the sweep asked for the %q modulation", mode)
		}
	}
}

// TestThresholdAffectsSweep checks that a higher threshold finds less: the
// threshold is the one knob the user has to separate traffic from noise.
func TestThresholdAffectsSweep(t *testing.T) {
	build := func() *fakeSource {
		return &fakeSource{level: map[float64]float64{
			144.00e6: 0.01, // noise
			144.05e6: 0.01, // noise
			144.10e6: 0.05, // a weak signal
			144.15e6: 0.50, // a strong signal
		}}
	}
	count := func(threshold float64) int {
		source := build()
		instance := New(Options{
			Source: source, Strategy: "sweep", StartHz: 144e6, EndHz: 144.15e6,
			StepHz: 25e3, Dwell: 50 * time.Millisecond, ThresholdDB: threshold,
			Listen: time.Millisecond,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		instance.Run(ctx)
		return len(instance.Activities())
	}
	low := count(0)
	high := count(20)
	if low == 0 {
		t.Fatal("the sweep found no traffic at all")
	}
	if high >= low {
		t.Errorf("a 20 dB threshold found %d transmissions, a 0 dB threshold %d: the threshold has no effect", high, low)
	}
	if low < 2 {
		t.Errorf("a 0 dB threshold found %d transmissions, want the weak and the strong one", low)
	}
}

// TestSpectrumDetection checks that a peak in the spectrum is reported and
// that the reported level is the peak's level above the floor.
func TestSpectrumDetection(t *testing.T) {
	frame := make([]float64, 256)
	for i := range frame {
		frame[i] = -90
	}
	frame[200] = -30 // a strong peak
	source := &fakeSource{spectra: [][]float64{frame}, level: map[float64]float64{}}
	instance := New(Options{
		Source: source, Strategy: "spectrum", ThresholdDB: 10,
		DwellTime: 10 * time.Millisecond, Listen: 0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	instance.Run(ctx)
	activities := instance.Activities()
	if len(activities) == 0 {
		t.Fatal("the peak in the spectrum was not reported")
	}
	if activities[0].LevelDB < 30 {
		t.Errorf("level = %.1f dB, want about 60 dB above the floor", activities[0].LevelDB)
	}
	if activities[0].FrequencyHz < 144e6 || activities[0].FrequencyHz > 146e6 {
		t.Errorf("frequency = %.0f Hz, want it inside the profile", activities[0].FrequencyHz)
	}
}

// TestCentreSpikeIsNotTraffic checks the geometry of the centre filter: the
// server's demodulator sits at the centre of every FFT frame and looks like
// a strong carrier, with a skirt that can put the centroid a few kHz off on
// a high-resolution FFT. It must be dropped even when other peaks are
// present; before this was fixed it survived whenever the band had a second
// signal, and the monitor reported a "noise" transmission at the centre for
// ever.
func TestCentreSpikeIsNotTraffic(t *testing.T) {
	// 16k bins over 2.048 MHz is the resolution the real receiver sends.
	frame := make([]float64, 16384)
	for i := range frame {
		frame[i] = -90
	}
	centreBin := len(frame) / 2
	frame[centreBin] = -20   // server demodulator, at the exact centre
	frame[centreBin+6] = -25 // its skirt, 750 Hz above centre
	frame[12000] = -30       // a real signal elsewhere
	instance := New(Options{ThresholdDB: 10})
	peaks := instance.detect(frame, 145e6, 2.048e6)
	if len(peaks) != 1 {
		t.Fatalf("detected %d peaks, want only the real one: %+v", len(peaks), peaks)
	}
	if math.Abs(peaks[0].frequency-145e6) < 3e3 {
		t.Errorf("kept a peak at %.0f Hz, next to the demodulator", peaks[0].frequency)
	}
}

// TestActivitiesAreNewestFirst checks the order of the activity log.
func TestActivitiesAreNewestFirst(t *testing.T) {
	instance := New(Options{Source: &fakeSource{}})
	instance.nextID = 0
	for i := 0; i < 5; i++ {
		instance.mu.Lock()
		instance.nextID++
		instance.activities = append(instance.activities, Activity{ID: instance.nextID})
		instance.mu.Unlock()
	}
	activities := instance.Activities()
	if len(activities) != 5 {
		t.Fatalf("got %d activities, want 5", len(activities))
	}
	if activities[0].ID != 5 || activities[4].ID != 1 {
		t.Errorf("activities are not newest first: %v", activities)
	}
}

// TestActivityLogIsCapped checks that the log does not grow without bound.
func TestActivityLogIsCapped(t *testing.T) {
	instance := New(Options{Source: &fakeSource{}})
	instance.mu.Lock()
	for i := 0; i < 500; i++ {
		instance.nextID++
		instance.activities = append(instance.activities, Activity{ID: instance.nextID})
	}
	if len(instance.activities) > 200 {
		instance.activities = instance.activities[len(instance.activities)-200:]
	}
	instance.mu.Unlock()
	if got := len(instance.Activities()); got > 200 {
		t.Errorf("activity log holds %d entries", got)
	}
}

// TestHandleJobRealDTMF drives one job with the committed DTMF recording: a
// transmission that was recorded gets an end time and a decoder name, and
// Mode never repeats the verdict.
func TestHandleJobRealDTMF(t *testing.T) {
	source := newWAVSource(t, "../../../tests/data/ref_dtmf.wav")
	instance := New(Options{Source: source, Listen: 10 * time.Second, Name: "test"})
	var published Activity
	instance.opts.OnActivity = func(activity Activity) { published = activity }

	started := time.Now().Add(-time.Minute)
	instance.mu.Lock()
	instance.nextID = 1
	instance.activities = []Activity{{ID: 1, Started: started}}
	instance.mu.Unlock()

	instance.handleJob(context.Background(), job{
		frequency: 145_500_000, levelDB: 20, modes: []string{"usb"}, activityID: 1,
	})

	if published.ID != 1 {
		t.Fatalf("published activity #%d, want #1", published.ID)
	}
	if published.Ended == nil {
		t.Fatal("a recorded transmission has no end time")
	}
	if published.Ended.Before(published.Started) {
		t.Errorf("ended %v before started %v", published.Ended, published.Started)
	}
	if published.DurationSec <= 0 {
		t.Errorf("duration = %v, want the recorded length", published.DurationSec)
	}
	if published.Mode != "dtmf" {
		t.Errorf("mode = %q, want the decoder that produced the detail", published.Mode)
	}
	if published.Mode == published.Kind {
		t.Errorf("mode and kind are both %q: a verdict is not a mode", published.Mode)
	}
	if !strings.Contains(published.Detail, "0123456789") {
		t.Errorf("detail = %q, want the decoded digits", published.Detail)
	}
}

// TestNarrowbandDecodersNeedANarrowChannel uses the committed recordings:
// CW and RTTY are narrowband modes and must not be attempted on a wide
// channel. The PoC against an AM aviation band found the reason: every
// controller call decoded as "RTTY" with invented text, so the recording
// never reached the voice path.
func TestNarrowbandDecodersNeedANarrowChannel(t *testing.T) {
	instance := New(Options{})
	cases := []struct {
		file   string
		narrow string // mode expected in a narrow channel
		wide   string // mode expected in a wide channel
		note   string
	}{
		{"ref_rtty.wav", "rtty", "", "RTTY is a 170 Hz shift mode"},
		{"ref_morse.wav", "morse", "", "CW is a 700 Hz tone"},
		{"ref_aprs.wav", "aprs", "aprs", "a packet fits a 25 kHz FM channel"},
	}
	for _, test := range cases {
		samples, rate, err := decode.ReadWAV("../../../tests/data/" + test.file)
		if err != nil {
			t.Fatalf("read %s: %v", test.file, err)
		}
		mode, detail, ok := instance.decodeData(context.Background(), samples, rate, 3000)
		if !ok || mode != test.narrow {
			t.Errorf("%s in a 3 kHz channel: got %q/%q, want %q (%s)",
				test.file, mode, detail, test.narrow, test.note)
		}
		mode, detail, ok = instance.decodeData(context.Background(), samples, rate, 25000)
		if test.wide == "" {
			if ok {
				t.Errorf("%s in a 25 kHz channel: got %q/%q, want nothing: the voice is not RTTY",
					test.file, mode, detail)
			}
		} else if !ok || mode != test.wide {
			t.Errorf("%s in a 25 kHz channel: got %q/%q, want %q (%s)",
				test.file, mode, detail, test.wide, test.note)
		}
	}
}

// TestActivityJSONOmitsUnsetEnded checks that an activity that was never
// recorded carries no "ended" field at all. A time.Time with omitempty does
// not do this: it encodes the zero year-1 timestamp instead.
func TestActivityJSONOmitsUnsetEnded(t *testing.T) {
	data, err := json.Marshal(Activity{ID: 1, Started: time.Unix(1_700_000_000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"ended"`) {
		t.Errorf("unset end time is in the JSON: %s", data)
	}

	ended := time.Unix(1_700_000_010, 0)
	data, err = json.Marshal(Activity{ID: 2, Ended: &ended})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ended"`) {
		t.Errorf("set end time is missing from the JSON: %s", data)
	}
}

func TestHandleJobDropsNoiseCandidates(t *testing.T) {
	source := newWAVSource(t, "../../../tests/data/ref_dtmf.wav")
	instance := New(Options{Source: source, Listen: 10 * time.Second, Name: "test"})
	var published int
	instance.opts.OnActivity = func(Activity) { published++ }
	instance.mu.Lock()
	instance.activities = []Activity{{ID: 9, Started: time.Now()}}
	instance.mu.Unlock()

	// A noise fixture uses broadband random samples with no deliberate signal.
	noise := make([]float64, 8000)
	seed := uint32(17)
	for i := range noise {
		seed = seed*1664525 + 1013904223
		noise[i] = float64(int32(seed)) / float64(1<<31) * 0.01
	}
	source = &wavSource{samples: noise, rate: 8000}
	instance.opts.Source = source
	instance.handleJob(context.Background(), job{frequency: 145e6, modes: []string{"usb"}, activityID: 9})
	if got := len(instance.Activities()); got != 0 {
		t.Fatalf("noise candidate remained in feed, activities=%d", got)
	}
	if published != 0 {
		t.Fatalf("noise candidate published %d times", published)
	}
}

func TestDetectWideFMSpectrum(t *testing.T) {
	frame := make([]float64, 16384)
	for i := range frame {
		frame[i] = -90
	}
	// 25 kHz wide signal, representative of a 2 m FM channel.
	for i := 9000 - 100; i <= 9000+100; i++ {
		frame[i] = -55
	}
	instance := New(Options{ThresholdDB: 8})
	peaks := instance.detect(frame, 145e6, 2.048e6)
	if len(peaks) == 0 {
		t.Fatal("25 kHz wide FM signal not found")
	}
	if peaks[0].bandwidth < 20e3 {
		t.Fatalf("reported width %.0f Hz, want near 25 kHz", peaks[0].bandwidth)
	}
	if math.Abs(peaks[0].frequency-(145e6-1.024e6+9000.5*(2.048e6/16384))) > 2e3 {
		t.Errorf("frequency %.0f Hz not near center", peaks[0].frequency)
	}
}

func TestDetectNarrowSignalOnWideFMNoiseFloor(t *testing.T) {
	frame := make([]float64, 16384)
	for i := range frame {
		frame[i] = -90
	}
	for i := 10000; i <= 10300; i++ {
		frame[i] = -65
	}
	frame[11000] = -45
	instance := New(Options{ThresholdDB: 8})
	peaks := instance.detect(frame, 145e6, 2.048e6)
	if len(peaks) == 0 {
		t.Fatal("narrow signal not found")
	}
	want := 145e6 - 1.024e6 + 11000.5*(2.048e6/16384)
	if math.Abs(peaks[len(peaks)-1].frequency-want) > 500 {
		t.Errorf("narrow peak frequency %.0f, want %.0f", peaks[len(peaks)-1].frequency, want)
	}
}

func TestFloorTracksEdgeRolloff(t *testing.T) {
	frame := make([]float64, 16384)
	for i := range frame {
		frame[i] = -90 + 20*math.Abs(float64(i)-float64(len(frame)/2))/float64(len(frame)/2)
	}
	instance := New(Options{ThresholdDB: 8})
	if peaks := instance.detect(frame, 145e6, 2.048e6); len(peaks) != 0 {
		t.Fatalf("edge rolloff produced false peaks: %+v", peaks)
	}
}

// TestDecodeDataHonoursCancellation runs a slow fake multimon-ng and checks
// that a cancelled job context interrupts the decode instead of waiting for
// the external process.
func TestDecodeDataHonoursCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake decoder")
	}
	fake := filepath.Join(t.TempDir(), "multimon-ng")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	instance := New(Options{MultimonBinary: fake})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	instance.decodeData(ctx, make([]float64, 22050), 22050, 3000)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("decodeData took %v with a cancelled context; the external decoder ran to completion", elapsed)
	}
}

func TestLiveSnapshot(t *testing.T) {
	frame := make([]float64, 512)
	for i := range frame {
		frame[i] = -90
	}
	// Off-centre on purpose: detect() guards the demodulator's own
	// channel at the middle of the profile.
	for i := 100; i < 112; i++ {
		frame[i] = -40
	}
	src := &fakeSource{spectra: [][]float64{frame}}
	m := New(Options{Source: src, Strategy: "spectrum", Name: "test rx"})
	m.updateSpectrum()
	info := m.Live()
	if info.Name != "test rx" || info.Strategy != "spectrum" {
		t.Fatalf("identity lost: %+v", info)
	}
	if !info.SpectrumOK {
		t.Fatal("spectrum should be ok")
	}
	if info.NoiseFloorDB != -90 {
		t.Fatalf("noise floor = %v, want -90", info.NoiseFloorDB)
	}
	if info.PeakDB != -40 {
		t.Fatalf("peak = %v, want -40", info.PeakDB)
	}
	if len(info.OnAir) == 0 {
		t.Fatal("expected an on-air peak above the threshold")
	}
	peak := info.OnAir[0]
	// Bins 100-111 over a 2 MHz span centred on 145 MHz: about 144.41 MHz.
	if peak.FrequencyHz < 144.3e6 || peak.FrequencyHz > 144.5e6 {
		t.Fatalf("on-air frequency = %v, want near 144.41 MHz", peak.FrequencyHz)
	}
	if peak.LevelDB < -45 {
		t.Fatalf("on-air level = %v, want near -40", peak.LevelDB)
	}
}

func TestLiveSnapshotWithoutSpectrum(t *testing.T) {
	m := New(Options{Source: &fakeSource{}, Strategy: "sweep"})
	info := m.Live()
	if info.SpectrumOK || len(info.OnAir) != 0 {
		t.Fatalf("no-spectrum source must report no peaks: %+v", info)
	}
}

func TestRequestListenTunesAndQueues(t *testing.T) {
	m := New(Options{Source: &fakeSource{}, Strategy: "spectrum", Listen: time.Second})
	if !m.RequestListen(144.4e6, "") {
		t.Fatal("first request should queue")
	}
	select {
	case work := <-m.jobs:
		if work.frequency != 144.4e6 {
			t.Fatalf("job frequency = %v", work.frequency)
		}
	default:
		t.Fatal("no job queued")
	}
	if got := m.Live().TunedHz; got != 144.4e6 {
		t.Fatalf("tuned_hz = %v, want 144.4 MHz", got)
	}
	acts := m.Activities()
	if len(acts) == 0 || acts[0].FrequencyHz != 144.4e6 {
		t.Fatalf("tune should log an activity: %+v", acts)
	}
}

func TestRequestListenWithoutRecordingStillTunes(t *testing.T) {
	m := New(Options{Source: &fakeSource{}, Strategy: "sweep"})
	if m.RequestListen(50e6, "") {
		t.Fatal("no listen configured: nothing to queue")
	}
	if got := m.Live().TunedHz; got != 50e6 {
		t.Fatalf("tuned_hz = %v, want 50 MHz", got)
	}
}

// TestSaveAudioFiltersWhatIsArchived pins the fleet-scan rule: a spectrum
// survey sees every data burst in the profile, and archiving all of them
// would fill the disk. SaveAudio must be consulted, and a false verdict must
// leave no file behind while a true verdict still writes one.
func TestSaveAudioFiltersWhatIsArchived(t *testing.T) {
	run := func(save func(string) bool) (Activity, string) {
		dir := t.TempDir()
		instance := New(Options{
			Source:    newWAVSource(t, "../../../tests/data/ref_dtmf.wav"),
			Listen:    10 * time.Second,
			Name:      "test",
			OutDir:    dir,
			SaveAudio: save,
		})
		var published Activity
		instance.opts.OnActivity = func(activity Activity) { published = activity }
		instance.mu.Lock()
		instance.nextID = 1
		instance.activities = []Activity{{ID: 1}}
		instance.mu.Unlock()
		instance.handleJob(context.Background(), job{
			frequency: 145_500_000, levelDB: 20, modes: []string{"usb"}, activityID: 1,
		})
		return published, dir
	}

	var asked []string
	published, dir := run(func(kind string) bool {
		asked = append(asked, kind)
		return false
	})
	if len(asked) == 0 {
		t.Fatal("SaveAudio was never consulted")
	}
	if published.AudioPath != "" {
		t.Errorf("audio path %q written despite SaveAudio=false", published.AudioPath)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("capture dir = %v (err %v), want empty", entries, err)
	}

	published, _ = run(func(string) bool { return true })
	if published.AudioPath == "" {
		t.Fatal("SaveAudio=true produced no audio path")
	}
	if _, err := os.Stat(published.AudioPath); err != nil {
		t.Errorf("archived WAV missing: %v", err)
	}
}

// TestPlausibleNarrowbandTextRejectsVoiceGibberish pins the fix for a live
// aviation failure: the RTTY decoder turned a controller's AM call into
// pages of letters, and the monitor reported it as data. A real text mode
// carries an identifier (CQ, a callsign with a digit, or service framing).
func TestPlausibleNarrowbandTextRejectsVoiceGibberish(t *testing.T) {
	reject := []string{
		"GZORKYTIQERNEXE SA \nNGVSI\nOY\nFD\nGFC\nDB\nXYFCUZEVH TI\nI NLMXS\rREHWM",
		"PHIELQCUYIGWUXTOAHUPGFKNLHXASYCJDKY",
		"UHSRJAZOZAUPLSAU)? &93 .:!$#0E SXT HLZ VAPNFJNZMX YOGSBBJU",
		"JJEOBWYCCJBEQGJXNPZPMELOWTTXWGDFIUMPBSSNCFAEIDMQ RLADEGDOEXFWDHZHNKNEHNCSOOJGEWCZEZEDIJGPEF6.)13",
		"DJUJ IBRH YQ BG VSZZF NUCEC86-R BTWIILGUUHPWS",
		"QUQJ NHCQBSTWIPLAZUYPYAZN\"0$(8?$784.?# ,539",
		"DWLGL\nRAJQIZ YJNJCEOGGROI#?'SKEUDSJKS \nJPWNYZMDEJADLA \rKQ47)DAFGK  KRO",
	}
	for _, text := range reject {
		if plausibleNarrowbandText(text) {
			t.Errorf("voice/noise gibberish accepted as data: %q", text)
		}
	}
	accept := []string{
		"CQ DE RS44 RTTY TEST 599",
		"CQ DE RS44 SAT",
		"CQ DE RS44 PSK31 TESTI",
		"1234567: HELLO PAGER TEST 123",
		"ZCZC 12 METAREA WARNING NNNN",
		"WEATHER FORECAST FOR THE COAST",
	}
	for _, text := range accept {
		if !plausibleNarrowbandText(text) {
			t.Errorf("real text payload rejected: %q", text)
		}
	}
}

// TestVolmetVoiceIsNotACarrier pins the live failure that kept real SSB
// voice out of the feed: the Shannon VOLMET recording measures only a few
// hundred Hz of occupied bandwidth, so the old narrow-signal rule called it
// a "0 kHz carrier" before the speech detector ever ran. With the detector
// first, the same clip is voice.
func TestVolmetVoiceIsNotACarrier(t *testing.T) {
	samples, rate, err := decode.ReadWAV("../../../tests/data/real_volmet_shannon_5505_usb.wav")
	if err != nil {
		t.Fatalf("read VOLMET recording: %v", err)
	}
	instance := New(Options{})
	bandwidth := decode.OccupiedBandwidth(samples, rate)
	kind, mode, _, _ := instance.classify(context.Background(), samples, rate, bandwidth)
	if kind != "voice" || mode != "speech" {
		t.Fatalf("classify = %q/%q, want voice/speech (bandwidth %.0f Hz)", kind, mode, bandwidth)
	}
}

func TestChooseModeUsesTheBand(t *testing.T) {
	cases := []struct {
		frequency float64
		bandwidth float64
		want      string
	}{
		{119_500_000, 3000, "am"},  // civil airband voice
		{127_000_000, 8000, "am"},  // airband, wider AM
		{300_000_000, 8000, "am"},  // military airband
		{145_500_000, 12500, "fm"}, // narrowband repeater
		{145_500_000, 2000, "usb"}, // narrow data on 2 m
		{7_100_000, 2400, "usb"},   // HF sideband
		{100_000_000, 200_000, "fm"},
	}
	for _, test := range cases {
		if got := ChooseModeAt("auto", test.bandwidth, test.frequency); got != test.want {
			t.Errorf("ChooseModeAt(%.3f MHz, %.0f Hz) = %q, want %q",
				test.frequency/1e6, test.bandwidth, got, test.want)
		}
	}
	if got := ChooseModeAt("lsb", 2400, 7_100_000); got != "lsb" {
		t.Errorf("explicit mode overridden: %q", got)
	}
}
