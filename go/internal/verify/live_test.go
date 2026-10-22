package verify

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestLiveRejectsNoSignalGibberishAndFixture(t *testing.T) {
	cases := []struct{ kind, mode, detail string }{
		{"noise", "usb", "no signal above the noise floor"},
		{"data", "rtty", "Y EDEUI JYWREEZNGTCG LAQAATNILMGOKW"},
		{"data", "aprs", "fixture: M0XXX>APZSAT-0:>test"},
	}
	for _, a := range cases {
		if err := Live(a.kind, a.mode, a.detail, nil); err == nil {
			t.Errorf("accepted %+v", a)
		}
	}
}
func TestLiveAcceptsStructuredAX25AndReadableSpeech(t *testing.T) {
	if err := Live("data", "aprs", "M0XXX-9>APZSAT-0:>Hello from space!", nil); err != nil {
		t.Fatal(err)
	}
	if err := Live("voice", "speech", "Today the weather will be sunny across the region with clear skies and warm temperatures.", nil); err != nil {
		t.Fatal(err)
	}
}
func TestLiveRequiresSSTVImageGeometry(t *testing.T) {
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 320, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 320; x++ {
			if (x+y)%2 == 0 {
				img.Set(x, y, color.White)
			}
		}
	}
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := Live("sstv", "sstv", "", b.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := Live("", "sstv", "", []byte("not png")); err == nil {
		t.Fatal("invalid image accepted")
	}
}

func TestTextGates(t *testing.T) {
	real := "CQ CQ CQ DE DDK2 DDH7 DDK9\r\nFREQUENCIES   4583 KHZ   7646 KHZ   10100.8 KHZ\r\n"
	if r := Text("rtty", real, "DDK2"); !r.Live {
		t.Errorf("real DWD text rejected: %s", r.Reason)
	}
	garbage := "XUAWYQP XIV NSS ;(66'36/?68\"3'6?1'''666''\"666''!0'/6?/#:"
	if r := Text("rtty", garbage, "DDK2"); r.Live {
		t.Error("garbage passed the gate")
	}
	noise := "ET ET E I T EE TI EI TIE"
	if r := Text("rtty", noise, ""); r.Live {
		t.Error("E/T noise passed the gate")
	}
	navtex := "ZCZC FA01\nNAVAREA ONE WARNING 123\nGALE WARNING NORTH SEA\nNNNN"
	if r := Text("navtex", navtex, ""); !r.Live {
		t.Errorf("navtex text rejected: %s", r.Reason)
	}
}

func TestCWGate(t *testing.T) {
	if r := CW("VVV = OK0EU/B = OK0EU/B", "OK0EU"); !r.Live {
		t.Errorf("real beacon rejected: %s", r.Reason)
	}
	if r := CW("E T I E A N", "OK0EU"); r.Live {
		t.Error("morse noise passed")
	}
}

func pngBytes(fill func(x, y int) uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, 320, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 320; x++ {
			img.SetGray(x, y, color.Gray{fill(x, y)})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestImageGate(t *testing.T) {
	chart := pngBytes(func(x, y int) uint8 { return uint8((x*7 + y*13) % 256) })
	if r := Image("wefax", chart); !r.Live {
		t.Errorf("content image rejected: %s", r.Reason)
	}
	blank := pngBytes(func(x, y int) uint8 { return 200 })
	if r := Image("wefax", blank); r.Live {
		t.Error("blank image passed")
	}
}

// TestTextMarginalCapture: a heavily faded RTTY capture that happens to
// contain one real fragment must not pass without the anchor.
func TestTextMarginalCapture(t *testing.T) {
	marginal := "AYTYRHCYRHRY CQ VC VUTDO RRQOPDX GADFTRO VK QENVIEXAAFRT ENNRGUXRP VHE S 101;&.8 KHZ KRK URCYRHRS ZRYJ HRGHNXRZJYRYCFGYCH"
	if r := Text("rtty", marginal, "DDH7"); r.Live {
		t.Errorf("marginal capture passed: %s", r.Reason)
	}
}

func TestMaritimeWordHitsDoesNotLoosenGate(t *testing.T) {
	if n := MaritimeWordHits("GALE WARNING NORTH SEA"); n < 3 {
		t.Errorf("hits = %d", n)
	}
	good := "ZCZC FA01 GALE WARNING NORTH SEA NNNN"
	if r := Text("navtex", good, ""); !r.Live {
		t.Errorf("framed bulletin with two distinct maritime words rejected: %s", r.Reason)
	}
	if r := Text("navtex", "ZCZC FA01 THIS IS A TEST OF THE EMERGENCY BROADCAST SYSTEM NNNN", ""); r.Live {
		t.Error("non-maritime framed text passed the NAVTEX gate")
	}
	if r := Text("noise", good, ""); r.Live {
		t.Error("NAVTEX-looking noise passed under a different decoder kind")
	}
	if r := Text("navtex", "GALE WARNING NORTH SEA STORM WIND", ""); r.Live {
		t.Error("unframed maritime-looking noise passed the NAVTEX gate")
	}
}

func TestFTXGate(t *testing.T) {
	if r := FTX(0, true, ""); r.Live {
		t.Fatal("empty must not pass")
	}
	if r := FTX(2, false, "K1ABC W9XYZ -11"); r.Live {
		t.Fatal("FT4 must not publish")
	}
	if r := FTX(1, true, "000000 FT8 +0.3 1000 Hz CQ K1ABC FN42"); !r.Live {
		t.Fatal(r.Reason)
	}
	if r := FTX(1, true, "000000 FT8 +0.3 1000 Hz hello"); r.Live {
		t.Fatal("no callsign")
	}
}

func TestWSPRGate(t *testing.T) {
	if r := WSPR(1, "0532 WSPR -25 F4BAD JN25 20"); r.Live {
		t.Fatal("single spot must not publish")
	}
	if r := WSPR(2, "F4BAD JN25 20\nE79Q JN83 23"); !r.Live {
		t.Fatal(r.Reason)
	}
}

func TestTextAcceptsAccentedNavtexAndSpacedAnchors(t *testing.T) {
	// Accented real traffic used to fail the printable/bytes ratio (the
	// gate mixed rune counts with byte counts).
	result := Text("navtex", "ZCZC GA57 BARCELONA METEOROLÓGICO ÁREA GALE WARNING FORECAST NNNN", "")
	if !result.Live {
		t.Fatalf("accented navtex rejected: %+v", result)
	}
	// Anchors must survive decoder spacing quirks.
	result = Text("voice", "VOZ  DE  GIJON meteorológico routine forecast valid", "VOZ DE GIJON")
	if !result.Live {
		t.Fatalf("spaced anchor rejected: %+v", result)
	}
	if result := Text("rtty", "SHIP SHORE BROADCAST BUT NO ANCHOR WORD MATCHES", "ELBE 1"); result.Live {
		t.Fatalf("missing anchor accepted: %+v", result)
	}
}
