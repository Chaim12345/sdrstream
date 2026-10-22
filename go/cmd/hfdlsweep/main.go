package main

import (
	"fmt"
	"os"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/hfdl"
)

func main() {
	x, rate, err := decode.ReadWAV(os.Args[1])
	if err != nil { panic(err) }
	for c := 300.0; c <= 2600.0; c += 50 {
		fr := hfdl.DecodeAudio(x, float64(rate), c, 0)
		if len(fr) > 0 {
			fmt.Printf("carrier %.0f: %d frames\n", c, len(fr))
			for _, f := range fr {
				for _, l := range hfdl.Describe(f) { fmt.Println("   ", l) }
			}
		}
	}
	fmt.Println("sweep done")
}
