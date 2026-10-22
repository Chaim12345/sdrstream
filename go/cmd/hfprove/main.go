// Command hfprove proves the digimode decoders on real over-the-air audio:
// it records full, UTC-aligned FT8 (15 s) and WSPR (120 s) cycles plus
// PSK31 and CW listens from one public KiwiSDR and runs the internal
// decoders on the captures. Nothing is synthesised: every printed decode
// came off the named receiver at the printed UTC time.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

// recorder is one tuned live audio source.
type recorder interface {
	Tune(freqHz int64, mode string) error
	Record(dur time.Duration) ([]int16, int, error)
	// StartOf reports the UTC time of the first returned sample, given the
	// wall-clock times around Record and the captured duration. KiwiSDR
	// audio is read straight off the socket, so the first sample is the
	// call time; WebSDR buffers audio in a background read loop, so the
	// first sample is (end of Record) minus (captured duration).
	StartOf(callStart, callEnd time.Time, captured time.Duration) time.Time
}

type kiwiRec struct{ c *kiwisdr.Client }

func (k kiwiRec) Tune(f int64, m string) error { return k.c.Tune(context.Background(), f, m) }
func (k kiwiRec) Record(d time.Duration) ([]int16, int, error) {
	return k.c.Record(context.Background(), d)
}
func (k kiwiRec) StartOf(callStart, callEnd time.Time, captured time.Duration) time.Time {
	return callStart
}

type webRec struct{ c *websdr.Client }

func (w webRec) Tune(f int64, m string) error {
	return w.c.Tune(context.Background(), float64(f)/1000, m, nil)
}
func (w webRec) Record(d time.Duration) ([]int16, int, error) {
	return w.c.Record(context.Background(), d)
}
func (w webRec) StartOf(callStart, callEnd time.Time, captured time.Duration) time.Time {
	return callEnd.Add(-captured)
}

func writeWAV(path string, samples []int16, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataLen := uint32(len(samples) * 2)
	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, uint32(36)+dataLen)
	f.Write([]byte("WAVEfmt "))
	binary.Write(f, binary.LittleEndian, uint32(16))
	binary.Write(f, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(f, binary.LittleEndian, uint16(1)) // mono
	binary.Write(f, binary.LittleEndian, uint32(uint32(rate)))
	binary.Write(f, binary.LittleEndian, uint32(uint32(rate)*2))
	binary.Write(f, binary.LittleEndian, uint16(2))
	binary.Write(f, binary.LittleEndian, uint16(16))
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, dataLen)
	return binary.Write(f, binary.LittleEndian, samples)
}

