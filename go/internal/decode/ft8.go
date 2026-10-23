package decode

// Native FT8 and FT4 decoder (Go port of the decode path of kgoba/ft8_lib,
// MIT License, Copyright (c) 2018 Karlis Goba; protocols and the LDPC(174,91)
// code are from WSJT-X by Joe Taylor K1JT et al.). No external binary needed.
//
// Pipeline: 12 kHz audio -> oversampled waterfall (2x time, 2x frequency)
// -> Costas sync search -> soft bit metrics -> belief propagation LDPC ->
// CRC-14 -> 77-bit message unpack. A message is only reported when LDPC
// parity checks all pass AND the CRC-14 matches, so false decodes are
// vanishingly rare (about 2^-14 per candidate that converges).

import (
	"fmt"
	"math"
	"math/cmplx"
	"sort"
	"strings"
)

const (
	ftxRate     = 12000
	ftxMaxCands = 140
	ftxMinScore = 10
	ftxLDPCIter = 25
	ftxFMin     = 200.0
	ftxFMax     = 3000.0
	ftxTimeOSR  = 2
	ftxFreqOSR  = 2
)

// FTXMessage is one CRC-verified FT8 or FT4 message.
type FTXMessage struct {
	Protocol string  // "FT8" or "FT4"
	Text     string  // unpacked message, for example "CQ DX R6WA LN32"
	FreqHz   float64 // audio frequency of the lowest tone
	DT       float64 // time offset against the nominal slot start, seconds
	Score    int     // Costas sync score (higher is stronger)
	SNR      float64 // rough SNR estimate in dB (score/2, not 2.5 kHz referenced)
	CRC      uint16  // CRC-14 that matched
	Type     string  // message type label (std, nonstd, free, telemetry, unsupported)
	SlotUnix int64   // slot start (Unix seconds), set by FTXDecodeStream
	Engine   string  // decode engine: "go" or the reference tool name (empty = Go)
}

var (
	ft8Costas = [7]int{3, 1, 4, 0, 6, 5, 2}
	ft4Costas = [4][4]int{{0, 1, 3, 2}, {1, 0, 2, 3}, {2, 3, 1, 0}, {3, 2, 0, 1}}
	ft8Gray   = [8]int{0, 1, 3, 2, 5, 6, 4, 7}
	ft4Gray   = [4]int{0, 1, 3, 2}
)

type ftxProto struct {
	name       string
	symbolSec  float64
	slotSec    float64
	tones      int
	nSync      int // sync groups
	lenSync    int
	syncOffset int
	syncStart  int // first sync symbol index
}

var (
	protoFT8 = ftxProto{"FT8", 0.160, 15.0, 8, 3, 7, 36, 0}
	protoFT4 = ftxProto{"FT4", 0.048, 7.5, 4, 4, 4, 33, 1}
)

type waterfall struct {
	p         ftxProto
	numBlocks int
	numBins   int
	minBin    int
	stride    int // block stride in mag
	mag       []uint8
}

type ftxCand struct {
	score, timeOffset, timeSub, freqOffset, freqSub int
}

func (w *waterfall) base(c ftxCand) int {
	off := c.timeOffset
	off = off*ftxTimeOSR + c.timeSub
	off = off*ftxFreqOSR + c.freqSub
	return off*w.numBins + c.freqOffset
}

