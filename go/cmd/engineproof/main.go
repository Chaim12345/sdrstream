// Proof harness: run the live scan decode path (HFDecodeForScan, the same
// entry the WebSDR scanner and hfschedule use) over the committed live
// captures and print the gated result text with engine labels.
//
// decodeHF derives slot times from time.Now()-duration, correct for a
// capture that just finished. To replay a historical capture faithfully we
// trim its leading samples up to the first true UTC slot boundary, so the
// now-based cut matches the original phase.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func run(path, kind, name string, startUnix float64) {
	samples, rate, err := decode.ReadWAV(path)
	if err != nil {
		fmt.Println("READ ERR", err)
		return
	}
	slot := 15.0
	if kind == "wspr" {
		slot = 120.0
	}
	// Trim the lead-in up to the first true UTC slot boundary so the
	// content slots align with the cut grid.
	next := math.Ceil(startUnix/slot) * slot
	if drop := int((next - startUnix) * float64(rate)); drop > 0 && drop < len(samples) {
		fmt.Printf("(trimmed %.2fs lead-in to UTC slot boundary)\n", next-startUnix)
		samples = samples[drop:]
	}
	pcm := make([]int16, len(samples))
	for i, s := range samples {
		v := int(s * 32768)
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		pcm[i] = int16(v)
	}
	// decodeHF anchors its UTC slot cut at time.Now()-duration. Replay is
	// faithful only when that lands on a true slot boundary, so wait for
	// the wall clock to reach the matching phase (<= one slot).
	dur := float64(len(pcm)) / float64(rate)
	for {
		phase := math.Mod(math.Mod(float64(time.Now().UnixNano())/1e9, slot)-math.Mod(dur, slot)+slot, slot)
		if phase < 0.25 || phase > slot-0.25 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r := decode.HFDecodeForScan(ctx, decode.HFTarget{Name: name, Kind: kind, DialKHz: 7074, Mode: "usb"}, "websdr.ewi.utwente.nl:8901", pcm, rate)
	fmt.Printf("== %s == gate.Live=%v reason=%q\n", name, r.Gate.Live, r.Gate.Reason)
	fmt.Println(r.Text)
}

func main() {
	var start float64
	fmt.Sscanf(os.Args[4], "%f", &start)
	run(os.Args[1], os.Args[2], os.Args[3], start)
}
