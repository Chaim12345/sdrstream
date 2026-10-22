// Command ftxdecode decodes FT8 or FT4 from a WAV file. With -start (Unix
// seconds of the first sample) it cuts the audio at UTC slot boundaries;
// without it the file is treated as one slot.
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func main() {
	proto := flag.String("proto", "FT8", "FT8 or FT4")
	start := flag.Float64("start", 0, "Unix time of the first sample (0 = single slot)")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: ftxdecode [-proto FT8|FT4] [-start unix] file.wav")
	}
	samples, rate, err := decode.ReadWAV(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	var msgs []decode.FTXMessage
	if *start > 0 {
		msgs = decode.FTXDecodeStream(samples, rate, *start, *proto)
	} else {
		msgs = decode.FTXDecode(samples, rate, *proto)
	}
	for _, m := range msgs {
		slot := ""
		if m.SlotUnix > 0 {
			slot = time.Unix(m.SlotUnix, 0).UTC().Format("150405 ")
		}
		fmt.Printf("%s%s %+5.1f %+5.2f %4.0f Hz  %s\n", slot, m.Protocol, m.SNR, m.DT, m.FreqHz, m.Text)
	}
	fmt.Printf("%d messages\n", len(msgs))
}