func buildWaterfall(samples []float64, p ftxProto) *waterfall {
	block := int(float64(ftxRate) * p.symbolSec)
	sub := block / ftxTimeOSR
	nfft := block * ftxFreqOSR
	minBin := int(ftxFMin * p.symbolSec)
	maxBin := int(ftxFMax*p.symbolSec) + 1
	nb := maxBin - minBin
	maxBlocks := int(p.slotSec / p.symbolSec)
	w := &waterfall{p: p, numBins: nb, minBin: minBin, stride: ftxTimeOSR * ftxFreqOSR * nb}
	norm := 2.0 / float64(nfft)
	win := make([]float64, nfft)
	for i := range win {
		s := math.Sin(math.Pi * float64(i) / float64(nfft))
		win[i] = norm * s * s
	}
	last := make([]float64, nfft)
	nblocks := len(samples) / block
	if nblocks > maxBlocks {
		nblocks = maxBlocks
	}
	w.numBlocks = nblocks
	w.mag = make([]uint8, nblocks*w.stride)
	plan := newFFTPlan(nfft)
	buf := make([]complex128, nfft)
	for b := 0; b < nblocks; b++ {
		pos := b * block
		off := b * w.stride
		for ts := 0; ts < ftxTimeOSR; ts++ {
			copy(last, last[sub:])
			copy(last[nfft-sub:], samples[pos+ts*sub:pos+(ts+1)*sub])
			for i := 0; i < nfft; i++ {
				buf[i] = complex(win[i]*last[i], 0)
			}
			plan.transform(buf)
			for fs := 0; fs < ftxFreqOSR; fs++ {
				for bin := minBin; bin < maxBin; bin++ {
					src := bin*ftxFreqOSR + fs
					c := buf[src]
					m2 := real(c)*real(c) + imag(c)*imag(c)
					db := 10 * math.Log10(1e-12+m2)
					sc := int(2*db + 240)
					if sc < 0 {
						sc = 0
					} else if sc > 255 {
						sc = 255
					}
					w.mag[off] = uint8(sc)
					off++
				}
			}
		}
	}
	return w
}

func (w *waterfall) syncScore(c ftxCand) int {
	p := w.p
	score, n := 0, 0
	base := w.base(c)
	at := func(i int) int {
		if i < 0 || i >= len(w.mag) {
			return -1
		}
		return int(w.mag[i])
	}
	for m := 0; m < p.nSync; m++ {
		for k := 0; k < p.lenSync; k++ {
			block := p.syncStart + p.syncOffset*m + k
			babs := c.timeOffset + block
			if babs < 0 {
				continue
			}
			if babs >= w.numBlocks {
				break
			}
			var sm int
			if p.name == "FT8" {
				sm = ft8Costas[k]
			} else {
				sm = ft4Costas[m][k]
			}
			p8 := base + block*w.stride
			v := at(p8 + sm)
			if sm > 0 {
				score += v - at(p8+sm-1)
				n++
			}
			if sm < p.tones-1 {
				score += v - at(p8+sm+1)
				n++
			}
			if k > 0 && babs > 0 {
				score += v - at(p8+sm-w.stride)
				n++
			}
			if k+1 < p.lenSync && babs+1 < w.numBlocks {
				score += v - at(p8+sm+w.stride)
				n++
			}
		}
	}
	if n > 0 {
		score /= n
	}
	return score
}

