package main

import (
	"fmt"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

func main() {
	text, _ := tle.LoadCatnr(25338, 24, false, 60*time.Second)
	tr, _ := tracking.FromTLEText(text, "NOAA 15")
	utc := time.Date(2026, 10, 23, 7, 11, 45, 0, time.UTC)
	idt := utc.In(time.FixedZone("IDT", 3*3600))
	fmt.Println("same instant:", utc, "/", idt)
	l1, _ := tr.LookAngle(utc, 51.80, 5.71, 0)
	l2, _ := tr.LookAngle(idt, 51.80, 5.71, 0)
	fmt.Printf("LookAngle(UTC tag) el=%.2f  LookAngle(IDT tag) el=%.2f\n", l1.Elevation, l2.Elevation)
}
