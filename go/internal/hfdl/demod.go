// HFDL physical layer: audio (or complex baseband) to link-layer octets.
//
// The framing, scrambler, interleaver, constants and state machine follow
// dumphfdl's hfdl.c (Copyright (c) Tomasz Lemiech, GPL-3.0-or-later). The
// carrier recovery loop follows the same file. The Kaiser-free windowed-sinc
// resampler, Gardner timing loop and soft PSK demapper are written for this
// project, with the LLR scaling taken from liquid-dsp (MIT, Joseph Gaeddert).
//
// Not ported: the LMS equalizer. Clean audio did not need it in tests; weak
// or multipath signals will decode worse than dumphfdl does.
package hfdl

import (
	"math"
	"math/cmplx"
)

const (
	SymbolRate  = 1800
	sps         = 3
	aLen        = 127
	m1Len       = 127
	m2Len       = 15
	tLen        = 15
	frameLen    = 30
	slotSingle  = 72
	slotDouble  = 168
	maxSymbols  = slotDouble * frameLen
	prekeyLen   = 448
	singleFrame = prekeyLen + 2*aLen + m1Len + m2Len + 9*tLen + slotSingle*(frameLen+tLen)
	// SSBCarrierOffsetHz is where the HFDL channel centre sits in USB audio.
	SSBCarrierOffsetHz = 1440
)

type params struct {
	arity, segs, rate, shift int
}

var frameParams = [8]params{
	{1, slotSingle, 4, 17}, {1, slotSingle, 2, 17}, {2, slotSingle, 2, 17}, {3, slotSingle, 2, 17},
	{1, slotDouble, 4, 23}, {1, slotDouble, 2, 23}, {2, slotDouble, 2, 23}, {3, slotDouble, 2, 23},
}

// Bit rates by M1 index, for reporting.
func (f Frame) BitRate() int {
	p := frameParams[f.M1]
	return SymbolRate * p.arity / p.rate * frameLen / (frameLen + tLen)
}

var mf = [19]float64{
	-0.0170974647427123, 0.01148231492068473, 0.03138375667422348, 0.009454398851680437,
	-0.04161644170893816, -0.06451564801420356, -0.005495792933327306, 0.1316404671361545,
	0.2759693160697777, 0.3375901874933208, 0.2759693160697777, 0.1316404671361545,
	-0.005495792933327306, -0.06451564801420356, -0.04161644170893816, 0.009454398851680437,
	0.03138375667422348, 0.01148231492068473, -0.0170974647427123,
}

var aOctets = [16]byte{0x5b, 0xbc, 0x74, 0x57, 0x03, 0xd9, 0x89, 0x39, 0xf2, 0x08, 0xd5, 0x36, 0x94, 0x2c, 0x32, 0xfe}

var m1Bits = [m1Len]uint8{
	0, 1, 1, 1, 0, 1, 1, 0, 1, 1, 1, 1, 0, 1, 0, 0, 0, 1, 0, 1, 1, 0, 0,
	1, 0, 1, 1, 1, 1, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 1, 1, 0, 1, 1,
	0, 0, 0, 1, 1, 1, 0, 0, 1, 1, 1, 0, 1, 0, 1, 1, 1, 0, 0, 0, 0, 1, 0, 0, 1, 1,
	0, 0, 0, 0, 0, 1, 0, 1, 0, 1, 0, 1, 1, 0, 1, 0, 0, 1, 0, 0, 1, 0, 1, 0, 0, 1,
	1, 1, 1, 0, 0, 1, 0, 0, 0, 1, 1, 0, 1, 0, 1, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1,
}

var m1Shifts = [8]int{72, 82, 113, 123, 61, 103, 93, 9}

// Costas proportional gain is a Demod field. dumphfdl uses 0.1; without its
// equalizer the 1200 bps QPSK sample only decodes with 0.05, while 300 bps
// needs 0.1, so DecodeAudio runs both and keeps frames that pass the FCS.
const loopBeta = 0.047

const trainSeq = 0x9AF

var aBits [aLen]uint8
var m1Tpl [8][m1Len]uint8

func init() {
	for i := 0; i < aLen; i++ {
		aBits[i] = aOctets[i/8] >> (7 - i%8) & 1
	}
	for s, sh := range m1Shifts {
		for j := 0; j < m1Len; j++ {
			m1Tpl[s][j] = m1Bits[(sh+j)%m1Len]
		}
	}
}

