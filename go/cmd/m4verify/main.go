// Command m4verify exercises the KiwiSDR pre-capture drain (review item M4):
// it records on frequency A, retunes to B, and records again. If the drain
// inside Record works, the B capture contains only post-tune audio from its
// first sample, and its length matches the requested duration.
//
// Development/QA tool: one connection, short recordings, clean disconnect.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

func writeWAV(path string, samples []int16, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataLen := uint32(len(samples) * 2)
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], 36+dataLen)
	copy(hdr[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1)
	binary.LittleEndian.PutUint16(hdr[22:24], 1)
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(hdr[32:34], 2)
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], dataLen)
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	return binary.Write(f, binary.LittleEndian, samples)
}

func main() {
	server := flag.String("server", "", "KiwiSDR host[:port] or URL")
	freqA := flag.Float64("freqA", 198.0, "first (loud, continuous) dial frequency in kHz")
	modeA := flag.String("modeA", "am", "mode for frequency A")
	freqB := flag.Float64("freqB", 4583.0, "second (target) dial frequency in kHz")
	modeB := flag.String("modeB", "usb", "mode for frequency B")
	secsA := flag.Float64("secsA", 4, "seconds to record on A (control)")
	secsB := flag.Float64("secsB", 12, "seconds to record on B after retune")
	outPrefix := flag.String("out", "/tmp/m4", "output WAV path prefix")
	flag.Parse()
	if *server == "" {
		log.Fatal("-server required")
	}
	ctx := context.Background()
	client, err := kiwisdr.New(*server, func() kiwisdr.Options { o := kiwisdr.DefaultOptions(); o.AllowPrivate = true; return o }())
	if err != nil {
		log.Fatalf("new: %v", err)
	}
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer client.Close()

	start := time.Now()
	if err := client.Tune(ctx, int64(*freqA*1000), *modeA); err != nil {
		log.Fatalf("tune A: %v", err)
	}
	sampA, rate, err := client.Record(ctx, time.Duration(*secsA*float64(time.Second)))
	if err != nil {
		log.Fatalf("record A: %v", err)
	}
	if err := writeWAV(*outPrefix+"_A.wav", sampA, rate); err != nil {
		log.Fatalf("wav A: %v", err)
	}
	fmt.Printf("A: %d samples at %d Hz = %.2f s (asked %.1f) -> %s_A.wav\n",
		len(sampA), rate, float64(len(sampA))/float64(rate), *secsA, *outPrefix)

	if err := client.Tune(ctx, int64(*freqB*1000), *modeB); err != nil {
		log.Fatalf("tune B: %v", err)
	}
	tB := time.Now()
	sampB, rateB, err := client.Record(ctx, time.Duration(*secsB*float64(time.Second)))
	if err != nil {
		log.Fatalf("record B: %v", err)
	}
	wallB := time.Since(tB)
	if err := writeWAV(*outPrefix+"_B.wav", sampB, rateB); err != nil {
		log.Fatalf("wav B: %v", err)
	}
	fmt.Printf("B: %d samples at %d Hz = %.2f s (asked %.1f), wall %.2f s -> %s_B.wav\n",
		len(sampB), rateB, float64(len(sampB))/float64(rateB), *secsB, wallB.Seconds(), *outPrefix)
	fmt.Printf("server info: %+v\n", client.InfoSnapshot())
	_ = start
}
