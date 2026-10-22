package decode

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os/exec"
	"strings"
	"testing"
)

func sameCall(header string, rate int, noise float64, rng *rand.Rand) []float64 {
	var s []float64
	silence := func(sec float64) {
		for i := 0; i < int(sec*float64(rate)); i++ {
			s = append(s, noise*rng.NormFloat64())
		}
	}
	silence(2.3)
	for i := 0; i < 3; i++ {
		for _, v := range SAMEModulate(header, rate) {
			s = append(s, v+noise*rng.NormFloat64())
		}
		silence(1)
	}
	silence(4)
	return s
}

const testHeader = "ZCZC-WXR-TOR-039173-039051+0030-2951830-KCLE/NWS-"

func TestSAMEScanSynthetic(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, rate := range []int{16000, 22050, 44100} {
		got := SAMEScan(sameCall(testHeader, rate, 0.02, rng), rate)
		if len(got) != 1 {
			t.Fatalf("rate %d: got %d alerts: %+v", rate, len(got), got)
		}
		a := got[0]
		if a.Raw != testHeader || a.Event != "TOR" || a.EventName != "Tornado Warning" || a.DurationMin != 30 {
			t.Errorf("rate %d: bad decode %+v", rate, a)
		}
		if len(a.Areas) != 2 || !strings.Contains(a.Areas[0], "OH") {
			t.Errorf("areas = %v", a.Areas)
		}
		if a.Issued != "day 295 18:30 UTC" || a.OriginName != "National Weather Service" {
			t.Errorf("issued/origin = %q / %q", a.Issued, a.OriginName)
		}
		if a.Repeats < 1 {
			t.Errorf("repeats = %d", a.Repeats)
		}
	}
}

func TestSAMEScanRejectsNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	s := make([]float64, 16000*20)
	for i := range s {
		s[i] = 0.3 * rng.NormFloat64()
	}
	if got := SAMEScan(s, 16000); len(got) != 0 {
		t.Fatalf("noise decoded as SAME: %+v", got)
	}
}

func TestVoteSAMERepairs(t *testing.T) {
	// One bit-error in each of three repeats, at different positions.
	a := []byte(testHeader)
	b := []byte(testHeader)
	c := []byte(testHeader)
	a[10] = 'X'
	b[20] = '9'
	c[30] = 'Q'
	got := VoteSAME([]string{string(a), string(b), string(c)})
	if len(got) != 1 || got[0].Raw != testHeader {
		t.Fatalf("vote failed: %+v", got)
	}
}

func TestValidSAME(t *testing.T) {
	good := []string{testHeader, "ZCZC-CIV-RWT-012345-012346-012347+0015-1231200-WXYZ/FM -"}
	for _, g := range good {
		if !ValidSAME(g) {
			t.Errorf("should be valid: %q", g)
		}
	}
	bad := []string{"ZCZC-WXR-TOR-03917+0030-2951830-KCLE/NWS-", "ZCZC-WXR-TOR-039173+030-2951830-KCLE/NWS-", "hello", testHeader[:len(testHeader)-1]}
	for _, g := range bad {
		if ValidSAME(g) {
			t.Errorf("should be invalid: %q", g)
		}
	}
}

// TestSAMESurvivesMP3 sends a synthetic alert through a real 32 kbps 22.05 kHz
// MP3 encode and decode, which is what NWR relays do to it, and expects the
// header to survive. Skipped without ffmpeg.
func TestSAMESurvivesMP3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	rng := rand.New(rand.NewSource(3))
	const rate = 22050
	pcm := sameCall(testHeader, rate, 0.01, rng)
	raw := make([]byte, 2*len(pcm))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(raw[2*i:], uint16(int16(v*32000)))
	}
	enc := exec.Command("ffmpeg", "-loglevel", "error", "-f", "s16le", "-ar", "22050", "-ac", "1", "-i", "pipe:0", "-b:a", "32k", "-f", "mp3", "pipe:1")
	enc.Stdin = bytes.NewReader(raw)
	mp3, err := enc.Output()
	if err != nil {
		t.Skipf("encode: %v", err)
	}
	dec := exec.Command("ffmpeg", "-loglevel", "error", "-i", "pipe:0", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	dec.Stdin = bytes.NewReader(mp3)
	out, err := dec.Output()
	if err != nil {
		t.Fatal(err)
	}
	back := make([]float64, len(out)/2)
	for i := range back {
		back[i] = float64(int16(binary.LittleEndian.Uint16(out[2*i:]))) / 32768
	}
	got := SAMEScan(back, 16000)
	if len(got) != 1 || got[0].Raw != testHeader {
		t.Fatalf("header lost through MP3: %+v", got)
	}
}