// recordAligned waits until lead seconds before the next multiple of
// slotSecs, then records for dur, returning samples with the exact UTC
// time of the first sample.
func recordAligned(ctx context.Context, c recorder, slotSecs int64, lead time.Duration, dur time.Duration) ([]int16, int, time.Time, error) {
	now := time.Now().UTC()
	next := now.Truncate(time.Duration(slotSecs) * time.Second).Add(time.Duration(slotSecs) * time.Second)
	wait := time.Until(next.Add(-lead))
	if wait > 0 {
		fmt.Printf("  waiting %s for slot boundary %s\n", wait.Round(time.Second), next.Format("15:04:05"))
		select {
		case <-ctx.Done():
			return nil, 0, time.Time{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	callStart := time.Now().UTC()
	samples, rate, err := c.Record(dur)
	callEnd := time.Now().UTC()
	if err != nil {
		return samples, rate, callStart, err
	}
	start := c.StartOf(callStart, callEnd, time.Duration(float64(len(samples))/float64(rate)*float64(time.Second)))
	return samples, rate, start, err
}

func main() {
	server := flag.String("server", "", "receiver host[:port] or URL")
	kind := flag.String("kind", "kiwisdr", "kiwisdr|websdr")
	out := flag.String("out", "", "directory to save the capture WAVs (default: none)")
	band := flag.String("band", "20m", "20m|40m preset for default frequencies")
	ft8Freq := flag.Float64("ft8", 0, "FT8 dial kHz (default per band)")
	wsprFreq := flag.Float64("wspr", 0, "WSPR dial kHz (default per band)")
	pskFreq := flag.Float64("psk", 0, "PSK31 listen kHz (default per band)")
	cwFreq := flag.Float64("cw", 0, "CW listen kHz (default per band)")
	skip := flag.String("skip", "", "comma list: ft8,wspr,psk,cw")
	crosscheck := flag.Bool("crosscheck", false, "cross-check decodes against PSKReporter (FT8) and wspr.live (WSPR)")
	ledger := flag.String("ledger", "", "append proof-ledger JSONL items (with audio paths) to this file")
	grace := flag.Duration("grace", 75*time.Second, "wait after the WSPR window before querying wspr.live (spot upload lag)")
	flag.Parse()
	if *server == "" {
		log.Fatal("-server required")
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	var ledgerItems []ledgerItem
	skipped := map[string]bool{}
	for _, s := range splitComma(*skip) {
		skipped[s] = true
	}
	if *band == "40m" {
		if *ft8Freq == 0 {
			*ft8Freq = 7074
		}
		if *wsprFreq == 0 {
			*wsprFreq = 7038.6
		}
		if *pskFreq == 0 {
			*pskFreq = 7070
		}
		if *cwFreq == 0 {
			*cwFreq = 7030
		}
	} else {
		if *ft8Freq == 0 {
			*ft8Freq = 14074
		}
		if *wsprFreq == 0 {
			*wsprFreq = 14095.6
		}
		if *pskFreq == 0 {
			*pskFreq = 14070
		}
		if *cwFreq == 0 {
			*cwFreq = 14040
		}
	}

	ctx := context.Background()
	var rec recorder
	if *kind == "websdr" {
		c, err := websdr.New(*server, func() websdr.Options { o := websdr.DefaultOptions(); o.AllowPrivate = true; return o }())
		if err != nil {
			log.Fatalf("new: %v", err)
		}
		if err := c.Connect(ctx); err != nil {
			log.Fatalf("connect: %v", err)
		}
		defer c.Close()
		rec = webRec{c}
		fmt.Printf("connected to WebSDR %s\n", *server)
	} else {
		c, err := kiwisdr.New(*server, func() kiwisdr.Options { o := kiwisdr.DefaultOptions(); o.AllowPrivate = true; return o }())
		if err != nil {
			log.Fatalf("new: %v", err)
		}
		if err := c.Connect(ctx); err != nil {
			log.Fatalf("connect: %v", err)
		}
		defer c.Close()
		rec = kiwiRec{c}
		fmt.Printf("connected to KiwiSDR %s (%+v)\n", *server, c.InfoSnapshot())
	}

	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	if !skipped["ft8"] {
		fmt.Printf("\n== FT8: %.3f MHz USB, one full 15 s cycle ==\n", *ft8Freq/1000)
		if err := rec.Tune(int64(*ft8Freq*1000), "usb"); err != nil {
			log.Printf("tune: %v", err)
		} else {
			samples, rate, start, err := recordAligned(ctx, rec, 15, 700*time.Millisecond, 16*time.Second)
			if err != nil {
				log.Printf("record: %v", err)
			} else {
				fmt.Printf("  captured %.1f s starting %s UTC\n", float64(len(samples))/float64(rate), start.Format("15:04:05.000"))
				if *out != "" {
					p := filepath.Join(*out, "ft8_"+start.Format("150405")+"Z.wav")
					if err := writeWAV(p, samples, rate); err == nil {
						fmt.Printf("  saved %s\n", p)
					}
				}
				msgs, engine, derr := decode.FTXDecodeAuto(ctx, toF64(samples), rate, "FT8")
				if derr != nil {
					log.Printf("ft8 decode: %v", derr)
				}
				for _, m := range msgs {
					fmt.Printf("  %s %s %+5.1f dB %+4.1f s %4.0f Hz  %s\n",
						start.UTC().Format("150405"), m.Protocol, m.SNR, m.DT, m.FreqHz, m.Text)
				}
				fmt.Printf("  FT8: %d messages decoded from live audio (engine: %s)\n", len(msgs), engine)
				if *crosscheck || *ledger != "" {
					audio := ""
					if *out != "" {
						audio = filepath.Join(*out, "ft8_"+start.Format("150405")+"Z.wav")
					}
					seen := map[string]bool{}
					spots := map[string][]string{}
					if *crosscheck {
						for _, m := range msgs {
							for _, c := range ft8Senders(m.Text) {
								if seen[c] || len(seen) >= 10 {
									continue
								}
								seen[c] = true
								if rc, err := pskrSpots(ctx, httpClient, c, 15*time.Minute); err == nil {
									spots[c] = rc
								} else {
									log.Printf("pskreporter %s: %v", c, err)
								}
								time.Sleep(300 * time.Millisecond)
							}
						}
					}
					for _, m := range msgs {
						verified := "unverified"
						ref := ""
						if *crosscheck {
							for _, c := range ft8Senders(m.Text) {
								if len(spots[c]) > 0 {
									verified = "verified live"
									ref = fmt.Sprintf("pskreporter: %s heard by %s", c, strings.Join(spots[c][:min(3, len(spots[c]))], ","))
								}
							}
						}
						fmt.Printf("    [%s] %s\n", verified, m.Text)
						ledgerItems = append(ledgerItems, ledgerItem{
							Source: "sdrstream-hfprove", Kind: "digimode",
							ID:        fmt.Sprintf("hfprove|ft8|%d|%s", start.Unix(), m.Text),
							Label:     m.Text,
							Detail:    fmt.Sprintf("FT8 %.3f MHz %+.1f dB %+.1f s %.0f Hz via %s", *ft8Freq/1000, m.SNR, m.DT, m.FreqHz, *server),
							ProviderT: start.UTC().Format(time.RFC3339),
							FetchedAt: time.Now().UTC().Format(time.RFC3339),
							DecodedBy: "sdrstream/" + engine,
							Ref:       ref, Verified: verified, Audio: audio,
						})
					}
				}
			}
		}
	}

	if !skipped["wspr"] {
		fmt.Printf("\n== WSPR: %.4f MHz USB, one full 120 s cycle ==\n", *wsprFreq/1000)
		if err := rec.Tune(int64(*wsprFreq*1000), "usb"); err != nil {
			log.Printf("tune: %v", err)
		} else {
			samples, rate, start, err := recordAligned(ctx, rec, 120, time.Second, 122*time.Second)
			if err != nil {
				log.Printf("record: %v", err)
			} else {
				fmt.Printf("  captured %.1f s starting %s UTC\n", float64(len(samples))/float64(rate), start.Format("15:04:05.000"))
				if *out != "" {
					p := filepath.Join(*out, "wspr_"+start.Format("150405")+"Z.wav")
					if err := writeWAV(p, samples, rate); err == nil {
						fmt.Printf("  saved %s\n", p)
					}
				}
				wmsgs, wengine, derr := decode.WSPRDecodeAuto(ctx, toF64(samples), rate)
				if derr != nil {
					log.Printf("wspr decode: %v", derr)
				}
				for _, m := range wmsgs {
					fmt.Printf("  %s %+4.0f dB %+4.1f  %4.0f Hz  %s\n",
						start.UTC().Format("1504"), m.SNR, m.DT, m.FreqHz, m.Text)
				}
				fmt.Printf("  WSPR: %d messages decoded from live audio (engine: %s)\n", len(wmsgs), wengine)
				if *crosscheck || *ledger != "" {
					audio := ""
					if *out != "" {
						audio = filepath.Join(*out, "wspr_"+start.Format("150405")+"Z.wav")
					}
					var spots map[string]int
					if *crosscheck && len(wmsgs) > 0 {
						if wait := time.Until(start.Add(120 * time.Second).Add(*grace)); wait > 0 {
							fmt.Printf("  waiting %s for wspr.live spot uploads\n", wait.Round(time.Second))
							time.Sleep(wait)
						}
						var calls []string
						seen := map[string]bool{}
						for _, m := range wmsgs {
							if c := wsprSender(m.Text); c != "" && !seen[c] {
								seen[c] = true
								calls = append(calls, c)
							}
						}
						var err error
						if spots, err = wsprLiveSpots(ctx, httpClient, calls, start.Add(-30*time.Second)); err != nil {
							log.Printf("wspr.live: %v", err)
						}
					}
					for _, m := range wmsgs {
						verified := "unverified"
						ref := ""
						if *crosscheck && spots[wsprSender(m.Text)] > 0 {
							verified = "verified live"
							ref = fmt.Sprintf("wspr.live: %d spot(s) for %s", spots[wsprSender(m.Text)], wsprSender(m.Text))
						}
						fmt.Printf("    [%s] %s\n", verified, m.Text)
						ledgerItems = append(ledgerItems, ledgerItem{
							Source: "sdrstream-hfprove", Kind: "digimode",
							ID:        fmt.Sprintf("hfprove|wspr|%d|%s", start.Unix(), m.Text),
							Label:     m.Text,
							Detail:    fmt.Sprintf("WSPR %.4f MHz %+.0f dB %+.1f s %.0f Hz via %s", *wsprFreq/1000, m.SNR, m.DT, m.FreqHz, *server),
							ProviderT: start.UTC().Format(time.RFC3339),
							FetchedAt: time.Now().UTC().Format(time.RFC3339),
							DecodedBy: "sdrstream/" + wengine,
							Ref:       ref, Verified: verified, Audio: audio,
						})
					}
				}
			}
		}
	}

	if !skipped["psk"] {
		fmt.Printf("\n== PSK31: %.3f MHz USB, 30 s listen ==\n", *pskFreq/1000)
		if err := rec.Tune(int64(*pskFreq*1000), "usb"); err != nil {
			log.Printf("tune: %v", err)
		} else {
			start := time.Now().UTC()
			samples, rate, err := rec.Record(30 * time.Second)
			if err != nil {
				log.Printf("record: %v", err)
			} else {
				fmt.Printf("  captured %.1f s starting %s UTC\n", float64(len(samples))/float64(rate), start.Format("15:04:05.000"))
				if *out != "" {
					p := filepath.Join(*out, "psk31_"+start.Format("150405")+"Z.wav")
					if err := writeWAV(p, samples, rate); err == nil {
						fmt.Printf("  saved %s\n", p)
					}
				}
				sigs := decode.PSK31Scan(toF64(samples), rate)
				for _, s := range sigs {
					fmt.Printf("  %4.0f Hz: %q\n", s.FreqHz, s.Text)
				}
				fmt.Printf("  PSK31: %d signals decoded from live audio\n", len(sigs))
			}
		}
	}

	if !skipped["cw"] {
		fmt.Printf("\n== CW: %.3f MHz USB, 30 s listen ==\n", *cwFreq/1000)
		if err := rec.Tune(int64(*cwFreq*1000), "usb"); err != nil {
			log.Printf("tune: %v", err)
		} else {
			start := time.Now().UTC()
			samples, rate, err := rec.Record(30 * time.Second)
			if err != nil {
				log.Printf("record: %v", err)
			} else {
				fmt.Printf("  captured %.1f s starting %s UTC\n", float64(len(samples))/float64(rate), start.Format("15:04:05.000"))
				p := filepath.Join(os.TempDir(), "hfprove_cw.wav")
				if *out != "" {
					p = filepath.Join(*out, "cw_"+start.Format("150405")+"Z.wav")
				}
				if err := writeWAV(p, samples, rate); err != nil {
					log.Printf("wav: %v", err)
				} else {
					fmt.Printf("  saved %s\n", p)
					hz, snr := decode.FindDominantTone(toF64(samples), rate)
					fmt.Printf("  dominant tone %.0f Hz, SNR %.1f dB\n", hz, snr)
					res, err := decode.MorseDecode(p)
					if err != nil {
						log.Printf("morse: %v", err)
					} else {
						fmt.Printf("  CW text: %q (tone %.0f Hz, dot %.0f ms)\n", res.Text, res.ToneHz, res.DotMs)
					}
				}
			}
		}
	}
	if *ledger != "" {
		if err := appendLedger(*ledger, ledgerItems); err != nil {
			log.Printf("ledger: %v", err)
		} else {
			fmt.Printf("\nledger: %d items appended to %s\n", len(ledgerItems), *ledger)
		}
	}
	fmt.Println("\ndone - all decodes above are from live over-the-air audio")
}

func toF64(in []int16) []float64 {
	out := make([]float64, len(in))
	for i, s := range in {
		out[i] = float64(s) / 32768.0 // decoders expect ReadWAV-style +/-1 audio
	}
	return out
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	return append(out, cur)
}
