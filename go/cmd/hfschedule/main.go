// Command hfschedule records scheduled HF services without touching a running app.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/hfschedule"
)

func load(ctx context.Context, path, url string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream-hfschedule/1.0 (schedule fetch; no polling)")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if len(b) > 4*1024*1024 {
		return nil, fmt.Errorf("schedule larger than 4 MiB")
	}
	return b, err
}
func main() {
	eibi := flag.String("eibi", "", "local EiBi eibi.txt (otherwise fetch once)")
	extra := flag.String("extra", "", "local supplemental EiBi-format file merged into the schedule (e.g. NAVTEX slots)")
	noaa := flag.String("noaa", "", "local NOAA Boston text (otherwise fetch once)")
	source := flag.String("source", "both", "eibi, noaa, or both")
	match := flag.String("match", "", "case-insensitive station/product substring; comma-separated alternatives match any")
	freq := flag.Float64("freq", 0, "optional exact carrier frequency in kHz")
	kind := flag.String("kind", "", "optional broadcast, rtty, navtex or wefax filter")
	receivers := flag.String("receivers", "", "JSON receiver array file, required for capture")
	out := flag.String("out", "captures/hfschedule", "isolated state and capture directory")
	listen := flag.Duration("listen", 30*time.Second, "capture length, capped by broadcast end and 20 minutes")
	run := flag.Bool("run", false, "record due entries as a continuous service (default is plan only)")
	once := flag.Bool("once", false, "perform at most one due capture and exit")
	poll := flag.Duration("poll", hfschedule.DefaultPollInterval, "service poll interval between due-entry checks")
	reload := flag.Duration("reload", hfschedule.DefaultReloadInterval, "service schedule reload interval; a failed reload keeps the previous schedule")
	horizon := flag.Duration("horizon", 24*time.Hour, "plan horizon")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *source != "eibi" && *source != "noaa" && *source != "both" {
		log.Fatal("unknown -source")
	}
	if *listen < time.Second || *listen > 20*time.Minute {
		log.Fatal("-listen must be 1s to 20m")
	}
	// loadEntries fetches and filters the schedule; the service reuses it
	// for every reload, so a run never drives tonight's listening from a
	// stale snapshot.
	noaaWarned := false
	loadEntries := func(lctx context.Context) ([]hfschedule.Entry, error) {
		var entries []hfschedule.Entry
		if *source != "noaa" {
			b, err := load(lctx, *eibi, hfschedule.EiBiURL)
			if err != nil {
				return nil, err
			}
			v, err := hfschedule.ParseEiBi(strings.NewReader(string(b)))
			if err != nil {
				return nil, err
			}
			entries = append(entries, v...)
		}
		if *source != "eibi" {
			b, err := load(lctx, *noaa, hfschedule.NOAAURL)
			if err != nil {
				return nil, err
			}
			v, err := hfschedule.ParseNOAA(strings.NewReader(string(b)))
			if err != nil {
				return nil, err
			}
			entries = append(entries, v...)
			if !noaaWarned {
				valid, updated := hfschedule.NOAAValidity(strings.NewReader(string(b)))
				log.Printf("NOAA Boston timetable header: valid %q, updated %q; the hosted schedule is a planning source, not proof of transmission", valid, updated)
				noaaWarned = true
			}
		}
		if *extra != "" {
			b, err := os.ReadFile(*extra)
			if err != nil {
				return nil, err
			}
			v, err := hfschedule.ParseEiBiSource(strings.NewReader(string(b)), "file:"+*extra)
			if err != nil {
				return nil, fmt.Errorf("extra schedule: %w", err)
			}
			entries = append(entries, v...)
		}
		var terms []string
		for _, term := range strings.Split(*match, ",") {
			if t := strings.TrimSpace(strings.ToLower(term)); t != "" {
				terms = append(terms, t)
			}
		}
		var filtered []hfschedule.Entry
		for _, e := range entries {
			name := strings.ToLower(e.Target.Name)
			matched := len(terms) == 0
			for _, t := range terms {
				if strings.Contains(name, t) {
					matched = true
					break
				}
			}
			if matched && (*kind == "" || e.Target.Kind == *kind) && (*freq == 0 || e.Target.CarrierKHz == *freq) {
				filtered = append(filtered, e)
			}
		}
		return filtered, nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if !*run {
		filtered, err := loadEntries(ctx)
		if err != nil {
			log.Fatal(err)
		}
		if err := enc.Encode(hfschedule.Plan(filtered, time.Now(), *horizon)); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *receivers == "" {
		log.Fatal("-receivers JSON required")
	}
	b, err := os.ReadFile(*receivers)
	if err != nil {
		log.Fatal(err)
	}
	var rs []hfschedule.Receiver
	if err = json.Unmarshal(b, &rs); err != nil {
		log.Fatal(err)
	}
	runner := hfschedule.Runner{Dir: *out, Receivers: rs, Listen: *listen}
	if *once {
		filtered, err := loadEntries(ctx)
		if err != nil {
			log.Fatal(err)
		}
		a, err := runner.RunOnce(ctx, filtered)
		if err != nil {
			log.Fatal(err)
		}
		if a != nil {
			if err = enc.Encode(a); err != nil {
				log.Fatal(err)
			}
		}
		return
	}
	svc := hfschedule.Service{
		Runner:         &runner,
		Load:           loadEntries,
		PollInterval:   *poll,
		ReloadInterval: *reload,
		Logf:           log.Printf,
		OnAudit: func(a hfschedule.Audit) {
			if err := enc.Encode(a); err != nil {
				log.Printf("hfschedule: encode audit: %v", err)
			}
		},
	}
	if err := svc.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
