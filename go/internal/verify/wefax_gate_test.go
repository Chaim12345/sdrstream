package verify

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

// wefaxTestPNG renders a 600x400 mid-gray PNG with enough variance to
// pass the structural Image checks.
func wefaxTestPNG(t *testing.T, flat bool) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 600, 400))
	for i := range img.Pix {
		if flat {
			img.Pix[i] = 128
		} else {
			img.Pix[i] = uint8((i/600 + i%37) % 256)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWEFAXImageGate(t *testing.T) {
	good := wefaxTestPNG(t, false)
	cases := []struct {
		name                           string
		data                           []byte
		sync                           string
		wedge, drift, perScore, deskew float64
		wantLive                       bool
	}{
		{"valid phasing wedge", good, "phasing-wedge", 0.95, -163, 0, 0.27, true},
		{"valid phasing wedge large drift fixture", good, "phasing-wedge", 0.95, 657, 0, 0.28, true},
		{"false wedge contrast", good, "phasing-wedge", 0.28, 2981, 0, 0.28, false},
		{"low wedge contrast inside drift bound", good, "phasing-wedge", 0.30, 1184, 0, 0.98, false},
		{"good mid-chart line correlation", good, "line-correlation", 0.26, 667, 0.37, 0.38, true},
		{"biased mid-chart fit", good, "line-correlation", 0.28, 2596, 0.31, 0.29, false},
		{"weak period score inside drift bound", good, "line-correlation", 0.28, 900, 0.31, 0.40, false},
		{"unknown sync", good, "", 0.9, 100, 0, 0.5, false},
		{"flat PNG rejected structurally", wefaxTestPNG(t, true), "phasing-wedge", 0.95, 0, 0, 0.5, false},
		{"garbage rejected structurally", []byte("not a png"), "phasing-wedge", 0.95, 0, 0, 0.5, false},
	}
	for _, tc := range cases {
		r := WEFAXImage("wefax", tc.data, tc.sync, tc.wedge, tc.drift, tc.perScore, tc.deskew)
		if r.Live != tc.wantLive {
			t.Errorf("%s: live=%v want %v (reason %q)", tc.name, r.Live, tc.wantLive, r.Reason)
		}
		if r.Live && tc.name == "valid phasing wedge" {
			if got := r.Reason; got == "image ok" {
				t.Error("verified reason must name the sync evidence, not the generic PNG wording")
			}
		}
	}
}
