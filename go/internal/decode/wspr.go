package decode

// Native WSPR (2 minute) decoder: a Go port of the single-pass path of
// wsprd from WSJT-X (License: GNU GPL v3, Copyright 2001-2018 Joe Taylor
// K1JT, 2014-2018 Steven Franke K9AN, Fano decoder Copyright 1994 Phil Karn
// KA9Q). Not ported: signal subtraction, noncoherent sequence detection,
// OSD, and the callsign hash table (type 3 messages show <...>).
//
// Pipeline: audio -> complex baseband at 375 Hz centred on 1500 Hz ->
// half-symbol spectrogram -> peak candidates -> sync search over frequency,
// time and drift -> soft symbols -> deinterleave -> Fano decode of the
// K=32 r=1/2 convolutional code -> 50-bit unpack. A decode is accepted
// only if the Fano decoder converges inside its cycle budget, the callsign
// and grid fields unpack to valid values, and the soft-symbol rms is
// plausible; the code has no CRC, so these checks plus the verify gate
// (known-callsign structure, repeat decodes) are the safeguard.

import (
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
)

// WSPRMessage is one decoded WSPR spot.
type WSPRMessage struct {
	Call    string
	Grid    string
	DBm     int
	Text    string  // "CALL GRID DBM" or "CALL DBM"
	FreqHz  float64 // audio frequency of the lower tone (Hz above the dial)
	DT      float64 // seconds relative to the nominal start (1 s into the window)
	SNR     float64 // dB in 2500 Hz, rough
	Drift   float64 // Hz per window
	Sync    float64
	Metric  uint32
	Type    int // 1 standard, 2 extended prefix/suffix, 3 hashed
	WindowT int64
}

var wsprSync = [162]int{
	1, 1, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 0, 0, 0, 1, 0,
	0, 1, 0, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 0, 1,
	0, 0, 0, 0, 0, 0, 1, 0, 1, 1, 0, 0, 1, 1, 0, 1, 0, 0, 0, 1,
	1, 0, 1, 0, 0, 0, 0, 1, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 0, 1,
	0, 0, 1, 0, 1, 1, 0, 0, 0, 1, 1, 0, 1, 0, 1, 0, 0, 0, 1, 0,
	0, 0, 0, 0, 1, 0, 0, 1, 0, 0, 1, 1, 1, 0, 1, 1, 0, 0, 1, 1,
	0, 1, 0, 0, 0, 1, 1, 1, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 1, 0, 1, 1, 0, 0, 0, 1, 1, 0,
	0, 0,
}

const (
	wsprRate   = 375.0
	wsprPoints = 46080
)

