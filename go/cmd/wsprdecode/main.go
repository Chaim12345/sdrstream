// Command wsprdecode decodes WSPR from a WAV file. With -start (Unix
// seconds of the first sample) it cuts the audio at even UTC minutes and
// decodes every full 2-minute window; without it the file is one window.
// -dump DIR also writes each window as a 12 kHz WAV named YYMMDD_HHMM.wav
// so the reference wsprd can be run on the same audio.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func main() {
	start := flag.Float64("start", 0, "Unix time of the first sample (0 = single window)")
	dump := flag.String("dump", "", "write 12 kHz window WAVs here")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: wsprdecode [-start unix] [-dump dir] file.wav")
	}
	samples, rate, err := decode.ReadWAV(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	if *start == 0 {
		for _, m := range decode.WSPRDecode(samples, rate) {
			fmt.Printf("%+5.1f %+5.2f %9.1f %s\n", m.SNR, m.DT, m.FreqHz, m.Text)
		}
		return
	}
	for _, w := range decode.WSPRWindows(samples, rate, *start) {
		t := time.Unix(w.Start, 0).UTC()
		fmt.Printf("window %s\n", t.Format("2006-01-02 15:04"))
		if *dump != "" {
			p := filepath.Join(*dump, t.Format("060102_1504")+".wav")
			s16 := make([]int16, len(w.Audio12k))
			for i, v := range w.Audio12k {
				s16[i] = int16(v * 32767)
			}
			os.WriteFile(p, decode.WriteWAV16(s16, 12000), 0o644)
		}
		for _, m := range w.Messages {
			fmt.Printf("  %+5.1f %+5.2f %9.1f %s\n", m.SNR, m.DT, m.FreqHz, m.Text)
		}
	}
}
