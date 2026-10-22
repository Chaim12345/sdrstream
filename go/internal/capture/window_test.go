package capture

import (
	"testing"
	"time"
)

func TestAlignedWindowRequiresFullSamples(t *testing.T) {
	at := time.Date(2026, 10, 22, 10, 11, 7, 0, time.UTC)
	w := NewHFWindow(at, "ft8", 14074000, 100)
	if w.Start.Second()%15 != 0 || w.End.Sub(w.Start) != 15*time.Second {
		t.Fatalf("%v %v", w.Start, w.End)
	}
	w.Append(w.Start, make([]float64, 700))
	if w.Complete() {
		t.Fatal("partial complete")
	}
	w.Append(w.Start.Add(7*time.Second), make([]float64, 800))
	if !w.Complete() {
		t.Fatal("aligned interval incomplete")
	}
	ws := NewHFWindow(at, "wspr", 7038600, 100)
	if ws.Start.Second() != 0 || ws.Start.Minute()%2 != 0 || ws.End.Sub(ws.Start) != 2*time.Minute {
		t.Fatalf("%v %v", ws.Start, ws.End)
	}
}
