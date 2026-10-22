package decode

import "math"

// SAMEModulate synthesises one SAME burst (sixteen 0xAB preamble bytes then
// msg) for tests and fixtures:
// 8N1 LSB first, phase-continuous AFSK at amplitude 0.5.
func SAMEModulate(msg string, rate int) []float64 {
	data := append(make([]byte, 0, 16+len(msg)), []byte{}...)
	for i := 0; i < 16; i++ {
		data = append(data, 0xAB)
	}
	data = append(data, []byte(msg)...)
	spb := float64(rate) / easBaud
	var out []float64
	phase := 0.0
	n := 0
	for _, b := range data {
		bits := []int{0}
		for d := 0; d < 8; d++ {
			bits = append(bits, int(b>>d)&1)
		}
		bits = append(bits, 1)
		for _, bit := range bits {
			f := easSpaceHz
			if bit == 1 {
				f = easMarkHz
			}
			end := int(float64(n+1) * spb)
			for len(out) < end {
				phase += 2 * math.Pi * f / float64(rate)
				out = append(out, 0.5*math.Sin(phase))
			}
			n++
		}
	}
	return out
}