func (w *waterfall) findCandidates() []ftxCand {
	var out []ftxCand
	for ts := 0; ts < ftxTimeOSR; ts++ {
		for fs := 0; fs < ftxFreqOSR; fs++ {
			for to := -10; to < 20; to++ {
				for fo := 0; fo+w.p.tones-1 < w.numBins; fo++ {
					c := ftxCand{timeOffset: to, timeSub: ts, freqOffset: fo, freqSub: fs}
					c.score = w.syncScore(c)
					if c.score >= ftxMinScore {
						out = append(out, c)
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > ftxMaxCands {
		out = out[:ftxMaxCands]
	}
	return out
}

func (w *waterfall) magF(i int) float64 { return float64(w.mag[i])/2 - 120 }

func max2(a, b float64) float64 {
	if a >= b {
		return a
	}
	return b
}

func (w *waterfall) likelihood(c ftxCand) []float64 {
	p := w.p
	log174 := make([]float64, 174)
	base := w.base(c)
	if p.name == "FT8" {
		for k := 0; k < 58; k++ {
			sym := k + 7
			if k >= 29 {
				sym = k + 14
			}
			bit := 3 * k
			blk := c.timeOffset + sym
			if blk < 0 || blk >= w.numBlocks {
				continue
			}
			var s2 [8]float64
			for j := 0; j < 8; j++ {
				s2[j] = w.magF(base + sym*w.stride + ft8Gray[j])
			}
			m4 := func(a, b, c, d int) float64 { return max2(max2(s2[a], s2[b]), max2(s2[c], s2[d])) }
			log174[bit] = m4(4, 5, 6, 7) - m4(0, 1, 2, 3)
			log174[bit+1] = m4(2, 3, 6, 7) - m4(0, 1, 4, 5)
			log174[bit+2] = m4(1, 3, 5, 7) - m4(0, 2, 4, 6)
		}
	} else {
		for k := 0; k < 87; k++ {
			sym := k + 13
			if k < 29 {
				sym = k + 5
			} else if k < 58 {
				sym = k + 9
			}
			bit := 2 * k
			blk := c.timeOffset + sym
			if blk < 0 || blk >= w.numBlocks {
				continue
			}
			var s2 [4]float64
			for j := 0; j < 4; j++ {
				s2[j] = w.magF(base + sym*w.stride + ft4Gray[j])
			}
			log174[bit] = max2(s2[2], s2[3]) - max2(s2[0], s2[1])
			log174[bit+1] = max2(s2[1], s2[3]) - max2(s2[0], s2[2])
		}
	}
	// normalise to the variance the BP decoder was tuned for
	var sum, sum2 float64
	for _, v := range log174 {
		sum += v
		sum2 += v * v
	}
	variance := (sum2 - sum*sum/174) / 174
	if variance <= 0 {
		return nil
	}
	f := math.Sqrt(24 / variance)
	for i := range log174 {
		log174[i] *= f
	}
	return log174
}

func fastTanh(x float64) float64 {
	if x < -4.97 {
		return -1
	}
	if x > 4.97 {
		return 1
	}
	x2 := x * x
	return x * (945 + x2*(105+x2)) / (945 + x2*(420+x2*15))
}

func fastAtanh(x float64) float64 {
	x2 := x * x
	return x * (945 + x2*(-735+x2*64)) / (945 + x2*(-1050+x2*225))
}

func ldpcErrors(cw *[174]uint8) int {
	errs := 0
	for m := 0; m < 83; m++ {
		var x uint8
		for i := 0; i < int(ftxNumRows[m]); i++ {
			x ^= cw[ftxNm[m][i]-1]
		}
		if x != 0 {
			errs++
		}
	}
	return errs
}

// bpDecode runs sum-product belief propagation; returns the hard decisions
// and the number of failed parity checks (0 means a valid codeword).
func bpDecode(llr []float64, iters int) ([174]uint8, int) {
	var tov [174][3]float64
	var toc [83][7]float64
	var plain [174]uint8
	minErr := 83
	for it := 0; it < iters; it++ {
		sum := 0
		for n := 0; n < 174; n++ {
			if llr[n]+tov[n][0]+tov[n][1]+tov[n][2] > 0 {
				plain[n] = 1
				sum++
			} else {
				plain[n] = 0
			}
		}
		if sum == 0 {
			break
		}
		e := ldpcErrors(&plain)
		if e < minErr {
			minErr = e
			if e == 0 {
				break
			}
		}
		for m := 0; m < 83; m++ {
			for ni := 0; ni < int(ftxNumRows[m]); ni++ {
				n := int(ftxNm[m][ni]) - 1
				t := llr[n]
				for mi := 0; mi < 3; mi++ {
					if int(ftxMn[n][mi])-1 != m {
						t += tov[n][mi]
					}
				}
				toc[m][ni] = fastTanh(-t / 2)
			}
		}
		for n := 0; n < 174; n++ {
			for mi := 0; mi < 3; mi++ {
				m := int(ftxMn[n][mi]) - 1
				t := 1.0
				for ni := 0; ni < int(ftxNumRows[m]); ni++ {
					if int(ftxNm[m][ni])-1 != n {
						t *= toc[m][ni]
					}
				}
				tov[n][mi] = -2 * fastAtanh(t)
			}
		}
	}
	return plain, minErr
}

func ftxCRC(msg []byte, nbits int) uint16 {
	const poly, width = 0x2757, 14
	const top = 1 << (width - 1)
	var rem uint16
	idx := 0
	for b := 0; b < nbits; b++ {
		if b%8 == 0 {
			rem ^= uint16(msg[idx]) << (width - 8)
			idx++
		}
		if rem&top != 0 {
			rem = (rem << 1) ^ poly
		} else {
			rem <<= 1
		}
	}
	return rem & ((top << 1) - 1)
}

func packBits(bits []uint8, n int) []byte {
	out := make([]byte, (n+7)/8)
	for i := 0; i < n; i++ {
		if bits[i] != 0 {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
	return out
}

// decodeCandidate returns the 10-byte payload when LDPC and CRC both pass.
func (w *waterfall) decodeCandidate(c ftxCand) (payload [10]byte, crc uint16, ok bool) {
	llr := w.likelihood(c)
	if llr == nil {
		return
	}
	plain, errs := bpDecode(llr, ftxLDPCIter)
	if errs > 0 {
		return
	}
	a91 := packBits(plain[:], 91)
	extracted := uint16(a91[9]&0x07)<<11 | uint16(a91[10])<<3 | uint16(a91[11])>>5
	a91[9] &= 0xF8
	a91[10] = 0
	calc := ftxCRC(a91, 96-14)
	if calc != extracted {
		return
	}
	for i := 0; i < 10; i++ {
		payload[i] = a91[i]
		if w.p.name == "FT4" {
			payload[i] ^= ft4XOR[i]
		}
	}
	return payload, calc, true
}

// FTXDecode decodes one FT8 (15 s) or FT4 (7.5 s) slot of audio. protocol
// is "FT8" or "FT4". samples start at the slot boundary (a late or early
// start of up to a second or two is tolerated by the time search).
func FTXDecode(samples []float64, rate int, protocol string) []FTXMessage {
	p := protoFT8
	if strings.EqualFold(protocol, "FT4") {
		p = protoFT4
	}
	s := resampleTo12k(samples, rate)
	slotN := int(p.slotSec * ftxRate)
	if len(s) > slotN {
		s = s[:slotN]
	}
	if len(s) < slotN {
		s = append(s, make([]float64, slotN-len(s))...)
	}
	w := buildWaterfall(s, p)
	hashes := &callHashes{}
	seen := map[[10]byte]bool{}
	var out []FTXMessage
	for pass := 0; pass < 3; pass++ {
		var subtract []ftxCand
		for _, c := range w.findCandidates() {
			pl, crc, ok := w.decodeCandidate(c)
			if !ok || seen[pl] {
				continue
			}
			seen[pl] = true
			subtract = append(subtract, c)
			text, typ := unpackFTX(pl, hashes)
			freq := (float64(w.minBin+c.freqOffset) + float64(c.freqSub)/ftxFreqOSR) / p.symbolSec
			dt := (float64(c.timeOffset)+float64(c.timeSub)/ftxTimeOSR)*p.symbolSec - 0.5
			out = append(out, FTXMessage{Protocol: p.name, Text: text, FreqHz: freq, DT: dt,
				Score: c.score, SNR: float64(c.score) * 0.5, CRC: crc, Type: typ})
		}
		if len(subtract) == 0 || p.name != "FT8" || pass == 2 {
			break
		}
		source := *w
		source.mag = append([]uint8(nil), w.mag...)
		for _, c := range subtract {
			w.suppress(c, &source)
		}
	}
	return out
}

// FTXDecodeStream decodes a longer capture by cutting it at UTC slot
// boundaries. startUnix is the capture start time (Unix seconds, fractional).
// Partial slots at either end are skipped.
func FTXDecodeStream(samples []float64, rate int, startUnix float64, protocol string) []FTXMessage {
	p := protoFT8
	if strings.EqualFold(protocol, "FT4") {
		p = protoFT4
	}
	slot := p.slotSec
	first := math.Ceil(startUnix/slot) * slot
	dur := float64(len(samples)) / float64(rate)
	var out []FTXMessage
	seen := map[string]bool{}
	for t := first; t+slot <= startUnix+dur+0.001; t += slot {
		i0 := int((t - startUnix) * float64(rate))
		i1 := i0 + int(slot*float64(rate))
		if i1 > len(samples) {
			i1 = len(samples)
		}
		for _, m := range FTXDecode(samples[i0:i1], rate, p.name) {
			key := fmt.Sprintf("%d|%s", int64(t), m.Text)
			if seen[key] {
				continue
			}
			seen[key] = true
			m.SlotUnix = int64(t)
			out = append(out, m)
		}
	}
	return out
}

func resampleTo12k(x []float64, rate int) []float64 {
	if rate == ftxRate {
		return append([]float64(nil), x...)
	}
	if rate <= 0 {
		// Without a real source rate there is no resampling ratio;
		// dividing by it would panic on a negative or unbounded length.
		return append([]float64(nil), x...)
	}
	ratio := float64(rate) / ftxRate
	n := int(float64(len(x)) / ratio)
	out := make([]float64, n)
	// box-average over the source span (a crude anti-alias lowpass when
	// decimating), linear interpolation when upsampling
	for i := range out {
		a := float64(i) * ratio
		if ratio > 1 {
			lo, hi := int(a), int(a+ratio)
			if hi > len(x) {
				hi = len(x)
			}
			var s float64
			for j := lo; j < hi; j++ {
				s += x[j]
			}
			if hi > lo {
				out[i] = s / float64(hi-lo)
			}
		} else {
			j := int(a)
			f := a - float64(j)
			if j+1 < len(x) {
				out[i] = x[j]*(1-f) + x[j+1]*f
			} else if j < len(x) {
				out[i] = x[j]
			}
		}
	}
	return out
}

// ---- mixed radix FFT (sizes with factors 2,3,5 are fast; others fall back
// to the O(n^2) DFT on the remaining prime factor) ----

type fftPlan struct {
	n       int
	twiddle []complex128
	factors []int
}

func newFFTPlan(n int) *fftPlan {
	p := &fftPlan{n: n, twiddle: make([]complex128, n)}
	for i := range p.twiddle {
		p.twiddle[i] = cmplx.Exp(complex(0, -2*math.Pi*float64(i)/float64(n)))
	}
	m := n
	for _, f := range []int{4, 2, 3, 5} {
		for m%f == 0 {
			p.factors = append(p.factors, f)
			m /= f
		}
	}
	for f := 7; m > 1; f += 2 {
		for m%f == 0 {
			p.factors = append(p.factors, f)
			m /= f
		}
	}
	return p
}

func (p *fftPlan) transform(x []complex128) {
	tmp := make([]complex128, p.n)
	p.rec(x, tmp, 1, p.n, p.factors)
	copy(x, tmp)
}

// rec computes the DFT of in[0:n*stride:stride] into out[0:n] (decimation in time).
func (p *fftPlan) rec(in, out []complex128, stride, n int, factors []int) {
	if n == 1 {
		out[0] = in[0]
		return
	}
	f := factors[0]
	m := n / f
	// sub-transforms of the f decimated sequences land in out[q*m:(q+1)*m]
	for q := 0; q < f; q++ {
		p.rec(in[q*stride:], out[q*m:(q+1)*m], stride*f, m, factors[1:])
	}
	tw := p.n / n
	res := make([]complex128, n)
	for k := 0; k < m; k++ {
		for r := 0; r < f; r++ {
			kk := k + r*m
			var s complex128
			for q := 0; q < f; q++ {
				s += out[q*m+k] * p.twiddle[(q*kk*tw)%p.n]
			}
			res[kk] = s
		}
	}
	copy(out, res)
}

// ---- message unpacking ----

type callHashes struct {
	calls []struct {
		call string
		n22  uint32
	}
}

func (h *callHashes) save(call string) {
	n22, ok := callHash22(call)
	if !ok {
		return
	}
	for _, c := range h.calls {
		if c.call == call {
			return
		}
	}
	h.calls = append(h.calls, struct {
		call string
		n22  uint32
	}{call, n22})
}

func (h *callHashes) lookup(bits int, v uint32) string {
	for _, c := range h.calls {
		if c.n22>>(22-uint(bits)) == v {
			return "<" + c.call + ">"
		}
	}
	return "<...>"
}

const (
	ftxChars        = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ+-./?"
	ftxAlnumSpSlash = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ/"
	ftxAlnumSp      = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	ftxLettersSp    = " ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	ftxAlnum        = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	ftxNumeric      = "0123456789"
	ftxNTokens      = 2063592
	ftxMax22        = 4194304
	ftxMaxGrid4     = 32400
)

func callHash22(call string) (uint32, bool) {
	var n58 uint64
	i := 0
	for ; i < len(call) && i < 11; i++ {
		j := strings.IndexByte(ftxAlnumSpSlash, call[i])
		if j < 0 {
			return 0, false
		}
		n58 = 38*n58 + uint64(j)
	}
	for ; i < 11; i++ {
		n58 *= 38
	}
	return uint32(((47055833459 * n58) >> (64 - 22)) & 0x3FFFFF), true
}

func unpack28(n28 uint32, ip, i3 int, h *callHashes) (string, bool) {
	if n28 < ftxNTokens {
		switch {
		case n28 == 0:
			return "DE", true
		case n28 == 1:
			return "QRZ", true
		case n28 == 2:
			return "CQ", true
		case n28 <= 1002:
			return fmt.Sprintf("CQ %03d", n28-3), true
		case n28 <= 532443:
			n := n28 - 1003
			var a [4]byte
			for i := 3; i >= 0; i-- {
				a[i] = ftxLettersSp[n%27]
				n /= 27
			}
			return "CQ " + strings.TrimLeft(string(a[:]), " "), true
		}
		return "", false
	}
	n28 -= ftxNTokens
	if n28 < ftxMax22 {
		return h.lookup(22, n28), true
	}
	n := n28 - ftxMax22
	var c [6]byte
	c[5] = ftxLettersSp[n%27]
	n /= 27
	c[4] = ftxLettersSp[n%27]
	n /= 27
	c[3] = ftxLettersSp[n%27]
	n /= 27
	c[2] = ftxNumeric[n%10]
	n /= 10
	c[1] = ftxAlnum[n%36]
	n /= 36
	c[0] = ftxAlnumSp[n%37]
	s := string(c[:])
	var res string
	switch {
	case strings.HasPrefix(s, "3D0") && s[3] != ' ':
		res = "3DA0" + strings.TrimSpace(s[3:])
	case s[0] == 'Q' && s[1] >= 'A' && s[1] <= 'Z':
		res = "3X" + strings.TrimSpace(s[1:])
	default:
		res = strings.TrimSpace(s)
	}
	if len(res) < 3 {
		return "", false
	}
	if ip != 0 {
		switch i3 {
		case 1:
			res += "/R"
		case 2:
			res += "/P"
		default:
			return "", false
		}
	}
	h.save(res)
	return res, true
}

func unpackGrid(ig, ir int) string {
	if ig <= ftxMaxGrid4 {
		g := []byte{byte('A' + (ig/1800)%18), byte('A' + (ig/100)%18), byte('0' + (ig/10)%10), byte('0' + ig%10)}
		if ir > 0 {
			return "R " + string(g)
		}
		return string(g)
	}
	irpt := ig - ftxMaxGrid4
	switch irpt {
	case 1:
		return ""
	case 2:
		return "RRR"
	case 3:
		return "RR73"
	case 4:
		return "73"
	}
	r := fmt.Sprintf("%+03d", irpt-35)
	if ir > 0 {
		r = "R" + r
	}
	return r
}

func joinFields(f ...string) string {
	var parts []string
	for _, s := range f {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

func unpackFTX(p [10]byte, h *callHashes) (string, string) {
	i3 := int(p[9]>>3) & 7
	n3 := int(p[8]<<2)&4 | int(p[9]>>6)&3
	switch {
	case i3 == 1 || i3 == 2:
		n29a := uint32(p[0])<<21 | uint32(p[1])<<13 | uint32(p[2])<<5 | uint32(p[3])>>3
		n29b := uint32(p[3]&7)<<26 | uint32(p[4])<<18 | uint32(p[5])<<10 | uint32(p[6])<<2 | uint32(p[7])>>6
		ir := int(p[7]&0x20) >> 5
		ig := int(p[7]&0x1f)<<10 | int(p[8])<<2 | int(p[9])>>6
		a, ok1 := unpack28(n29a>>1, int(n29a&1), i3, h)
		b, ok2 := unpack28(n29b>>1, int(n29b&1), i3, h)
		if !ok1 || !ok2 {
			return "unpack error", "unsupported"
		}
		return joinFields(a, b, unpackGrid(ig, ir)), "std"
	case i3 == 3:
		// ARRL RTTY layout: TU(1), call(28), call(28), R(1),
		// report(3), exchange(13), type(3). Protocol per WSJT-X packjt77.
		bit := func(start, n int) uint32 {
			var v uint32
			for k := start; k < start+n; k++ {
				v = v<<1 | uint32(p[k/8]>>(7-uint(k%8))&1)
			}
			return v
		}
		a, ok1 := unpack28(bit(1, 28), 0, 1, h)
		b, ok2 := unpack28(bit(29, 28), 0, 1, h)
		ex := int(bit(61, 13))
		exchange := ""
		if ex > 0 && ex < 8000 {
			exchange = fmt.Sprintf("%04d", ex)
		} else if ex > 8000 && ex <= 8171 {
			exchange = rttyMultipliers[ex-8001]
		}
		if !ok1 || !ok2 || exchange == "" {
			return "invalid RTTY exchange", "unsupported"
		}
		tu, r := "", ""
		if bit(0, 1) != 0 {
			tu = "TU;"
		}
		if bit(57, 1) != 0 {
			r = "R"
		}
		return joinFields(tu, a, b, r, fmt.Sprintf("5%d9", int(bit(58, 3))+2), exchange), "rtty"
	case i3 == 4:
		n12 := uint32(p[0])<<4 | uint32(p[1])>>4
		n58 := uint64(p[1]&0x0f)<<54 | uint64(p[2])<<46 | uint64(p[3])<<38 | uint64(p[4])<<30 |
			uint64(p[5])<<22 | uint64(p[6])<<14 | uint64(p[7])<<6 | uint64(p[8])>>2
		iflip := int(p[8]>>1) & 1
		nrpt := int(p[8]&1)<<1 | int(p[9]>>7)
		icq := int(p[9]>>6) & 1
		var c11 [11]byte
		for i := 10; i >= 0; i-- {
			c11[i] = ftxAlnumSpSlash[n58%38]
			n58 /= 38
		}
		call := strings.TrimSpace(string(c11[:]))
		if len(call) >= 3 {
			h.save(call)
		}
		other := h.lookup(12, n12)
		c1, c2 := other, call
		if iflip != 0 {
			c1, c2 = call, other
		}
		if icq != 0 {
			return joinFields("CQ", c2), "nonstd"
		}
		extra := [4]string{"", "RRR", "RR73", "73"}[nrpt]
		return joinFields(c1, c2, extra), "nonstd"
	case i3 == 0 && n3 == 0:
		var b [9]byte
		carry := byte(0)
		for i := 0; i < 9; i++ {
			b[i] = carry<<7 | p[i]>>1
			carry = p[i] & 1
		}
		var c [13]byte
		for idx := 12; idx >= 0; idx-- {
			var rem uint16
			for i := 0; i < 9; i++ {
				rem = rem<<8 | uint16(b[i])
				b[i] = byte(rem / 42)
				rem %= 42
			}
			c[idx] = ftxChars[rem]
		}
		return strings.TrimSpace(string(c[:])), "free"
	case i3 == 0 && n3 == 5:
		var b [9]byte
		carry := byte(0)
		for i := 0; i < 9; i++ {
			b[i] = carry<<7 | p[i]>>1
			carry = p[i] & 1
		}
		return fmt.Sprintf("telemetry %X", b), "telemetry"
	}
	return fmt.Sprintf("unsupported message i3=%d n3=%d", i3, n3), "unsupported"
}

// suppress removes decoded tracks from the search waterfall, leaving the
// local noise floor for a subsequent search. Only CRC-verified tracks enter.
func (w *waterfall) suppress(c ftxCand, source *waterfall) {
	plain, err := bpDecode(source.likelihood(c), ftxLDPCIter)
	if err != 0 {
		return
	}
	var tones [79]int
	for i := 0; i < 79; i++ {
		switch {
		case i < 7:
			tones[i] = ft8Costas[i]
		case i >= 36 && i < 43:
			tones[i] = ft8Costas[i-36]
		case i >= 72:
			tones[i] = ft8Costas[i-72]
		default:
			k := i - 7
			if i >= 43 {
				k = i - 14
			}
			tones[i] = ft8Gray[int(plain[k*3])*4+int(plain[k*3+1])*2+int(plain[k*3+2])]
		}
	}
	for i, tone := range tones {
		for dt := -1; dt <= 1; dt++ {
			for df := -1; df <= 1; df++ {
				time := (c.timeOffset+i)*ftxTimeOSR + c.timeSub + dt
				freq := c.freqOffset*ftxFreqOSR + c.freqSub + tone*ftxFreqOSR + df
				if time < 0 || time >= w.numBlocks*ftxTimeOSR || freq < ftxFreqOSR*2 || freq >= (w.numBins-2)*ftxFreqOSR {
					continue
				}
				idx := (time*ftxFreqOSR+freq%ftxFreqOSR)*w.numBins + freq/ftxFreqOSR
				a, b := w.mag[idx-2], w.mag[idx+2]
				floor := (int(a) + int(b)) / 2
				if int(w.mag[idx]) > floor {
					w.mag[idx] = uint8(floor)
				}
			}
		}
	}
}

// RTTY exchange enumeration from the WSJT-X on-air protocol.
var rttyMultipliers = strings.Fields("AL AK AZ AR CA CO CT DE FL GA HI ID IL IN IA KS KY LA ME MD MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC SD TN TX UT VT VA WA WV WI WY NB NS QC ON MB SK AB BC NWT NF LB NU YT PEI DC DR FR GD GR OV ZH ZL X01 X02 X03 X04 X05 X06 X07 X08 X09 X10 X11 X12 X13 X14 X15 X16 X17 X18 X19 X20 X21 X22 X23 X24 X25 X26 X27 X28 X29 X30 X31 X32 X33 X34 X35 X36 X37 X38 X39 X40 X41 X42 X43 X44 X45 X46 X47 X48 X49 X50 X51 X52 X53 X54 X55 X56 X57 X58 X59 X60 X61 X62 X63 X64 X65 X66 X67 X68 X69 X70 X71 X72 X73 X74 X75 X76 X77 X78 X79 X80 X81 X82 X83 X84 X85 X86 X87 X88 X89 X90 X91 X92 X93 X94 X95 X96 X97 X98 X99")
