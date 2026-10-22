// Command hfcap records audio from a public KiwiSDR receiver into a WAV
// file. It is a development tool for the HF decode pipeline: one
// connection, one short recording, clean disconnect.
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

func main() {
	server := flag.String("server", "", "KiwiSDR host[:port] or URL")
	freqKHz := flag.Float64("freq", 7038.5, "dial frequency in kHz")
	mode := flag.String("mode", "usb", "kiwisdr mode")
	secs := flag.Float64("secs", 12, "seconds to record (short listens; pass explicitly for targeted decodes)")
	out := flag.String("out", "/tmp/cap.wav", "output WAV path")
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
	if err := client.Tune(ctx, int64(*freqKHz*1000), *mode); err != nil {
		log.Fatalf("tune: %v", err)
	}
	samples, rate, err := client.Record(ctx, time.Duration(*secs*float64(time.Second)))
	if err != nil {
		log.Fatalf("record: %v", err)
	}
	if err := writeWAV(*out, samples, rate); err != nil {
		log.Fatalf("wav: %v", err)
	}
	peak := 0
	for _, s := range samples {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	fmt.Printf("recorded %d samples at %d Hz -> %s (peak %d, %.1f s)\n",
		len(samples), rate, *out, peak, float64(len(samples))/float64(rate))
	fmt.Printf("server info: %+v\n", client.InfoSnapshot())
}

func writeWAV(path string, samples []int16, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data := len(samples) * 2
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(36+data))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1)
	binary.LittleEndian.PutUint16(hdr[22:24], 1)
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(hdr[32:34], 2)
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(data))
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, 2)
	for _, s := range samples {
		binary.LittleEndian.PutUint16(buf, uint16(s))
		if _, err := f.Write(buf); err != nil {
			return err
		}
	}
	return nil
}
