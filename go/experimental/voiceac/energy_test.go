package voiceac

import (
	"math"
	"math/rand"
	"testing"
)

func fixture(kind string, dc float64) []float64 {
	x := make([]float64, 8000*4)
	rng := rand.New(rand.NewSource(7253))
	for i := range x {
		t := float64(i) / 8000
		v := 0.
		switch kind {
		case "noise":
			v = .002 * (rng.Float64()*2 - 1)
		case "carrier":
			v = .04 * math.Sin(2*math.Pi*500*t)
		case "modulated-carrier":
			v = (.005 + .08*math.Pow(math.Sin(2*math.Pi*1.7*t), 8)) * math.Sin(2*math.Pi*500*t)
		case "activity-envelope":
			if t > 1 && t < 2 {
				v = .12 * math.Sin(2*math.Pi*321*t)
			} else {
				v = .001 * (rng.Float64()*2 - 1)
			}
		}
		x[i] = v + dc
	}
	return x
}
func TestDeveloperConstantOffsetInvariance(t *testing.T) {
	for _, k := range []string{"constant", "noise", "carrier", "modulated-carrier", "activity-envelope"} {
		a, e := Analyze(fixture(k, 0), 8000)
		if e != nil {
			t.Fatal(e)
		}
		b, e := Analyze(fixture(k, .2), 8000)
		if e != nil {
			t.Fatal(e)
		}
		for i := range a.Frames {
			if math.Abs(a.Frames[i].ACRMS-b.Frames[i].ACRMS) > 1e-14 {
				t.Fatalf("%s noninvariant", k)
			}
		}
		if len(a.Segments) != len(b.Segments) {
			t.Fatal("offset changed activity")
		}
		for i := range a.Segments {
			if a.Segments[i] != b.Segments[i] {
				t.Fatal("offset changed boundaries")
			}
		}
	}
}
func TestDeveloperActivityControlsNotSpeechProof(t *testing.T) {
	for _, k := range []string{"constant", "noise", "carrier"} {
		r, e := Analyze(fixture(k, .2), 8000)
		if e != nil {
			t.Fatal(e)
		}
		if len(r.Segments) > 0 {
			t.Fatalf("steady %s marked activity", k)
		}
	}
	for _, k := range []string{"activity-envelope", "modulated-carrier"} {
		r, e := Analyze(fixture(k, .2), 8000)
		if e != nil {
			t.Fatal(e)
		}
		if len(r.Segments) == 0 {
			t.Fatal("missing developer activity")
		}
		t.Logf("%s coverage%g: modulation is not speech evidence", k, r.Coverage)
	}
}
func TestDeveloperFiniteAndNativeTiming(t *testing.T) {
	x := make([]float64, 11999)
	for i := 3000; i < 7000; i++ {
		x[i] = .1 * math.Sin(float64(i))
	}
	r, e := Analyze(x, 11999)
	if e != nil {
		t.Fatal(e)
	}
	if r.FrameSamples != 239 {
		t.Fatal("frame")
	}
	for i, f := range r.Frames {
		if f.Lo != i*239 || f.Hi != (i+1)*239 {
			t.Fatal("native support")
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.MaxFloat64} {
		x[0] = v
		if _, e := Analyze(x, 11999); e == nil {
			t.Fatal("nonfinite frame accepted")
		}
	}
}