// Frame is one demodulated, FEC-decoded HFDL burst.
type Frame struct {
	M1       int // frame mode index into the 8 speed/slot combinations
	Double   bool
	Data     []byte  // link-layer octets (MPDU or SPDU), FCS included
	TrainBER float64 // fraction of wrong training bits, a sync-quality measure
	FreqErr  float64 // Hz, from the carrier loop at A2
}

type state int

const (
	stA1 state = iota
	stA2
	stM1
	stM2
	stTrain
	stData1
	stData2
)

// Demod is a streaming HFDL demodulator for real audio.
type Demod struct {
	fs, carrier float64
	alpha       float64
	// front end
	in    []float64 // history of input audio
	inOff int       // absolute index of in[0]
	nOut  int       // output samples produced at 5400 Hz
	phase float64
	// post-resampler
	agcG, agcP float64
	mfBuf      [19]complex128
	// timing
	mfHist  []complex128
	mfOff   int
	nMF     int
	tNext   float64
	yPrev   complex128
	havePrv bool
	pwr     float64
	gErr    float64
	// carrier loop
	cPhi, cDphi float64
	// framer
	st       state
	wanted   int
	retries  int
	bits     []uint8 // last aLen bits
	emit     int     // 1 bits, 2 symbols, 3 skip
	mask     uint8
	m1       int
	arity    int
	curArity int
	segs     int
	trainCnt int
	trainIdx int
	train    []complex128
	data     []complex128
	cur      *[]complex128
	symCnt   int
	trainBad int
	trainTot int
	freqErr  float64
	Frames   []Frame
}

// NewDemod makes a demodulator for audio sampled at fs. The HFDL channel is
// assumed centred carrierHz in the audio (1440 for a USB receiver tuned to
// the assigned frequency).
func NewDemod(fs, carrierHz float64) *Demod {
	d := &Demod{fs: fs, carrier: carrierHz, agcG: 1, pwr: 1, alpha: 0.1}
	d.resetFramer()
	return d
}

func (d *Demod) resetFramer() {
	d.st = stA1
	d.wanted = 1
	d.retries = 0
	d.curArity = 1
	d.trainBad, d.trainTot = 0, 0
	d.trainIdx = 0
	d.train = d.train[:0]
	d.data = d.data[:0]
	d.cur = &d.train
	d.emit = 1
	d.mask = 0
	d.bits = d.bits[:0]
}

func (d *Demod) resetLoops() {
	d.cPhi, d.cDphi = 0, 0
	d.havePrv = false
}

