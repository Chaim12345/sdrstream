package verify

import (
	"strings"
	"testing"
)

// Multilingual gate behavior: the rune-count fix must accept real
// accented and non-Latin service text at the SAME quality bar, and must
// keep rejecting short, corrupt, or gibberish output. These tests pin the
// expected behavior; none of the thresholds were relaxed to make them
// pass.
func TestTextMultilingualGroundTruth(t *testing.T) {
	accept := []struct {
		name, kind, text, anchor string
	}{
		{"spanish-accented-navtex-anchor", "navtex", "ZCZC GA57 A CORUÑA AVISO NUMERO 12 GALE WARNING VIENTO DEL SUROESTE NNNN", "A CORUÑA"},
		{"spanish-accented-rtty-anchor", "rtty", "VOZ DE GIJÓN METEOROLÓGICO AVISO DE GALERNA VÁLIDO DESDE LA MEDIANOCHE", "VOZ DE GIJÓN"},
		{"german-umlaut-speech", "speech", "Wetterwarnung für die Küste: Sturmböen und schwere Böen aus Südwest während der Nacht", ""},
		{"french-accent-speech", "speech", "Bulletin météorologique: vents forts près des côtes ce soir, visibilité réduite", ""},
		{"portuguese-speech", "speech", "Aviso de tempestade para a costa norte durante a madrugada com visibilidade reduzida", ""},
		{"cyrillic-speech", "speech", "Прогноз погоды для прибрежных районов на ближайшие сутки, усиление ветра ночью", ""},
		// Spoken-number VOLMET, as ASR actually returns it; raw METAR
		// teletype is digit-dominant data and the speech floor rejects it
		// by design (45% letter minimum), which the negatives pin below.
		{"spoken-volmet-speech", "speech", "Shannon airport one two hundred Zulu wind three four zero degrees one two knots visibility one zero kilometres temperature one eight dew point zero niner", ""},
	}
	for _, tc := range accept {
		var ok bool
		if tc.kind == "speech" {
			ok = readableText(tc.text, 32, 0.68)
			if err := Live("voice", "speech", tc.text, nil); err != nil {
				ok = false
			}
		} else {
			ok = Text(tc.kind, tc.text, tc.anchor).Live
		}
		if !ok {
			t.Errorf("%s: real multilingual text rejected", tc.name)
		}
	}
}

func TestTextRejectsCorruptAndGibberish(t *testing.T) {
	reject := []struct {
		name, kind, text, anchor string
	}{
		{"invalid-utf8-rtty", "rtty", string([]byte{0xff, 0xfe, 0x80, 0x81, 0xff, 0x90, 0xa0, 0xff, 0xfe, 0x80, 0x81, 0xff, 0x90, 0xa0, 0xff, 0xfe, 0x80, 0x81, 0xff, 0x90, 0xa0}), "DDH7"},
		{"mostly-replacement-speech", "speech", strings.Repeat("� ", 30) + "the weather", ""},
		{"short-cyrillic-speech", "speech", "Гроза", ""},
		{"combining-mark-spam-speech", "speech", "a̷̢̛͎̝̦̓̈́̾̿e̷̢̛͎̝̦̓̈́̾̿i̷̢̛͎̝̦̓̈́̾̿ơ̷̢͎̝̦̓̈́̾̿ư̷̢͎̝̦̓̈́̾̿ a̷ei̷ou̷", ""},
		{"raw-metar-as-speech", "speech", "METAR LEZG 101200Z 34012KT 9999 SCT040 18/09 Q1018 NOSIG", ""},
	}
	for _, tc := range reject {
		if tc.kind == "speech" {
			if readableText(tc.text, 32, 0.68) {
				t.Errorf("%s: corrupt text passed the speech readability floor", tc.name)
			}
			if err := Live("voice", "speech", tc.text, nil); err == nil {
				t.Errorf("%s: corrupt text passed the live speech gate", tc.name)
			}
		} else {
			if r := Text(tc.kind, tc.text, tc.anchor); r.Live {
				t.Errorf("%s: corrupt text passed: %s", tc.name, r.Reason)
			}
		}
	}
}

// A degraded but mostly intact transcript keeps passing: the gate is a
// readability floor, not an error-correction verdict. This pins current
// behavior deliberately - tightening it would need owner grounding.
func TestTextToleratesPartialCorruption(t *testing.T) {
	text := "Meteorología: aviso de galerna en la costa " + strings.Repeat("�", 8) + " durante la noche"
	if !readableText(text, 32, 0.68) {
		t.Fatal("mostly-intact accented transcript with a corrupt burst rejected")
	}
}

// AX.25 addresses are ASCII by protocol: accented or non-Latin
// "callsigns" cannot come from a real frame and must not validate.
func TestAX25RejectsNonASCIIAddress(t *testing.T) {
	if err := Live("data", "aprs", "M0XÓ-9>APZSAT-0:>accented address", nil); err == nil {
		t.Fatal("non-ASCII AX.25 address accepted")
	}
	if err := Live("data", "aprs", "M0XXX-9>APZSAT-0:>ASCII address stays fine", nil); err != nil {
		t.Fatalf("legitimate ASCII frame rejected: %v", err)
	}
}
