package decode

import (
	"bytes"
	"image/png"
	"math"
	"testing"
)

// A lack of FSK range is not evidence that these legitimate final rows
// are outside the image. Test all four tails identified by independent QA.
func TestWEFAXLowModulationTailPreserved(t *testing.T) {
	for _, rate := range []int{8000, 7119} {
		for _, kind := range []string{"white", "black", "grey", "gradient"} {
			t.Run(kind, func(t *testing.T) {

				a := synthWEFAXExactLines(float64(rate), 60, 80, 3)
				phase := 0.0
				start := 68 * rate // last ten of 140 lines, five seconds
				for i := start; i < len(a); i++ {
					grey := 0.4
					if ((i-start)/200)%2 == 0 {
						grey = .6
					}
					switch kind {
					case "white":
						grey = 1
					case "black":
						grey = 0
					case "gradient":
						grey = .3 + .4*math.Mod(float64(i-start), .5*float64(rate))/(.5*float64(rate))
					}
					phase += 2 * math.Pi * (1500 + 800*grey) / float64(rate)
					a[i] = .6 * math.Sin(phase)
				}
				a = append(a, make([]float64, 8*rate)...)
				data, rep, e := WEFAXImageDetailed(a, rate, 120, 576)
				if e != nil {
					t.Fatal(e)
				}
				im, e := png.Decode(bytes.NewReader(data))
				if e != nil {
					t.Fatal(e)
				}
				if im.Bounds().Dy() < 140 {
					t.Fatalf("%s tail: height%d loses legitimate rows, need >=140; report%+v", kind, im.Bounds().Dy(), rep)
				}
			})
		}
	}
}
func TestWEFAXTruncateNilRaw(t *testing.T) {
	tr := &wefaxTrace{ratio: make([]float64, 10), pb: make([]float64, 10), pw: make([]float64, 10)}
	tr.truncate(5)
	if len(tr.ratio) != 5 || tr.raw != nil {
		t.Fatal("truncate changed optional raw convention")
	}
}
