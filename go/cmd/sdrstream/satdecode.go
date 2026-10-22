package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
)

// satdecode.go: the fetch-and-decode pipeline for satellite downlinks.
// Where "fetch" downloads SatNOGS artefacts and "decode" runs one
// demodulator on one local WAV, satdecode chains the whole path: pick
// recent observations of a satellite, download the audio, the waterfall
// and the station-decoded frames, convert the audio, and decode it into
// readable telemetry and images with the built-in demodulators
// (AFSK/GMSK/BPSK to AX.25 + APRS, NOAA APT and SSTV to PNG).

// satdecodeMode resolves the decoder chain for one observation: the
// explicit -mode flag wins, otherwise the observation's modulation and
// the satellite's identity pick the chain. "auto" tries the likeliest
// decoder first and falls back: a GMSK observation that yields nothing
// is retried as AFSK and vice versa.
func satdecodeChain(mode, obsMode, satName string, freqHz int64) []string {
	if mode != "auto" {
		return []string{mode}
	}
	up := strings.ToUpper(obsMode + " " + satName)
	switch {
	case strings.Contains(up, "SSTV"):
		return []string{"sstv"}
	case strings.Contains(up, "NOAA") || (freqHz >= 137000000 && freqHz <= 138000000 && strings.Contains(up, "APT")):
		return []string{"apt"}
	case strings.Contains(up, "BPSK"):
		return []string{"bpsk", "gmsk", "afsk"}
	case strings.Contains(up, "GMSK"), strings.Contains(up, "MSK"), strings.Contains(up, "FSK"):
		return []string{"gmsk", "afsk"}
	default:
		// FM voice satellites carrying packet (ISS APRS, most 1k2
		// birds) are AFSK; if nothing decodes, try the 9k6 data modes.
		return []string{"afsk", "gmsk"}
	}
}

func printAX25(prefix string, frames []decode.AX25) int {
	shown := 0
	for _, f := range frames {
		fmt.Printf("%s %s\n", prefix, f.String())
		ap := f.APRS()
		if ap.Kind != "other" || ap.HasPos {
			fmt.Printf("%s     aprs: kind=%s", prefix, ap.Kind)
			if ap.HasPos {
				fmt.Printf(" pos=%.4f,%.4f", ap.Lat, ap.Lon)
			}
			if ap.HasAlt {
				fmt.Printf(" alt=%.0fm", ap.Altitude)
			}
			if ap.Text != "" {
				fmt.Printf(" text=%q", ap.Text)
			}
			if ap.Comment != "" {
				fmt.Printf(" comment=%q", ap.Comment)
			}
			fmt.Println()
		}
		shown++
	}
	return shown
}

func cmdSatdecode(args []string) error {
	fs := flag.NewFlagSet("satdecode", flag.ExitOnError)
	name := fs.String("satellite", "", "satellite name from the catalogue, or a substring")
	norad := fs.Int("norad", 0, "NORAD id (use when the satellite is not in the catalogue, e.g. NOAA 19 = 33591)")
	limit := fs.Int("limit", 40, "how many observations to look through")
	count := fs.Int("count", 2, "how many observations to fetch and decode")
	out := fs.String("out", "satdecode", "output directory")
	mode := fs.String("mode", "auto", "auto | afsk | gmsk | bpsk | apt | sstv")
	baud := fs.Int("baud", 0, "pin the packet baud rate (0 = search the usual rates)")
	audioPath := fs.String("audio", "", "decode this local audio file (wav/ogg/mp3) instead of fetching from SatNOGS")
	fs.Parse(args)

	if *audioPath != "" {
		return satdecodeAudio(*audioPath, *mode, *baud, *out)
	}
	if *name == "" && *norad == 0 {
		return fmt.Errorf("satdecode: pass -satellite NAME, -norad ID, or -audio FILE")
	}
	noradID := *norad
	satName := fmt.Sprintf("NORAD %d", noradID)
	if noradID == 0 {
		sat, err := satByName(*name)
		if err != nil {
			return err
		}
		noradID = sat.NoradID
		satName = sat.Name
	}
	fmt.Printf("satdecode: %s (NORAD %d), scanning up to %d observations\n", satName, noradID, *limit)
	observations, err := satnogs.Observations(noradID, *limit, 60*time.Second)
	if err != nil {
		return err
	}
	var withData []satnogs.Observation
	for _, o := range observations {
		if o.PayloadURL != "" || o.DemodURL != "" || o.WaterfallURL != "" {
			withData = append(withData, o)
		}
	}
	if len(withData) == 0 {
		fmt.Println("no observations with downloadable data")
		return nil
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	report := strings.Builder{}
	decoded := 0
	for _, obs := range withData[:min(*count, len(withData))] {
		line := fmt.Sprintf("== #%d %s %s station=%s mode=%s", obs.ID, obs.Start.UTC().Format("2006-01-02 15:04"), obs.SatName, obs.Station, obs.Mode)
		fmt.Println(line)
		report.WriteString(line + "\n")
		if obs.WaterfallURL != "" {
			target := filepath.Join(*out, fmt.Sprintf("%d_waterfall.png", obs.ID))
			if err := satnogs.Download(obs.WaterfallURL, target, 120*time.Second); err != nil {
				fmt.Println("   waterfall:", err)
			} else {
				fmt.Println("   waterfall:", target)
			}
		}
		if obs.DemodURL != "" {
			target := filepath.Join(*out, fmt.Sprintf("%d_frames.dat", obs.ID))
			if err := satnogs.Download(obs.DemodURL, target, 300*time.Second); err != nil {
				fmt.Println("   demod data:", err)
			} else if data, err := os.ReadFile(target); err == nil {
				frames := decode.ParseBlobFrames(data)
				fmt.Printf("   station-decoded frames: %d (%s)\n", len(frames), target)
				for _, bf := range frames {
					mark := "crc-ok"
					if !bf.CRCValid {
						mark = "no-fcs"
					}
					fmt.Printf("    [%s] %s\n", mark, bf.AX25.String())
					report.WriteString(fmt.Sprintf("  [%s] %s\n", mark, bf.AX25.String()))
				}
			}
		}
		if obs.PayloadURL == "" {
			fmt.Println("   no audio for this observation")
			continue
		}
		ext := ".ogg"
		audioFile := filepath.Join(*out, fmt.Sprintf("%d_audio%s", obs.ID, ext))
		if err := satnogs.Download(obs.PayloadURL, audioFile, 300*time.Second); err != nil {
			fmt.Println("   audio:", err)
			continue
		}
		wavFile := filepath.Join(*out, fmt.Sprintf("%d_audio.wav", obs.ID))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := decode.ConvertToWAV(ctx, "", audioFile, wavFile)
		cancel()
		if err != nil {
			fmt.Println("   convert:", err)
			continue
		}
		n := satdecodeWAV(wavFile, satdecodeChain(*mode, obs.Mode, obs.SatName, obs.FrequencyHz), *baud, *out, obs.ID, &report)
		decoded += n
	}
	reportPath := filepath.Join(*out, "report.txt")
	if err := os.WriteFile(reportPath, []byte(report.String()), 0o644); err == nil {
		fmt.Println("report:", reportPath)
	}
	fmt.Printf("satdecode: %d frame(s)/image(s) decoded from live SatNOGS captures\n", decoded)
	return nil
}

// satdecodeAudio handles the -audio form: convert if needed, then decode.
func satdecodeAudio(path, mode string, baud int, out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	wav := path
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		wav = filepath.Join(out, "input.wav")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := decode.ConvertToWAV(ctx, "", path, wav)
		cancel()
		if err != nil {
			return err
		}
	}
	report := strings.Builder{}
	satdecodeWAV(wav, satdecodeChain(mode, "", "", 0), baud, out, 0, &report)
	reportPath := filepath.Join(out, "report.txt")
	if err := os.WriteFile(reportPath, []byte(report.String()), 0o644); err == nil {
		fmt.Println("report:", reportPath)
	}
	return nil
}

