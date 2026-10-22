package main

import (
	"fmt"
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"os"
)

func main() {
	s, r, _ := decode.ReadWAV(os.Args[1])
	for _, g := range decode.PSK31Scan(s, r) {
		fmt.Printf("%.1f Hz snr %.1f inv %d chars %d: %q\n", g.FreqHz, g.SNR, g.Invalid, g.Chars, g.Text)
	}
}
