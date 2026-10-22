package decode

import (
	"math"
	"math/cmplx"
	"sort"
	"strings"
)

// PSK31Signal is one carrier found and decoded by PSK31Scan.
type PSK31Signal struct {
	FreqHz  float64
	SNR     float64 // spectral peak over local median, dB
	Text    string
	Invalid int // varicode segments that did not map to a character
	Chars   int
}

// PSK31Scan finds narrowband carriers in a capture and decodes each as BPSK31
// with symbol timing recovery (re-estimated every 64 symbols so a receiver
// clock offset does not slip the symbol clock) and frequency tracking.
// Original code (no third-party source).
func PSK31Scan(samples []float64, rate int) []PSK31Signal {
	x := resampleTo12k(samples, rate)
	if len(x) < 12000*4 {
		return nil
	}
	peaks := psk31Peaks(x)
	var out []PSK31Signal
	for _, pk := range peaks {
		text, invalid := psk31Demod(x, pk.f)
		chars := len([]rune(text))
		if chars == 0 {
			continue
		}
		out = append(out, PSK31Signal{FreqHz: pk.f, SNR: pk.snr, Text: text, Invalid: invalid, Chars: chars})
	}
	return out
}

type psk31Peak struct{ f, snr float64 }

func psk31Peaks(x []float64) []psk31Peak {
	const n = 4096 // 2.93 Hz bins at 12 kHz
	plan := newFFTPlan(n)
	buf := make([]complex128, n)
	avg := make([]float64, n/2)
	cnt := 0
	for s := 0; s+n <= len(x); s += n / 2 {
		for i := 0; i < n; i++ {
			w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
			buf[i] = complex(x[s+i]*w, 0)
		}
		plan.transform(buf)
		for k := 0; k < n/2; k++ {
			avg[k] += real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
		}
		cnt++
	}
	binHz := 12000.0 / n
	lo, hi := int(300/binHz), int(2800/binHz)
	var pk []psk31Peak
	for k := lo + 40; k < hi-40; k++ {
		if avg[k] <= avg[k-1] || avg[k] < avg[k+1] {
			continue
		}
		win := append([]float64(nil), avg[k-40:k+41]...)
		sort.Float64s(win)
		med := win[len(win)/2]
		if med <= 0 {
			continue
		}
		snr := 10 * math.Log10(avg[k]/med)
		if snr < 9 {
			continue
		}
		// parabolic refinement
		a, b, c := avg[k-1], avg[k], avg[k+1]
		d := a - 2*b + c
		off := 0.0
		if d != 0 {
			off = 0.5 * (a - c) / d
		}
		pk = append(pk, psk31Peak{(float64(k) + off) * binHz, snr})
	}
	sort.Slice(pk, func(i, j int) bool { return pk[i].snr > pk[j].snr })
	var sel []psk31Peak
	for _, p := range pk {
		ok := true
		for _, q := range sel {
			if math.Abs(p.f-q.f) < 45 {
				ok = false
			}
		}
		if ok {
			sel = append(sel, p)
		}
		if len(sel) >= 10 {
			break
		}
	}
	return sel
}

func psk31Demod(x []float64, f float64) (string, int) {
	const spb = 384 // samples per symbol at 12 kHz
	const sub = 16  // 24 sub-samples per symbol after boxcar decimation
	nsub := len(x) / sub
	bb := make([]complex128, nsub)
	for i := 0; i < nsub; i++ {
		var s complex128
		for j := 0; j < sub; j++ {
			t := i*sub + j
			ph := -2 * math.Pi * f * float64(t) / 12000
			s += complex(x[t], 0) * complex(math.Cos(ph), math.Sin(ph))
		}
		bb[i] = s
	}
	const per = 24
	nsym := nsub / per
	if nsym < 40 {
		return "", 0
	}
	const seg = 64
	prevOff := -1
	var zs []complex128
	for s0 := 0; s0+8 < nsym; s0 += seg {
		segLen := seg
		if nsym-s0 < segLen {
			segLen = nsym - s0
		}
		bestO, bestE := 0, -1.0
		for o := 0; o < per; o++ {
			var e float64
			for k := 0; k < segLen; k++ {
				var z complex128
				for j := 0; j < per; j++ {
					idx := (s0+k)*per + o + j
					if idx < nsub {
						z += bb[idx]
					}
				}
				e += real(z)*real(z) + imag(z)*imag(z)
			}
			// the symbol integral is largest when aligned
			if e > bestE {
				bestE, bestO = e, o
			}
		}
		if prevOff >= 0 {
			// stay on the same branch of the circular offset
			for bestO-prevOff > per/2 {
				bestO -= per
			}
			for prevOff-bestO > per/2 {
				bestO += per
			}
		}
		prevOff = bestO
		for k := 0; k < segLen; k++ {
			var z complex128
			for j := 0; j < per; j++ {
				idx := (s0+k)*per + bestO + j
				if idx >= 0 && idx < nsub {
					z += bb[idx]
				}
			}
			zs = append(zs, z)
		}
	}
	// differential detection with per-segment residual rotation removal
	var bitsB strings.Builder
	for s0 := 1; s0 < len(zs); s0 += seg {
		end := s0 + seg
		if end > len(zs) {
			end = len(zs)
		}
		var acc complex128
		for k := s0; k < end; k++ {
			d := zs[k] * cmplx.Conj(zs[k-1])
			acc += d * d
		}
		rot := cmplx.Phase(acc) / 2
		rc := cmplx.Exp(complex(0, -rot))
		for k := s0; k < end; k++ {
			d := zs[k] * cmplx.Conj(zs[k-1]) * rc
			if real(d) >= 0 {
				bitsB.WriteByte('1')
			} else {
				bitsB.WriteByte('0')
			}
		}
	}
	text, invalid, _ := decodeVaricodeStrict(bitsB.String())
	return text, invalid
}
