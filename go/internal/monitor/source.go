package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/icecast"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/openwebrx"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

// openWebRXSource watches an OpenWebRX server. It is the richest source:
// the server sends its own FFT, so the monitor can see every transmission
// in the profile's span, not just the one the server is tuned to.
type openWebRXSource struct {
	client  *openwebrx.Client
	name    string
	profile string // display name selected at connect, empty for the default

	mu       sync.Mutex
	bins     []float64
	centre   float64
	span     float64
	haveSpec bool
	// Freshness/gatekeeping: lastFFT is the newest spectrum frame's
	// arrival; dead flips once the client's events channel closes. A
	// frozen FFT from a dropped connection must not keep "scanning dead
	// air" forever (review H2).
	lastFFT time.Time
	dead    bool
}

// OpenWebRXSource connects to an OpenWebRX server and starts collecting
// its spectrum frames. The optional profile selects a wide profile so that
// more of the band is visible.
func OpenWebRXSource(ctx context.Context, server, profile string) (Source, error) {
	return dialOpenWebRX(ctx, server, profile, false)
}

func dialOpenWebRX(ctx context.Context, server, profile string, allowPrivate bool) (Source, error) {
	options := openwebrx.DefaultOptions()
	options.AllowPrivate = allowPrivate
	client, err := openwebrx.New(server, options)
	if err != nil {
		return nil, err
	}
	if _, err := client.Connect(ctx); err != nil {
		return nil, err
	}
	source := &openWebRXSource{client: client, name: server, profile: profile}
	if profile != "" {
		// A failed profile resolve/select would otherwise leak the live
		// websocket: every rotated-profile visit against a server that
		// dropped its profiles dripped one FD and one read-loop goroutine.
		key, err := resolveProfile(client, profile)
		if err != nil {
			client.Close()
			return nil, err
		}
		if err := client.SelectProfile(ctx, key); err != nil {
			client.Close()
			return nil, fmt.Errorf("monitor: select profile %q: %w", profile, err)
		}
		// Give the server a moment to switch profile and settle.
		time.Sleep(700 * time.Millisecond)
	}
	go source.pump(ctx)
	return source, nil
}

// ProfileNames lists the tunable profiles the receiver advertised. The
// fleet scanner rotates through them between visits so a station with
// several bands is not surveyed on only its default profile forever.
func (s *openWebRXSource) ProfileNames() []string {
	profiles := s.client.ProfileList()
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if name := strings.TrimSpace(profile.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ActiveProfile is the display name selected for this connection, or empty
// when the server's default profile is in use.
func (s *openWebRXSource) ActiveProfile() string { return s.profile }

// resolveProfile turns a profile name into the key the server expects.
// The server identifies a profile as "sdr_id|profile_id", so a name has to
// be looked up before it can be selected.
func resolveProfile(client *openwebrx.Client, name string) (string, error) {
	profiles := client.ProfileList()
	for _, profile := range profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), strings.TrimSpace(name)) {
			return profile.Key, nil
		}
	}
	// Also accept the key itself, or a unique prefix of the name.
	for _, profile := range profiles {
		if profile.Key == name {
			return profile.Key, nil
		}
	}
	var names []string
	for _, profile := range profiles {
		names = append(names, profile.Name)
	}
	return "", fmt.Errorf("monitor: no profile named %q; the server offers: %s",
		name, strings.Join(names, ", "))
}

// pump keeps the latest spectrum frame.
func (s *openWebRXSource) pump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-s.client.Events():
			if !ok {
				// The receiver connection is over (socket error, server
				// restart). Keep serving the last known spectrum for a
				// short grace period; Spectrum() now gates by freshness.
				s.mu.Lock()
				s.dead = true
				s.mu.Unlock()
				return
			}
			switch event.Type {
			case "fft":
				// The secondary FFT belongs to the server's second
				// receiver, which is tuned somewhere else entirely; only
				// the primary spectrum matches the audio we can record.
				if event.Secondary {
					continue
				}
				if bins, ok := event.Value.([]float64); ok && len(bins) > 0 {
					s.mu.Lock()
					s.bins = bins
					s.lastFFT = time.Now()
					s.mu.Unlock()
				}
			case "config":
				config, ok := event.Value.(openwebrx.Config)
				if !ok {
					continue
				}
				s.mu.Lock()
				if config.CenterFreq != nil {
					s.centre = float64(*config.CenterFreq)
				}
				if config.SampRate != nil {
					s.span = *config.SampRate
				}
				s.mu.Unlock()
			}
		}
	}
}

