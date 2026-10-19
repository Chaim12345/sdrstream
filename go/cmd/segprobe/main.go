package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Chaim12345/sdrstream/go/internal/openwebrx"
)

func main() {
	ctx := context.Background()
	t0 := time.Now()
	client, err := openwebrx.New("wss://sdr2.justjakob.de/ws/", openwebrx.DefaultOptions())
	if err != nil {
		fmt.Println("new:", err)
		return
	}
	t1 := time.Now()
	if _, err := client.Connect(ctx); err != nil {
		fmt.Println("connect:", err)
		return
	}
	t2 := time.Now()
	freq := int64(438800000)
	if err := client.Tune(ctx, &freq, "nfm", nil); err != nil {
		fmt.Println("tune:", err)
		return
	}
	t3 := time.Now()
	samples, rate, err := client.RecordSamples(ctx, 5*time.Second)
	t4 := time.Now()
	client.Close()
	fmt.Printf("new:      %v\nconnect:  %v (center=%v compression=%v)\ntune:     %v\nrecord5s: %v (got %d samples @%d)\ntotal:    %v\n",
		t1.Sub(t0), t2.Sub(t1), client.Config.CenterFreq, client.Config.AudioCompression,
		t3.Sub(t2), t4.Sub(t3), len(samples), rate, t4.Sub(t0))
}
