package vocoder

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// fixtures live in the Python package's tests/data directory.
func fixture(t *testing.T, name string) []byte {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "data", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// readRef decodes a reference file of little-endian int16 samples.
func readRef(t *testing.T, name string) []int16 {
	data := fixture(t, name)
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return out
}

func equalSamples(a, b []int16) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		if a[i] != b[i] {
			return i, false
		}
	}
	return -1, true
}

// TestSingleFrames decodes each captured frame on its own and compares against
// the reference PCM the Python implementation produced from the same bytes.
func TestSingleFrames(t *testing.T) {
	for _, name := range []string{"websdr_0000", "websdr_0004", "websdr_0020"} {
		t.Run(name, func(t *testing.T) {
			v := New()
			got := v.Decode(fixture(t, name+".bin"))
			want := readRef(t, name+"_ref.bin")
			if index, ok := equalSamples(got, want); !ok {
				if index < 0 {
					t.Fatalf("length mismatch: got %d want %d", len(got), len(want))
				}
				t.Fatalf("sample %d differs: got %d want %d", index, got[index], want[index])
			}
		})
	}
}

// TestStream checks that decoder state carries across messages, which is what
// the LPC filter and AGC depend on.
func TestStream(t *testing.T) {
	cases := []struct {
		frames []string
		ref    string
	}{
		{[]string{"websdr_0000.bin", "websdr_0020.bin"}, "websdr_stream_a_ref.bin"},
		{[]string{"websdr_0000.bin", "websdr_0004.bin", "websdr_0020.bin"}, "websdr_stream_b_ref.bin"},
	}
	for _, tc := range cases {
		v := New()
		var got []int16
		for _, name := range tc.frames {
			got = append(got, v.Decode(fixture(t, name))...)
		}
		want := readRef(t, tc.ref)
		if index, ok := equalSamples(got, want); !ok {
			if index < 0 {
				t.Fatalf("%s: length mismatch: got %d want %d", tc.ref, len(got), len(want))
			}
			t.Fatalf("%s: sample %d differs: got %d want %d", tc.ref, index, got[index], want[index])
		}
	}
}