// Spectrum returns the latest FFT frame.
func (s *openWebRXSource) Spectrum() ([]float64, float64, float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bins) == 0 || s.span <= 0 {
		return nil, s.centre, s.span, false
	}
	// Freshness gate: OpenWebRX pushes frames continuously while alive, so
	// a frame older than a minute (connection down, readLoop expired)
	// means the picture is frozen — scanning it would report hundreds of
	// false "steady carrier" activities from one dead snapshot.
	if s.dead && time.Since(s.lastFFT) > 10*time.Second {
		return nil, s.centre, s.span, false
	}
	if time.Since(s.lastFFT) > time.Minute {
		return nil, s.centre, s.span, false
	}
	return s.bins, s.centre, s.span, true
}

// Listen retunes and records.
func (s *openWebRXSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	frequency := int64(hz)
	if err := s.client.Tune(ctx, &frequency, mode, nil); err != nil {
		return nil, 0, err
	}
	// Give the server a moment to retune and settle before recording.
	time.Sleep(400 * time.Millisecond)
	// Drop audio buffered at the old frequency during the settle window.
	s.client.DrainAudio()
	samples, rate, err := s.client.RecordSamples(ctx, dur)
	if err != nil {
		return nil, 0, err
	}
	out := make([]float64, len(samples))
	for i, s := range samples {
		out[i] = float64(s) / 32768
	}
	return out, rate, nil
}

// Close shuts the connection.
func (s *openWebRXSource) Close() error {
	s.client.Close()
	return nil
}

// webSDRSource watches a WebSDR server. WebSDR sends audio only, so this
// source can listen but cannot see the spectrum; the monitor then reports
// what it hears on the tuned frequency.
type webSDRSource struct {
	client *websdr.Client
	name   string
}

// WebSDRSource connects to a WebSDR server.
func WebSDRSource(ctx context.Context, server string) (Source, error) {
	return dialWebSDR(ctx, server, false)
}

func dialWebSDR(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	client, err := websdr.New(server, websdr.Options{Reconnect: true, AllowPrivate: allowPrivate})
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return &webSDRSource{client: client, name: server}, nil
}

// Spectrum reports that this source has no spectrum.
func (s *webSDRSource) Spectrum() ([]float64, float64, float64, bool) { return nil, 0, 0, false }

// Listen retunes and records.
func (s *webSDRSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	if err := s.client.Tune(ctx, hz/1000, mode, nil); err != nil {
		return nil, 0, err
	}
	time.Sleep(400 * time.Millisecond)
	// Drop audio buffered at the old frequency during the settle window.
	s.client.DrainAudio()
	samples, rate, err := s.client.Record(ctx, dur)
	if err != nil {
		return nil, 0, err
	}
	out := make([]float64, len(samples))
	for i, v := range samples {
		out[i] = float64(v) / 32768
	}
	return out, rate, nil
}

// Close shuts the connection.
func (s *webSDRSource) Close() error {
	s.client.Close()
	return nil
}

// kiwiSource watches a KiwiSDR server. Like WebSDR it has no spectrum in
// this client, so it can only listen.
type kiwiSource struct {
	client *kiwisdr.Client
	// wf is the companion /W/F waterfall connection when the server
	// accepted one; it gives the monitor the full-band panorama. Nil
	// when the waterfall dial failed - the source can still listen.
	wf *kiwisdr.WaterfallClient
	// wfErr is why wf is nil; surfaced through WaterfallStatus so a
	// spectrum scan never silently degrades to audio-only.
	wfErr error
	name  string
}

// KiwiSource connects to a KiwiSDR server.
func KiwiSource(ctx context.Context, server string) (Source, error) {
	return dialKiwi(ctx, server, false)
}

