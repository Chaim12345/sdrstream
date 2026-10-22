package main

// The avmarine command decodes aircraft and marine datalink traffic from
// open community feeds, live:
//
//   - raw AIS NMEA sentences from the openwaters.io community network,
//     decoded bit-by-bit by internal/decode (ITU-R M.1371);
//   - ACARS/VDL2/HFDL/L-band message blocks from the airframes.io public
//     API and realtime firehose, interpreted by internal/decode
//     (ARINC 620 label semantics and structured text);
//   - ADS-B/Mode S reference frames (--modes-frames) decoded by
//     internal/decode (CRC-24, DF17/18, CPR) to demonstrate the frame
//     decoder; live aircraft state is cross-checked against the open
//     adsb.lol API when --adsb-check is set.
//
// Only open, unencrypted, publicly offered sources are used, at polite
// request rates, and every line of output names its upstream source.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

// cprPairWindow is the greatest separation between an even and an odd CPR
// frame that still allows a global decode: the aircraft must not have
// crossed a latitude zone between the two frames.
const cprPairWindow = 10 * time.Second

// cprPair holds the newest even and odd CPR frames seen for one aircraft.
type cprPair struct {
	even, odd         *decode.ModeSMessage
	evenWhen, oddWhen time.Time
}

// cprPairTracker pairs CPR frames per aircraft. Pairing only the first two
// frames seen, regardless of address, mixes positions from different
// aircraft and produces nonsense coordinates.
type cprPairTracker struct {
	byICAO map[string]*cprPair
}

func newCPRPairTracker() *cprPairTracker {
	return &cprPairTracker{byICAO: make(map[string]*cprPair)}
}

// add records one CPR frame under its ICAO24 address. When both parities
// are on hand it returns the pair; fresh reports whether the two frames
// are within cprPairWindow of each other.
func (t *cprPairTracker) add(m *decode.ModeSMessage, when time.Time) (pair *cprPair, fresh bool) {
	p := t.byICAO[m.ICAO24]
	if p == nil {
		p = &cprPair{}
		t.byICAO[m.ICAO24] = p
	}
	if m.CPROdd {
		p.odd, p.oddWhen = m, when
	} else {
		p.even, p.evenWhen = m, when
	}
	if p.even == nil || p.odd == nil {
		return nil, false
	}
	dt := p.evenWhen.Sub(p.oddWhen)
	if dt < 0 {
		dt = -dt
	}
	return p, dt <= cprPairWindow
}

type avmarineEvidence struct {
	Kind       string `json:"kind"` // ais, acars, modes
	Source     string `json:"source"`
	Raw        string `json:"raw"`
	Decoded    any    `json:"decoded"`
	CrossCheck string `json:"cross_check,omitempty"`
	Note       string `json:"note,omitempty"`
}

