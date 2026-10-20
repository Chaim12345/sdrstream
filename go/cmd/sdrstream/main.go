// Command sdrstream records audio from public WebSDR / OpenWebRX servers and
// chases satellite passes through them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Chaim12345/sdrstream/go/internal/catalogue"
	"github.com/Chaim12345/sdrstream/go/internal/common"
	"github.com/Chaim12345/sdrstream/go/internal/decode"
	"github.com/Chaim12345/sdrstream/go/internal/directory"
	"github.com/Chaim12345/sdrstream/go/internal/discovery"
	"github.com/Chaim12345/sdrstream/go/internal/military"
	"github.com/Chaim12345/sdrstream/go/internal/openwebrx"
	"github.com/Chaim12345/sdrstream/go/internal/satellite"
	"github.com/Chaim12345/sdrstream/go/internal/satellites"
	"github.com/Chaim12345/sdrstream/go/internal/satnogs"
	"github.com/Chaim12345/sdrstream/go/internal/web"
	"github.com/Chaim12345/sdrstream/go/internal/websdr"
)

const usage = `record and stream from public SDR servers

usage: sdrstream <command> [flags]

commands:
  record       record audio from a server to a WAV file
  info         show a server's capabilities
  satellites   list the amateur satellite catalogue
  passes       predict satellite passes over a receiver
  satrecord    record a pass, tracking Doppler
  discover     find OpenWebRX receivers for a downlink
  undocumented satellites heard by SatNOGS but missing from the table
  military     government downlinks a public receiver can reach
  fetch        download SatNOGS captures for a satellite
  observations show real SatNOGS receptions of a satellite
  decode       demodulate recorded WAV files (AFSK/APRS, Morse)
  scan         scan a WAV for transmissions (voice, data, CW)
  bands        sweep receiver bands and report live traffic
  spectrum     identify activity from an OpenWebRX server's own FFT
               (-server URL [-all-profiles] [-frames N] [-threshold dB])
  web          serve the browser UI
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	command := os.Args[1]
	args := os.Args[2:]
	var err error
	switch command {
	case "record":
		err = cmdRecord(args)
	case "info":
		err = cmdInfo(args)
	case "satellites":
		err = cmdSatellites(args)
	case "passes":
		err = cmdPasses(args)
	case "satrecord":
		err = cmdSatrecord(args)
	case "discover":
		err = cmdDiscover(args)
	case "undocumented":
		err = cmdUndocumented(args)
	case "military":
		err = cmdMilitary(args)
	case "fetch":
		err = cmdFetch(args)
	case "observations":
		err = cmdObservations(args)
	case "web":
		err = cmdWeb(args)
	case "decode":
		err = cmdDecode(args)
	case "scan":
		err = cmdScan(args)
	case "bands":
		err = cmdBands(args)
	case "spectrum":
		err = cmdSpectrum(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseWhen accepts an ISO timestamp or a relative delay such as +15m.
func parseWhen(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "+") {
		text := trimmed[1:]
		if len(text) < 2 {
			return time.Time{}, fmt.Errorf("delay must end in s, m or h")
		}
		scale := map[string]float64{"s": 1, "m": 60, "h": 3600}[strings.ToLower(text[len(text)-1:])]
		number, err := strconv.ParseFloat(text[:len(text)-1], 64)
		if err != nil || scale == 0 {
			return time.Time{}, fmt.Errorf("expected a delay like +15m")
		}
		return time.Now().Add(time.Duration(number * scale * float64(time.Second))), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			if parsed.Location() == nil {
				return parsed.UTC(), nil
			}
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected ISO time or +delay (e.g. +15m), got %q", value)
}

func satByName(name string) (satellites.Sat, error) {
	want := strings.ToUpper(strings.TrimSpace(name))
	for _, sat := range satellites.Table {
		if strings.ToUpper(sat.Name) == want {
			return sat, nil
		}
	}
	for _, sat := range satellites.Table {
		if strings.Contains(strings.ToUpper(sat.Name), want) {
			return sat, nil
		}
	}
	return satellites.Sat{}, fmt.Errorf("unknown satellite %q; see 'satellites' command", name)
}

// resolveReceiver returns the receiver and observer coordinates for the
// satellite subcommands.
func resolveReceiver(serverURL string, kind string, latArg, lonArg string) (catalogue.Receiver, float64, float64, error) {
	for _, receiver := range catalogue.Table {
		if strings.TrimRight(serverURL, "/") == strings.TrimRight(receiver.URL, "/") {
			return receiver, receiver.Latitude, receiver.Longitude, nil
		}
	}
	if latArg != "" && lonArg != "" {
		lat, errLat := strconv.ParseFloat(latArg, 64)
		lon, errLon := strconv.ParseFloat(lonArg, 64)
		if errLat != nil || errLon != nil {
			return catalogue.Receiver{}, 0, 0, fmt.Errorf("--lat/--lon must be numbers")
		}
		return catalogue.Receiver{URL: serverURL, Kind: kind, Latitude: lat, Longitude: lon}, lat, lon, nil
	}
	if serverURL != "" {
		return catalogue.Receiver{URL: serverURL, Kind: kind}, 0, 0, fmt.Errorf("server %q not in catalogue; give --lat/--lon", serverURL)
	}
	return catalogue.Receiver{}, 0, 0, fmt.Errorf("give --lat/--lon, or --server with a catalogue URL")
}

func cmdRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	server := fs.String("server", "", "server URL or host[:port]")
	kind := fs.String("kind", "openwebrx", "protocol: openwebrx or websdr")
	freq := fs.String("freq", "", "frequency: Hz for openwebrx, kHz for websdr")
	mode := fs.String("mode", "lsb", "modulation")
	seconds := fs.Float64("seconds", 10, "recording duration in seconds")
	out := fs.String("out", "recording.wav", "output file")
	noReconnect := fs.Bool("no-reconnect", false, "do not reconnect when the connection drops")
	fs.Parse(args)
	if *server == "" {
		return fmt.Errorf("--server is required")
	}
	ctx := context.Background()
	if *kind == "websdr" {
		client, err := websdr.New(*server, websdr.Options{Reconnect: !*noReconnect})
		if err != nil {
			return err
		}
		if err := client.Connect(ctx); err != nil {
			return err
		}
		defer client.Close()
		kHz, _ := strconv.ParseFloat(*freq, 64)
		if err := client.Tune(ctx, kHz, *mode, nil); err != nil {
			return err
		}
		fmt.Printf("recording %.1fs ...\n", *seconds)
		samples, rate, err := client.Record(ctx, time.Duration(*seconds*float64(time.Second)))
		if err != nil {
			return err
		}
		return finish(samples, rate, *out)
	}
	client, err := openwebrx.New(*server, openwebrx.DefaultOptions())
	if err != nil {
		return err
	}
	if _, err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close()
	frequency, _ := strconv.ParseInt(*freq, 10, 64)
	if err := client.Tune(ctx, &frequency, *mode, nil); err != nil {
		return err
	}
	fmt.Printf("recording %.1fs ...\n", *seconds)
	samples, rate, err := client.RecordSamples(ctx, time.Duration(*seconds*float64(time.Second)))
	if err != nil {
		return err
	}
	return finish(samples, rate, *out)
}

func finish(samples []int16, rate int, out string) error {
	if len(samples) == 0 {
		return fmt.Errorf("no audio received")
	}
	file, err := os.Create(out)
	if err != nil {
		return err
	}
	defer file.Close()
	peak := 0
	sumSquare := 0.0
	for _, sample := range samples {
		magnitude := int(sample)
		if magnitude < 0 {
			magnitude = -magnitude
		}
		if magnitude > peak {
			peak = magnitude
		}
		sumSquare += float64(sample) * float64(sample)
	}
	rms := 0.0
	if len(samples) > 0 {
		rms = sqrt(sumSquare / float64(len(samples)))
	}
	frames, err := common.WriteWAV(file, samples, rate, 1)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d samples @ %d Hz, peak=%d rms=%.0f\n", out, frames, rate, peak, rms)
	return nil
}

func sqrt(value float64) float64 {
	if value <= 0 {
		return 0
	}
	x := value
	for i := 0; i < 32; i++ {
		x = (x + value/x) / 2
	}
	return x
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	server := fs.String("server", "", "server URL or host[:port]")
	kind := fs.String("kind", "openwebrx", "protocol: openwebrx or websdr")
	freq := fs.String("freq", "", "frequency in kHz (websdr only)")
	mode := fs.String("mode", "lsb", "modulation")
	fs.Parse(args)
	if *server == "" {
		return fmt.Errorf("--server is required")
	}
	ctx := context.Background()
	if *kind == "websdr" {
		client, err := websdr.New(*server, websdr.Options{})
		if err != nil {
			return err
		}
		if err := client.Connect(ctx); err != nil {
			return err
		}
		defer client.Close()
		kHz := 10000.0
		if *freq != "" {
			kHz, _ = strconv.ParseFloat(*freq, 64)
		}
		if err := client.Tune(ctx, kHz, strings.ToUpper(*mode), nil); err != nil {
			return err
		}
		samples, _, err := client.Record(ctx, 2*time.Second)
		if err != nil {
			return err
		}
		fmt.Println("server    :", client.URL)
		fmt.Println("samplerate:", client.Samplerate)
		fmt.Println("samples   :", len(samples))
		if len(samples) > 0 {
			peak := 0
			for _, sample := range samples {
				if magnitude := abs16(sample); magnitude > peak {
					peak = magnitude
				}
			}
			fmt.Println("peak      :", peak)
		}
		return nil
	}
	client, err := openwebrx.New(*server, openwebrx.DefaultOptions())
	if err != nil {
		return err
	}
	config, err := client.Connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	fmt.Println("server   :", client.URL)
	fmt.Println("center   :", derefInt64(config.CenterFreq))
	fmt.Println("samp_rate:", derefFloat64(config.SampRate))
	fmt.Println("start_mod:", derefString(config.StartMod))
	fmt.Println("audio    :", derefString(config.AudioCompression))
	fmt.Println("fft      :", derefString(config.FFTCompression))
	fmt.Println("profiles :", len(client.Profiles))
	fmt.Println("modes    :", len(client.Modes))
	if len(client.Modes) > 0 {
		names := make([]string, 0, len(client.Modes))
		for _, mode := range client.Modes {
			if name, ok := mode["modulation"].(string); ok {
				names = append(names, name)
			}
		}
		fmt.Println("  " + strings.Join(names, ","))
	}
	return nil
}

func abs16(value int16) int {
	if value < 0 {
		return -int(value)
	}
	return int(value)
}

func derefInt64(p *int64) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefFloat64(p *float64) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefString(p *string) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func cmdSatellites(args []string) error {
	if len(args) == 0 {
		for _, band := range []string{"2m", "70cm", "23cm"} {
			var inBand []satellites.Sat
			for _, sat := range satellites.Table {
				if band == bandOf(sat) {
					inBand = append(inBand, sat)
				}
			}
			if len(inBand) == 0 {
				continue
			}
			sort.Slice(inBand, func(i, j int) bool { return inBand[i].Name < inBand[j].Name })
			fmt.Printf("%s (%d):\n", band, len(inBand))
			for _, sat := range inBand {
				notes := sat.Notes
				if len(notes) > 46 {
					notes = notes[:46]
				}
				fmt.Printf("  %-9s %10.4f MHz  %-4s %s\n", sat.Name,
					float64(sat.DownlinkHz)/1e6, sat.Mode, notes)
			}
		}
		return nil
	}
	sat, err := satByName(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s (NORAD %d)\n", sat.Name, sat.NoradID)
	fmt.Printf("  downlink : %.4f MHz  %s\n", float64(sat.DownlinkHz)/1e6, sat.Mode)
	if sat.UplinkHz != nil {
		fmt.Printf("  uplink   : %.4f MHz", float64(*sat.UplinkHz)/1e6)
		if sat.ToneHz != nil {
			fmt.Printf(" tone %.1f Hz", *sat.ToneHz)
		}
		fmt.Println()
	}
	fmt.Printf("  status   : %s\n", sat.Status)
	if sat.Notes != "" {
		fmt.Printf("  notes    : %s\n", sat.Notes)
	}
	receivers := catalogue.ForFrequency(float64(sat.DownlinkHz))
	fmt.Printf("  receivers: %d in the catalogue can hear this downlink\n", len(receivers))
	for _, receiver := range receivers[:min(8, len(receivers))] {
		fmt.Printf("    %-46s (%8.2f,%9.2f)\n", receiver.URL, receiver.Latitude, receiver.Longitude)
	}
	return nil
}

func bandOf(sat satellites.Sat) string {
	switch {
	case sat.DownlinkHz >= 144_000_000 && sat.DownlinkHz < 148_000_000:
		return "2m"
	case sat.DownlinkHz >= 420_000_000 && sat.DownlinkHz < 450_000_000:
		return "70cm"
	case sat.DownlinkHz >= 1240_000_000 && sat.DownlinkHz < 1300_000_000:
		return "23cm"
	case sat.DownlinkHz >= 50_000_000 && sat.DownlinkHz < 54_000_000:
		return "6m"
	}
	return "other"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func cmdPasses(args []string) error {
	fs := flag.NewFlagSet("passes", flag.ExitOnError)
	name := fs.String("satellite", "", "satellite name, e.g. SO-50 or ISS")
	lat := fs.String("lat", "", "observer latitude")
	lon := fs.String("lon", "", "observer longitude")
	hours := fs.Float64("hours", 12, "how far ahead to search")
	minElevation := fs.Float64("min-elevation", 10, "minimum peak elevation in degrees")
	when := fs.String("when", "", "start time: ISO 8601 or a delay like +15m")
	server := fs.String("server", "", "receiver URL, looked up in the catalogue")
	kind := fs.String("kind", "websdr", "protocol of the receiver")
	fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("--satellite is required")
	}
	sat, err := satByName(*name)
	if err != nil {
		return err
	}
	receiver, latitude, longitude, err := resolveReceiver(*server, *kind, *lat, *lon)
	if err != nil {
		return err
	}
	start := time.Now()
	if *when != "" {
		start, err = parseWhen(*when)
		if err != nil {
			return err
		}
	}
	plan, err := satellite.PlanPass(sat, &receiver,
		floatPtr(latitude), floatPtr(longitude), start, *hours, *minElevation,
		90, 60, "", false)
	if err != nil {
		return err
	}
	fmt.Println(plan.Summary())
	return nil
}

func floatPtr(value float64) *float64 { return &value }

func cmdSatrecord(args []string) error {
	fs := flag.NewFlagSet("satrecord", flag.ExitOnError)
	name := fs.String("satellite", "", "satellite name")
	lat := fs.String("lat", "", "observer latitude")
	lon := fs.String("lon", "", "observer longitude")
	hours := fs.Float64("hours", 12, "how far ahead to search")
	minElevation := fs.Float64("min-elevation", 10, "minimum peak elevation in degrees")
	when := fs.String("when", "", "start time: ISO 8601 or +delay")
	server := fs.String("server", "", "receiver URL from the catalogue")
	kind := fs.String("kind", "websdr", "protocol of the receiver")
	out := fs.String("out", "satellite.wav", "output file")
	dryRun := fs.Bool("dry-run", false, "show the plan without recording")
	offlineTle := fs.Bool("offline-tle", false, "use the cached TLE set even if stale")
	fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("--satellite is required")
	}
	sat, err := satByName(*name)
	if err != nil {
		return err
	}
	receiver, latitude, longitude, err := resolveReceiver(*server, *kind, *lat, *lon)
	if err != nil {
		return err
	}
	start := time.Now()
	if *when != "" {
		start, err = parseWhen(*when)
		if err != nil {
			return err
		}
	}
	plan, err := satellite.PlanPass(sat, &receiver, floatPtr(latitude), floatPtr(longitude),
		start, *hours, *minElevation, 90, 60, "", *offlineTle)
	if err != nil {
		return err
	}
	fmt.Println(plan.Summary())
	if *dryRun {
		return nil
	}
	_, err = satellite.Chase(context.Background(), plan, *out, 300, 2*time.Second,
		satellite.SegmentSeconds, *offlineTle, func(message string) { fmt.Println(message) })
	return err
}

func cmdDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	frequency := fs.Float64("frequency", 0, "downlink in kHz, e.g. 145800")
	band := fs.String("band", "", "keep only receivers covering this amateur band")
	limit := fs.Int("limit", 0, "probe only the first N servers")
	timeout := fs.Float64("timeout", 30, "directory fetch timeout in seconds")
	fs.Parse(args)
	fmt.Println("reading", directory.DirectoryURL, "...")
	receivers, err := directory.Discover(context.Background(),
		time.Duration(*timeout*float64(time.Second)), *limit)
	if err != nil {
		return err
	}
	fmt.Printf("probed receivers that answered: %d\n", len(receivers))
	if *frequency > 0 {
		receivers = directory.ForFrequency(*frequency*1000, receivers, *band)
		fmt.Printf("able to hear %.4f MHz: %d\n", *frequency, len(receivers))
	} else if *band != "" {
		var filtered []directory.DiscoverResult
		for _, receiver := range receivers {
			for _, candidate := range receiver.SatelliteBands {
				if candidate == *band {
					filtered = append(filtered, receiver)
					break
				}
			}
		}
		receivers = filtered
	}
	for _, receiver := range receivers {
		bands := strings.Join(receiver.SatelliteBands, ",")
		if bands == "" {
			bands = "-"
		}
		fmt.Printf("  %-46s (%8.4f,%9.4f) %s\n", receiver.URL, receiver.Latitude,
			receiver.Longitude, bands)
	}
	return nil
}

func cmdUndocumented(args []string) error {
	fs := flag.NewFlagSet("undocumented", flag.ExitOnError)
	limit := fs.Int("limit", 100, "how many observations to scan")
	status := fs.String("status", "good", "observation status to scan")
	fs.Parse(args)
	found, err := discovery.Unknown(*limit, *status, 30*time.Second)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Println("no undocumented satellites in the recent feed")
		return nil
	}
	fmt.Printf("%d satellites heard by SatNOGS but missing from the table:\n", len(found))
	for _, entry := range found {
		fmt.Println("  " + entry.Summary())
	}
	return nil
}

func cmdMilitary(args []string) error {
	fs := flag.NewFlagSet("military", flag.ExitOnError)
	live := fs.Bool("live", false, "check against a live directory sweep")
	timeout := fs.Float64("timeout", 30, "directory fetch timeout in seconds")
	fs.Parse(args)
	var receivers []catalogue.Receiver
	if *live {
		fmt.Println("probing the public directory, this takes about 20s ...")
		results, err := directory.Discover(context.Background(),
			time.Duration(*timeout*float64(time.Second)), 0)
		if err != nil {
			return err
		}
		for _, result := range results {
			receiver := catalogue.Receiver{URL: result.URL, Kind: "openwebrx",
				Latitude: result.Latitude, Longitude: result.Longitude, Bands: result.Bands}
			receivers = append(receivers, receiver)
		}
	}
	fmt.Println(military.Report(receivers))
	return nil
}

func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	name := fs.String("satellite", "", "satellite name")
	out := fs.String("out", "satnogs", "output directory")
	limit := fs.Int("limit", 25, "how many observations to look through")
	count := fs.Int("count", 1, "how many observations to download")
	waterfall := fs.Bool("waterfall", true, "download the waterfall images")
	demod := fs.Bool("demod", true, "download the demodulated data files")
	fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("--satellite is required")
	}
	sat, err := satByName(*name)
	if err != nil {
		return err
	}
	observations, err := satnogs.Observations(sat.NoradID, *limit, 60*time.Second)
	if err != nil {
		return err
	}
	var withData []satnogs.Observation
	for _, observation := range observations {
		if (*waterfall && observation.WaterfallURL != "") || (*demod && observation.DemodURL != "") {
			withData = append(withData, observation)
		}
	}
	if len(withData) == 0 {
		fmt.Printf("no observations of %s with downloadable data\n", sat.Name)
		return nil
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	saved := 0
	for _, observation := range withData[:min(*count, len(withData))] {
		if *waterfall && observation.WaterfallURL != "" {
			target := filepath.Join(*out, fmt.Sprintf("%d_waterfall.png", observation.ID))
			if err := satnogs.Download(observation.WaterfallURL, target, 120*time.Second); err != nil {
				fmt.Fprintf(os.Stderr, "waterfall %d: %v\n", observation.ID, err)
			} else {
				fmt.Println("wrote", target)
				saved++
			}
		}
		if *demod && observation.DemodURL != "" {
			suffix := observation.DemodURL[strings.LastIndex(observation.DemodURL, "/")+1:]
			target := filepath.Join(*out, fmt.Sprintf("%d_%s.dat", observation.ID, suffix))
			if err := satnogs.Download(observation.DemodURL, target, 300*time.Second); err != nil {
				fmt.Fprintf(os.Stderr, "demod %d: %v\n", observation.ID, err)
			} else {
				info, _ := os.Stat(target)
				fmt.Printf("wrote %s (%d bytes)\n", target, info.Size())
				saved++
			}
		}
	}
	fmt.Printf("saved %d file(s) to %s\n", saved, *out)
	return nil
}

func cmdObservations(args []string) error {
	fs := flag.NewFlagSet("observations", flag.ExitOnError)
	name := fs.String("satellite", "", "satellite name")
	limit := fs.Int("limit", 15, "how many observations to show")
	fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("--satellite is required")
	}
	sat, err := satByName(*name)
	if err != nil {
		return err
	}
	fmt.Printf("%s (NORAD %d)\n", sat.Name, sat.NoradID)
	observations, err := satnogs.Observations(sat.NoradID, *limit, 30*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("\n%d recent observations:\n", len(observations))
	for _, observation := range observations[:min(*limit, len(observations))] {
		fmt.Println("  " + observation.Summary())
	}
	counts := satnogs.ObservedFrequencies(observations)
	if len(counts) > 0 {
		type pair struct {
			frequency int64
			count     int
		}
		var pairs []pair
		for frequency, count := range counts {
			pairs = append(pairs, pair{frequency, count})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
		fmt.Println("\nobserved downlinks:")
		for _, entry := range pairs {
			fmt.Printf("  %9.4f MHz  %3d observations\n", float64(entry.frequency)/1e6, entry.count)
		}
	}
	return nil
}

// defaultBands are the standard amateur satellite bands used when a
// receiver is not in the catalogue.
var defaultBands = []catalogue.Band{
	{Name: "6m", CenterKHz: 50250, SampleRateKHz: 2048},
	{Name: "2m", CenterKHz: 145000, SampleRateKHz: 2048},
	{Name: "70cm", CenterKHz: 433000, SampleRateKHz: 2048},
}

// lookupReceiver finds a catalogue entry whose URL matches, tolerating a
// missing or extra port.
func lookupReceiver(url string) (catalogue.Receiver, bool) {
	norm := func(s string) string {
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimSuffix(s, "/")
		if i := strings.Index(s, ":"); i >= 0 {
			s = s[:i]
		}
		return s
	}
	for _, r := range catalogue.Table {
		if r.URL == url || norm(r.URL) == norm(url) {
			if r.URL != url {
				r.URL = url
			}
			return r, true
		}
	}
	return catalogue.Receiver{}, false
}

// cmdBands sweeps the amateur bands of one or more receivers, listening
// briefly at each step, and reports every frequency where traffic was
// heard. Hits can be saved as WAV and decoded.
func cmdBands(args []string) error {
	fs := flag.NewFlagSet("bands", flag.ExitOnError)
	server := fs.String("server", "", "receiver URL (default: sweep every catalogue receiver)")
	kind := fs.String("kind", "websdr", "websdr | openwebrx")
	bandName := fs.String("band", "", "restrict to one band name (2m, 70cm, 6m, ...)")
	stepKHz := fs.Float64("step", 25, "dial step in kHz")
	dwell := fs.Float64("dwell", 1.2, "seconds to listen at each step")
	minLevel := fs.Float64("min-level", -55, "minimum RMS level in dB to report")
	outDir := fs.String("out-dir", "", "if set, save each hit as a WAV here")
	limit := fs.Int("limit", 0, "maximum receivers to sweep (0 = all)")
	fs.Parse(args)

	var receivers []catalogue.Receiver
	if *server != "" {
		if known, ok := lookupReceiver(*server); ok {
			receivers = []catalogue.Receiver{known}
		} else {
			receivers = []catalogue.Receiver{{URL: *server, Kind: *kind, Bands: defaultBands}}
		}
	} else {
		receivers = catalogue.Table
	}
	if *limit > 0 && len(receivers) > *limit {
		receivers = receivers[:*limit]
	}

	fmt.Printf("scanning %d receiver(s), step %.0f kHz, dwell %.1fs\n", len(receivers), *stepKHz, *dwell)
	total := 0
	for _, receiver := range receivers {
		if len(receiver.Bands) == 0 {
			fmt.Printf("== %s: no band list, skipping\n", receiver.URL)
			continue
		}
		for _, band := range receiver.Bands {
			if *bandName != "" && band.Name != *bandName {
				continue
			}
			lowKHz := band.CenterKHz - band.SampleRateKHz/2
			highKHz := band.CenterKHz + band.SampleRateKHz/2
			fmt.Printf("== %s band %s  %.3f-%.3f MHz\n", receiver.URL, band.Name, lowKHz/1e3, highKHz/1e3)
			hits, err := sweepBand(receiver, lowKHz, highKHz, *stepKHz, *dwell, *minLevel, *outDir)
			if err != nil {
				fmt.Printf("   error: %v\n", err)
				continue
			}
			if len(hits) == 0 {
				fmt.Println("   nothing above threshold")
				continue
			}
			fmt.Print(decode.Report(hits))
			total += len(hits)
		}
	}
	fmt.Printf("\n%d hits total\n", total)
	return nil
}

// sweepBand tunes one receiver across a band, capturing and classifying at
// each step, optionally saving hits to disk.
func sweepBand(receiver catalogue.Receiver, lowKHz, highKHz, stepKHz, dwell, minLevel float64, outDir string) ([]decode.BandHit, error) {
	ctx := context.Background()
	client, err := websdr.New(receiver.URL, websdr.Options{Reconnect: true})
	if err != nil {
		return nil, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	defer client.Close()

	var hits []decode.BandHit
	for kHz := lowKHz; kHz <= highKHz; kHz += stepKHz {
		if err := client.Tune(ctx, kHz, "fm", nil); err != nil {
			continue
		}
		samples, rate, err := client.Record(ctx, time.Duration(dwell*float64(time.Second)))
		if err != nil || len(samples) < rate/10 {
			continue
		}
		floats := make([]float64, len(samples))
		for i, s := range samples {
			floats[i] = float64(s) / 32768.0
		}
		hit := decode.Classify(floats, rate)
		hit.FrequencyHz = kHz * 1000
		hit.Mode = "fm"
		if hit.LevelDB < minLevel {
			continue
		}
		hits = append(hits, hit)
		if outDir != "" {
			name := fmt.Sprintf("%s/band_%.4fMHz_%s.wav", outDir, kHz/1e3, hit.Kind)
			_ = common.WriteWAVFile(name, samples, rate)
		}
	}
	return hits, nil
}

// cmdSpectrum identifies activity on an OpenWebRX receiver by reading its
// server-side FFT frames. This is far more reliable than listening to
// demodulated audio, which AGC flattens: the server computes and calibrates
// the spectrum, so a peak is real energy above the local noise floor. With
// -all-profiles it switches through every SDR profile the server offers.
func cmdSpectrum(args []string) error {
	fs := flag.NewFlagSet("spectrum", flag.ExitOnError)
	server := fs.String("server", "", "OpenWebRX receiver URL (required)")
	frames := fs.Int("frames", 40, "FFT frames to average")
	threshold := fs.Float64("threshold", 8, "report peaks this many dB above the noise floor")
	top := fs.Int("top", 25, "maximum peaks to print")
	allProfiles := fs.Bool("all-profiles", false, "switch through every SDR profile the server offers")
	fs.Parse(args)
	if *server == "" {
		return fmt.Errorf("spectrum: -server is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// One connection to learn the profile list, then a fresh connection per
	// profile: some servers close the socket when the SDR is restarted for a
	// profile switch, and a fresh client also clears stale FFT frames.
	probe, _, err := connectWithRetry(ctx, *server, 4)
	if err != nil {
		return err
	}
	profiles := probe.ProfileList()
	probe.Close()
	fmt.Printf("== %s\n", *server)

	type target struct {
		key  string
		name string
	}
	targets := []target{{"", "current profile"}}
	if *allProfiles {
		targets = nil
		for _, p := range profiles {
			targets = append(targets, target{p.Key, p.Name})
		}
		if len(targets) == 0 {
			fmt.Println("   no profiles advertised; scanning the current one")
			targets = []target{{"", "current profile"}}
		}
	}

	for _, t := range targets {
		scanProfile(ctx, *server, t.key, t.name, *frames, *threshold, *top)
		time.Sleep(2 * time.Second) // be polite between reconnects
	}
	return nil
}

// connectWithRetry dials and handshakes, retrying a few times with backoff
// because some servers reject rapid successive connections.
func connectWithRetry(ctx context.Context, server string, attempts int) (*openwebrx.Client, openwebrx.Config, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		client, err := openwebrx.New(server, openwebrx.DefaultOptions())
		if err != nil {
			return nil, openwebrx.Config{}, err
		}
		cfg, err := client.Connect(ctx)
		if err == nil {
			return client, cfg, nil
		}
		client.Close()
		lastErr = err
		select {
		case <-time.After(time.Duration(i+1) * 2 * time.Second):
		case <-ctx.Done():
			return nil, openwebrx.Config{}, ctx.Err()
		}
	}
	return nil, openwebrx.Config{}, lastErr
}

// scanProfile connects, optionally switches to one SDR profile, and prints
// the peaks in its averaged spectrum.
func scanProfile(ctx context.Context, server, key, name string, frames int, threshold float64, top int) {
	client, cfg, err := connectWithRetry(ctx, server, 3)
	if err != nil {
		fmt.Printf("-- %s: connect: %v\n", name, err)
		return
	}
	defer client.Close()
	if key != "" {
		if err := client.SelectProfile(ctx, key); err != nil {
			fmt.Printf("-- %s: selectprofile: %v\n", name, err)
			return
		}
		if next, ok := awaitConfig(ctx, client, 15*time.Second); ok {
			cfg = next
		} else {
			fmt.Printf("-- %s: no config after switch\n", name)
			return
		}
		time.Sleep(1500 * time.Millisecond) // let the old profile's frames drain
	}
	spectra := collectFrames(ctx, client, frames, 20*time.Second)
	if len(spectra) == 0 {
		fmt.Printf("-- %s: no FFT frames\n", name)
		return
	}
	avg := openwebrx.AverageSpectra(spectra)
	centre, span := 0.0, 0.0
	if cfg.CenterFreq != nil {
		centre = float64(*cfg.CenterFreq)
	}
	if cfg.SampRate != nil {
		span = *cfg.SampRate
	}
	fmt.Printf("-- %s: %d frames, %.4f MHz +/- %.0f kHz\n",
		name, len(spectra), centre/1e6, span/2e3)
	peaks := openwebrx.FindSpectrumPeaks(avg, centre, span, threshold)
	if len(peaks) == 0 {
		fmt.Printf("   no peaks above noise+%.0f dB\n", threshold)
		return
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].LevelDB > peaks[j].LevelDB })
	for i, p := range peaks {
		if i >= top {
			break
		}
		fmt.Printf("     %11.1f kHz  %6.1f dB\n", p.FreqHz/1000, p.LevelDB)
	}
}

// collectFrames gathers up to n FFT frames from the client, stopping early
// on timeout.
func collectFrames(ctx context.Context, client *openwebrx.Client, n int, timeout time.Duration) [][]float64 {
	var spectra [][]float64
	deadline := time.After(timeout)
	for len(spectra) < n {
		select {
		case e := <-client.Events():
			if e.Type == "fft" && !e.Secondary {
				if vals, ok := e.Value.([]float64); ok && len(vals) > 0 {
					spectra = append(spectra, vals)
				}
			}
		case <-deadline:
			return spectra
		case <-ctx.Done():
			return spectra
		}
	}
	return spectra
}

// awaitConfig waits for the next config event that carries a centre
// frequency, which is how a profile switch reports the new tuning.
func awaitConfig(ctx context.Context, client *openwebrx.Client, timeout time.Duration) (openwebrx.Config, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case e := <-client.Events():
			if e.Type == "config" {
				if cfg, ok := e.Value.(openwebrx.Config); ok && cfg.CenterFreq != nil {
					return cfg, true
				}
			}
		case <-deadline:
			return openwebrx.Config{}, false
		case <-ctx.Done():
			return openwebrx.Config{}, false
		}
	}
}

func cmdWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "address to bind")
	port := fs.Int("port", 8000, "port to bind")
	capturesDir := fs.String("captures-dir", "../captures", "directory of saved WAV captures to list and serve")
	fs.Parse(args)
	server := web.New(*capturesDir)
	return server.Serve(fmt.Sprintf("%s:%d", *host, *port))
}

// cmdDecode extracts data from a recorded pass: APRS packets via
// AFSK/HDLC/AX.25, and Morse text from CW beacons.
func cmdDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	mode := fs.String("mode", "auto", "auto | aprs | morse | dtmf | rtty | psk31 | sstv | pocsag | all")
	offset := fs.Float64("offset", 0, "audio centre frequency in Hz for psk31/rtty (0 = auto)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("decode: pass one or more WAV files")
	}
	for _, path := range fs.Args() {
		fmt.Printf("== %s\n", path)
		samples, rate, err := decode.ReadWAV(path)
		if err != nil {
			fmt.Printf("   read: %v\n", err)
			continue
		}
		wantAll := *mode == "all"

		if *mode == "dtmf" || wantAll {
			if s := decode.DTMFDecode(samples, rate); s != "" {
				fmt.Printf("   dtmf:   %s\n", s)
			} else if *mode == "dtmf" {
				fmt.Println("   dtmf:   no tones")
			}
		}
		if *mode == "rtty" || wantAll {
			if s := decode.RTTYDecode(samples, rate, 0, 0, 0); s != "" {
				fmt.Printf("   rtty:   %s\n", strings.TrimSpace(s))
			} else if *mode == "rtty" {
				fmt.Println("   rtty:   nothing")
			}
		}
		if *mode == "psk31" || wantAll {
			if s := decode.PSK31Decode(samples, rate, *offset); s != "" {
				fmt.Printf("   psk31:  %s\n", s)
			} else if *mode == "psk31" {
				fmt.Println("   psk31:  nothing")
			}
		}
		if *mode == "sstv" || wantAll {
			hits := decode.SSTVDetect(samples, rate)
			if len(hits) == 0 && *mode == "sstv" {
				fmt.Println("   sstv:   no VIS header")
			}
			for _, h := range hits {
				fmt.Printf("   sstv:   %s (VIS 0x%02X) at %.1f s\n", h.Mode, h.VIS, h.StartSec)
			}
		}
		if *mode == "pocsag" || wantAll {
			msgs := decode.POCSAGDecode(samples, rate, 0)
			if len(msgs) == 0 && *mode == "pocsag" {
				fmt.Println("   pocsag: no pages")
			}
			for _, m := range msgs {
				fmt.Printf("   pocsag: %s\n", m.String())
			}
		}

		if *mode == "aprs" || *mode == "morse" || *mode == "auto" {
			// Prefer multimon-ng (the reference decoder) when installed.
			if decode.MultimonAvailable() && *mode != "morse" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				lines, err := decode.MultimonDecode(ctx, path, "AFSK1200")
				cancel()
				if err != nil {
					fmt.Printf("   multimon: %v\n", err)
				} else if len(lines) == 0 {
					fmt.Println("   multimon: no packets")
				} else {
					for _, l := range lines {
						fmt.Printf("   packet: %s\n", l)
					}
				}
			} else if *mode == "auto" || *mode == "aprs" {
				packets, err := decode.AFSKFrames(path)
				if err != nil {
					fmt.Printf("   aprs: %v\n", err)
				} else if len(packets) == 0 {
					fmt.Println("   aprs: no packets")
				} else {
					for _, p := range packets {
						fmt.Printf("   packet: %s\n", p.String())
					}
				}
			}
			if *mode == "auto" || *mode == "morse" {
				res, err := decode.MorseDecode(path)
				if err != nil {
					fmt.Printf("   morse: %v\n", err)
				} else {
					fmt.Printf("   morse: tone %.0f Hz (SNR %.0f dB, %d key-downs)\n",
						res.ToneHz, res.SnrdB, res.DotsFound)
					if res.Text != "" {
						fmt.Printf("   text:  %s\n", res.Text)
					}
				}
			}
		}
	}
	return nil
}

// cmdScan lists every transmission event found in a recording, classified
// as packet data, CW, voice or carrier, with timing and strength.
func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("scan: pass one or more WAV files")
	}
	for _, path := range fs.Args() {
		fmt.Printf("== %s\n", path)
		events, err := decode.ScanTransmissions(path)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			fmt.Println("   no transmissions detected")
			continue
		}
		for _, e := range events {
			fmt.Printf("   %7.1f-%7.1f s  %5.1f s  peak %5.1f dB  %s\n",
				e.StartSec, e.StartSec+e.DurationSec, e.DurationSec,
				e.PeakDB, e.Description)
		}
	}
	return nil
}
