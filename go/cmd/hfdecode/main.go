// Command hfdecode runs the internal decoders over a WAV capture.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func main() {
	kind := flag.String("kind", "morse", "morse|rtty|rttyauto|navtex|sstv|wefax|scan")
	mark := flag.Float64("mark", 0, "rtty mark Hz")
	space := flag.Float64("space", 0, "rtty space Hz")
	baud := flag.Float64("baud", 0, "rtty baud")
	center := flag.Float64("center", 0, "navtex center Hz")
	flag.Parse()
	path := flag.Arg(0)
	if path == "" {
		log.Fatal("usage: hfdecode [flags] file.wav")
	}
	samples, rate, err := decode.ReadWAV(path)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d samples at %d Hz\n", len(samples), rate)
	switch *kind {
	case "morse":
		res, err := decode.MorseDecode(path)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("text: %q\ntone: %.0f Hz dot: %.0f ms\n", res.Text, res.ToneHz, res.DotMs)
	case "rtty":
		fmt.Printf("text: %q\n", decode.RTTYDecode(samples, rate, *mark, *space, *baud))
	case "rttyauto":
		text, inverted := decode.RTTYDecodeAuto(samples, rate, *mark, *space, *baud)
		fmt.Printf("inverted: %v quality: %.0f\ntext: %q\n", inverted, decode.RTTYQuality(text), text)
	case "navtex":
		for _, m := range decode.NAVTEXDecode(samples, rate, *center) {
			fmt.Printf("station=%s subject=%s serial=%s\n%s\n---\n", m.Station, m.Subject, m.Serial, m.Text)
		}
	case "wefax":
		pngData, rep, err := decode.WEFAXImageDetailed(samples, rate, 120, 576)
		if err != nil {
			log.Fatalf("wefax: %v (report %+v)", err, rep)
		}
		out := path + ".png"
		if err := os.WriteFile(out, pngData, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("tones %.0f/%.0f Hz, pair %.2f, anticorr %.2f, wedge %.2f at %.1fs\n",
			rep.BlackHz, rep.WhiteHz, rep.PairConfidence, rep.AntiCorrelation, rep.WedgeContrast, rep.PhasingStartSec)
		fmt.Printf("clock %.1f samples/line (drift %+.0f ppm), %d lines (%d phasing), %dx%d\n",
			rep.SamplesPerLine, rep.DriftPPM, rep.Lines, rep.PhasingLines, rep.Width, rep.Height)
		fmt.Printf("wrote %s (%d bytes)\n", out, len(pngData))
	case "sstv":
		png, err := decode.SSTVImage(samples, rate)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("png bytes: %d\n", len(png))
	case "scan":
		ts, err := decode.ScanTransmissions(path)
		if err != nil {
			log.Fatal(err)
		}
		for _, t := range ts {
			fmt.Printf("%.1fs +%.1fs peak %.0f dB tone %.0f Hz: %s\n", t.StartSec, t.DurationSec, t.PeakDB, t.ToneHz, t.Description)
		}
	}
}