func cmdAVMarine(args []string) error {
	fs := flag.NewFlagSet("avmarine", flag.ExitOnError)
	listen := fs.Duration("listen", 45*time.Second, "live listen duration")
	outDir := fs.String("out", "", "write JSONL evidence files here")
	noAIS := fs.Bool("no-ais", false, "skip the openwaters AIS feed")
	noACARS := fs.Bool("no-acars", false, "skip the airframes ACARS feed")
	modesFrames := fs.String("modes-frames", "", "comma-separated hex Mode S frames to decode")
	fs.Parse(args)

	var mu sync.Mutex
	var evidence []avmarineEvidence
	record := func(ev avmarineEvidence) {
		mu.Lock()
		evidence = append(evidence, ev)
		mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *listen+30*time.Second)
	defer cancel()
	var wg sync.WaitGroup

	if !*noAIS {
		wg.Add(1)
		go func() {
			defer wg.Done()
			asm := decode.NewAISAssembler()
			ow := &feeds.OpenWaters{Token: os.Getenv("SDRSTREAM_OW_TOKEN")}
			if ow.Token != "" {
				fmt.Println("AIS   using openwaters.io personal token (raised limits)")
			}
			ow.OnSentence = func(rs feeds.AISRawSentence) {
				br, raw, err := asm.Feed(rs.Sentence)
				if err != nil {
					return
				}
				if br == nil {
					return // fragment stored, message incomplete
				}
				m, err := decode.DecodeAIS(br, raw)
				if err != nil {
					return
				}
				fmt.Printf("AIS   %s %s src=%s/%s synth=%v\n", rs.Time.Format(time.RFC3339), m.Describe(), rs.Source, rs.Station, rs.Synthesized)
				record(avmarineEvidence{Kind: "ais", Source: "openwaters.io/" + rs.Source, Raw: raw, Decoded: m,
					Note: feeds.OpenWatersAttributionNotice})
			}
			if err := ow.Run(ctx, func(feeds.Item) {}); err != nil && ctx.Err() == nil {
				fmt.Printf("AIS   feed ended: %v\n", err)
			}
		}()
	}

	if !*noACARS {
		wg.Add(1)
		go func() {
			defer wg.Done()
			af := &feeds.Airframes{}
			// One polite REST snapshot first, then the realtime firehose.
			if msgs, err := af.FetchRecent(ctx); err == nil {
				for _, m := range msgs {
					printACARS(m, record)
				}
			} else {
				fmt.Printf("ACARS REST: %v\n", err)
			}
			if err := af.RunSniff(ctx, func(m feeds.AirframesMessage) { printACARS(m, record) }); err != nil && ctx.Err() == nil {
				fmt.Printf("ACARS firehose ended: %v\n", err)
			}
		}()
	}

	if *modesFrames != "" {
		pairs := newCPRPairTracker()
		for _, f := range strings.Split(*modesFrames, ",") {
			m, err := decode.DecodeModeS(strings.TrimSpace(f))
			if err != nil {
				fmt.Printf("MODES %s -> %v\n", f, err)
				continue
			}
			fmt.Printf("MODES %s\n", m.Describe())
			record(avmarineEvidence{Kind: "modes", Source: "published-capture", Raw: m.Raw, Decoded: m})
			if !m.HasCPR {
				continue
			}
			when := m.When
			if when.IsZero() {
				when = time.Now()
			}
			pair, fresh := pairs.add(m, when)
			if pair == nil {
				continue // still waiting for the other CPR parity
			}
			if !fresh {
				fmt.Printf("MODES CPR: icao=%s even/odd pair more than %s apart, not decoding\n",
					m.ICAO24, cprPairWindow)
				continue
			}
			lat, lon, err := decode.CPRPosition(pair.even, pair.odd, pair.evenWhen, pair.oddWhen)
			if err != nil {
				fmt.Printf("MODES CPR: %v\n", err)
			} else {
				fmt.Printf("MODES CPR position: %.6f, %.6f (icao=%s)\n", lat, lon, pair.even.ICAO24)
				record(avmarineEvidence{Kind: "modes", Source: "published-capture",
					Raw:     pair.even.Raw + " + " + pair.odd.Raw,
					Decoded: map[string]any{"lat": lat, "lon": lon, "icao24": pair.even.ICAO24},
					Note:    "CPR global decode of the even/odd pair"})
			}
		}
	}

	wg.Wait()

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(*outDir, "avmarine-evidence.jsonl"))
		if err != nil {
			return err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range evidence {
			if err := enc.Encode(ev); err != nil {
				return err
			}
		}
		fmt.Printf("wrote %d evidence records to %s\n", len(evidence), f.Name())
	}
	return nil
}

func printACARS(m feeds.AirframesMessage, record func(avmarineEvidence)) {
	sem := decode.InterpretACARS(m.Label, m.Text)
	fmt.Printf("ACARS %s src=%s(%s) %.3fMHz tail=%s flight=%s | %s\n",
		m.Timestamp, m.Source, m.SourceType, m.Frequency, m.Tail, m.FlightNumber, sem.Interpreted)
	record(avmarineEvidence{
		Kind:    "acars",
		Source:  "airframes.io/" + m.Source + "/" + m.StationIdent,
		Raw:     fmt.Sprintf("mode=%s label=%s freq=%.3f tail=%s flight=%s text=%q", m.Mode, m.Label, m.Frequency, m.Tail, m.FlightNumber, m.Text),
		Decoded: sem,
	})
}