func dialKiwi(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	options := kiwisdr.DefaultOptions()
	options.AllowPrivate = allowPrivate
	client, err := kiwisdr.New(server, options)
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	source := &kiwiSource{client: client, name: server}
	// The waterfall is a second connection on the same receiver. It is
	// best-effort: a server with no free waterfall channel still makes
	// a perfectly good audio source.
	if wf, err := kiwisdr.NewWaterfall(server, options); err != nil {
		source.wfErr = err
	} else if err := wf.Connect(ctx); err != nil {
		wf.Close()
		source.wfErr = err
	} else {
		source.wf = wf
	}
	return source, nil
}

// WaterfallStatus says whether the panorama is live, and why not when it
// is not. Monitor startup logs it so a spectrum scan against a Kiwi
// without a waterfall channel is a visible misconfiguration, not a
// silent audio-only receiver.
func (s *kiwiSource) WaterfallStatus() string {
	if s.wf != nil {
		return "waterfall: live panorama"
	}
	if s.wfErr != nil {
		return "waterfall unavailable: " + s.wfErr.Error()
	}
	return "waterfall unavailable"
}

// RefineSpectrum zooms the waterfall in around freqHz until one bin is
// fine enough to tune narrowband from, waits for the zoomed frame, and
// restores the full panorama before returning. The panorama's 29 kHz
// bins spot energy; this gives the frequency to listen to.
func (s *kiwiSource) RefineSpectrum(ctx context.Context, freqHz float64) ([]float64, float64, float64, bool) {
	if s.wf == nil {
		return nil, 0, 0, false
	}
	defer s.wf.SetZoom(0, 0)
	if _, _, err := s.wf.RefineZoom(freqHz, 1500); err != nil {
		return nil, 0, 0, false
	}
	return s.wf.AwaitSpectrum(ctx, 4*time.Second)
}

// Spectrum returns the waterfall panorama when the companion /W/F
// connection is live, and reports no spectrum otherwise.
func (s *kiwiSource) Spectrum() ([]float64, float64, float64, bool) {
	if s.wf == nil {
		return nil, 0, 0, false
	}
	return s.wf.Spectrum()
}

// Listen retunes and records.
func (s *kiwiSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	if err := s.client.Tune(ctx, int64(hz), mode); err != nil {
		return nil, 0, err
	}
	time.Sleep(400 * time.Millisecond)
	samples, rate, err := s.client.Record(ctx, dur)
	if err != nil {
		return nil, 0, err
	}
	out := make([]float64, len(samples))
	for i, v := range samples {
		out[i] = float64(v) / 32768
	}
	return out, rate, nil
}

// Close shuts the connection.
func (s *kiwiSource) Close() error {
	s.client.Close()
	if s.wf != nil {
		s.wf.Close()
	}
	return nil
}

// SourceFor builds the receiver source for a protocol name. The receiver
// address is guarded against loopback/private targets; use SourceForLocal
// when the user explicitly names a local receiver.
func SourceFor(ctx context.Context, kind, server, profile string) (Source, error) {
	return sourceFor(ctx, kind, server, profile, false)
}

// SourceForLocal is SourceFor without the SSRF guard, for receivers the
// user named explicitly (a lab SDR on the LAN, a loopback instance).
func SourceForLocal(ctx context.Context, kind, server, profile string) (Source, error) {
	return sourceFor(ctx, kind, server, profile, true)
}

func sourceFor(ctx context.Context, kind, server, profile string, allowPrivate bool) (Source, error) {
	switch kind {
	case "openwebrx", "":
		return dialOpenWebRX(ctx, server, profile, allowPrivate)
	case "websdr":
		return dialWebSDR(ctx, server, allowPrivate)
	case "icecast", "shoutcast", "nwr":
		return icecast.NewSource(server), nil
	case "kiwi", "kiwisdr":
		return dialKiwi(ctx, server, allowPrivate)
	case "kiwi-iq":
		return dialKiwiIQSource(ctx, server, allowPrivate)
	case "ubersdr":
		return dialUberSource(ctx, server, allowPrivate)
	case "spyserver":
		return dialSpyServer(ctx, server, allowPrivate)
	}
	return nil, fmt.Errorf("monitor: unknown receiver kind %q", kind)
}

// dialSpyServer connects to a SpyServer remote-IQ receiver. SpyServers have
// no profiles and no band scan (IQ-only stream); the monitor listens on the
// configured channel.
func dialSpyServer(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	return spyServerSource(ctx, server, 0, 0, 0, allowPrivate)
}
