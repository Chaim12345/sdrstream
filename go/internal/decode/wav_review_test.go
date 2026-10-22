package decode

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeReviewWAV builds a 16-bit PCM WAV whose data chunk declares
// declaredSize bytes but whose file is truncated to actually hold only the
// given samples.
func writeReviewWAV(t *testing.T, rate, channels int, samples []int16, declaredSize uint32) string {
	t.Helper()
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1)
	binary.LittleEndian.PutUint16(hdr[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(rate))
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], declaredSize)
	var body []byte
	for _, v := range samples {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		body = append(body, b[:]...)
	}
	path := filepath.Join(t.TempDir(), "probe.wav")
	if err := os.WriteFile(path, append(hdr, body...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadWAVTruncatedData: the data chunk claims more bytes than the file
// holds. Decode the samples actually present instead of panicking on a
// slice bounds check.
func TestReadWAVTruncatedData(t *testing.T) {
	samples := make([]int16, 50)
	for i := range samples {
		samples[i] = int16(i * 100)
	}
	path := writeReviewWAV(t, 8000, 1, samples, 4000) // declares 2000 samples, holds 50
	got, rate, err := ReadWAV(path)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if rate != 8000 {
		t.Errorf("rate = %d, want 8000", rate)
	}
	if len(got) != len(samples) {
		t.Fatalf("got %d samples, want the %d that survived truncation", len(got), len(samples))
	}
	for i, v := range samples {
		want := float64(v) / 32768.0
		if got[i] != want {
			t.Fatalf("sample %d = %v, want %v", i, got[i], want)
		}
	}
}

// TestReadWAVChannelsTruncatedData: same truncation through the
// multichannel reader, including a trailing partial frame.
func TestReadWAVChannelsTruncatedData(t *testing.T) {
	samples := make([]int16, 51) // 25 full stereo frames + 1 dangling sample
	path := writeReviewWAV(t, 8000, 2, samples, 1<<20)
	ch, _, err := ReadWAVChannels(path)
	if err != nil {
		t.Fatalf("ReadWAVChannels: %v", err)
	}
	if len(ch) != 2 {
		t.Fatalf("got %d channels, want 2", len(ch))
	}
	if len(ch[0]) != 25 || len(ch[1]) != 25 {
		t.Errorf("channel lengths %d/%d, want 25/25", len(ch[0]), len(ch[1]))
	}
}

// TestReadWAVRejectsBadRate: a zero sample rate would divide-by-zero or
// mis-size every downstream resampler; reject it at load time.
func TestReadWAVRejectsBadRate(t *testing.T) {
	path := writeReviewWAV(t, 0, 1, []int16{1, 2, 3, 4}, 8)
	if _, _, err := ReadWAV(path); err == nil {
		t.Error("ReadWAV accepted sample rate 0")
	}
	if _, _, err := ReadWAVChannels(path); err == nil {
		t.Error("ReadWAVChannels accepted sample rate 0")
	}
}

// TestResampleGuards: a non-positive source rate must not produce a
// negative or unbounded output length.
func TestResampleGuards(t *testing.T) {
	x := []float64{0.1, 0.2, 0.3, 0.4}
	if got := resampleLinear(x, 0, 12000); len(got) != len(x) {
		t.Errorf("resampleLinear(from=0): %d samples, want input length %d", len(got), len(x))
	}
	if got := resampleLinear(x, -8000, 12000); len(got) != len(x) {
		t.Errorf("resampleLinear(from<0): %d samples, want input length %d", len(got), len(x))
	}
	if got := resampleTo12k(x, 0); len(got) != len(x) {
		t.Errorf("resampleTo12k(rate=0): %d samples, want input length %d", len(got), len(x))
	}
	if got := resampleTo12k(x, -1); len(got) != len(x) {
		t.Errorf("resampleTo12k(rate<0): %d samples, want input length %d", len(got), len(x))
	}
}
