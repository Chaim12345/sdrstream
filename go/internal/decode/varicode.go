package decode

// varicode maps each ASCII byte to its PSK31 Varicode bit pattern, MSB
// first. Varicode is a self-synchronising code: characters never contain
// two consecutive zero bits, and a single "00" separates one character
// from the next. Frequent English letters therefore get the shortest
// patterns ("e" is just "11", space is "1").
var varicode = [128]string{
	"1010101011", "1011011011", "1011101101", "1101110111",
	"1011101011", "1101011111", "1011101111", "1011111101",
	"1011111111", "11101111", "11101", "1101101111",
	"1011011101", "11111", "1101110101", "1110101011",
	"1011110111", "1011110101", "1110101101", "1110101111",
	"1101011011", "1101101011", "1101101101", "1101010111",
	"1101111011", "1101111101", "1110110111", "1101010101",
	"1101011101", "1110111011", "1011111011", "1101111111",
	"1", "111111111", "101011111", "111110101",
	"111011011", "1011010101", "1010111011", "101111111",
	"11111011", "11110111", "101101111", "111011111",
	"1110101", "110101", "1010111", "110101111",
	"10110111", "10111101", "11101101", "11111111",
	"101110111", "101011011", "101101011", "110101101",
	"110101011", "110110111", "11110101", "110111101",
	"111101101", "1010101", "111010111", "1010101111",
	"1010111101", "1111101", "11101011", "10101101",
	"10110101", "1110111", "11011011", "11111101",
	"101010101", "1111111", "111111101", "101111101",
	"11010111", "10111011", "11011101", "10101011",
	"11010101", "111011101", "10101111", "1101111",
	"1101101", "101010111", "110110101", "101011101",
	"101110101", "101111011", "1010101101", "111110111",
	"111101111", "111111011", "1010111111", "101101101",
	"1011011111", "1011", "1011111", "101111",
	"101101", "11", "111101", "1011011",
	"101011", "1101", "111101011", "10111111",
	"11011", "111011", "1111", "111",
	"111111", "110111111", "10101", "10111",
	"101", "110111", "1111011", "1101011",
	"11011111", "1011101", "111010101", "1010110111",
	"110111011", "1010110101", "1011010111", "1110110101",
}

// varicodeDecode is the reverse lookup, built once from varicode.
var varicodeDecode = func() map[string]byte {
	m := make(map[string]byte, len(varicode))
	for ch, code := range varicode {
		m[code] = byte(ch)
	}
	return m
}()

// decodeVaricode turns a PSK31 bit string (one bit per byte, MSB first)
// into text. A run of two or more zero bits separates characters; the bits
// between separators form one character code. Single zero bits are part of
// a character and must be kept.
// decodeVaricodeStrict decodes a varicode stream and also reports how many
// segments were not valid varicode codes. Every character of a real PSK31
// signal comes from the table, so a stream with invalid segments is noise
// that happened to look like text.
func decodeVaricodeStrict(bits string) (string, int, int) {
	var out []byte
	invalid, idles := 0, 0
	flush := func(seg string) {
		if seg == "" {
			return
		}
		if ch, ok := varicodeDecode[seg]; ok {
			out = append(out, ch)
		} else {
			invalid++
		}
	}
	i, start := 0, 0
	for i < len(bits) {
		if bits[i] == '0' && i+1 < len(bits) && bits[i+1] == '0' {
			flush(bits[start:i])
			for i < len(bits) && bits[i] == '0' {
				i++
			}
			start = i
			idles++
			continue
		}
		// PSK31 separates characters with an idle pattern: alternating
		// bits (0x55 twice, repeated while the transmitter waits) or a
		// run of zeros. The idle never appears inside a character, but a
		// varicode code always ends in a one and the idle starts with a
		// zero, so the run reaches one bit into the character that
		// precedes it. Try every split of the run and keep the one that
		// leaves a valid code in front of the idle.
		if run := idleRun(bits, i); run >= 12 {
			split := -1
			for k := 0; k+12 <= run; k++ {
				segment := bits[start : i+k]
				if segment == "" {
					continue
				}
				if _, ok := varicodeDecode[segment]; ok {
					split = k
					break
				}
			}
			if split < 0 {
				flush(bits[start:i])
				i += run
			} else {
				flush(bits[start : i+split])
				i += run - split
			}
			idles++
			start = i
			continue
		}
		i++
	}
	flush(bits[start:])
	return string(out), invalid, idles
}

// idleLength is the length of the PSK31 idle pattern: two 0x55 bytes.
const idleLength = 16

// idleRun counts the alternating bits starting at position i (the bit
// before i is used as the reference).
func idleRun(bits string, i int) int {
	run := 0
	for j := i; j < len(bits); j++ {
		if j > 0 && bits[j] == bits[j-1] {
			break
		}
		run++
	}
	return run
}

func decodeVaricode(bits string) string {
	var out []byte
	flush := func(seg string) {
		if seg == "" {
			return
		}
		if ch, ok := varicodeDecode[seg]; ok {
			out = append(out, ch)
		}
	}
	i, start := 0, 0
	for i < len(bits) {
		if bits[i] == '0' && i+1 < len(bits) && bits[i+1] == '0' {
			flush(bits[start:i])
			for i < len(bits) && bits[i] == '0' {
				i++
			}
			start = i
			continue
		}
		i++
	}
	flush(bits[start:])
	return string(out)
}