// wsprBaseband returns I/Q at 375 Hz for a 120 s window, centre 1500 Hz.
func wsprBaseband(samples []float64, rate int) (id, qd []float64) {
	x := resampleTo12k(samples, rate)
	const dec = 32
	// windowed-sinc lowpass, cutoff 160 Hz at 12 kHz
	const taps = 1025
	h := make([]float64, taps)
	fc := 160.0 / 12000.0
	var sum float64
	for i := range h {
		m := float64(i - taps/2)
		var s float64
		if m == 0 {
			s = 2 * fc
		} else {
			s = math.Sin(2*math.Pi*fc*m) / (math.Pi * m)
		}
		win := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(taps-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(taps-1))
		h[i] = s * win
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	n := len(x)
	// mix to baseband (positive baseband frequency = audio above 1500 Hz)
	mi := make([]float64, n)
	mq := make([]float64, n)
	for i := 0; i < n; i++ {
		ph := -2 * math.Pi * 1500 * float64(i) / 12000
		mi[i] = x[i] * math.Cos(ph)
		mq[i] = x[i] * math.Sin(ph)
	}
	id = make([]float64, 65536)
	qd = make([]float64, 65536)
	for o := 0; o < wsprPoints; o++ {
		c := o * dec
		var si, sq float64
		for t := 0; t < taps; t++ {
			k := c + t - taps/2
			if k < 0 || k >= n {
				continue
			}
			si += h[t] * mi[k]
			sq += h[t] * mq[k]
		}
		id[o], qd[o] = si, sq
	}
	return
}

type wsprCand struct {
	freq, snr, drift, sync float64
	shift                  int
}

func wsprSyncDemod(id, qd []float64, np int, f1 *float64, ifmin, ifmax int, fstep float64, shift1 *int,
	lagmin, lagmax, lagstep int, drift1 *float64, symfac float64, mode int) (sync float64, symbols [162]uint8) {
	const dt = 1.0 / 375.0
	const df = 375.0 / 256.0
	twopidt := 2 * math.Pi * dt
	df15, df05 := df*1.5, df*0.5
	if mode == 0 {
		ifmin, ifmax, fstep = 0, 0, 0
	}
	if mode == 1 {
		lagmin, lagmax = *shift1, *shift1
	}
	if mode == 2 {
		lagmin, lagmax, ifmin, ifmax = *shift1, *shift1, 0, 0
	}
	if lagstep <= 0 {
		lagstep = 1
	}
	syncmax := -1e30
	bestShift := 0
	fbest := 0.0
	var c, s [4][256]float64
	fplast := -10000.0
	var fsymb [162]float64
	var bestSym [162]float64
	for ifreq := ifmin; ifreq <= ifmax; ifreq++ {
		f0 := *f1 + float64(ifreq)*fstep
		for lag := lagmin; lag <= lagmax; lag += lagstep {
			ss, totp := 0.0, 0.0
			for i := 0; i < 162; i++ {
				fp := f0 + (*drift1/2.0)*(float64(i)-81.0)/81.0
				if fp != fplast {
					offs := [4]float64{-df15, -df05, df05, df15}
					for t := 0; t < 4; t++ {
						dphi := twopidt * (fp + offs[t])
						cd, sd := math.Cos(dphi), math.Sin(dphi)
						c[t][0], s[t][0] = 1, 0
						for j := 1; j < 256; j++ {
							c[t][j] = c[t][j-1]*cd - s[t][j-1]*sd
							s[t][j] = c[t][j-1]*sd + s[t][j-1]*cd
						}
					}
					fplast = fp
				}
				var ii, qq [4]float64
				for j := 0; j < 256; j++ {
					k := lag + i*256 + j
					if k > 0 && k < np {
						for t := 0; t < 4; t++ {
							ii[t] += id[k]*c[t][j] + qd[k]*s[t][j]
							qq[t] += -id[k]*s[t][j] + qd[k]*c[t][j]
						}
					}
				}
				var p [4]float64
				for t := 0; t < 4; t++ {
					p[t] = math.Sqrt(ii[t]*ii[t] + qq[t]*qq[t])
				}
				totp += p[0] + p[1] + p[2] + p[3]
				cmet := (p[1] + p[3]) - (p[0] + p[2])
				if wsprSync[i] == 1 {
					ss += cmet
				} else {
					ss -= cmet
				}
				if mode == 2 {
					if wsprSync[i] == 1 {
						fsymb[i] = p[3] - p[1]
					} else {
						fsymb[i] = p[2] - p[0]
					}
				}
			}
			ss /= totp
			if ss > syncmax {
				syncmax = ss
				bestShift = lag
				fbest = f0
				if mode == 2 {
					bestSym = fsymb
				}
			}
		}
	}
	if mode <= 1 {
		*shift1 = bestShift
		*f1 = fbest
		return syncmax, symbols
	}
	var fsum, f2sum float64
	for i := 0; i < 162; i++ {
		fsum += bestSym[i] / 162
		f2sum += bestSym[i] * bestSym[i] / 162
	}
	fac := math.Sqrt(f2sum - fsum*fsum)
	if math.IsNaN(fac) || fac <= 0 {
		// Constant (or empty) symbol powers give a zero or negative
		// variance; dividing by it would spread NaN through the soft
		// symbols. Fall back to unscaled symbols instead.
		fac = 1
	}
	for i := 0; i < 162; i++ {
		v := symfac * bestSym[i] / fac
		if v > 127 {
			v = 127
		}
		if v < -128 {
			v = -128
		}
		symbols[i] = uint8(int(v + 128))
	}
	return syncmax, symbols
}

func wsprDeinterleave(sym *[162]uint8) {
	var tmp [162]uint8
	p := 0
	for i := 0; p < 162; i++ {
		j := int(bits.Reverse8(uint8(i)))
		if j < 162 {
			tmp[p] = sym[j]
			p++
		}
	}
	*sym = tmp
}

const wsprPoly1, wsprPoly2 = 0xf2d05351, 0xe4613c47

func wsprEncode(enc uint32) int {
	a := bits.OnesCount32(enc&wsprPoly1) & 1
	b := bits.OnesCount32(enc&wsprPoly2) & 1
	return a<<1 | b
}

var wsprMet [2][256]int

func init() {
	const bias = 0.45
	for i := 0; i < 256; i++ {
		wsprMet[0][i] = int(math.Round(10 * (wsprMetric[i] - bias)))
		wsprMet[1][i] = int(math.Round(10 * (wsprMetric[255-i] - bias)))
	}
}

type fanoNode struct {
	enc   uint32
	gamma int
	met   [4]int
	tm    [2]int
	i     int
}

// wsprFano decodes 81 information bits from deinterleaved soft symbols.
// Returns the 10 data bytes, final metric and ok=false on timeout.
func wsprFano(symbols *[162]uint8, delta int, maxCycles int) (data [10]byte, metric uint32, ok bool) {
	const nbits = 81
	nodes := make([]fanoNode, nbits+1)
	last := nbits - 1
	tail := nbits - 31
	for k := 0; k <= last; k++ {
		s0, s1 := symbols[2*k], symbols[2*k+1]
		nodes[k].met[0] = wsprMet[0][s0] + wsprMet[0][s1]
		nodes[k].met[1] = wsprMet[0][s0] + wsprMet[1][s1]
		nodes[k].met[2] = wsprMet[1][s0] + wsprMet[0][s1]
		nodes[k].met[3] = wsprMet[1][s0] + wsprMet[1][s1]
	}
	np := 0
	nodes[0].enc = 0
	lsym := wsprEncode(0)
	m0 := nodes[0].met[lsym]
	m1 := nodes[0].met[3^lsym]
	if m0 > m1 {
		nodes[0].tm = [2]int{m0, m1}
	} else {
		nodes[0].tm = [2]int{m1, m0}
		nodes[0].enc++
	}
	nodes[0].i = 0
	maxc := maxCycles * nbits
	t := 0
	nodes[0].gamma = 0
	i := 1
	for ; i <= maxc; i++ {
		ngamma := nodes[np].gamma + nodes[np].tm[nodes[np].i]
		if ngamma >= t {
			if nodes[np].gamma < t+delta {
				for ngamma >= t+delta {
					t += delta
				}
			}
			nodes[np+1].gamma = ngamma
			nodes[np+1].enc = nodes[np].enc << 1
			np++
			if np == last+1 {
				break
			}
			lsym = wsprEncode(nodes[np].enc)
			if np >= tail {
				nodes[np].tm[0] = nodes[np].met[lsym]
			} else {
				m0 = nodes[np].met[lsym]
				m1 = nodes[np].met[3^lsym]
				if m0 > m1 {
					nodes[np].tm = [2]int{m0, m1}
				} else {
					nodes[np].tm = [2]int{m1, m0}
					nodes[np].enc++
				}
			}
			nodes[np].i = 0
			continue
		}
		for {
			if np == 0 || nodes[np-1].gamma < t {
				t -= delta
				if nodes[np].i != 0 {
					nodes[np].i = 0
					nodes[np].enc ^= 1
				}
				break
			}
			np--
			if np < tail && nodes[np].i != 1 {
				nodes[np].i++
				nodes[np].enc ^= 1
				break
			}
		}
	}
	metric = uint32(nodes[np].gamma)
	q := 7
	for b := 0; b < nbits>>3; b++ {
		data[b] = byte(nodes[q].enc)
		q += 8
	}
	return data, metric, i < maxc
}

const wsprChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ "

func wsprUnpackCall(n int32) (string, bool) {
	if n >= 262177560 {
		return "", false
	}
	var tmp [6]byte
	for k := 5; k >= 3; k-- {
		tmp[k] = wsprChars[n%27+10]
		n /= 27
	}
	tmp[2] = wsprChars[n%10]
	n /= 10
	tmp[1] = wsprChars[n%36]
	n /= 36
	tmp[0] = wsprChars[n]
	s := strings.TrimLeft(string(tmp[:]), " ")
	return strings.TrimRight(s, " "), true
}

func wsprUnpackGrid(n int32) (string, bool) {
	n >>= 7
	if n >= 32400 {
		return "", false
	}
	dlat := int(n%180) - 90
	dlong := int(n/180)*2 - 180 + 2
	if dlong < -180 {
		dlong += 360
	}
	if dlong > 180 {
		dlong += 360
	}
	nlong := int(60.0 * (180.0 - float64(dlong)) / 5.0)
	n1, n2 := nlong/240, (nlong-240*(nlong/240))/24
	g0 := wsprChars[10+n1]
	g2 := wsprChars[n2]
	nlat := int(60.0 * float64(dlat+90) / 2.5)
	m1, m2 := nlat/240, (nlat-240*(nlat/240))/24
	g1 := wsprChars[10+m1]
	g3 := wsprChars[m2]
	return string([]byte{g0, g1, g2, g3}), true
}

func wsprUnpackPfx(np int32, call string) (string, bool) {
	if np < 60000 {
		var p [3]byte
		n := np
		for i := 2; i >= 0; i-- {
			nc := n % 37
			switch {
			case nc <= 9:
				p[i] = byte(nc) + 48
			case nc <= 35:
				p[i] = byte(nc) + 55
			default:
				p[i] = ' '
			}
			n /= 37
		}
		pfx := strings.TrimLeft(string(p[:]), " ")
		return pfx + "/" + call, true
	}
	nc := np - 60000
	switch {
	case nc <= 9:
		return call + "/" + string(rune(nc+48)), true
	case nc <= 35:
		return call + "/" + string(rune(nc+55)), true
	case nc >= 36 && nc <= 125:
		return call + "/" + string([]byte{byte((nc-26)/10 + 48), byte((nc-26)%10 + 48)}), true
	}
	return "", false
}

// wsprUnpack converts 10 decoded bytes into a spot. ok=false for invalid fields.
func wsprUnpack(d [10]byte) (WSPRMessage, bool) {
	var n1, n2 int32
	n1 = int32(d[0])<<20 + int32(d[1])<<12 + int32(d[2])<<4 + int32(d[3]>>4)&15
	n2 = int32(d[3]&15)<<18 + int32(d[4])<<10 + int32(d[5])<<2 + int32(d[6]>>6)&3
	call, ok := wsprUnpackCall(n1)
	if !ok {
		return WSPRMessage{}, false
	}
	grid, ok := wsprUnpackGrid(n2)
	if !ok {
		return WSPRMessage{}, false
	}
	ntype := int(n2&127) - 64
	m := WSPRMessage{Call: call, Grid: grid}
	switch {
	case ntype >= 0 && ntype <= 62:
		nu := ntype % 10
		if nu == 0 || nu == 3 || nu == 7 {
			m.DBm, m.Type = ntype, 1
			m.Text = fmt.Sprintf("%s %s %2d", call, grid, ntype)
			return m, true
		}
		nadd := nu
		if nu > 3 {
			nadd = nu - 3
		}
		if nu > 7 {
			nadd = nu - 7
		}
		n3 := n2/128 + 32768*int32(nadd-1)
		pc, ok := wsprUnpackPfx(n3, call)
		if !ok {
			return WSPRMessage{}, false
		}
		dbm := ntype - nadd
		u := dbm % 10
		if !(u == 0 || u == 3 || u == 7 || u == 10) {
			return WSPRMessage{}, false
		}
		m.Call, m.Grid, m.DBm, m.Type = pc, "", dbm, 2
		m.Text = fmt.Sprintf("%s %2d", pc, dbm)
		return m, true
	case ntype < 0:
		dbm := -(ntype + 1)
		u := dbm % 10
		if len(call) < 6 {
			call = fmt.Sprintf("%-6s", call)
		}
		g6 := string(call[5]) + call[:5]
		isAl := func(b byte) bool { return b >= 'A' && b <= 'Z' }
		isDg := func(b byte) bool { return b >= '0' && b <= '9' }
		if (u != 0 && u != 3 && u != 7 && u != 10) || !isAl(g6[0]) || !isAl(g6[1]) || !isDg(g6[2]) || !isDg(g6[3]) || ntype == -64 {
			return WSPRMessage{}, false
		}
		m.Call, m.Grid, m.DBm, m.Type = "<...>", strings.TrimSpace(g6), dbm, 3
		m.Text = fmt.Sprintf("<...> %s %2d", strings.TrimSpace(g6), dbm)
		return m, true
	}
	return WSPRMessage{}, false
}

// WSPRDecode decodes one 2-minute WSPR window. samples must start at the
// even-minute boundary (the nominal transmission starts 1 s later; a few
// seconds of offset are searched). Time budget is bounded by maxCycles.
func WSPRDecode(samples []float64, rate int) []WSPRMessage {
	id, qd := wsprBaseband(samples, rate)
	npoints := wsprPoints
	const df = 375.0 / 256.0 / 2
	nffts := 4*(npoints/512) - 1
	ps := make([][]float64, 512)
	for j := range ps {
		ps[j] = make([]float64, nffts)
	}
	plan := newFFTPlan(512)
	buf := make([]complex128, 512)
	var w [512]float64
	for i := range w {
		w[i] = math.Sin(0.006147931 * float64(i))
	}
	for i := 0; i < nffts; i++ {
		for j := 0; j < 512; j++ {
			k := i*128 + j
			buf[j] = complex(id[k]*w[j], qd[k]*w[j])
		}
		plan.transform(buf)
		for j := 0; j < 512; j++ {
			src := (j + 256) % 512
			re, im := real(buf[src]), imag(buf[src])
			ps[j][i] = re*re + im*im
		}
	}
	psavg := make([]float64, 512)
	for j := 0; j < 512; j++ {
		for i := 0; i < nffts; i++ {
			psavg[j] += ps[j][i]
		}
	}
	smspec := make([]float64, 411)
	for i := range smspec {
		for j := -3; j <= 3; j++ {
			smspec[i] += psavg[256-205+i+j]
		}
	}
	srt := append([]float64(nil), smspec...)
	sort.Float64s(srt)
	noise := srt[122]
	if noise <= 0 {
		return nil
	}
	minSNR := math.Pow(10, -8.0/10)
	for j := range smspec {
		smspec[j] = smspec[j]/noise - 1
		if smspec[j] < minSNR {
			smspec[j] = 0.1 * minSNR
		}
	}
	var cands []wsprCand
	for j := 1; j < 410 && len(cands) < 200; j++ {
		if smspec[j] > smspec[j-1] && smspec[j] > smspec[j+1] {
			f := float64(j-205) * df
			if f >= -110 && f <= 110 {
				cands = append(cands, wsprCand{freq: f, snr: 10*math.Log10(smspec[j]) - 26.3})
			}
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].snr > cands[b].snr })
	maxdrift := 4
	// coarse search
	for j := range cands {
		smax := -1e30
		if0 := int(cands[j].freq/df + 256)
		for ifr := if0 - 2; ifr <= if0+2; ifr++ {
			for k0 := -10; k0 < 22; k0++ {
				for idr := -maxdrift; idr <= maxdrift; idr++ {
					ss, pw := 0.0, 0.0
					for k := 0; k < 162; k++ {
						ifd := ifr + int((float64(k)-81.0)/81.0*float64(idr)/(2.0*df))
						kindex := k0 + 2*k
						if kindex >= 0 && kindex < nffts && ifd-3 >= 0 && ifd+3 < 512 {
							p0 := math.Sqrt(ps[ifd-3][kindex])
							p1 := math.Sqrt(ps[ifd-1][kindex])
							p2 := math.Sqrt(ps[ifd+1][kindex])
							p3 := math.Sqrt(ps[ifd+3][kindex])
							ss += float64(2*wsprSync[k]-1) * ((p1 + p3) - (p0 + p2))
							pw += p0 + p1 + p2 + p3
						}
					}
					if pw == 0 {
						continue
					}
					s1 := ss / pw
					if s1 > smax {
						smax = s1
						cands[j].shift = 128 * (k0 + 1)
						cands[j].drift = float64(idr)
						cands[j].freq = float64(ifr-256) * df
						cands[j].sync = s1
					}
				}
			}
		}
	}
	const symfac = 50.0
	const minsync1, minsync2 = 0.10, 0.12
	// keep the strongest coarse candidates; the refinement is the cost centre
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].sync > cands[b].sync })
	if len(cands) > 60 {
		cands = cands[:60]
	}
	for len(cands) > 0 && cands[len(cands)-1].sync < 0.07 {
		cands = cands[:len(cands)-1]
	}
	refine := func(j int) {
		f1 := cands[j].freq
		drift1 := cands[j].drift
		shift1 := cands[j].shift
		lagmin, lagmax, lagstep := shift1-128, shift1+128, 64
		sync1, _ := wsprSyncDemod(id, qd, npoints, &f1, 0, 0, 0, &shift1, lagmin, lagmax, lagstep, &drift1, symfac, 0)
		sync1, _ = wsprSyncDemod(id, qd, npoints, &f1, -2, 2, 0.25, &shift1, lagmin, lagmax, lagstep, &drift1, symfac, 1)
		dp := drift1 + 0.5
		syncp, _ := wsprSyncDemod(id, qd, npoints, &f1, 0, 0, 0, &shift1, lagmin, lagmax, lagstep, &dp, symfac, 1)
		dm := drift1 - 0.5
		syncm, _ := wsprSyncDemod(id, qd, npoints, &f1, 0, 0, 0, &shift1, lagmin, lagmax, lagstep, &dm, symfac, 1)
		if syncp > sync1 {
			drift1, sync1 = dp, syncp
		} else if syncm > sync1 {
			drift1, sync1 = dm, syncm
		}
		if sync1 > minsync1 {
			lagmin, lagmax, lagstep = shift1-32, shift1+32, 16
			sync1, _ = wsprSyncDemod(id, qd, npoints, &f1, 0, 0, 0, &shift1, lagmin, lagmax, lagstep, &drift1, symfac, 0)
			sync1, _ = wsprSyncDemod(id, qd, npoints, &f1, -2, 2, 0.05, &shift1, lagmin, lagmax, lagstep, &drift1, symfac, 1)
			cands[j].freq, cands[j].shift, cands[j].drift, cands[j].sync = f1, shift1, drift1, sync1
		}
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for wk := 0; wk < 2; wk++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				refine(j)
			}
		}()
	}
	for j := range cands {
		jobs <- j
	}
	close(jobs)
	wg.Wait()
	// de-duplicate
	var uniq []wsprCand
	for _, c := range cands {
		dupe := false
		for k := range uniq {
			if math.Abs(c.freq-uniq[k].freq) < 0.05 && abs(c.shift-uniq[k].shift) < 16 {
				dupe = true
				if c.sync > uniq[k].sync {
					uniq[k] = c
				}
				break
			}
		}
		if !dupe && c.sync > minsync2 {
			uniq = append(uniq, c)
		}
	}
	const minrms = 52.0 * (symfac / 64.0)
	var out []WSPRMessage
	for _, c := range uniq {
		f1, drift1 := c.freq, c.drift
		for idt := 0; idt <= 16; idt++ {
			ii := (idt + 1) / 2
			if idt%2 == 1 {
				ii = -ii
			}
			ii *= 8
			sh := c.shift + ii
			_, sym := wsprSyncDemod(id, qd, npoints, &f1, 0, 0, 0, &sh, 0, 0, 0, &drift1, symfac, 2)
			var sq float64
			for _, s := range sym {
				y := float64(s) - 128
				sq += y * y
			}
			if math.Sqrt(sq/162) <= minrms {
				continue
			}
			wsprDeinterleave(&sym)
			data, metric, ok := wsprFano(&sym, 60, 10000)
			if !ok {
				continue
			}
			m, ok := wsprUnpack(data)
			if !ok {
				continue
			}
			m.FreqHz = 1500 + f1
			m.DT = float64(c.shift+ii)/wsprRate - 1.0
			m.SNR = c.snr
			m.Drift = drift1
			m.Sync = c.sync
			m.Metric = metric
			dup := false
			for _, o := range out {
				if o.Text == m.Text && math.Abs(o.FreqHz-m.FreqHz) < 4 {
					dup = true
				}
			}
			if !dup {
				out = append(out, m)
			}
			break
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].FreqHz < out[b].FreqHz })
	return out
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// WSPRWindow is one even-minute window of a longer capture.
type WSPRWindow struct {
	Start    int64 // Unix seconds of the window start
	Audio12k []float64
	Messages []WSPRMessage
}

// WSPRWindows cuts a capture at even UTC minutes and decodes each window
// that is fully covered (at least 114 s of audio).
func WSPRWindows(samples []float64, rate int, startUnix float64) []WSPRWindow {
	dur := float64(len(samples)) / float64(rate)
	first := math.Ceil(startUnix/120) * 120
	var out []WSPRWindow
	for t := first; t+114 <= startUnix+dur; t += 120 {
		i0 := int((t - startUnix) * float64(rate))
		i1 := i0 + 120*rate
		if i1 > len(samples) {
			i1 = len(samples)
		}
		seg := samples[i0:i1]
		w := WSPRWindow{Start: int64(t), Audio12k: resampleTo12k(seg, rate)}
		w.Messages = WSPRDecode(seg, rate)
		for k := range w.Messages {
			w.Messages[k].WindowT = int64(t)
		}
		out = append(out, w)
	}
	return out
}
