// Package adpcm decodes the 4-bit IMA ADPCM used by OpenWebRX audio and FFT
// streams.
//
// Audio payloads are wrapped in SYNC markers that periodically carry the
// decoder state, so a stream can be joined mid-flight and resynchronise. FFT
// payloads are raw ADPCM with no markers.
//
// The implementation mirrors AdpcmDecoder from the openwebrx-js client so the
// output is bit-identical to the reference. In particular the step size used
// for a nibble is the one after the index update, and the index update is
// applied before clamping.
package adpcm

var indexTable = [16]int{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

var stepTable = [89]int{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
	253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
	1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
	3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
	11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
	32767,
}

// syncMarker introduces a decoder-state header in the audio stream.
var syncMarker = [4]byte{'S', 'Y', 'N', 'C'}

// syncInterval is the number of ADPCM bytes decoded between two state headers.
const syncInterval = 1000

// Decoder is a streaming IMA ADPCM decoder. State is retained between calls,
// so payloads may be fed in arbitrary chunks.
type Decoder struct {
	stepIndex int
	predictor int
	step      int

	synchronized    int
	phase           int
	syncBuffer      [4]byte
	syncBufferIndex int
	syncCounter     int
}

// New returns a decoder in its initial state.
func New() *Decoder { return &Decoder{} }

// Reset returns the decoder to its initial state.
func (d *Decoder) Reset() { *d = Decoder{} }

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// decodeNibble decodes a single 4-bit sample and returns the new predictor.
func (d *Decoder) decodeNibble(nibble int) int {
	d.stepIndex += indexTable[nibble]
	d.stepIndex = clamp(d.stepIndex, 0, 88)

	diff := d.step >> 3
	if nibble&1 != 0 {
		diff += d.step >> 2
	}
	if nibble&2 != 0 {
		diff += d.step >> 1
	}
	if nibble&4 != 0 {
		diff += d.step
	}
	if nibble&8 != 0 {
		diff = -diff
	}

	d.predictor += diff
	d.predictor = clamp(d.predictor, -32768, 32767)

	d.step = stepTable[d.stepIndex]
	return d.predictor
}

// Decode decodes raw ADPCM, two samples per byte, ignoring sync markers.
func (d *Decoder) Decode(data []byte) []int16 {
	out := make([]int16, 0, len(data)*2)
	for _, b := range data {
		out = append(out, int16(d.decodeNibble(int(b&0x0F))))
		out = append(out, int16(d.decodeNibble(int((b>>4)&0x0F))))
	}
	return out
}

// DecodeWithSync decodes an audio payload that may contain SYNC state headers.
func (d *Decoder) DecodeWithSync(data []byte) []int16 {
	out := make([]int16, 0, len(data)*2)
	for _, b := range data {
		switch d.phase {
		case 0:
			if b != syncMarker[d.synchronized] {
				d.synchronized = 0
			}
			if b == syncMarker[d.synchronized] {
				d.synchronized++
			}
			if d.synchronized == len(syncMarker) {
				d.syncBufferIndex = 0
				d.phase = 1
			}
		case 1:
			d.syncBuffer[d.syncBufferIndex] = b
			d.syncBufferIndex++
			if d.syncBufferIndex == 4 {
				d.stepIndex = int(int16(uint16(d.syncBuffer[0]) | uint16(d.syncBuffer[1])<<8))
				d.predictor = int(int16(uint16(d.syncBuffer[2]) | uint16(d.syncBuffer[3])<<8))
				d.syncCounter = syncInterval
				d.phase = 2
			}
		default:
			out = append(out, int16(d.decodeNibble(int(b&0x0F))))
			out = append(out, int16(d.decodeNibble(int((b>>4)&0x0F))))
			d.syncCounter--
			if d.syncCounter <= 0 {
				d.synchronized = 0
				d.phase = 0
			}
		}
	}
	return out
}
