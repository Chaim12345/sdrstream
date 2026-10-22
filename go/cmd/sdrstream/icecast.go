package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/icecast"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

const icecastUsage = `usage: sdrstream icecast <list|listen|sweep> [flags]

  list    -nwr | -genre G   list NOAA Weather Radio relays or directory streams
  listen  -url U            record one stream, decode SAME, transcribe speech
  sweep   -nwr | -genre G   visit streams one at a time (polite) and scan each
                            for SAME alert headers
`

func cmdIcecast(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, icecastUsage)
		return fmt.Errorf("icecast: missing subcommand")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch args[0] {
	case "list":
		return icecastList(ctx, args[1:])
	case "listen":
		return icecastListen(ctx, args[1:])
	case "sweep":
		return icecastSweep(ctx, args[1:])
	}
	fmt.Fprint(os.Stderr, icecastUsage)
	return fmt.Errorf("icecast: unknown subcommand %q", args[0])
}

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "sdrstream")
	}
	return ""
}

type streamRef struct{ Name, URL string }

func resolveStreams(ctx context.Context, c *icecast.Client, nwr bool, genre string, limit int) ([]streamRef, error) {
	var out []streamRef
	if nwr {
		list, err := c.FetchNWR(ctx, "")
		if err != nil {
			return nil, err
		}
		for _, s := range icecast.Live(list) {
			out = append(out, streamRef{fmt.Sprintf("%s %s, %s %s MHz", s.Callsign, s.City, s.State, s.MHz), s.URL})
		}
	} else {
		entries, err := c.FetchDirectory(ctx, "", defaultCacheDir(), 24*time.Hour)
		if err != nil {
			return nil, err
		}
		for _, e := range icecast.Filter(entries, genre, "audio/") {
			out = append(out, streamRef{e.Name, e.ListenURL})
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func icecastList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("icecast list", flag.ExitOnError)
	nwr := fs.Bool("nwr", false, "list NOAA Weather Radio relays from noaaweatherradio.org")
	genre := fs.String("genre", "weather", "directory genre/name filter (dir.xiph.org)")
	limit := fs.Int("limit", 0, "maximum rows (0 = all)")
	fs.Parse(args)
	streams, err := resolveStreams(ctx, icecast.NewClient(), *nwr, *genre, *limit)
	if err != nil {
		return err
	}
	for _, s := range streams {
		fmt.Printf("%-52s %s\n", s.Name, s.URL)
	}
	fmt.Printf("%d stream(s)\n", len(streams))
	return nil
}

func reportSAME(samples []float64, rate int) int {
	alerts := decode.SAMEScan(samples, rate)
	for _, a := range alerts {
		fmt.Printf("   SAME:   %s\n           raw %s\n", a, a.Raw)
	}
	return len(alerts)
}

func icecastListen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("icecast listen", flag.ExitOnError)
	url := fs.String("url", "", "stream URL")
	dur := fs.Duration("dur", 60*time.Second, "how long to record")
	out := fs.String("out", "", "write the decoded audio to this WAV file")
	asr := fs.Bool("asr", true, "transcribe speech with whisper / NIM when available")
	fs.Parse(args)
	if *url == "" {
		return fmt.Errorf("icecast listen: -url is required")
	}
	c := icecast.NewClient()
	fmt.Printf("== %s (%s)\n", *url, *dur)
	cap, err := c.Capture(ctx, *url, *dur)
	if err != nil {
		return err
	}
	fmt.Printf("   stream: name=%q type=%s %d kbps, %d bytes, %.1f s of audio, rms %.4f\n",
		cap.Info.Name, cap.Info.ContentType, cap.Info.BitrateKbps, cap.Bytes,
		float64(len(cap.Samples))/icecast.PCMRate, icecast.RMS(cap.Samples))
	for _, t := range cap.Titles {
		fmt.Printf("   title:  %s\n", t)
	}
	if *out != "" {
		if err := voice.WriteWAV(*out, cap.Samples, icecast.PCMRate); err != nil {
			return err
		}
		fmt.Printf("   wrote %s\n", *out)
	}
	if n := reportSAME(cap.Samples, icecast.PCMRate); n == 0 {
		fmt.Println("   SAME:   no valid headers in this recording")
	}
	hit := decode.Classify(cap.Samples, icecast.PCMRate)
	fmt.Printf("   class:  %s\n", hit.Kind)
	if !*asr {
		return nil
	}
	if len(voice.DetectSpeech(cap.Samples, icecast.PCMRate, voice.VADOptions{})) == 0 {
		fmt.Println("   voice:  no speech detected")
		return nil
	}
	tr, err := voice.TranscribeBest(ctx, cap.Samples, icecast.PCMRate, voice.DefaultOptions(), voice.DefaultNIMOptions())
	if err != nil {
		fmt.Printf("   voice:  %v\n", err)
		return nil
	}
	v := voice.GuardTranscript(cap.Samples, icecast.PCMRate, tr.Text)
	if v.Rejected {
		fmt.Printf("   voice:  transcript dropped by guard: %s\n", v.Reason)
		return nil
	}
	fmt.Printf("   voice:  %s\n", strings.TrimSpace(v.Text))
	return nil
}

func icecastSweep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("icecast sweep", flag.ExitOnError)
	nwr := fs.Bool("nwr", false, "sweep NOAA Weather Radio relays")
	genre := fs.String("genre", "weather", "directory genre/name filter")
	max := fs.Int("max", 20, "streams to visit")
	dur := fs.Duration("dur", 20*time.Second, "recording length per stream")
	pause := fs.Duration("pause", 2*time.Second, "wait between streams")
	fs.Parse(args)
	c := icecast.NewClient()
	streams, err := resolveStreams(ctx, c, *nwr, *genre, *max)
	if err != nil {
		return err
	}
	found := 0
	for i, s := range streams {
		if ctx.Err() != nil {
			break
		}
		fmt.Printf("[%d/%d] %s\n", i+1, len(streams), s.Name)
		cap, err := c.Capture(ctx, s.URL, *dur)
		if err != nil {
			fmt.Printf("   skip: %v\n", err)
		} else {
			fmt.Printf("   %.1f s, rms %.4f\n", float64(len(cap.Samples))/icecast.PCMRate, icecast.RMS(cap.Samples))
			found += reportSAME(cap.Samples, icecast.PCMRate)
		}
		select {
		case <-ctx.Done():
		case <-time.After(*pause):
		}
	}
	fmt.Printf("%d SAME header(s) in %d stream(s)\n", found, len(streams))
	return nil
}