// Feed processes audio samples; completed frames accumulate in d.Frames.
func (d *Demod) Feed(x []float64) {
	d.in = append(d.in, x...)
	const half = 24
	outRate := float64(SymbolRate * sps)
	step := d.fs / outRate
	cut := 1350.0 // Hz, single-sided passband of the complex baseband
	for {
		t := float64(d.nOut) * step
		hi := int(math.Floor(t)) + half
		if hi >= d.inOff+len(d.in) {
			break
		}
		var acc complex128
		for m := int(math.Floor(t)) - half + 1; m <= hi; m++ {
			if m < 0 {
				continue
			}
			u := t - float64(m)
			w := 0.5 + 0.5*math.Cos(math.Pi*u/float64(half))
			h := 2 * cut / d.fs * sinc(2*cut/d.fs*u) * w
			ph := -2 * math.Pi * d.carrier * float64(m) / d.fs
			acc += complex(d.in[m-d.inOff]*h, 0) * cmplx.Rect(1, ph)
		}
		d.nOut++
		d.baseband(acc)
	}
	// drop consumed input
	keep := int(math.Floor(float64(d.nOut)*step)) - half - 2
	if keep > d.inOff+4096 {
		n := keep - d.inOff
		d.in = append(d.in[:0], d.in[n:]...)
		d.inOff = keep
	}
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// baseband takes one complex sample at 3 samples/symbol.
func (d *Demod) baseband(s complex128) {
	// AGC (liquid agc_crcf, bandwidth 0.01)
	const alpha = 0.01
	y := s * complex(d.agcG, 0)
	y2 := real(y)*real(y) + imag(y)*imag(y)
	d.agcP = (1-alpha)*d.agcP + alpha*y2
	if d.agcP > 1e-6 {
		d.agcG *= math.Exp(-0.5 * alpha * math.Log(d.agcP))
	}
	if d.agcG > 1e6 {
		d.agcG = 1e6
	}
	copy(d.mfBuf[1:], d.mfBuf[:18])
	d.mfBuf[0] = y
	var m complex128
	for i, c := range mf {
		m += d.mfBuf[i] * complex(c, 0)
	}
	d.mfHist = append(d.mfHist, m)
	d.nMF++
	d.timing()
}

func (d *Demod) interp(t float64) complex128 {
	// cubic Lagrange on samples floor(t)-1..floor(t)+2
	i := int(math.Floor(t))
	mu := t - float64(i)
	at := func(k int) complex128 { return d.mfHist[k-d.mfOff] }
	y0, y1, y2, y3 := at(i-1), at(i), at(i+1), at(i+2)
	c0 := -mu * (mu - 1) * (mu - 2) / 6
	c1 := (mu + 1) * (mu - 1) * (mu - 2) / 2
	c2 := -(mu + 1) * mu * (mu - 2) / 2
	c3 := (mu + 1) * mu * (mu - 1) / 6
	return y0*complex(c0, 0) + y1*complex(c1, 0) + y2*complex(c2, 0) + y3*complex(c3, 0)
}

// timing is a Gardner loop on the matched-filter output.
func (d *Demod) timing() {
	if d.tNext == 0 {
		d.tNext = 8
	}
	for d.tNext+3 < float64(d.mfOff+len(d.mfHist)) && d.tNext-2.5 >= float64(d.mfOff) {
		cur := d.interp(d.tNext)
		mid := d.interp(d.tNext - float64(sps)/2)
		if d.havePrv {
			e := real(cmplx.Conj(mid) * (cur - d.yPrev))
			// normalise by the symbol power so loop gain is level independent
			p := real(cur)*real(cur) + imag(cur)*imag(cur)
			if p > 1e-9 {
				e /= p
			}
			if e > 1 {
				e = 1
			} else if e < -1 {
				e = -1
			}
			d.tNext += -0.05 * e
		}
		d.yPrev = cur
		d.havePrv = true
		d.tNext += float64(sps)
		d.symbol(cur)
	}
	if lim := int(d.tNext) - 8; lim-d.mfOff > 2048 {
		n := lim - d.mfOff
		d.mfHist = append(d.mfHist[:0], d.mfHist[n:]...)
		d.mfOff = lim
	}
}

func (d *Demod) hard(s complex128, arity int) (sym int, xhat complex128) {
	switch arity {
	case 1:
		if real(s) > 0 {
			return 0, 1
		}
		return 1, -1
	case 2:
		r := math.Sqrt2 / 2
		sym, re, im := 0, r, r
		if real(s) <= 0 {
			sym |= 1
			re = -r
		}
		if imag(s) <= 0 {
			sym |= 2
			im = -r
		}
		return sym, complex(re, im)
	}
	// 8PSK, liquid layout: symbol = gray(k), point k at angle k*pi/4
	k := int(math.Round(cmplx.Phase(s)/(math.Pi/4))) & 7
	g := k ^ (k >> 1)
	return g, cmplx.Rect(1, float64(k)*math.Pi/4)
}

func (d *Demod) symbol(raw complex128) {
	// symbol-rate amplitude normalisation
	p := real(raw)*real(raw) + imag(raw)*imag(raw)
	d.pwr = 0.99*d.pwr + 0.01*p
	if d.pwr > 1e-9 {
		raw /= complex(math.Sqrt(d.pwr), 0)
	}
	// Costas loop
	d.cPhi += d.cDphi
	if d.cPhi > math.Pi {
		d.cPhi -= 2 * math.Pi
	} else if d.cPhi < -math.Pi {
		d.cPhi += 2 * math.Pi
	}
	s := raw * cmplx.Rect(1, -d.cPhi)
	if math.Abs(d.cDphi) > 0.25 && d.st == stA1 {
		d.resetLoops()
	}
	_, xh := d.hard(s, d.curArity)
	err := imag(s * cmplx.Conj(xh))
	if err > 1 {
		err = 1
	} else if err < -1 {
		err = -1
	}
	la := d.alpha
	d.cPhi += la * err
	d.cDphi += loopBeta * la * la * err
	d.symCnt++
	if d.symCnt >= 13*singleFrame && d.st == stA1 {
		d.symCnt = 0
		d.resetLoops()
	}
	switch d.emit {
	case 1:
		sym, _ := d.hard(s, d.curArity)
		b := uint8(sym) ^ d.mask
		for i := 0; i < d.curArity; i++ {
			d.pushBit(b >> i & 1)
		}
	case 2:
		*d.cur = append(*d.cur, s)
	}
	if d.wanted > 1 {
		d.wanted--
		return
	}
	d.advance()
}

func (d *Demod) pushBit(b uint8) {
	if len(d.bits) == aLen {
		copy(d.bits, d.bits[1:])
		d.bits = d.bits[:aLen-1]
	}
	d.bits = append(d.bits, b)
}

func corr(tpl []uint8, bits []uint8) float64 {
	if len(bits) < len(tpl) {
		return 0
	}
	same := 0
	for i := range tpl {
		if tpl[i] == bits[i] {
			same++
		}
	}
	return 2*float64(same)/float64(len(tpl)) - 1
}

func (d *Demod) advance() {
	switch d.st {
	case stA1:
		c := corr(aBits[:], d.bits)
		if math.Abs(c) > 0.36 {
			d.mask = 0
			if c < 0 {
				d.mask = 0xff
			}
			d.wanted = aLen
			d.retries = 0
			d.st = stA2
		}
	case stA2:
		c := corr(aBits[:], d.bits)
		if math.Abs(c) > 0.3 {
			d.freqErr = d.cDphi * SymbolRate / (2 * math.Pi)
			d.wanted = m1Len
			d.retries = 0
			d.st = stM1
		} else {
			d.retries++
			if d.retries >= 3 {
				d.resetFramer()
			}
		}
	case stM1:
		best, bi := 0.0, -1
		for i := range m1Tpl {
			if c := math.Abs(corr(m1Tpl[i][:], d.bits)); c > best {
				best, bi = c, i
			}
		}
		if best > 0.3 {
			p := frameParams[bi]
			d.m1, d.segs, d.arity = bi, p.segs, p.arity
			d.wanted = m2Len
			d.st = stM2
			d.emit = 3
		} else {
			d.resetFramer()
		}
	case stM2:
		d.train = d.train[:0]
		d.wanted = tLen
		d.trainCnt = 9
		d.trainIdx = 0
		d.st = stTrain
		d.emit = 2
		d.data = d.data[:0]
	case stTrain:
		d.scoreTrain()
		d.train = d.train[:0]
		if d.trainCnt > 1 {
			d.trainCnt--
			d.wanted = tLen
		} else if d.segs > 0 {
			d.wanted = frameLen / 2
			d.st = stData1
			d.curArity = d.arity
			d.cur = &d.data
		} else {
			d.finish()
			d.resetFramer()
			d.symCnt = 0
		}
	case stData1:
		d.wanted = frameLen / 2
		d.st = stData2
	case stData2:
		d.segs--
		d.curArity = 1
		d.cur = &d.train
		d.st = stTrain
		d.trainCnt = 1
		d.wanted = tLen
	}
}

func (d *Demod) scoreTrain() {
	var got uint32
	for _, s := range d.train {
		b := uint32(0)
		if real(s) <= 0 {
			b = 1
		}
		b ^= uint32(d.mask & 1)
		got = got<<1 | b
	}
	x := got ^ trainSeq
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	d.trainBad += n
	d.trainTot += tLen
}

// Descrambler: 15-bit LFSR, polynomial 0x4001, seed 0x4d4b, restarted every 120 symbols.
type descr struct {
	reg uint32
	pos int
}

func (s *descr) next() uint32 {
	if s.pos == 120 {
		s.pos = 0
		s.reg = 0x4d4b
	}
	s.pos++
	// liquid msequence_advance: output is the new bit, parity(reg & genpoly)
	fb := parity(s.reg & 0x4001)
	s.reg = (s.reg<<1 | fb) & 0x7fff
	return fb
}

func parity(x uint32) uint32 {
	x ^= x >> 8
	x ^= x >> 4
	x ^= x >> 2
	x ^= x >> 1
	return x & 1
}

const (
	dlRows = 40
	dlPop  = 9
)

func (d *Demod) softBits(s complex128, arity int, out []uint8) {
	clamp := func(v float64) uint8 {
		v = v*16 + 127
		if v > 255 {
			v = 255
		} else if v < 0 {
			v = 0
		}
		return uint8(v)
	}
	switch arity {
	case 1:
		out[0] = clamp(-2 * real(s) * 4.0)
	case 2:
		out[0] = clamp(-2 * imag(s) * 5.8)
		out[1] = clamp(-2 * real(s) * 5.8)
	default:
		// max-log LLR over the 8PSK constellation, bit 0 is the MSB
		var d0, d1 [3]float64
		for i := range d0 {
			d0[i], d1[i] = 1e9, 1e9
		}
		for k := 0; k < 8; k++ {
			g := k ^ (k >> 1)
			dist := cmplx.Abs(s - cmplx.Rect(1, float64(k)*math.Pi/4))
			dist *= dist
			for b := 0; b < 3; b++ {
				if g>>(2-b)&1 == 1 {
					d1[b] = math.Min(d1[b], dist)
				} else {
					d0[b] = math.Min(d0[b], dist)
				}
			}
		}
		for b := 0; b < 3; b++ {
			out[b] = clamp((d0[b] - d1[b]) * 4.0)
		}
	}
}

func (d *Demod) finish() {
	p := frameParams[d.m1]
	nsym := p.segs * frameLen
	if len(d.data) != nsym {
		return
	}
	cols := nsym * p.arity / dlRows
	table := make([]uint8, dlRows*cols)
	row, col := 0, 0
	ds := descr{reg: 0x4d4b}
	soft := make([]uint8, 3)
	flip := 1.0
	if d.mask&1 == 1 {
		flip = -1
	}
	for _, s := range d.data {
		f := flip
		if ds.next() == 1 {
			f = -f
		}
		d.softBits(s*complex(f, 0), p.arity, soft)
		for j := 0; j < p.arity; j++ {
			table[row*cols+col] = soft[j]
			row++
			if row == dlRows {
				row = 0
				col++
			}
			col -= p.shift
			if col < 0 {
				col += cols
			}
		}
	}
	nenc := nsym * p.arity
	vin := nenc
	if p.rate == 4 {
		vin /= 2
	}
	in := make([]uint8, vin)
	row, col = 0, 0
	pop := func() uint8 {
		v := table[row*cols+col]
		row = (row + dlPop) % dlRows
		if row == 0 {
			col++
		}
		return v
	}
	for i := range in {
		if p.rate == 4 {
			a, b := pop(), pop()
			in[i] = (a & b) + ((a ^ b) >> 1)
		} else {
			in[i] = pop()
		}
	}
	nbits := vin / 2
	out := viterbiDecode(in, nbits)
	n := nbits / 8
	if nbits%8 != 0 {
		n++
	}
	out = out[:n]
	for i := range out {
		out[i] = reverseByte(out[i])
	}
	ber := 1.0
	if d.trainTot > 0 {
		ber = float64(d.trainBad) / float64(d.trainTot)
	}
	d.Frames = append(d.Frames, Frame{M1: d.m1, Double: p.segs == slotDouble, Data: out, TrainBER: ber, FreqErr: d.freqErr})
}

// DecodeAudio demodulates a whole audio buffer and returns every frame whose
// frame check sequence verifies, parsed. Frames that fail the FCS are dropped:
// nothing unverified is returned.
func DecodeAudio(x []float64, fs, carrierHz float64, freq int) []DecodedFrame {
	var out []DecodedFrame
	for _, a := range []float64{0.1, 0.05} {
		d := NewDemod(fs, carrierHz)
		d.alpha = a
		d.Feed(x)
		for _, f := range d.Frames {
			p := ParsePDU(f.Data, freq)
			if !p.CRCOK {
				continue
			}
			dup := false
			for _, o := range out {
				if string(o.Frame.Data) == string(f.Data) {
					dup = true
				}
			}
			if !dup {
				out = append(out, DecodedFrame{Frame: f, PDU: p})
			}
		}
	}
	return out
}

// DecodedFrame is a frame with a verified FCS and its parse.
type DecodedFrame struct {
	Frame Frame
	PDU   PDU
}
