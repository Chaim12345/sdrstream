// Command sdrstream records audio from public WebSDR / OpenWebRX servers and
// chases satellite passes through them.
package main

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/chaim12345/sdrstream/go/internal/hfdl"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/directory"
	"github.com/chaim12345/sdrstream/go/internal/discovery"
	"github.com/chaim12345/sdrstream/go/internal/feeds"
	"github.com/chaim12345/sdrstream/go/internal/gallery"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/military"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/openwebrx"
	"github.com/chaim12345/sdrstream/go/internal/rdio"
	"github.com/chaim12345/sdrstream/go/internal/relays"
	"github.com/chaim12345/sdrstream/go/internal/satellite"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
	"github.com/chaim12345/sdrstream/go/internal/sources"
	"github.com/chaim12345/sdrstream/go/internal/verify"
	"github.com/chaim12345/sdrstream/go/internal/voice"
	"github.com/chaim12345/sdrstream/go/internal/web"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

const usage = `record and stream from public SDR servers

usage: sdrstream <command> [flags]

commands:
  icecast      ingest Icecast/Shoutcast and NOAA Weather Radio streams
  record       record audio from a server to a WAV file
  info         show a server's capabilities
  satellites   list the amateur satellite catalogue
  passes       predict satellite passes over a receiver
  satrecord    record a pass, tracking Doppler
  discover     find OpenWebRX receivers for a downlink
  stations     list every public receiver in the directory
  sources      health-check every external data source (live/stale/unreachable)
  monitor      watch a receiver for any traffic and decode it
  undocumented satellites heard by SatNOGS but missing from the table
  military     government downlinks a public receiver can reach
  relays       list repeater/transponder satellites and frequencies
  fetch        download SatNOGS captures for a satellite
  observations show real SatNOGS receptions of a satellite
  gallery      collect real payload images/text from SatNOGS observations
  satdecode    fetch SatNOGS audio/frames and decode telemetry, APT, SSTV
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
	case "stations":
		err = cmdStations(args)
	case "sources":
		err = cmdSources(args)
	case "monitor":
		err = cmdMonitor(args)
	case "undocumented":
		err = cmdUndocumented(args)
	case "military":
		err = cmdMilitary(args)
	case "relays":
		err = cmdRelays(args)
	case "fetch":
		err = cmdFetch(args)
	case "observations":
		err = cmdObservations(args)
	case "gallery":
		err = cmdGallery(args)
	case "satdecode":
		err = cmdSatdecode(args)
	case "web":
		err = cmdWeb(args)
	case "feeds":
		err = cmdFeeds(args)
	case "avmarine":
		err = cmdAVMarine(args)
	case "icecast":
		err = cmdIcecast(args)
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
	kind := fs.String("kind", "openwebrx", "protocol: openwebrx, websdr or kiwi")
	freq := fs.String("freq", "", "frequency: Hz for openwebrx and kiwi, kHz for websdr")
	mode := fs.String("mode", "lsb", "modulation")
	seconds := fs.Float64("seconds", 10, "recording duration in seconds")
	out := fs.String("out", "recording.wav", "output file")
	noReconnect := fs.Bool("no-reconnect", false, "do not reconnect when the connection drops")
	profileSub := fs.String("profile", "", "openwebrx: select the first profile whose name contains this text (case-insensitive) before tuning; \"list\" prints the profiles and exits")
	fs.Parse(args)
	if *server == "" {
		return fmt.Errorf("--server is required")
	}
	ctx := context.Background()
	if *kind == "kiwi" {
		kiwiOpts := kiwisdr.DefaultOptions()
		kiwiOpts.AllowPrivate = true // operator-named server
		client, err := kiwisdr.New(*server, kiwiOpts)
		if err != nil {
			return err
		}
		if err := client.Connect(ctx); err != nil {
			return err
		}
		defer client.Close()
		frequency, _ := strconv.ParseInt(*freq, 10, 64)
		if err := client.Tune(ctx, frequency, *mode); err != nil {
			return err
		}
		fmt.Printf("recording %.1fs from %s ...\n", *seconds, *server)
		samples, rate, err := client.Record(ctx, time.Duration(*seconds*float64(time.Second)))
		if err != nil {
			return err
		}
		info := client.InfoSnapshot()
		if info.Version != "" {
			fmt.Printf("kiwi: version %s, %d channels, GPS %.4f/%.4f\n",
				info.Version, info.RXChannels, info.GPSLat, info.GPSLon)
		}
		return finish(samples, rate, *out)
	}
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
	client, err := openwebrx.New(*server, operatorOpenWebRXOptions())
	if err != nil {
		return err
	}
	if _, err := client.Connect(ctx); err != nil {
		return err
	}
	defer client.Close()
	if *profileSub != "" {
		time.Sleep(1500 * time.Millisecond) // profile list arrives after connect
		profiles := client.ProfileList()
		if *profileSub == "list" {
			for _, p := range profiles {
				fmt.Printf("%s\t%s\n", p.Key, p.Name)
			}
			return nil
		}
		picked := false
		for _, p := range profiles {
			if strings.Contains(strings.ToLower(p.Name), strings.ToLower(*profileSub)) {
				fmt.Printf("profile: %s\n", p.Name)
				if err := client.SelectProfile(ctx, p.Key); err != nil {
					return err
				}
				picked = true
				time.Sleep(2 * time.Second)
				break
			}
		}
		if !picked {
			return fmt.Errorf("no profile matches %q", *profileSub)
		}
	}
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
	client, err := openwebrx.New(*server, operatorOpenWebRXOptions())
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

// cmdStations lists every receiver in the public directory, including the
// KiwiSDR and WebSDR entries the discovery sweep does not probe.
func cmdStations(args []string) error {
	fs := flag.NewFlagSet("stations", flag.ExitOnError)
	kind := fs.String("type", "", "receiver type: OpenWebRX, KiwiSDR or WebSDR")
	limit := fs.Int("limit", 40, "how many stations to print (0 = all)")
	asJSON := fs.Bool("json", false, "print JSON")
	timeout := fs.Float64("timeout", 30, "directory fetch timeout in seconds")
	fs.Parse(args)
	fmt.Println("reading", directory.DirectoryURL, "...")
	stations, err := directory.FetchStations(context.Background(),
		time.Duration(*timeout*float64(time.Second)))
	if err != nil {
		return err
	}
	counts := directory.CountByType(stations)
	var types []string
	for kind := range counts {
		types = append(types, kind)
	}
	sort.Strings(types)
	fmt.Printf("%d stations:", len(stations))
	for _, kind := range types {
		fmt.Printf(" %d %s", counts[kind], kind)
	}
	fmt.Println()

	want := strings.ToLower(strings.TrimSpace(*kind))
	var filtered []directory.Station
	for _, station := range stations {
		if want != "" && strings.ToLower(station.Type) != want {
			continue
		}
		filtered = append(filtered, station)
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", " ")
		return encoder.Encode(filtered)
	}
	shown := filtered
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	for _, station := range shown {
		fmt.Printf("  %-10s %-32s %-44s (%8.4f,%9.4f)\n",
			station.Type, clip(station.Name, 32), station.URL,
			station.Latitude, station.Longitude)
	}
	if len(shown) < len(filtered) {
		fmt.Printf("  ... %d more (use -limit 0 for all)\n", len(filtered)-len(shown))
	}
	return nil
}

// clip shortens a label to width characters.
func clip(text string, width int) string {
	if len(text) <= width {
		return text
	}
	return text[:width-1] + "…"
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

// cmdRelays lists the satellites that repeat uplink traffic, with the
// frequencies a listener needs.
func cmdRelays(args []string) error {
	fs := flag.NewFlagSet("relays", flag.ExitOnError)
	all := fs.Bool("all", false, "include satellites whose status is not active")
	kind := fs.String("kind", "", "restrict to one kind (fm, linear, digipeater, transponder)")
	fs.Parse(args)
	want := strings.ToLower(*kind)
	var filtered []relays.Relay
	for _, relay := range relays.Table(*all) {
		if want != "" && !strings.Contains(strings.ToLower(string(relay.Kind)), want) {
			continue
		}
		filtered = append(filtered, relay)
	}
	if len(filtered) == 0 {
		fmt.Println("no matching relay satellites")
		return nil
	}
	fmt.Printf("%d relay satellite(s):\n", len(filtered))
	for _, relay := range filtered {
		sat := relay.Sat
		uplink, tone := "-", "-"
		if sat.UplinkHz != nil {
			uplink = fmt.Sprintf("%.4f MHz", float64(*sat.UplinkHz)/1e6)
		}
		if sat.ToneHz != nil {
			tone = fmt.Sprintf("%.1f Hz", *sat.ToneHz)
		}
		fmt.Printf("  %-9s %-18s down %10.4f MHz  up %-12s ctcss %-7s %s\n",
			sat.Name, relay.Kind, float64(sat.DownlinkHz)/1e6, uplink, tone, sat.Notes)
	}
	return nil
}

// cmdMonitor watches a receiver for any traffic, not just satellites, and
// decodes or transcribes whatever it finds.
// monitorVoiceOptions builds the speech options for the monitor, applying a
// built-in radio phraseology prompt when the caller does not supply one: the
// jargon is the part whisper gets wrong on its own.
// monitorNIMOptions builds the hosted NVIDIA speech backend options from
// the environment; an empty key disables that backend.
func monitorNIMOptions() voice.NIMOptions {
	return voice.DefaultNIMOptions()
}

// rdioCallFor maps a decoded activity onto an Rdio Scanner call: the
// frequency is the talkgroup, the kind is the group and the decoder output
// or transcript becomes the tag shown in the archive.
func rdioCallFor(activity monitor.Activity, system string) rdio.Call {
	tag := activity.Detail
	if len(tag) > 160 {
		tag = tag[:160]
	}
	return rdio.Call{
		AudioPath:      activity.AudioPath,
		Timestamp:      activity.Started,
		FrequencyHz:    activity.FrequencyHz,
		SystemLabel:    system,
		TalkgroupLabel: fmt.Sprintf("%.4f MHz %s", activity.FrequencyHz/1e6, activity.Kind),
		TalkgroupGroup: activity.Kind,
		TalkgroupTag:   tag,
	}
}

func monitorVoiceOptions(language string, threads, beam int, prompt string) voice.Options {
	if prompt == "" {
		prompt = "Radio traffic. Callsign, over, roger, copy, QSO, CQ, frequency, signal report, antenna, transponder, runway, tower, ground, approach, contact, squawk, wind, heading, cleared."
	}
	return voice.Options{
		Binary:   os.Getenv("SDRSTREAM_WHISPER"),
		Model:    os.Getenv("SDRSTREAM_WHISPER_MODEL"),
		Language: language,
		Threads:  threads,
		BeamSize: beam,
		Prompt:   prompt,
	}
}

func cmdMonitor(args []string) error {
	fs := flag.NewFlagSet("monitor", flag.ExitOnError)
	server := fs.String("server", "", "receiver URL")
	kind := fs.String("kind", "openwebrx", "receiver protocol: openwebrx, websdr or kiwi")
	profile := fs.String("profile", "", "OpenWebRX profile to use (a wide one shows more)")
	strategy := fs.String("strategy", "sweep", "sweep (listen across the band) or spectrum (use the receiver's FFT)")
	startHz := fs.Float64("start", 144000000, "sweep start frequency in Hz")
	endHz := fs.Float64("end", 146000000, "sweep end frequency in Hz")
	stepHz := fs.Float64("step", 25000, "sweep step in Hz")
	stepDwell := fs.Duration("dwell", 400*time.Millisecond, "how long to listen at each sweep step")
	threshold := fs.Float64("threshold", 10, "dB above the noise floor to call a peak a transmission")
	listen := fs.Duration("listen", 8*time.Second, "how much audio to record per transmission (0 = none)")
	mode := fs.String("mode", "auto", "modulation used when listening (auto picks fm or usb)")
	voiceOn := fs.Bool("voice", true, "transcribe voice traffic")
	voiceLang := fs.String("voice-lang", "auto", "language hint for speech recognition (auto detects)")
	voiceThreads := fs.Int("voice-threads", 0, "threads for whisper.cpp (0 = let whisper decide)")
	voiceBeam := fs.Int("voice-beam", 5, "beam-search width for whisper.cpp (1 = greedy)")
	voicePrompt := fs.String("voice-prompt", "", "domain hint for whisper.cpp (radio phraseology, callsigns); empty = built-in radio prompt")
	label := fs.String("label", "", "programme or station label carried on activities and gallery entries (e.g. the broadcast station heard)")
	rdioURL := fs.String("rdio", "", "upload each decoded transmission to an Rdio Scanner server (e.g. http://127.0.0.1:3000)")
	rdioKey := fs.String("rdio-key", "", "Rdio Scanner API key (a key with '*' systems accepts any talkgroup)")
	rdioSystem := fs.String("rdio-system", "", "system label shown in Rdio Scanner (default: the receiver URL)")
	out := fs.String("out", "", "directory to save the recorded transmissions")
	minutes := fs.Float64("minutes", 0, "stop after this many minutes (0 = run until interrupted)")
	fs.Parse(args)
	if *server == "" {
		return fmt.Errorf("--server is required")
	}
	// An Rdio upload needs the WAV on disk, so a target implies captures.
	if *rdioURL != "" && *out == "" {
		*out = filepath.Join("captures", "rdio")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt)
	defer stopSignals()

	source, err := monitor.SourceForLocal(ctx, *kind, *server, *profile)
	if err != nil {
		return err
	}
	defer source.Close()

	rdioQueue := rdio.NewQueue(rdio.Options{
		BaseURL:     *rdioURL,
		APIKey:      *rdioKey,
		SystemLabel: *rdioSystem,
	}, func(format string, args ...interface{}) { fmt.Printf(format+"\n", args...) })
	defer rdioQueue.Close()

	instance := monitor.New(monitor.Options{
		Source:          source,
		Strategy:        *strategy,
		StartHz:         *startHz,
		EndHz:           *endHz,
		StepHz:          *stepHz,
		Dwell:           *stepDwell,
		ThresholdDB:     *threshold,
		Listen:          *listen,
		Mode:            *mode,
		Voice:           *voiceOn,
		VoiceOptions:    monitorVoiceOptions(*voiceLang, *voiceThreads, *voiceBeam, *voicePrompt),
		VoiceNIMOptions: monitorNIMOptions(),
		OutDir:          *out,
		Name:            *server,
		Label:           *label,
		Logf:            func(format string, args ...interface{}) { fmt.Printf(format+"\n", args...) },
		OnActivity: func(activity monitor.Activity) {
			line := fmt.Sprintf("#%d %s %.4f MHz %s",
				activity.ID, activity.Started.UTC().Format("15:04:05"),
				activity.FrequencyHz/1e6, activity.Kind)
			if activity.Mode != "" {
				line += " (" + activity.Mode + ")"
			}
			if activity.LevelDB > 0 {
				line += fmt.Sprintf(" %.0f dB", activity.LevelDB)
			}
			fmt.Println(line)
			if activity.Detail != "" {
				fmt.Printf("    %s\n", activity.Detail)
			}
			if activity.AudioPath != "" && activity.Kind != "noise" && activity.Kind != "silence" {
				if err := verify.Live(activity.Kind, activity.Mode, activity.Detail, nil, activity.VerificationAnchor); err != nil {
					fmt.Printf("    rdio: skipped unverified capture: %v\n", err)
				} else {
					rdioQueue.Enqueue(rdioCallFor(activity, *rdioSystem))
				}
			}
		},
	})
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return err
		}
	}
	fmt.Printf("monitoring %s (%s); spectrum threshold %.0f dB, listen %s, voice %v\n",
		*server, *kind, *threshold, *listen, *voiceOn)
	if !voice.Available(monitorVoiceOptions(*voiceLang, *voiceThreads, *voiceBeam, *voicePrompt)) && !voice.NIMAvailable(monitorNIMOptions()) {
		fmt.Println("note: no speech backend found; voice traffic is reported but not transcribed")
	} else if voice.NIMAvailable(monitorNIMOptions()) {
		fmt.Println("speech backend: NVIDIA NIM (hosted), local whisper.cpp as fallback")
	} else {
		fmt.Println("speech backend: local whisper.cpp (set SDRSTREAM_NIM_API_KEY or NVIDIA_API_KEY to prefer hosted NIM)")
	}
	if *minutes > 0 {
		go func() {
			time.Sleep(time.Duration(*minutes * float64(time.Minute)))
			cancel()
		}()
	}
	err = instance.Run(ctx)
	if err == context.Canceled {
		fmt.Printf("\n%d activity(ies) seen\n", len(instance.Activities()))
		return nil
	}
	return err
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

// cmdGallery downloads the images and text that ground stations attached to
// their SatNOGS observations: waterfalls, decoded SSTV frames and payload
// files. Raw binary demod data is skipped unless -raw is set.
func cmdGallery(args []string) error {
	fs := flag.NewFlagSet("gallery", flag.ExitOnError)
	name := fs.String("satellite", "", "restrict to one satellite")
	days := fs.Int("days", 7, "how many days back to search")
	limit := fs.Int("limit", 25, "maximum observations to inspect")
	out := fs.String("out", "", "output directory (default: cache/gallery)")
	maxMB := fs.Int64("max-mb", 20, "skip artefacts larger than this")
	raw := fs.Bool("raw", false, "also keep raw binary demod data")
	timeout := fs.Duration("timeout", 60*time.Second, "HTTP timeout")
	fs.Parse(args)

	norad := 0
	label := "all satellites"
	if *name != "" {
		sat, err := satByName(*name)
		if err != nil {
			return err
		}
		norad = sat.NoradID
		label = sat.Name
	}
	// The window reaches two days past the local clock: SatNOGS publishes
	// observations with the start time of the pass, and a clock that lags
	// the network's would otherwise hide the newest recordings.
	end := time.Now().UTC().AddDate(0, 0, 2)
	start := end.AddDate(0, 0, -*days)
	fmt.Printf("searching SatNOGS observations of %s from the last %d day(s)...\n", label, *days)
	observations, err := satnogs.Between(start, end, norad, *limit, *timeout)
	if err != nil {
		return err
	}
	items := gallery.Collect(observations)
	if len(items) == 0 {
		fmt.Println("no observations with waterfall or demod data in that window")
		return nil
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(satnogs.CacheDir, "gallery")
	}
	saved, err := gallery.Fetch(items, dir, *timeout, *maxMB<<20, *raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "some artefacts failed: %v\n", err)
	}
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].Start.After(saved[j].Start) })
	lastID := -1
	for _, item := range saved {
		if item.ObservationID != lastID {
			lastID = item.ObservationID
			fmt.Printf("\n#%d %s %s %s %.4f MHz %s\n", item.ObservationID,
				item.Satellite, item.Start.UTC().Format("2006-01-02 15:04"),
				item.Station, float64(item.FrequencyHz)/1e6, item.Mode)
		}
		fmt.Printf("  %-6s %8s  %s\n", item.Kind, humanBytes(item.Size), item.Path)
		if item.Text != "" {
			line := strings.SplitN(item.Text, "\n", 2)[0]
			fmt.Printf("         %s\n", line)
		}
	}
	observationsWithData := map[int]bool{}
	for _, item := range saved {
		observationsWithData[item.ObservationID] = true
	}
	fmt.Printf("\n%d artefact(s) from %d observation(s) saved to %s\n",
		len(saved), len(observationsWithData), dir)
	return nil
}

// humanBytes renders a byte count for the gallery listing.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
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
	threshold := fs.Float64("threshold", 10, "report peaks this many dB above the noise floor")
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

// operatorOpenWebRXOptions allows private/loopback receivers: these commands
// connect to a server the operator named explicitly.
func operatorOpenWebRXOptions() openwebrx.Options {
	opts := openwebrx.DefaultOptions()
	opts.AllowPrivate = true
	return opts
}

// connectWithRetry dials and handshakes, retrying a few times with backoff
// because some servers reject rapid successive connections.
func connectWithRetry(ctx context.Context, server string, attempts int) (*openwebrx.Client, openwebrx.Config, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		client, err := openwebrx.New(server, operatorOpenWebRXOptions())
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
		case e, ok := <-client.Events():
			if !ok {
				// Connection is done; fewer frames than asked for is
				// better than spinning on a closed channel.
				return spectra
			}
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
		case e, ok := <-client.Events():
			if !ok {
				return openwebrx.Config{}, false
			}
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
	capturesMaxGB := fs.Float64("captures-max-gb", 0, "prune oldest captures above this size in GiB (0 = no size limit)")
	capturesRetain := fs.Duration("captures-retain", 0, "prune captures older than this (0 = no age limit)")
	galleryDir := fs.String("gallery-dir", "", "directory for SatNOGS gallery artefacts (default: cache/gallery)")
	scanStations := fs.String("scan-stations", "", "comma-separated public receivers for bounded parallel scan (one connection per receiver)")
	scanOpenDirectory := fs.Bool("scan-open-directory", false, "scan OpenWebRX and WebSDR entries from receiverbook")
	scanKiwiDirectory := fs.Bool("scan-kiwi-directory", false, "scan free-slot KiwiSDR entries from cached public directory")
	scanDirectoryRefresh := fs.Duration("scan-directory-refresh", time.Hour, "re-fetch receiver directories this often; 0 disables refresh")
	scanRecordAll := fs.Bool("scan-record-all", false, "archive audio for every detected activity, including unverified/noise/carrier clips")
	scanState := fs.String("scan-state", defaultRotationStatePath(), "file that carries HF target / OpenWebRX profile rotation across restarts (empty disables)")
	scanWSPR := fs.Bool("scan-wspr", false, "run a separate 250s WSPR capture on the configured HF receiver")
	scanHFServer := fs.String("scan-hf-server", "", "comma-separated WebSDR/OpenWebRX/Kiwi receivers for rotating FT8/WSPR cycles")
	kiwiPreflightOnly := fs.Bool("kiwi-preflight", false, "preflight Kiwi receivers and print first-pass verdict counts, then exit")
	scanStart := fs.Int("scan-start-connections", 49, "initial scanner concurrency ramp step")
	scanMax := fs.Int("scan-max-connections", 100, "integrator scanner maximum concurrency (hard limit 1000)")
	scanRamp := fs.String("scan-ramp", "49,100", "comma-separated ramp steps, capped by --scan-max-connections")
	scanGoodRate := fs.Float64("scan-advance-error-rate", 0.15, "advance if a completed wave has refusal+timeout rate below this fraction")
	scanRefusalBackoff := fs.Float64("scan-backoff-refusal-rate", 0.30, "step down if a completed wave refusal rate exceeds this fraction")
	scanLoadAdvance := fs.Float64("scan-advance-load-ratio", 0.70, "advance only below this load-average/core ratio")
	scanLoadBackoff := fs.Float64("scan-backoff-load-ratio", 0.90, "deprecated: the load guard owns load step-down (see --scan-guard-drop-*)")
	scanBandwidth := fs.Float64("scan-bandwidth-limit-mbps", 20, "estimated aggregate receive bandwidth limit in Mbit/s")
	scanStreamKbps := fs.Float64("scan-stream-kbps", 100, "estimated bandwidth per active receiver stream in kbit/s")
	scanTargetLimit := fs.Int("scan-target-limit", 40, "maximum sequential frequencies per station visit (1..40)")
	scanStationSession := fs.Duration("scan-station-session", 5*time.Minute, "maximum connection duration per station visit (<=5m)")
	scanGuardWindow := fs.Duration("scan-guard-window", 30*time.Second, "rolling window for the scanner load mean")
	scanGuardDropRatio := fs.Float64("scan-guard-drop-ratio", 0.85, "drop one ramp step only after the rolling load mean stays above this ratio")
	scanGuardDropFor := fs.Duration("scan-guard-drop-for", 60*time.Second, "sustained overload required before the load guard drops a step")
	scanGuardRestoreRatio := fs.Float64("scan-guard-restore-ratio", 0.65, "restore one held step after the rolling load mean stays below this ratio")
	scanGuardRestoreFor := fs.Duration("scan-guard-restore-for", 60*time.Second, "sustained calm required before the load guard restores a step")
	scanGuardMaxRSS := fs.Uint64("scan-guard-max-rss-mib", 512, "hard RSS cap in MiB: reaching it forces an immediate step down")
	scanGuardMaxFDs := fs.Int("scan-guard-max-fds", 400000, "hard open file descriptor cap: reaching it forces an immediate step down")
	scanGuardMaxGoroutines := fs.Int("scan-guard-max-goroutines", 256000, "hard goroutine cap: reaching it forces an immediate step down")
	scanPoolMode := fs.String("scan-pool", "health", "scanner receiver pool: 'health' = rotating 1-3 receivers chosen by the receiver health table; 'wide' = legacy ramp over the whole roster")
	scanPoolSize := fs.Int("scan-pool-size", 3, "health-pool receiver connections (1-3)")
	monitorServer := fs.String("monitor-server", "", "receiver to monitor for live traffic (empty = no monitoring)")
	monitorLabel := fs.String("monitor-label", "", "programme or station label for monitor activities and gallery entries")
	monitorKind := fs.String("monitor-kind", "openwebrx", "monitor receiver protocol: openwebrx, websdr or kiwi")
	monitorProfile := fs.String("monitor-profile", "", "OpenWebRX profile for the monitor")
	monitorStrategy := fs.String("monitor-strategy", "sweep", "monitor strategy: sweep, spectrum or broadcast")
	monitorChannels := fs.String("monitor-channels", "", "broadcast channel rotation: \"default\" (terrestrial VHF/UHF voice) or comma list of freqHz[:mode[:label]]")
	monitorChannelPause := fs.Duration("monitor-channel-pause", 2*time.Second, "rest between broadcast channel visits (keeps load inside budget)")
	monitorStart := fs.Float64("monitor-start", 144000000, "monitor sweep start frequency in Hz")
	monitorEnd := fs.Float64("monitor-end", 146000000, "monitor sweep end frequency in Hz")
	monitorStep := fs.Float64("monitor-step", 25000, "monitor sweep step in Hz")
	monitorThreshold := fs.Float64("monitor-threshold", 10, "monitor threshold in dB above the noise floor")
	monitorListen := fs.Duration("monitor-listen", 8*time.Second, "audio to record per transmission")
	monitorDwell := fs.Duration("monitor-dwell", 400*time.Millisecond, "time to sample each monitor frequency during a sweep")
	monitorMode := fs.String("monitor-mode", "auto", "modulation used for monitor audio (auto, fm, usb, am, etc.)")
	monitorVoice := fs.Bool("monitor-voice", true, "transcribe voice traffic")
	monitorVoiceLang := fs.String("monitor-voice-lang", "auto", "language hint for speech recognition")
	monitorVoiceThreads := fs.Int("monitor-voice-threads", 0, "threads for whisper.cpp")
	monitorVoiceBeam := fs.Int("monitor-voice-beam", 5, "beam-search width for whisper.cpp")
	monitorVoicePrompt := fs.String("monitor-voice-prompt", "", "domain hint for whisper.cpp")
	monitorOut := fs.String("monitor-out", "", "directory to save the recorded transmissions")
	rdioURL := fs.String("rdio", "", "upload verified live captures to an Rdio Scanner server (e.g. http://127.0.0.1:3000)")
	rdioKey := fs.String("rdio-key", "", "Rdio Scanner API key")
	rdioSystem := fs.String("rdio-system", "sdrstream", "system label shown in Rdio Scanner")
	monitorMultimon := fs.String("monitor-multimon", "", "optional local multimon-ng executable (receive-only decode fallback)")
	monitorDirewolf := fs.String("monitor-direwolf", "", "optional local Dire Wolf executable (receive-only APRS fallback)")
	sstvImageHelp := fs.String("sstv-image-format-help", "write decoded SSTV image (Martin M1/M2, Scottie S1, Robot 36) as PNG", "help text for SSTV image decoder")
	_ = sstvImageHelp
	noAuth := fs.Bool("no-auth", false, "run the web UI without a password (INSECURE: required for non-loopback binds when SDRSTREAM_UI_PASSWORD is unset)")
	noFeeds := fs.Bool("no-feeds", false, "disable public data feeds")
	feedsLat := fs.Float64("feeds-lat", 31.8, "feeds center latitude")
	feedsLon := fs.Float64("feeds-lon", 35.0, "feeds center longitude")
	aisKey := fs.String("ais-key", os.Getenv("SDRSTREAM_AIS_KEY"), "aisstream.io key; prefer SDRSTREAM_AIS_KEY")
	fs.Parse(args)
	if *capturesMaxGB < 0 || *capturesRetain < 0 || *scanDirectoryRefresh < 0 {
		return fmt.Errorf("--captures-max-gb, --captures-retain and --scan-directory-refresh cannot be negative")
	}
	if *scanPoolMode != "health" && *scanPoolMode != "wide" {
		return fmt.Errorf("--scan-pool must be 'health' or 'wide'")
	}
	if *scanPoolSize < 1 || *scanPoolSize > 3 {
		return fmt.Errorf("--scan-pool-size must be 1..3")
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if *kiwiPreflightOnly {
		return cmdKiwiPreflight(strings.Split(*scanStations, ","))
	}
	if *monitorMultimon != "" && decode.MultimonBinary(*monitorMultimon) == "" {
		return fmt.Errorf("-monitor-multimon: %q is not an executable file", *monitorMultimon)
	}
	if *monitorDirewolf != "" && decode.DirewolfBinary(*monitorDirewolf) == "" {
		return fmt.Errorf("-monitor-direwolf: %q is not an executable file", *monitorDirewolf)
	}
	var channels []monitor.Channel
	if *monitorChannels == "default" {
		channels = monitor.DefaultVoiceChannels()
	} else if *monitorChannels != "" {
		var err error
		if channels, err = monitor.ParseChannels(*monitorChannels); err != nil {
			return err
		}
	}
	server := web.New(*capturesDir, *galleryDir)
	// Rotation cursors survive a restart when the operator keeps the state
	// file; a corrupt or missing file just starts coverage from the top.
	configureRotationState(*scanState, log.Printf)
	// The board state is persisted with the server; seed the scanner's copy
	// so a restart does not rewrite decoded rows as "queued".
	scansMu.Lock()
	scans = server.ScanStatuses()
	scansMu.Unlock()
	server.NoAuth = *noAuth
	rdioQueue := rdio.NewQueue(rdio.Options{
		BaseURL:     *rdioURL,
		APIKey:      *rdioKey,
		SystemLabel: *rdioSystem,
	}, func(format string, args ...interface{}) { log.Printf("rdio: "+format, args...) })
	defer rdioQueue.Close()
	if *rdioURL != "" {
		server.SetRdioHook(func(activity monitor.Activity) {
			rdioQueue.Enqueue(rdioCallFor(activity, *rdioSystem))
		})
		log.Printf("rdio: uploading verified live captures to %s", *rdioURL)
	}
	// The proof ledger is not a feed: the receiver-capture ledger and the
	// metrics endpoints need it even when the public feeds are disabled.
	feedsManager := feeds.New(0, nil)
	ledgerBytes, ledgerOK := feeds.LedgerBytesFromEnv(os.Getenv)
	if !ledgerOK {
		log.Printf("feeds: ignoring invalid SDRSTREAM_LEDGER_MAX_MB (want 1-%d); using %d MiB", feeds.MaxLedgerMB, ledgerBytes>>20)
	}
	ledger := feeds.NewLedger(web.StateFile("sdrstream-ledger.jsonl"), ledgerBytes)
	defer ledger.Close()
	feedsManager.SetLedger(ledger)
	server.SetFeeds(feedsManager)
	if !*noFeeds {
		feedsCtx, feedsCancel := context.WithCancel(ctx)
		defer feedsCancel()
		feeds.Start(feedsCtx, feedsManager, *feedsLat, *feedsLon, *aisKey)
	}
	roster := newScanRoster(log.Printf)
	if strings.TrimSpace(*scanStations) != "" {
		roster.AddManual(strings.Split(*scanStations, ",")...)
	}
	var kiwiLoader *kiwisdr.Loader
	if *scanKiwiDirectory {
		cacheDir := filepath.Join(os.Getenv("HOME"), ".cache", "sdrstream")
		if stateDir := os.Getenv("SDRSTREAM_STATE_DIR"); stateDir != "" {
			cacheDir = filepath.Join(stateDir, "cache")
		}
		kiwiLoader = kiwisdr.NewLoader(cacheDir)
	}
	refreshStations := func(refreshCtx context.Context) ([]string, error) {
		var out []string
		if *scanOpenDirectory {
			stations, err := directory.FetchStations(refreshCtx, 30*time.Second)
			if err != nil {
				return nil, fmt.Errorf("fetch receiver directory: %w", err)
			}
			for _, station := range stations {
				if station.Type == "OpenWebRX" || station.Type == "WebSDR" {
					out = append(out, station.Type+"|"+station.URL)
				}
			}
			out = append(out, "WebSDR|http://websdr.ewi.utwente.nl:8901/", "OpenWebRX|http://jukebox.spdns.eu:6166/")
		}
		if *scanKiwiDirectory && kiwiLoader != nil {
			receivers, _, err := kiwiLoader.Load(refreshCtx)
			if err != nil {
				return nil, fmt.Errorf("load KiwiSDR directory: %w", err)
			}
			out = appendUsableKiwiStations(out, receivers)
		}
		return out, nil
	}
	roster.refresh = refreshStations
	roster.interval = *scanDirectoryRefresh
	if *scanOpenDirectory || *scanKiwiDirectory {
		initial, err := refreshStations(ctx)
		if err != nil {
			// The directory must not stop the server: losing receiverbook
			// would otherwise kill a web run that still has manual
			// --scan-stations, and the refresh loop retries anyway.
			log.Printf("scan: initial directory fetch failed, starting without it (%v)", err)
		}
		roster.SetDynamic(initial)
		if *scanOpenDirectory && *scanHFServer == "" {
			*scanHFServer = "http://websdr.ewi.utwente.nl:8901/"
		}
		roster.StartRefresh(ctx)
	}
	// Start the scanner whenever the roster has any source of stations,
	// not only after a successful initial fetch: with directory mode on,
	// the refresh loop repopulates the list and the next pass scans it.
	if len(roster.Snapshot()) > 0 || *scanOpenDirectory || *scanKiwiDirectory {
		config, err := newScanConfig(*scanStart, *scanMax, *scanRamp, *scanGoodRate, *scanRefusalBackoff, *scanLoadAdvance, *scanLoadBackoff, *scanBandwidth, *scanStreamKbps, *scanTargetLimit, *scanStationSession)
		if err != nil {
			return err
		}
		config.recordAll = *scanRecordAll
		guard := newLoadGuard(*scanGuardWindow, *scanGuardDropFor, *scanGuardRestoreFor, *scanGuardDropRatio, *scanGuardRestoreRatio, *scanGuardMaxRSS<<20, *scanGuardMaxFDs, *scanGuardMaxGoroutines)
		scannerCaptureDir = *capturesDir
		if *scanPoolMode == "health" {
			pool, err := newHealthPool(*scanPoolSize, healthTablePath(), log.Printf)
			if err != nil {
				return err
			}
			scanHealth = pool
			defer pool.save()
			go pool.saveLoop(ctx)
			go pool.beaconProber(ctx, roster)
			log.Printf("scan: health pool on, holding %d rotating receiver connection(s)", pool.size)
		}
		go runParallelScan(ctx, server, roster, *monitorMultimon, *monitorDirewolf, config, guard)
	} else if strings.TrimSpace(*scanStations) != "" {
		log.Printf("scan: ROSTER EMPTY: --scan-stations produced no usable entries (each needs a Kind| prefix, e.g. KiwiSDR|host:8073); scanner not started")
	}
	// The dedicated HF capture schedule is independent of directory scanning.
	if *scanHFServer != "" {
		go runDirectoryHFScan(ctx, server, *scanHFServer, *scanWSPR, *capturesDir, *scanRecordAll)
	}
	if *capturesMaxGB > 0 || *capturesRetain > 0 {
		go pruneCapturesLoop(ctx, *capturesDir, int64(*capturesMaxGB*float64(1<<30)), *capturesRetain, time.Minute, log.Printf)
	}
	monitorCfg := monitorDefaults{
		Strategy:     *monitorStrategy,
		Channels:     channels,
		ChannelPause: *monitorChannelPause,
		StartHz:      *monitorStart,
		EndHz:        *monitorEnd,
		StepHz:       *monitorStep,
		ThresholdDB:  *monitorThreshold,
		Dwell:        *monitorDwell,
		Listen:       *monitorListen,
		Mode:         *monitorMode,
		Voice:        *monitorVoice,
		VoiceOptions: monitorVoiceOptions(*monitorVoiceLang, *monitorVoiceThreads, *monitorVoiceBeam, *monitorVoicePrompt),
		VoiceNIM:     monitorNIMOptions(),
		Multimon:     *monitorMultimon,
		Direwolf:     *monitorDirewolf,
	}
	runtimeMon := &runtimeMonitor{server: server, cfg: monitorCfg}
	server.SetMonitorHooks(web.MonitorHooks{
		Start: func(spec web.MonitorStartSpec) error {
			outDir := *monitorOut
			if outDir == "" {
				outDir = *capturesDir
			}
			return runtimeMon.start(ctx, spec, *monitorLabel, outDir)
		},
		Stop: func() error {
			runtimeMon.stop()
			return nil
		},
	})
	if *monitorServer != "" {
		if err := runtimeMon.start(ctx, web.MonitorStartSpec{
			Server:  *monitorServer,
			Kind:    *monitorKind,
			Profile: *monitorProfile,
			Label:   *monitorLabel,
		}, *monitorLabel, *monitorOut); err != nil {
			return err
		}
		fmt.Printf("monitoring %s (%s)\n", *monitorServer, *monitorKind)
	}
	return server.ServeContext(ctx, fmt.Sprintf("%s:%d", *host, *port))
}

// monitorDefaults carries the flag-derived tuning shared by the
// --monitor-server monitor and monitors started at runtime from the UI.
type monitorDefaults struct {
	Strategy     string
	Channels     []monitor.Channel
	ChannelPause time.Duration
	StartHz      float64
	EndHz        float64
	StepHz       float64
	ThresholdDB  float64
	Dwell        time.Duration
	Listen       time.Duration
	Mode         string
	Voice        bool
	VoiceOptions voice.Options
	VoiceNIM     voice.NIMOptions
	Multimon     string
	Direwolf     string
}

// monitorOnActivity gates live text detections through verification
// before they join the activity gallery.
func monitorOnActivity(server *web.Server) func(monitor.Activity) {
	return func(activity monitor.Activity) {
		if activity.Detail != "" && (activity.Kind == "data" || activity.Kind == "cw" || activity.Kind == "sstv" || activity.Kind == "voice") {
			if err := verify.Live(activity.Kind, activity.Mode, activity.Detail, nil, activity.VerificationAnchor); err != nil {
				writeActivitySidecar(server, activity, false, err.Error())
				log.Printf("gallery: verification rejected live text: %v", err)
				return
			}
			writeActivitySidecar(server, activity, true, "live gate passed")
			if err := server.AddLiveArtifact(activity, nil); err != nil {
				log.Printf("gallery: save live text: %v", err)
			}
		}
	}
}

// monitorOnImage gates live reconstructed images through verification.
func monitorOnImage(server *web.Server) func(monitor.Activity, []byte) {
	return func(activity monitor.Activity, png []byte) {
		if err := verify.Live(activity.Kind, activity.Mode, "", png); err != nil {
			writeActivitySidecar(server, activity, false, err.Error())
			log.Printf("gallery: verification rejected live image: %v", err)
			return
		}
		writeActivitySidecar(server, activity, true, "live gate passed")
		if err := server.AddLiveArtifact(activity, png); err != nil {
			log.Printf("gallery: save live image: %v", err)
		}
	}
}

// monitorOnCapture writes a provenance sidecar for every archived clip.
func monitorOnCapture(server *web.Server) func(monitor.Activity, string) {
	return func(activity monitor.Activity, path string) {
		activity.AudioPath = path
		writeActivitySidecar(server, activity, false, "pending verification")
	}
}

// runtimeMonitor owns the one live monitor attached to the web server.
// Start replaces a running monitor (a phone or shared receiver should
// never carry two); Stop is idempotent. It backs /api/monitor/control's
// start and stop actions.
type runtimeMonitor struct {
	server *web.Server
	cfg    monitorDefaults

	mu     sync.Mutex
	cancel context.CancelFunc
	gen    int
}

// start builds and attaches a monitor for spec. Construction errors
// (unknown kind, unreachable or refusing receiver) are returned so the
// caller can report an honest failure to the UI.
func (m *runtimeMonitor) start(ctx context.Context, spec web.MonitorStartSpec, label, outDir string) error {
	monitorCtx, cancel := context.WithCancel(ctx)
	source, err := monitor.SourceForLocal(monitorCtx, spec.Kind, spec.Server, spec.Profile)
	if err != nil {
		cancel()
		return err
	}
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			cancel()
			return fmt.Errorf("monitor output dir: %w", err)
		}
	}
	if spec.Label != "" {
		label = spec.Label
	}
	startHz, endHz := m.cfg.StartHz, m.cfg.EndHz
	if spec.StartHz > 0 {
		startHz = spec.StartHz
	}
	if spec.EndHz > 0 {
		endHz = spec.EndHz
	}
	instance := monitor.New(monitor.Options{
		OnActivity:      monitorOnActivity(m.server),
		OnImage:         monitorOnImage(m.server),
		OnCapture:       monitorOnCapture(m.server),
		Source:          source,
		Strategy:        m.cfg.Strategy,
		Channels:        m.cfg.Channels,
		ChannelPause:    m.cfg.ChannelPause,
		StartHz:         startHz,
		EndHz:           endHz,
		StepHz:          m.cfg.StepHz,
		ThresholdDB:     m.cfg.ThresholdDB,
		Dwell:           m.cfg.Dwell,
		Listen:          m.cfg.Listen,
		Mode:            m.cfg.Mode,
		Voice:           m.cfg.Voice,
		VoiceOptions:    m.cfg.VoiceOptions,
		VoiceNIMOptions: m.cfg.VoiceNIM,
		MultimonBinary:  m.cfg.Multimon,
		DirewolfBinary:  m.cfg.Direwolf,
		OutDir:          outDir,
		Name:            spec.Server,
		Label:           label,
		Logf:            func(format string, args ...interface{}) { log.Printf("monitor: "+format, args...) },
	})
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = cancel
	m.gen++
	gen := m.gen
	m.mu.Unlock()
	m.server.SetMonitor(instance)
	go func() {
		if err := instance.Run(monitorCtx); err != nil && monitorCtx.Err() == nil {
			log.Printf("monitor: %v", err)
		}
		// If this monitor is still the attached one, detach so the UI
		// shows "no monitor" instead of a stale dead receiver.
		m.mu.Lock()
		still := m.gen == gen
		m.mu.Unlock()
		if still {
			m.server.SetMonitor(nil)
		}
	}()
	return nil
}

// stop detaches and cancels the running monitor, if any.
func (m *runtimeMonitor) stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
		m.gen++
	}
	m.mu.Unlock()
	m.server.SetMonitor(nil)
}

// cmdFeeds reports a bounded snapshot of the configured public live feeds.
func cmdFeeds(args []string) error {
	fs := flag.NewFlagSet("feeds", flag.ExitOnError)
	lat := fs.Float64("lat", 32.08, "center latitude")
	lon := fs.Float64("lon", 34.78, "center longitude")
	listen := fs.Duration("listen", 20*time.Second, "listen duration")
	aisKey := fs.String("ais-key", os.Getenv("SDRSTREAM_AIS_KEY"), "AIS API key")
	_ = fs.Parse(args)
	manager := feeds.New(0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), *listen+15*time.Second)
	defer cancel()
	feeds.Start(ctx, manager, *lat, *lon, *aisKey)
	select {
	case <-time.After(*listen):
	case <-ctx.Done():
	}
	snap := manager.Snapshot(40)
	for _, s := range snap.Sources {
		fmt.Printf("%s %s %d\n", s.Name, s.State, s.Items)
	}
	return nil
}

// cmdDecode extracts data from a recorded pass: APRS packets via
// AFSK/HDLC/AX.25, Morse text from CW beacons, DTMF digits, RTTY, PSK31,
// SSTV image headers, POCSAG pager pages, EAS/SAME alert headers and NAVTEX
// maritime broadcasts.
func cmdDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	mode := fs.String("mode", "auto", "auto | aprs | morse | dtmf | rtty | psk31 | sstv | pocsag | eas | navtex | hfdl | voice | all (every data mode, no speech)")
	offset := fs.Float64("offset", 0, "audio centre frequency in Hz for psk31/rtty (0 = auto)")
	imageOut := fs.String("sstv-image", "", "write decoded Martin M2 SSTV image as PNG")
	aptOut := fs.String("apt-image", "", "write decoded NOAA APT picture as PNG (with -mode apt)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("decode: pass one or more WAV files")
	}
	for _, path := range fs.Args() {
		fmt.Printf("== %s\n", path)
		if *mode == "acars" {
			chs, rate, err := decode.ReadWAVChannels(path)
			if err != nil {
				fmt.Printf("   read: %v\n", err)
				continue
			}
			n := 0
			for c, ch := range chs {
				for _, m := range decode.ACARSDecode(ch, rate, c) {
					n++
					fmt.Printf("   acars:  ch%d %.2fs %s\n", c, m.AtSec, m)
				}
			}
			if n == 0 {
				fmt.Println("   acars:  no CRC-valid blocks")
			}
			continue
		}
		samples, rate, err := decode.ReadWAV(path)
		if err != nil {
			fmt.Printf("   read: %v\n", err)
			continue
		}
		wantAll := *mode == "all"

		if *mode == "apt" {
			r := decode.APTDecode(samples, rate)
			ok, why := r.Verified()
			if r == nil {
				fmt.Println("   apt:    no sync found")
				continue
			}
			fmt.Printf("   apt:    lines=%d locked=%d syncA=%.2f syncB=%.2f verified=%v (%s)\n", r.Lines, r.LinesLocked, r.MeanSyncA, r.SyncB, ok, why)
			if *aptOut != "" && ok {
				if b, err := r.PNG(); err == nil {
					os.WriteFile(*aptOut, b, 0o644)
				}
			}
			continue
		}

		if *mode == "dtmf" || wantAll {
			if s := decode.DTMFDecode(samples, rate); s != "" {
				fmt.Printf("   dtmf:   %s\n", s)
			} else if *mode == "dtmf" {
				fmt.Println("   dtmf:   no tones")
			}
		}
		if *mode == "rtty" || wantAll {
			// Auto mark/space polarity: DWD-style F1B services key mark
			// low, amateurs key mark high.
			if s, inverted := decode.RTTYDecodeAuto(samples, rate, 0, 0, 0); s != "" {
				note := ""
				if inverted {
					note = " [inverted mark/space]"
				}
				fmt.Printf("   rtty:   %s%s\n", strings.TrimSpace(s), note)
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
		if *mode == "sstv" || wantAll || *imageOut != "" {
			hits := decode.SSTVDetect(samples, rate)
			if len(hits) == 0 && (*mode == "sstv" || *imageOut != "") {
				fmt.Println("   sstv:   no VIS header")
			}
			for _, h := range hits {
				fmt.Printf("   sstv:   %s (VIS 0x%02X) at %.1f s\n", h.Mode, h.VIS, h.StartSec)
			}
			if *imageOut != "" {
				pngBytes, err := decode.SSTVImage(samples, rate)
				if err != nil {
					fmt.Printf("   sstv image: %v\n", err)
				} else if err := os.WriteFile(*imageOut, pngBytes, 0o644); err != nil {
					fmt.Printf("   sstv image: write: %v\n", err)
				} else {
					fmt.Printf("   sstv image: wrote %s (%d bytes)\n", *imageOut, len(pngBytes))
				}
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
		if *mode == "flex" || wantAll {
			msgs := decode.FlexDecode(samples, rate)
			if len(msgs) == 0 && *mode == "flex" {
				fmt.Println("   flex:   no pages")
			}
			for _, m := range msgs {
				fmt.Printf("   flex:   %s\n", m.String())
			}
		}
		if *mode == "eas" || wantAll {
			alerts := decode.EASDecode(samples, rate)
			if len(alerts) == 0 && *mode == "eas" {
				fmt.Println("   eas:    no SAME headers")
			}
			for _, a := range alerts {
				fmt.Printf("   eas:    %s\n", a.String())
			}
		}
		if *mode == "hfdl" || wantAll {
			frames := hfdl.DecodeAudio(samples, float64(rate), hfdl.SSBCarrierOffsetHz, 0)
			if len(frames) == 0 && *mode == "hfdl" {
				fmt.Println("   hfdl:   no frame with a valid FCS")
			}
			for _, f := range frames {
				for _, line := range hfdl.Describe(f) {
					fmt.Printf("   hfdl:   %s\n", line)
				}
			}
		}
		if *mode == "navtex" || wantAll {
			msgs := decode.NAVTEXDecode(samples, rate, *offset)
			if len(msgs) == 0 && *mode == "navtex" {
				fmt.Println("   navtex: no messages")
			}
			for _, m := range msgs {
				fmt.Printf("   navtex: %s%s %s\n", m.Station, m.Subject, m.Serial)
				for _, line := range strings.Split(strings.TrimRight(m.Text, "\n"), "\n") {
					fmt.Printf("           %s\n", line)
				}
			}
		}

		// Speech recognition is deliberately not part of "all": whisper
		// takes tens of seconds per file, and a scan of a directory of
		// recordings would spend all its time transcribing noise. Asking
		// for voice is explicit.
		if *mode == "voice" || *mode == "speech" {
			segments := voice.DetectSpeech(samples, rate, voice.VADOptions{})
			if len(segments) == 0 {
				fmt.Println("   voice: no speech detected")
			} else {
				transcript, err := voice.TranscribeBest(context.Background(), samples, rate, voice.DefaultOptions(), voice.DefaultNIMOptions())
				result := voice.Result{Text: transcript.Text}
				if err != nil {
					fmt.Printf("   voice: %v\n", err)
				} else {
					fmt.Printf("   voice: %s\n", result.Text)
					fmt.Printf("          confidence %.0f%%, language %s, %d chunk(s), %.1f s of speech, model %s, fallback=%v\n",
						100*result.Confidence, result.Language, result.Chunks, result.SpeechSec, result.Model, result.Fallback)
				}
			}
		}

		if *mode == "aprs" || *mode == "morse" || *mode == "auto" || wantAll {
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
			} else if *mode == "auto" || *mode == "aprs" || wantAll {
				packets, err := decode.AFSKFrames(path)
				if err != nil {
					fmt.Printf("   aprs: %v\n", err)
				} else if len(packets) == 0 {
					fmt.Println("   aprs: no packets")
				} else {
					for _, p := range packets {
						fmt.Printf("   packet: %s\n", p.String())
						if a := p.APRS(); a.HasPos {
							fmt.Printf("           aprs %s: %.5f, %.5f", a.Kind, a.Lat, a.Lon)
							if a.HasAlt {
								fmt.Printf(" alt %.0f m", a.Altitude)
							}
							if a.Speed > 0 {
								fmt.Printf(" %.0f km/h @ %d", a.Speed, a.Course)
							}
							fmt.Println()
						}
					}
				}
			}
			if *mode == "auto" || *mode == "morse" || wantAll {
				res, err := decode.MorseDecode(path)
				if err != nil {
					fmt.Printf("   morse: %v\n", err)
				} else {
					if res.Text != "" {
						fmt.Printf("   morse: tone %.0f Hz (SNR %.0f dB, %d key-downs, dot %.0f ms)\n",
							res.ToneHz, res.SnrdB, res.DotsFound, res.DotMs)
						fmt.Printf("   text:  %s\n", res.Text)
					} else {
						fmt.Printf("   morse: no CW text (strongest tone %.0f Hz, SNR %.0f dB)\n",
							res.ToneHz, res.SnrdB)
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

type scanTarget struct {
	server    string
	frequency float64
	mode      string
}

type scanConfig struct {
	start, maximum                       int
	ramp                                 []int
	advanceErrorRate, backoffRefusalRate float64
	// backoffLoadRatio is deprecated: the load guard owns load step-down.
	advanceLoadRatio, backoffLoadRatio float64
	bandwidthLimitMbps, streamKbps     float64
	targetLimit                        int
	stationSession                     time.Duration
	recordAll                          bool
}

func newScanConfig(start, maximum int, ramp string, advanceError, backoffRefusal, advanceLoad, backoffLoad, bandwidth, streamKbps float64, targetLimit int, stationSession time.Duration) (scanConfig, error) {
	if maximum < 1 || maximum > scanHardMaxConnections {
		return scanConfig{}, fmt.Errorf("--scan-max-connections must be 1..%d", scanHardMaxConnections)
	}
	if targetLimit < 1 || targetLimit > 40 {
		return scanConfig{}, fmt.Errorf("--scan-target-limit must be 1..40")
	}
	if stationSession <= 0 || stationSession > 5*time.Minute {
		return scanConfig{}, fmt.Errorf("--scan-station-session must be >0 and <=5m")
	}
	steps := []int{}
	for _, raw := range strings.Split(ramp, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 {
			return scanConfig{}, fmt.Errorf("invalid --scan-ramp step %q", raw)
		}
		n = min(maximum, n)
		if len(steps) == 0 || steps[len(steps)-1] < n {
			steps = append(steps, n)
		}
	}
	if len(steps) == 0 {
		return scanConfig{}, fmt.Errorf("--scan-ramp must include at least one step")
	}
	if start < 1 {
		return scanConfig{}, fmt.Errorf("--scan-start-connections must be positive")
	}
	if start > maximum {
		return scanConfig{}, fmt.Errorf("--scan-start-connections cannot exceed scanner maximum")
	}
	if advanceError < 0 || advanceError > 1 || backoffRefusal < 0 || backoffRefusal > 1 || advanceLoad <= 0 || backoffLoad <= 0 || bandwidth <= 0 || streamKbps <= 0 {
		return scanConfig{}, fmt.Errorf("scan ramp rates and resource limits must be positive fractions/values")
	}
	if !slices.Contains(steps, start) {
		insert := len(steps)
		for i, step := range steps {
			if step > start {
				insert = i
				break
			}
		}
		steps = append(steps, 0)
		copy(steps[insert+1:], steps[insert:])
		steps[insert] = start
	}
	return scanConfig{start: start, maximum: maximum, ramp: steps, advanceErrorRate: advanceError, backoffRefusalRate: backoffRefusal, advanceLoadRatio: advanceLoad, backoffLoadRatio: backoffLoad, bandwidthLimitMbps: bandwidth, streamKbps: streamKbps, targetLimit: targetLimit, stationSession: stationSession}, nil
}

type stationBlock struct {
	Reason string
	Until  time.Time
}

var blockMu sync.Mutex

func blocklistPath() string {
	dir := os.Getenv("SDRSTREAM_STATE_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), "sdrstream-state")
	}
	return filepath.Join(dir, "station-blocklist.gob")
}
func loadBlocklist() map[string]stationBlock {
	m := map[string]stationBlock{}
	f, err := os.Open(blocklistPath())
	if err != nil {
		return m
	}
	defer f.Close()
	_ = gob.NewDecoder(f).Decode(&m)
	return m
}
func cleanShutdownBlocklistEntries(m map[string]stationBlock) (map[string]stationBlock, int) {
	cleaned := make(map[string]stationBlock, len(m))
	removed := 0
	for host, entry := range m {
		if isShutdownBlockReason(entry.Reason) {
			removed++
			continue
		}
		cleaned[host] = entry
	}
	return cleaned, removed
}

func isShutdownBlockReason(reason string) bool {
	text := strings.ToLower(reason)
	return strings.Contains(text, "use of closed network connection") || strings.Contains(text, "context canceled") || strings.Contains(text, "context canceled by user")
}

func purgeShutdownBlocklistEntries() int {
	blockMu.Lock()
	defer blockMu.Unlock()
	current := loadBlocklist()
	cleaned, removed := cleanShutdownBlocklistEntries(current)
	if removed > 0 {
		saveBlocklist(cleaned)
	}
	return removed
}

func saveBlocklist(m map[string]stationBlock) {
	path := blocklistPath()
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	if gob.NewEncoder(f).Encode(m) != nil {
		f.Close()
		_ = os.Remove(tmp)
		return
	}
	_ = f.Sync()
	_ = f.Close()
	_ = os.Rename(tmp, path)
}
func shouldBlockScanFailure(ctx context.Context, reason string) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !isShutdownBlockReason(reason)
}

func blockStationIfEligible(ctx context.Context, server *web.Server, host, reason string, duration time.Duration) {
	if shouldBlockScanFailure(ctx, reason) {
		blockStation(server, host, reason, duration)
	}
}

func blockStation(server *web.Server, host, reason string, duration time.Duration) {
	until := time.Now().Add(duration)
	blockMu.Lock()
	m := loadBlocklist()
	m[strings.ToLower(host)] = stationBlock{reason, until}
	saveBlocklist(m)
	blockMu.Unlock()
	t := until
	server.UpsertScanStatus(web.ScanStatus{Station: host, State: "blocked", Result: reason, BlockedUntil: &t, UpdatedAt: time.Now()})
}

// stationHostPortKey normalises a roster host to lowercase host:port for
// dedup and health-table keys. The receiver kind is deliberately not part
// of the key: one receiver gets one health record no matter which roster
// entry form (or call site) named it.
func stationHostPortKey(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" || u.Scheme == "wss" {
				port = "443"
			} else {
				port = "80"
			}
		}
		return strings.ToLower(net.JoinHostPort(u.Hostname(), port))
	}
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(raw), "/"))
}

func appendUsableKiwiStations(existing []string, candidates []kiwisdr.Receiver) []string {
	seen := make(map[string]bool, len(existing)+len(candidates))
	for _, item := range existing {
		parts := strings.SplitN(strings.TrimSpace(item), "|", 2)
		if len(parts) == 2 {
			seen[stationHostPortKey(parts[1])] = true
		}
	}
	for _, receiver := range candidates {
		if !receiver.Usable() {
			continue
		}
		key := stationHostPortKey(receiver.ServerURL())
		if seen[key] {
			continue
		}
		seen[key] = true
		existing = append(existing, "KiwiSDR|"+receiver.ServerURL())
	}
	return existing
}

// scanEmptyRoster makes an empty scan roster loud; the scan loop re-enters
// every 20 s, so without throttling the lines would drown the log.
var scanEmptyRoster = newEmptyRosterAlerter(log.Printf)

func runParallelScan(ctx context.Context, server *web.Server, roster *scanRoster, multimonPath, direwolfPath string, cfg scanConfig, guard *loadGuard) {
	for ctx.Err() == nil {
		runParallelScanOnce(ctx, server, roster, multimonPath, direwolfPath, cfg, guard)
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(20 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runParallelScanOnce(ctx context.Context, server *web.Server, roster *scanRoster, multimonPath, direwolfPath string, cfg scanConfig, guard *loadGuard) {
	purgeShutdownBlocklistEntries()
	var receivers []string
	if roster != nil {
		receivers = roster.Snapshot()
	}
	if guard == nil {
		guard = newLoadGuard(0, 0, 0, 0, 0, 0, 0, 0)
	}
	unique := make([]string, 0, len(receivers))
	seen := map[string]bool{}
	for _, raw := range receivers {
		parts := strings.SplitN(strings.TrimSpace(raw), "|", 2)
		if len(parts) != 2 {
			continue
		}
		kind, host := strings.TrimSpace(parts[0]), strings.TrimSpace(strings.TrimRight(parts[1], "/"))
		key := stationHostPortKey(host)
		if host == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, kind+"|"+host)
	}
	if len(unique) == 0 {
		scanEmptyRoster.empty("no stations", fmt.Sprintf("roster snapshot yielded 0 usable Kind|host entries from %d raw entries; check --scan-stations kinds and the directory refresh", len(receivers)))
		return
	}
	blockMu.Lock()
	blocked := loadBlocklist()
	blockMu.Unlock()
	activeUnique := make([]string, 0, len(unique))
	for _, entry := range unique {
		parts := strings.SplitN(entry, "|", 2)
		b, ok := blocked[strings.ToLower(parts[1])]
		if ok && time.Now().Before(b.Until) {
			t := b.Until
			server.UpsertScanStatus(web.ScanStatus{Station: parts[1], StationType: parts[0], State: "blocked", Result: b.Reason, BlockedUntil: &t, UpdatedAt: time.Now()})
			continue
		}
		activeUnique = append(activeUnique, entry)
	}
	dropped := len(unique) - len(activeUnique)
	unique = activeUnique
	if len(unique) == 0 {
		scanEmptyRoster.empty("all stations blocklisted", fmt.Sprintf("all %d roster stations are on the blocklist", dropped))
		return
	}
	rosterAll := unique
	if scanHealth != nil {
		unique = scanHealth.pickStations(unique)
		if len(unique) == 0 {
			scanEmptyRoster.empty("health pool picked nothing", fmt.Sprintf("0 of %d roster stations eligible; every receiver may be in failure cooldown", len(rosterAll)))
			return
		}
	}
	scanEmptyRoster.populated(len(unique))
	steps := cfg.ramp
	var current atomic.Int64
	current.Store(int64(min(cfg.maximum, cfg.start)))
	var active atomic.Int64
	var metricsMu sync.Mutex
	var guardMu sync.Mutex
	refusals, timeouts := 0, 0
	coreCount := runtime.NumCPU()
	updateMetrics := func() {
		load := readLoadAverage()
		guardMu.Lock()
		guard.observe(time.Now(), load/float64(max(1, coreCount)))
		guardMu.Unlock()
		metricsMu.Lock()
		cumulativeRefusals, cumulativeTimeouts := refusals, timeouts
		metricsMu.Unlock()
		target, paused := server.ScanControl()
		server.SetScanMetrics(web.ScanMetrics{RampStep: int(current.Load()), MaxConnections: cfg.maximum, TargetConcurrency: target, Paused: paused, ActiveConnections: int(active.Load()), Refusals: cumulativeRefusals, Timeouts: cumulativeTimeouts, LoadAverage: load, CoreCount: coreCount, EstimatedBandwidthMbps: float64(active.Load()) * cfg.streamKbps / 1000, BandwidthLimitMbps: cfg.bandwidthLimitMbps, MemoryBytes: func() uint64 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.Sys }(), OpenFDs: openFDCount()})
	}
	server.SetScanMetrics(web.ScanMetrics{RampStep: int(current.Load()), MaxConnections: cfg.maximum, TargetConcurrency: cfg.start, CoreCount: coreCount, BandwidthLimitMbps: cfg.bandwidthLimitMbps, MemoryBytes: func() uint64 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.Sys }(), OpenFDs: openFDCount()})
	updateMetrics()
	type result struct {
		host    string
		outcome scanOutcome
	}
	results := make(chan result, cfg.maximum)
	dispatchTick := time.NewTicker(time.Second)
	defer dispatchTick.Stop()
	inFlight := 0
	nextStation := 0
	activeStations := map[string]bool{}
	activeCancels := map[string]context.CancelFunc{}
	backoffUntil := map[string]time.Time{}
	backoffFor := map[string]time.Duration{}
	completedInWindow, windowIssues, windowRefusals := 0, 0, 0
	stageIndex := 0
	for i, step := range steps {
		if step <= int(current.Load()) {
			stageIndex = i
		}
	}
	for ctx.Err() == nil {
		for {
			_, paused := server.ScanControl()
			if !paused || ctx.Err() != nil {
				break
			}
			for _, cancel := range activeCancels {
				cancel()
			}
			time.Sleep(250 * time.Millisecond)
		}
		requested, paused := server.ScanControl()
		resourceLimit := resourceSafeConcurrency(coreCount)
		if !resourceLimit.safe && requested > 100 {
			requested = 100
		}
		if paused {
			requested = 0
		}
		// A deliberate manual target can raise concurrency above the auto-ramp
		// stage; auto mode remains ramp-limited.
		limit := min(cfg.maximum, requested)
		if !server.ScanControlManual() {
			limit = min(limit, int(current.Load()))
		}
		if scanHealth != nil {
			limit = min(limit, scanHealth.size)
		}
		for inFlight < limit && ctx.Err() == nil {
			dispatched := false
			for checked := 0; checked < len(unique); checked++ {
				station := unique[nextStation]
				nextStation = (nextStation + 1) % len(unique)
				parts := strings.SplitN(station, "|", 2)
				if len(parts) != 2 {
					continue
				}
				host := parts[1]
				if activeStations[host] || time.Now().Before(backoffUntil[host]) {
					continue
				}
				// A host reserved for a beacon measurement stays idle: the
				// prober must never be a second connection during a capture.
				if scanHealth != nil && !scanHealth.tryStartVisit(host) {
					continue
				}
				activeStations[host] = true
				stationCtx, cancel := context.WithTimeout(ctx, cfg.stationSession)
				activeCancels[host] = cancel
				inFlight++
				go func(kind, host string) {
					outcome := scanStation(stationCtx, server, kind, host, cfg.targetLimit, cfg.recordAll, multimonPath, direwolfPath, scannerCaptureDir, &active, updateMetrics)
					cancel()
					// A visit ended by our own session deadline is not a
					// remote refusal or timeout.
					outcome = normalizeStationOutcome(stationCtx, ctx, outcome)
					results <- result{host: host, outcome: outcome}
				}(parts[0], host)
				dispatched = true
				break
			}
			if !dispatched {
				break
			}
		}
		if inFlight == 0 {
			var earliest time.Time
			for _, station := range unique {
				parts := strings.SplitN(station, "|", 2)
				if len(parts) == 2 {
					t := backoffUntil[parts[1]]
					if !t.IsZero() && (earliest.IsZero() || t.Before(earliest)) {
						earliest = t
					}
				}
			}
			wait := time.Second
			if !earliest.IsZero() {
				wait = minDuration(maxDuration(time.Until(earliest), 50*time.Millisecond), time.Second)
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
		case <-dispatchTick.C:
			// Re-evaluate dispatch while sessions run: backoff expiries and
			// pause/target changes must not wait for the next finished
			// station (visits can run for minutes).
			updateMetrics()
			if scanHealth != nil && scanHealth.repickDue() {
				unique = scanHealth.pickStations(rosterAll)
				nextStation = 0
			}
		case r := <-results:
			if scanHealth != nil {
				scanHealth.observeOutcome(r.host, r.outcome)
			}
			receiverAtFault := (r.outcome.refused || r.outcome.timedOut) && !r.outcome.localResource
			if receiverAtFault {
				reason := "receiver refused or timed out"
				blockStationIfEligible(ctx, server, r.host, reason, 30*time.Minute)
			}
			inFlight--
			delete(activeStations, r.host)
			delete(activeCancels, r.host)
			if r.outcome.skipped {
				if _, backingOff := backoffUntil[r.host]; !backingOff {
					backoffUntil[r.host] = time.Now().Add(skippedStationRetry)
				}
			} else if receiverAtFault {
				d := backoffFor[r.host]
				if d == 0 {
					d = 30 * time.Second
				} else {
					d = minDuration(d*2, 15*time.Minute)
				}
				backoffFor[r.host] = d
				backoffUntil[r.host] = time.Now().Add(d)
			} else {
				backoffFor[r.host] = 0
				delete(backoffUntil, r.host)
			}
			completedInWindow++
			if r.outcome.refused {
				windowRefusals++
				windowIssues++
			}
			metricsMu.Lock()
			if r.outcome.refused {
				refusals++
			}
			if r.outcome.timedOut {
				timeouts++
				windowIssues++
			}
			metricsMu.Unlock()
			updateMetrics()
			// Decide after each 20 completed stations, not after the slowest one in a fixed wave.
			if completedInWindow >= 20 {
				now := time.Now()
				loadRatio := readLoadAverage() / float64(max(1, coreCount))
				refusalRate := float64(windowRefusals) / float64(completedInWindow)
				errorRate := float64(windowIssues) / float64(completedInWindow)
				guardMu.Lock()
				loadMean := guard.observe(now, loadRatio)
				stage := nextRampStage(stageIndex, len(steps), refusalRate, errorRate, loadMean, float64(current.Load())*cfg.streamKbps/1000, cfg)
				stageIndex = guard.adjust(now, stage, len(steps), readRSSBytes(), openFDCount(), runtime.NumGoroutine())
				guardMu.Unlock()
				current.Store(int64(min(cfg.maximum, steps[stageIndex])))
				completedInWindow, windowIssues, windowRefusals = 0, 0, 0
				updateMetrics()
			}
		}
	}
}

// nextRampStage applies the scanner's configured error, refusal, load and
// bandwidth gates to one completed sample window. Timeouts count as errors
// for advance decisions, while refusal rate alone controls refusal backoff.
// Load only gates advances here: load-driven step-down belongs to the load
// guard, which requires sustained overload before shedding a step.
func nextRampStage(stage, stageCount int, refusalRate, errorRate, loadMean, bandwidthMbps float64, cfg scanConfig) int {
	if stageCount <= 0 {
		return 0
	}
	stage = min(max(stage, 0), stageCount-1)
	if refusalRate > cfg.backoffRefusalRate && stage > 0 {
		return stage - 1
	}
	if errorRate < cfg.advanceErrorRate && loadMean < cfg.advanceLoadRatio && bandwidthMbps < cfg.bandwidthLimitMbps && stage+1 < stageCount {
		return stage + 1
	}
	return stage
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

const skippedStationRetry = time.Minute

type scanOutcome struct {
	refused, timedOut, localResource bool
	skipped                          bool
}

func preflightOutcome(verdict string) scanOutcome {
	switch verdict {
	case kiwisdr.VerdictBackoff, kiwisdr.VerdictBudget:
		return scanOutcome{skipped: true}
	case kiwisdr.VerdictTimeout:
		return scanOutcome{timedOut: true}
	default:
		return scanOutcome{refused: true}
	}
}
func normalizeStationOutcome(stationCtx, parentCtx context.Context, outcome scanOutcome) scanOutcome {
	if errors.Is(stationCtx.Err(), context.DeadlineExceeded) && parentCtx.Err() == nil {
		return scanOutcome{skipped: true}
	}
	return outcome
}

// classifyScanFailure sorts a failed visit into what the receiver did
// versus what this machine did. A local descriptor exhaustion (EMFILE/
// ENFILE, usually "too many open files" above the default ulimit) says
// nothing about the receiver: blocking it for half an hour would blacklist
// every healthy host the scanner happened to touch during the spike.
func classifyScanFailure(err error) scanOutcome {
	if err == nil {
		return scanOutcome{}
	}
	local := errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
	var ne net.Error
	isTimeout := errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) || strings.Contains(strings.ToLower(err.Error()), "timeout")
	text := strings.ToLower(err.Error())
	local = local || strings.Contains(text, "too many open files")
	// "too many" also covers Kiwi-style "too many users" refusals, so it
	// stays, but the local descriptor case never counts as a refusal.
	refused := !local && (strings.Contains(text, "refused") || strings.Contains(text, "rate limit") || strings.Contains(text, "too many") || strings.Contains(text, "429") || strings.Contains(text, "busy"))
	return scanOutcome{refused: refused, timedOut: isTimeout, localResource: local}
}

type resourceStatus struct {
	memoryBytes uint64
	openFDs     int
	safe        bool
}

func resourceSafeConcurrency(cores int) resourceStatus {
	rss := readRSSBytes()
	fds := openFDCount()
	safe := (cores <= 0 || readLoadAverage()/float64(cores) < .70) && rss < guardMaxRSSBytes && (fds == 0 || fds < guardMaxFDs) && runtime.NumGoroutine() < guardMaxGoroutines
	return resourceStatus{memoryBytes: rss, openFDs: fds, safe: safe}
}

func readLoadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

// kiwiPreflight gates Kiwi scan targets: /status check, 15 min cache, per-host
// backoff and a daily attempt budget.
var kiwiPreflight = kiwisdr.NewPreflighter()

// readRSSBytes reports the process resident set size, falling back to the
// runtime heap-system figure when /proc is unavailable.
func readRSSBytes() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 2 {
			if pages, err := strconv.ParseUint(fields[1], 10, 64); err == nil && pages > 0 {
				return pages * uint64(os.Getpagesize())
			}
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys
}

func openFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}

// cmdKiwiPreflight checks a bounded receiver list and prints aggregate verdicts.
func cmdKiwiPreflight(receivers []string) error {
	type item struct {
		host   string
		result kiwisdr.Status
	}
	jobs := make(chan string)
	results := make(chan item)
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for host := range jobs {
				results <- item{host, kiwiPreflight.Check(context.Background(), host)}
			}
		}()
	}
	go func() {
		for _, raw := range receivers {
			parts := strings.SplitN(strings.TrimSpace(raw), "|", 2)
			if len(parts) != 2 {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(parts[0]), "KiwiSDR") && !looksLikeKiwi(parts[1]) {
				continue
			}
			jobs <- strings.TrimSpace(parts[1])
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	counts := map[string]int{}
	for result := range results {
		counts[result.result.Verdict]++
		fmt.Printf("%s %s users=%d/%d ws=%s\n", result.host, result.result.Verdict, result.result.Users, result.result.UsersMax, result.result.WSPath)
	}
	fmt.Printf("preflight counts: %+v\n", counts)
	if diagnostic := kiwiPreflight.EgressDiagnostic(); diagnostic != "" {
		fmt.Println(diagnostic)
	}
	return nil
}

var directoryPreflightSlots = make(chan struct{}, 16)

// directoryPreflightHTTPClient builds the SSRF-guarded client for receiver
// preflights. A var so tests pointing at loopback fixtures can replace it.
var directoryPreflightHTTPClient = func() *http.Client { return common.PublicHTTPClient(8 * time.Second) }

var directoryStationPreflight = preflightDirectoryStation

func preflightDirectoryStation(ctx context.Context, stationType, rawURL string) error {
	select {
	case directoryPreflightSlots <- struct{}{}:
		defer func() { <-directoryPreflightSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	requestURL, err := common.NormalizeHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if u, err := url.Parse(requestURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("preflight: unsupported scheme in %q (http/https only)", rawURL)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "sdrstream/1.0 receiver-preflight")
	// Directory entries are untrusted: dial public addresses only, and let
	// the guarded transport re-validate every redirect hop.
	client := directoryPreflightHTTPClient()
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("preflight HTTP: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("preflight HTTP status %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return fmt.Errorf("preflight read: %w", err)
	}
	page := strings.ToLower(string(body))
	identified := false
	switch strings.ToLower(stationType) {
	case "openwebrx":
		identified = strings.Contains(page, "openwebrx") || strings.Contains(page, "openwebrx+")
	case "websdr":
		identified = strings.Contains(page, "websdr") || strings.Contains(page, "/~~stream")
	}
	if !identified {
		return fmt.Errorf("preflight reachable but server page did not identify as %s", stationType)
	}
	return nil
}

// scanSourceFor builds the receiver source for one station visit. It is a
// variable so tests can inject a fake receiver without a live server.
var scanSourceFor = monitor.SourceFor

func scanStation(ctx context.Context, server *web.Server, stationType, host string, targetLimit int, recordAll bool, multimonPath, direwolfPath, capturesDir string, active *atomic.Int64, updateMetrics func()) scanOutcome {
	// Trust the directory type. OpenWebRX also defaults to port 8073, so a
	// port heuristic sent OpenWebRX stations to the Kiwi preflight; it then
	// built http://http//host/status and blocklisted a healthy receiver as
	// "refused". Only guess from the name when the caller gave no type.
	if strings.EqualFold(stationType, "KiwiSDR") || (stationType == "" && looksLikeKiwi(host)) {
		preflight := kiwiPreflight.Check(ctx, host)
		if scanHealth != nil {
			scanHealth.observeProbe(host, preflight)
		}
		if !preflight.Free() {
			outcome := preflightOutcome(preflight.Verdict)
			state := "refused/skipped"
			if outcome.skipped {
				state = "skipped"
			}
			reason := "preflight " + preflight.Verdict + ": " + preflight.Err
			if preflight.Verdict == kiwisdr.VerdictBusy {
				reason = fmt.Sprintf("preflight busy: users %d/%d", preflight.Users, preflight.UsersMax)
			}
			if d := kiwiPreflight.EgressDiagnostic(); d != "" {
				reason += " | " + d
			}
			addScan(server, web.ScanStatus{StationType: stationType, Station: host, State: state, Result: reason, UpdatedAt: time.Now()})
			return outcome
		}
		return scanKiwiStation(ctx, server, host, targetLimit, recordAll, capturesDir, active, updateMetrics, preflight.WSPath)
	}
	active.Add(1)
	updateMetrics()
	protocol := "websdr"
	profile := ""
	if strings.EqualFold(stationType, "OpenWebRX") {
		protocol = "openwebrx"
		profile = nextOpenWebRXProfile(host)
	}
	// Preflight gets its own budget; the source then lives for the whole
	// station session. Do NOT create a short context for SourceFor and
	// cancel it after connect: the receiver clients run their websocket
	// read loop on that context, so cancelling it stopped the FFT/audio
	// stream right after a successful connection. The handshake has its own
	// dial and read deadlines, and the station session bounds the source.
	preflightCtx, cancelPreflight := context.WithTimeout(ctx, 10*time.Second)
	err := directoryStationPreflight(preflightCtx, stationType, host)
	cancelPreflight()
	if err != nil {
		active.Add(-1)
		updateMetrics()
		outcome := classifyScanFailure(err)
		reason := err.Error()
		if outcome.refused || outcome.timedOut {
			blockStationIfEligible(ctx, server, host, reason, 30*time.Minute)
		}
		return outcome
	}
	source, err := scanSourceFor(ctx, protocol, host, profile)
	if err != nil && profile != "" {
		// The server may have dropped or renamed the profile since the last
		// visit. Fall back to its default and relearn the list next time.
		log.Printf("scan %s: profile %q failed (%v); retrying the default profile", host, profile, err)
		resetOpenWebRXRotation(host)
		profile = ""
		source, err = scanSourceFor(ctx, protocol, host, "")
	}
	if err != nil {
		active.Add(-1)
		updateMetrics()
		outcome := classifyScanFailure(err)
		if outcome.refused || outcome.timedOut {
			blockStationIfEligible(ctx, server, host, err.Error(), 30*time.Minute)
		}
		return outcome
	}
	if strings.EqualFold(stationType, "OpenWebRX") {
		if lister, ok := source.(interface{ ProfileNames() []string }); ok {
			names := lister.ProfileNames()
			noteOpenWebRXProfiles(host, profile, names)
			if profile == "" {
				log.Printf("scan %s: surveying the default profile (%d available); next visit rotates", host, len(names))
			} else {
				log.Printf("scan %s: surveying profile %q (%d available)", host, profile, len(names))
			}
		}
	}
	defer func() { source.Close(); active.Add(-1); updateMetrics() }()
	if strings.EqualFold(stationType, "OpenWebRX") {
		return scanOpenWebRXSpectrum(ctx, server, source, stationType, host, targetLimit, recordAll, multimonPath, direwolfPath, capturesDir)
	}
	return scanFixedTargets(ctx, server, source, stationType, host, targetLimit, recordAll, multimonPath, direwolfPath, capturesDir)
}

// scanOpenWebRXSpectrum surveys the receiver's whole profile span from the
// server's own FFT instead of tuning a fixed list of frequencies. One
// connection sees every peak in a 1-2 MHz profile; each peak that persists
// is recorded, classified and decoded. If the server never sends an FFT, the
// same connection falls back to the fixed target list.
func scanOpenWebRXSpectrum(ctx context.Context, server *web.Server, source monitor.Source, stationType, host string, targetLimit int, recordAll bool, multimonPath, direwolfPath, capturesDir string) scanOutcome {
	key := addScan(server, web.ScanStatus{StationType: stationType, Station: host, Mode: "spectrum", State: "connecting", UpdatedAt: time.Now()})
	setScanResult(server, key, "listening", "waiting for the receiver FFT")

	var sawSpectrum atomic.Bool
	var findings atomic.Int32
	var mu sync.Mutex
	var lastText string

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	instance := monitor.New(monitor.Options{
		Source:          source,
		Strategy:        "spectrum",
		ThresholdDB:     10,
		DwellTime:       2 * time.Second,
		Cooldown:        30 * time.Minute,
		Listen:          8 * time.Second,
		Mode:            "auto",
		Voice:           true,
		VoiceOptions:    monitorVoiceOptions("auto", 2, 1, ""),
		VoiceNIMOptions: monitorNIMOptions(),
		MultimonBinary:  multimonPath,
		DirewolfBinary:  direwolfPath,
		OutDir:          capturesDir,
		SaveAudio: func(kind string) bool {
			if recordAll {
				// Silence is not a transmission; everything else the detector
				// found (including carrier/noise false positives) is archived.
				return kind != "silence"
			}
			return kind == "voice" || kind == "sstv"
		},
		Name: host,
		Logf: func(format string, args ...interface{}) { log.Printf("scan "+host+": "+format, args...) },
		OnCapture: func(activity monitor.Activity, path string) {
			// Record provenance immediately, before verification. The
			// callback below upgrades it when the live gate passes.
			activity.AudioPath = path
			writeActivitySidecar(server, activity, false, "pending verification")
		},
		OnActivity: func(activity monitor.Activity) {
			if activity.Kind == "noise" || activity.Kind == "silence" || activity.Kind == "unknown" {
				return
			}
			// SSTV is an image: OnImage carries the PNG and publishes it.
			if activity.Kind == "sstv" {
				return
			}
			freqKey := addScan(server, web.ScanStatus{StationType: stationType, Station: host, FrequencyHz: activity.FrequencyHz, Mode: activity.Mode, State: "decoding", UpdatedAt: time.Now()})
			if err := verify.Live(activity.Kind, activity.Mode, activity.Detail, nil, activity.VerificationAnchor); err != nil {
				writeActivitySidecar(server, activity, false, err.Error())
				setScanResult(server, freqKey, "unverified decode", err.Error())
				return
			}
			writeActivitySidecar(server, activity, true, "live gate passed")
			if err := server.AddLiveArtifact(activity, nil); err != nil {
				setScanResult(server, freqKey, "error", err.Error())
				return
			}
			findings.Add(1)
			mu.Lock()
			lastText = activity.Detail
			mu.Unlock()
			setScanResult(server, freqKey, "decoded", activity.Detail)
		},
		OnImage: func(activity monitor.Activity, png []byte) {
			if err := verify.Live(activity.Kind, activity.Mode, "", png); err != nil {
				writeActivitySidecar(server, activity, false, err.Error())
				return
			}
			writeActivitySidecar(server, activity, true, "live gate passed")
			if err := server.AddLiveArtifact(activity, png); err != nil {
				return
			}
			findings.Add(1)
			addScan(server, web.ScanStatus{StationType: stationType, Station: host, FrequencyHz: activity.FrequencyHz, Mode: activity.Mode, State: "decoded image", Result: fmt.Sprintf("%d bytes", len(png)), UpdatedAt: time.Now()})
		},
	})

	// Wait up to 10 s for the FFT. A station that never sends one gets the
	// fixed targets instead of an idle connection.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if _, centre, span, ok := source.Spectrum(); ok {
					sawSpectrum.Store(true)
					setScanResult(server, key, "listening",
						fmt.Sprintf("surveying %.3f MHz (%.2f MHz span)", centre/1e6, span/1e6))
					return
				}
			case <-deadline.C:
				cancelRun()
				return
			}
		}
	}()

	err := instance.Run(runCtx)
	cancelRun()
	if sawSpectrum.Load() {
		mu.Lock()
		text := lastText
		mu.Unlock()
		result := fmt.Sprintf("%d verified finding(s)", findings.Load())
		if text != "" {
			result += ": " + text
		}
		setScanResult(server, key, "session complete", result)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("scan %s: spectrum session: %v", host, err)
		}
		return scanOutcome{}
	}
	setScanResult(server, key, "no spectrum", "falling back to fixed frequencies")
	return scanFixedTargets(ctx, server, source, stationType, host, targetLimit, recordAll, multimonPath, direwolfPath, capturesDir)
}

func scanFixedTargets(ctx context.Context, server *web.Server, source monitor.Source, stationType, host string, targetLimit int, recordAll bool, multimonPath, direwolfPath, capturesDir string) scanOutcome {
	targets := []scanTarget{
		{server: host, frequency: 144800e3, mode: "fm"}, {server: host, frequency: 145500e3, mode: "fm"}, {server: host, frequency: 145800e3, mode: "fm"}, {server: host, frequency: 145825e3, mode: "fm"},
		{server: host, frequency: 146520e3, mode: "fm"}, {server: host, frequency: 433500e3, mode: "fm"}, {server: host, frequency: 434650e3, mode: "fm"}, {server: host, frequency: 446006250, mode: "fm"},
		{server: host, frequency: 446093750, mode: "fm"}, {server: host, frequency: 162400e3, mode: "fm"}, {server: host, frequency: 162425e3, mode: "fm"}, {server: host, frequency: 162450e3, mode: "fm"},
		{server: host, frequency: 162475e3, mode: "fm"}, {server: host, frequency: 162500e3, mode: "fm"}, {server: host, frequency: 162525e3, mode: "fm"}, {server: host, frequency: 162550e3, mode: "fm"},
		{server: host, frequency: 88.1e6, mode: "wfm"}, {server: host, frequency: 94.5e6, mode: "wfm"}, {server: host, frequency: 100e6, mode: "wfm"}, {server: host, frequency: 104.5e6, mode: "wfm"},
	}
	if strings.Contains(strings.ToLower(host), "jukebox.spdns.eu") {
		targets = []scanTarget{{server: host, frequency: 144800e3, mode: "fm"}}
	} else if strings.Contains(strings.ToLower(host), "websdr.ewi.utwente.nl") {
		targets = []scanTarget{{server: host, frequency: 162400e3, mode: "fm"}, {server: host, frequency: 162425e3, mode: "fm"}}
	}
	if targetLimit < len(targets) {
		targets = targets[:targetLimit]
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return scanOutcome{}
		}
		key := addScan(server, web.ScanStatus{StationType: stationType, Station: host, FrequencyHz: target.frequency, Mode: target.mode, State: "connecting", UpdatedAt: time.Now()})
		update := func(state, result string) { setScanResult(server, key, state, result) }
		update("connected", "")
		update("tuning", "")
		update("listening", "")
		listen := 12 * time.Second
		if strings.Contains(strings.ToLower(host), "jukebox.spdns.eu") {
			listen = 30 * time.Second
		}
		targetCtx, cancelTarget := context.WithTimeout(ctx, listen+5*time.Second)
		samples, rate, err := source.Listen(targetCtx, target.frequency, target.mode, listen)
		cancelTarget()
		if err != nil {
			result := classifyScanFailure(err)
			state := "error"
			if result.refused {
				state = "refused/skipped"
			}
			if result.timedOut {
				state = "timeout"
			}
			update(state, err.Error())
			// Blocklisting is for receivers that actually pushed us away:
			// a wrong tune or one bad frame says nothing about the host,
			// and an EMFILE spike is this machine's problem.
			if (result.refused || result.timedOut) && !result.localResource {
				blockStationIfEligible(ctx, server, host, err.Error(), 30*time.Minute)
			}
			return result
		}
		if strings.Contains(strings.ToLower(host), "jukebox.spdns.eu") {
			wavPath := filepath.Join(os.TempDir(), fmt.Sprintf("sdrstream-aprs-%d.wav", time.Now().UnixNano()))
			// The decoded-and-continued path used to skip the remove and
			// leak one temp WAV per APRS capture; tryJukeboxAPRS owns the
			// cleanup now, on every outcome.
			if tryJukeboxAPRS(server, samples, rate, wavPath, capturesDir, target.frequency, host, update) {
				continue
			}
		}

		decodeCtx, cancelDecode := context.WithTimeout(ctx, 12*time.Second)
		kind, decoder, detail, ok := monitor.New(monitor.Options{MultimonBinary: multimonPath, DirewolfBinary: direwolfPath, Voice: true, VoiceOptions: monitorVoiceOptions("en", 2, 1, "weather, radio, forecast"), VoiceNIMOptions: monitorNIMOptions()}).ClassifyForScan(decodeCtx, samples, rate, 3000)
		cancelDecode()
		verified := false
		gate := ""
		if ok && detail != "" {
			check := monitor.Activity{Kind: kind, Mode: decoder, Detail: detail}
			if err := verify.Live(check.Kind, check.Mode, check.Detail, nil, ""); err == nil {
				verified = true
			} else {
				gate = err.Error()
			}
		}
		var audioPath string
		if (recordAll && kind != "silence") || verified {
			meta := captureMeta{
				Kind: kind, Mode: decoder, Station: host, FrequencyHz: target.frequency,
				Started: time.Now().UTC(), Detail: detail, Gate: gate, Verified: verified,
				DurationSec: float64(len(samples)) / float64(rate),
			}
			if kind == "" {
				meta.Kind = "unknown"
			}
			if path, err := writeCaptureSamples(capturesDir, samples, rate, meta); err == nil {
				audioPath = path
			}
		}
		if !ok || detail == "" {
			update("no verified decode", "")
			continue
		}
		if !verified {
			update("unverified decode", gate)
			continue
		}
		activity := monitor.Activity{ID: int(time.Now().UnixNano()), Started: time.Now().UTC(), FrequencyHz: target.frequency, Kind: kind, Mode: decoder, Detail: detail, Source: host, AudioPath: audioPath}
		if err := server.AddLiveArtifact(activity, nil); err != nil {
			update("error", err.Error())
			continue
		}
		update("decoded", detail)
	}
	return scanOutcome{}
}

func runDirectoryHFScan(ctx context.Context, server *web.Server, host string, wspr bool, capturesDir string, recordAll bool) {
	// One orderly WebSDR session for both UTC-aligned digital cycles. The
	// client is closed on cancellation and never touches the public tunnel.
	client, err := websdr.New(host, websdr.DefaultOptions())
	if err != nil {
		return
	}
	if err := client.Connect(ctx); err != nil {
		return
	}
	defer client.Close()
	targets := []decode.HFTarget{{Name: "FT8 40m", Kind: "ft8", CarrierKHz: 7074, DialKHz: 7074, Mode: "usb", Listen: 15 * time.Second}}
	if wspr {
		targets = append(targets, decode.HFTarget{Name: "WSPR 40m", Kind: "wspr", CarrierKHz: 7038.6, DialKHz: 7038.6, Mode: "usb", Listen: 120 * time.Second})
	}
	for ctx.Err() == nil {
		for _, target := range targets {
			if ctx.Err() != nil {
				return
			}
			key := addScan(server, web.ScanStatus{StationType: "WebSDR", Station: host, FrequencyHz: target.DialKHz * 1000, Mode: target.Mode, State: "waiting UTC slot", UpdatedAt: time.Now()})
			if err := client.Tune(ctx, target.DialKHz, "usb", nil); err != nil {
				setScanResult(server, key, "blocked", "tune: "+err.Error())
				return
			}
			slot := int64(15)
			duration := 16 * time.Second
			if target.Kind == "wspr" {
				slot = 120
				duration = 122 * time.Second
			}
			if err := waitHFSlot(ctx, slot); err != nil {
				return
			}
			setScanResult(server, key, "listening", time.Now().UTC().Format("15:04:05Z"))
			samples, rate, err := client.Record(ctx, duration)
			callEnd := time.Now().UTC()
			if err != nil {
				setScanResult(server, key, "blocked", "record: "+err.Error())
				return
			}
			if rate <= 0 || len(samples) < rate*10 {
				setScanResult(server, key, "error", fmt.Sprintf("short WebSDR audio: %d samples at %dHz", len(samples), rate))
				continue
			}
			start := callEnd.Add(-time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second)))
			// WebSDR stream is an 8kHz +/-1 float-domain audio signal. Convert
			// to float for the same decoders used by hfprove (int16/32768).
			pcm := append([]int16(nil), samples...)
			f := make([]float64, len(pcm))
			for i, v := range pcm {
				f[i] = float64(v) / 32768.0
			}
			var lines []string
			if target.Kind == "ft8" {
				msgs, _, derr := decode.FTXDecodeStreamAuto(ctx, f, rate, float64(start.UnixNano())/1e9, "FT8")
				if derr != nil {
					setScanResult(server, key, "error", "decode: "+derr.Error())
					continue
				}
				for _, m := range msgs {
					lines = append(lines, fmt.Sprintf("%s FT8 %+.1f %.0f Hz %s engine=%s", time.Unix(m.SlotUnix, 0).UTC().Format("150405"), m.SNR, m.FreqHz, m.Text, m.Engine))
				}
			} else {
				windows, _, derr := decode.WSPRWindowsAuto(ctx, f, rate, float64(start.UnixNano())/1e9)
				if derr != nil {
					setScanResult(server, key, "error", "decode: "+derr.Error())
					continue
				}
				for _, w := range windows {
					for _, m := range w.Messages {
						lines = append(lines, fmt.Sprintf("%s WSPR %+.0f %.1f %.1f Hz %s engine=%s", time.Unix(w.Start, 0).UTC().Format("1504"), m.SNR, m.DT, m.FreqHz, m.Text, m.Engine))
					}
				}
			}
			text := strings.Join(lines, "\n")
			gate := verify.FTX(len(lines), true, text)
			if target.Kind == "wspr" {
				gate = verify.WSPR(len(lines), text)
			}
			path := ""
			if recordAll || gate.Live {
				// The clip is valuable even when this UTC slot produced no
				// message: it is the raw evidence the next decoder run can
				// revisit. Record-all keeps both cases.
				if saved, err := writeCaptureWAV(capturesDir, decode.WriteWAV16(pcm, rate), captureMeta{
					Kind: "data", Mode: target.Kind, Station: host,
					FrequencyHz: target.DialKHz * 1000, Started: start,
					Detail: text, Gate: gate.Reason, Target: target.Name,
					Verified: gate.Live,
				}); err != nil {
					if gate.Live {
						setScanResult(server, key, "verified decode; capture error", err.Error())
					}
				} else {
					path = saved
				}
			}
			if !gate.Live {
				setScanResult(server, key, "unverified decode", gate.Reason)
				continue
			}
			activity := monitor.Activity{ID: int(time.Now().UnixNano()), Started: start, FrequencyHz: target.DialKHz * 1000, Kind: "data", Mode: target.Kind, Detail: text, Source: host, AudioPath: path}
			if err := server.AddLiveArtifact(activity, nil); err != nil {
				setScanResult(server, key, "verified decode; gallery error", err.Error())
			} else {
				setScanResult(server, key, "decoded", gate.Reason)
			}
		}
		// After each pair, allow a short idle before starting the next aligned cycle.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func waitHFSlot(ctx context.Context, slot int64) error {
	now := time.Now().UTC()
	next := now.Truncate(time.Duration(slot) * time.Second).Add(time.Duration(slot) * time.Second)
	wait := time.Until(next.Add(-700 * time.Millisecond))
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func safeCaptureName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

func looksLikeKiwi(host string) bool {
	text := strings.ToLower(host)
	return strings.Contains(text, "kiwisdr") || strings.Contains(text, "kiwi")
}

// transcribeHFVoice is a seam for tests. HF voice targets are captured by the
// Kiwi session without a decoder, then turned into text here with the same
// NIM-first backend as the live monitor.
var transcribeHFVoice = func(ctx context.Context, samples []float64, rate int) (voice.Transcript, error) {
	return voice.TranscribeBest(ctx, samples, rate,
		monitorVoiceOptions("en", 2, 1, "marine weather, aviation, radio, forecast"),
		voice.DefaultNIMOptions())
}

// kiwiVoiceActivity turns a captured HF voice result into a verified gallery
// activity. decodeHF never transcribes voice, so the Kiwi scanner would other-
// wise mark every VOLMET/weather/time-signal capture as unverified. When
// capturesDir is set the WAV is kept too, which also feeds the Rdio hook.
func kiwiVoiceActivity(ctx context.Context, host string, result decode.HFResult, capturesDir string) (monitor.Activity, error) {
	return kiwiVoiceActivityAt(ctx, host, result, capturesDir, "")
}

// kiwiVoiceActivityAt is the same bridge, but lets the caller pass a WAV it
// already archived (record-all mode). Without audioPath the verified clip is
// written here, preserving the old single-call behavior.
func kiwiVoiceActivityAt(ctx context.Context, host string, result decode.HFResult, capturesDir, audioPath string) (monitor.Activity, error) {
	if len(result.AudioWAV) == 0 {
		return monitor.Activity{}, errors.New("no audio captured")
	}
	file, err := os.CreateTemp("", "sdrstream-kiwi-voice-*.wav")
	if err != nil {
		return monitor.Activity{}, err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(result.AudioWAV); err != nil {
		file.Close()
		return monitor.Activity{}, err
	}
	if err := file.Close(); err != nil {
		return monitor.Activity{}, err
	}
	samples, rate, err := decode.ReadWAV(name)
	if err != nil {
		return monitor.Activity{}, err
	}
	transcript, err := transcribeHFVoice(ctx, samples, rate)
	if err != nil {
		return monitor.Activity{}, err
	}
	text := strings.TrimSpace(transcript.Text)
	if err := verify.Live("voice", "speech", text, nil); err != nil {
		return monitor.Activity{}, err
	}
	// HF voice is the hallucination magnet: a hosted model would happily
	// read weather-forecast prose into static. The guard cross-checks the
	// transcript against the speech actually measured in the clip (canned
	// text and decoder loops are rejected upstream of the gallery).
	if verdict := voice.GuardTranscript(samples, rate, text); verdict.Rejected {
		return monitor.Activity{}, fmt.Errorf("transcript guard rejected (%s)", verdict.Reason)
	}
	activity := monitor.Activity{
		ID:          int(time.Now().UnixNano()),
		Started:     result.Started,
		FrequencyHz: result.Target.DialKHz * 1000,
		Kind:        "voice",
		Mode:        "speech",
		Detail:      text,
		Target:      result.Target.Name,
		Source:      host,
	}
	activity.AudioPath = audioPath
	if activity.AudioPath == "" && capturesDir != "" && len(result.AudioWAV) > 0 {
		source := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		name := fmt.Sprintf("VOICE_%dkHz_voice_%s_%d.wav", int(result.Target.DialKHz), safeCaptureName(source), activity.ID)
		path := filepath.Join(capturesDir, name)
		if err := os.MkdirAll(capturesDir, 0o755); err == nil {
			if err := os.WriteFile(path, result.AudioWAV, 0o644); err == nil {
				activity.AudioPath = path
			}
		}
	}
	return activity, nil
}

func scanKiwiStation(ctx context.Context, server *web.Server, host string, targetLimit int, recordAll bool, capturesDir string, active *atomic.Int64, updateMetrics func(), wsPath string) scanOutcome {
	// Start where the previous visit stopped. A 5-minute session cannot
	// reach all HF broadcasts, so without a cursor the first few targets
	// would be rescanned forever and the tail would never get a slot.
	targets := nextKiwiTargets(host, decode.HFBroadcasts)
	keys := make(map[string]string, len(targets))
	for _, target := range targets {
		if target.ActiveNow(time.Now()) {
			keys[target.Name] = ensureScan(server, web.ScanStatus{StationType: "KiwiSDR", Station: host, FrequencyHz: target.DialKHz * 1000, Mode: target.Mode, State: "queued", UpdatedAt: time.Now()})
		}
	}
	active.Add(1)
	updateMetrics()
	results, remaining, err := decode.RunHFSession(ctx, host, targets, decode.SessionOptions{
		Listen: 8 * time.Second, MaxTargets: targetLimit, MaxSession: 5 * time.Minute,
		SkipInactive: true, RecordFullAudio: true, WSPath: wsPath,
	})
	active.Add(-1)
	updateMetrics()
	noteKiwiProgress(host, results)
	ownDeadline := errors.Is(ctx.Err(), context.DeadlineExceeded)
	processed := make(map[string]bool, len(results))
	for _, result := range results {
		processed[result.Target.Name] = true
	}
	for _, result := range results {
		processed[result.Target.Name] = true
		key := keys[result.Target.Name]
		if key == "" {
			continue
		}
		// Record-all archives the raw clip before the gate, so an
		// unverified target still leaves evidence on disk. Without
		// record-all, only verified results are kept.
		audioPath, captureErr := saveHFResultCapture(capturesDir, host, result, recordAll)
		if captureErr != nil {
			log.Printf("scan %s: %s capture: %v", host, result.Target.Name, captureErr)
		}
		if result.Target.Kind == "voice" {
			activity, verr := kiwiVoiceActivityAt(ctx, host, result, capturesDir, audioPath)
			if verr != nil {
				setScanResult(server, key, "unverified decode", verr.Error())
				continue
			}
			if activity.AudioPath != "" {
				writeActivitySidecar(server, activity, true, "live gate passed")
			}
			if err := server.AddLiveArtifact(activity, nil); err != nil {
				setScanResult(server, key, "error", err.Error())
				continue
			}
			setScanResult(server, key, "decoded", activity.Detail)
			continue
		}
		if !result.Live() {
			setScanResult(server, key, "unverified decode", result.Gate.Reason)
			continue
		}
		activity := monitor.Activity{ID: int(time.Now().UnixNano()), Started: result.Started, FrequencyHz: result.Target.DialKHz * 1000, Kind: "data", Mode: result.Target.Kind, Detail: result.Text, VerificationAnchor: result.Target.Expect, Target: result.Target.Name, Source: host, AudioPath: audioPath}
		imageData := result.ImagePNG
		if len(imageData) > 0 {
			activity.Kind = result.Target.Kind
		}
		if err := server.AddLiveArtifact(activity, imageData); err != nil {
			setScanResult(server, key, "unverified decode", err.Error())
			continue
		}
		setScanResult(server, key, "decoded", result.Gate.Reason)
	}
	// Never advance past unprocessed targets: everything the session did not
	// finish is deferred to the next visit, not reported as failed.
	deferred := make(map[string]bool, len(remaining))
	for _, target := range remaining {
		deferred[target.Name] = true
		if processed[target.Name] {
			continue
		}
		if key := keys[target.Name]; key != "" {
			setScanResult(server, key, "deferred", "receiver session target/time cap; next visit resumes after the last processed target")
		}
	}
	if err != nil {
		outcome := classifyScanFailure(err)
		if ownDeadline {
			outcome = scanOutcome{skipped: true}
		}
		state := "error"
		if outcome.refused {
			state = "refused/skipped"
		}
		if outcome.timedOut {
			state = "timeout"
		}
		if ownDeadline {
			state = "deferred"
		}
		for _, target := range targets {
			if processed[target.Name] || deferred[target.Name] {
				continue
			}
			if key := keys[target.Name]; key != "" {
				setScanResult(server, key, state, err.Error())
			}
		}
		return outcome
	}
	return scanOutcome{}
}

func setScanResult(server *web.Server, key string, state, result string) {
	scansMu.Lock()
	defer scansMu.Unlock()
	for i := range scans {
		if scanStatusKey(scans[i]) == key {
			scans[i].State = state
			scans[i].Result = result
			scans[i].UpdatedAt = time.Now()
			server.UpsertScanStatus(scans[i])
			return
		}
	}
}
func addScan(server *web.Server, entry web.ScanStatus) string {
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}
	key := scanStatusKey(entry)
	scansMu.Lock()
	defer scansMu.Unlock()
	found := false
	for i := range scans {
		if scanStatusKey(scans[i]) == key {
			scans[i] = entry
			found = true
			break
		}
	}
	if !found {
		scans = append(scans, entry)
		if len(scans) > 500 {
			scans = append([]web.ScanStatus(nil), scans[len(scans)-500:]...)
		}
	}
	server.UpsertScanStatus(entry)
	return key
}

// ensureScan registers a target once without resetting a result from an
// earlier visit. A repeat station visit must not replace a decoded transcript
// with "queued" just because the receiver is being retuned.
func ensureScan(server *web.Server, entry web.ScanStatus) string {
	key := scanStatusKey(entry)
	scansMu.Lock()
	for i := range scans {
		if scanStatusKey(scans[i]) == key {
			scansMu.Unlock()
			return key
		}
	}
	scansMu.Unlock()
	return addScan(server, entry)
}

func scanStatusKey(s web.ScanStatus) string {
	return fmt.Sprintf("%s|%.0f|%s", s.Station, s.FrequencyHz, s.Mode)
}

var scansMu sync.Mutex
var scans []web.ScanStatus
var scannerCaptureDir string

// tryJukeboxAPRS runs the jukebox APRS fast path. It writes the samples to
// wavPath, always removes that temp file, and reports whether the activity
// was decoded and recorded (true = the caller moves to the next target).
func tryJukeboxAPRS(server *web.Server, samples []float64, rate int, wavPath, capturesDir string, frequency float64, host string, update func(state, result string)) (decoded bool) {
	if err := finishInt16(samples, rate, wavPath); err != nil {
		return false
	}
	defer func() { _ = os.Remove(wavPath) }()
	packets, decodeErr := decode.AFSKFrames(wavPath)
	if decodeErr != nil || len(packets) == 0 {
		return false
	}
	lines := make([]string, 0, len(packets))
	for _, packet := range packets {
		lines = append(lines, packet.String())
	}
	detail := strings.Join(lines, " | ")
	if verify.Live("data", "aprs", detail, nil) != nil {
		return false
	}
	capture := filepath.Join(capturesDir, "APRS_144800kHz_aprs_jukebox_spdns_eu_"+time.Now().UTC().Format("20060102T150405Z")+".wav")
	if os.MkdirAll(filepath.Dir(capture), 0755) != nil || finishInt16(samples, rate, capture) != nil {
		return false
	}
	server.AddCaptureMetadata(filepath.Base(capture), "aprs", "jukebox.spdns.eu:6166", detail, frequency, true)
	activity := monitor.Activity{ID: int(time.Now().UnixNano()), Started: time.Now().UTC(), FrequencyHz: frequency, Kind: "data", Mode: "aprs", Detail: detail, Source: host, AudioPath: capture}
	if err := server.AddLiveArtifact(activity, nil); err != nil {
		return false
	}
	update("decoded", detail)
	return true
}

func finishInt16(samples []float64, rate int, path string) error {
	pcm := make([]int16, len(samples))
	for i, v := range samples {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		pcm[i] = int16(v * 32767)
	}
	return common.WriteWAVFile(path, pcm, rate)
}

func plausibleBroadcastText(text string) bool {
	letters, spaces, total := 0, 0, 0
	for _, r := range text {
		total++
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			letters++
		}
		if r == ' ' || r == '\n' || r == '\r' {
			spaces++
		}
	}
	if total < 24 || letters < 18 || float64(letters)/float64(total) < 0.55 {
		return false
	}
	return spaces >= 2
}

// cmdSources health-checks every registered external data source and prints
// an honest per-source status table: live, stale, unreachable, restricted or
// needs-key.
func cmdSources(args []string) error {
	fs := flag.NewFlagSet("sources", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	timeout := fs.Float64("timeout", 10, "per-request timeout in seconds")
	concurrency := fs.Int("concurrency", 4, "global in-flight cap (per-host stays 1)")
	kind := fs.String("kind", "", "only this kind (comma-separated), e.g. receiver-directory,spots")
	fs.Parse(args)
	var kinds []string
	if *kind != "" {
		kinds = strings.Split(*kind, ",")
	}
	results := sources.HealthAll(context.Background(), sources.Options{
		Timeout:     time.Duration(*timeout * float64(time.Second)),
		Concurrency: *concurrency,
		Kinds:       kinds,
	})
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", " ")
		return encoder.Encode(results)
	}
	counts := map[sources.Status]int{}
	for _, result := range results {
		counts[result.Status]++
		detail := result.Detail
		if result.Note != "" {
			if detail != "" {
				detail += "; "
			}
			detail += result.Note
		}
		fmt.Printf("%-18s %-18s %-11s %6dms  %s\n",
			result.Name, result.Kind, result.Status, result.LatencyMs, detail)
	}
	order := []sources.Status{sources.StatusLive, sources.StatusStale, sources.StatusUnreachable,
		sources.StatusRestricted, sources.StatusNeedsKey}
	var summary []string
	for _, status := range order {
		if counts[status] > 0 {
			summary = append(summary, fmt.Sprintf("%d %s", counts[status], status))
		}
	}
	fmt.Printf("sources: %s\n", strings.Join(summary, ", "))
	return nil
}