// satdecodeWAV runs the decoder chain over one WAV and writes images for
// the picture modes. It returns how many frames/images were decoded.
func satdecodeWAV(wav string, chain []string, baud int, out string, obsID int, report *strings.Builder) int {
	samples, rate, err := decode.ReadWAV(wav)
	if err != nil {
		fmt.Println("   read:", err)
		return 0
	}
	decoded := 0
	for _, m := range chain {
		switch m {
		case "afsk":
			frames, err := decode.AFSKFrames(wav)
			if err != nil {
				fmt.Println("   afsk:", err)
				continue
			}
			if len(frames) > 0 {
				fmt.Printf("   afsk: %d AX.25 frame(s)\n", len(frames))
				decoded += printAX25("    ", frames)
				for _, f := range frames {
					report.WriteString("  afsk: " + f.String() + "\n")
				}
				return decoded
			}
			fmt.Println("   afsk: no frames")
		case "gmsk":
			bauds := decode.GMSKBauds
			if baud > 0 {
				bauds = []int{baud}
			}
			frames := decode.GMSKFrames(samples, rate, bauds)
			if len(frames) > 0 {
				fmt.Printf("   gmsk: %d AX.25 frame(s)\n", len(frames))
				decoded += printAX25("    ", frames)
				for _, f := range frames {
					report.WriteString("  gmsk: " + f.String() + "\n")
				}
				return decoded
			}
			fmt.Println("   gmsk: no frames")
		case "bpsk":
			bauds := decode.BPSKBauds
			if baud > 0 {
				bauds = []int{baud}
			}
			frames := decode.BPSKFrames(samples, rate, bauds, [2]float64{})
			if len(frames) > 0 {
				fmt.Printf("   bpsk: %d AX.25 frame(s)\n", len(frames))
				decoded += printAX25("    ", frames)
				for _, f := range frames {
					report.WriteString("  bpsk: " + f.String() + "\n")
				}
				return decoded
			}
			fmt.Println("   bpsk: no frames")
		case "apt":
			r := decode.APTDecode(samples, rate)
			if r == nil {
				fmt.Println("   apt: no sync found")
				continue
			}
			ok, why := r.Verified()
			fmt.Printf("   apt: lines=%d locked=%d verified=%v (%s)\n", r.Lines, r.LinesLocked, ok, why)
			if ok {
				png, err := r.PNG()
				if err == nil {
					target := filepath.Join(out, fmt.Sprintf("%d_apt.png", obsID))
					if os.WriteFile(target, png, 0o644) == nil {
						fmt.Println("   apt image:", target)
						report.WriteString("  apt image: " + target + "\n")
						decoded++
					}
				}
				return decoded
			}
		case "sstv":
			hits := decode.SSTVDetect(samples, rate)
			if len(hits) == 0 {
				fmt.Println("   sstv: no VIS found")
				continue
			}
			for _, h := range hits {
				fmt.Printf("   sstv: %s (VIS %d) at %.1fs, snr %.1f dB\n", h.Mode, h.VIS, h.StartSec, h.SnrdB)
			}
			png, err := decode.SSTVImage(samples, rate)
			if err != nil {
				fmt.Println("   sstv image:", err)
				continue
			}
			target := filepath.Join(out, fmt.Sprintf("%d_sstv.png", obsID))
			if os.WriteFile(target, png, 0o644) == nil {
				fmt.Println("   sstv image:", target)
				report.WriteString("  sstv image: " + target + "\n")
				decoded++
			}
			return decoded
		}
	}
	return decoded
}
