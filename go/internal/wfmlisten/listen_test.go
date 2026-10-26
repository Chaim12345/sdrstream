// Package wfmlisten is a LOCAL scratch offline chain (branch
// local-wfm-chain-scratch, never pushed): recorded IQ -> WFM MPX ->
// stereo -> de-emphasis -> 48 kHz stereo WAV. Listening claims come from
// the produced WAV and its measurements, not from energy heuristics.
package wfmlisten

import (
	"encoding/binary"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
)

func readF32IQ(path string) ([]complex64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n := len(raw) / 8
	iq := make([]complex64, n)
	for i := 0; i < n; i++ {
		re := math.Float32frombits(binary.LittleEndian.Uint32(raw[8*i:]))
		im := math.Float32frombits(binary.LittleEndian.Uint32(raw[8*i+4:]))
		iq[i] = complex(re, im)
	}
	return iq, nil
}

func fftMag(x []complex64) []float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	n >>= 1
	a := make([]complex128, n)
	for i := range a {
		a[i] = complex(float64(real(x[i])), float64(imag(x[i])))
	}
	fftRec(a)
	out := make([]float64, n)
	for i, v := range a {
		out[i] = real(v)*real(v) + imag(v)*imag(v)
	}
	return out
}

func fftRec(a []complex128) {
	n := len(a)
	if n <= 1 {
		return
	}
	ev := make([]complex128, n/2)
	od := make([]complex128, n/2)
	for i := 0; i < n/2; i++ {
		ev[i], od[i] = a[2*i], a[2*i+1]
	}
	fftRec(ev)
	fftRec(od)
	for k := 0; k < n/2; k++ {
		w := -2 * math.Pi * float64(k) / float64(n)
		t := complex(math.Cos(w), math.Sin(w)) * od[k]
		a[k], a[k+n/2] = ev[k]+t, ev[k]-t
	}
}

// peakOffset returns the CENTER of the widest contiguous above-threshold
// spectral blob (a broadcast FM signal deviates; the strongest single bin
// is often a sideband, not the carrier).
// welch averages 8192-point periodograms over the whole capture: at fine
// resolution a tone-modulated FM signal is discrete lines with nulls
// between them, which fragments any threshold blob; at 183 Hz bins the
// energy is a contiguous hump.
func welch(iq []complex64) []float64 {
	const n = 8192
	acc := make([]float64, n)
	cnt := 0
	for off := 0; off+n <= len(iq); off += n / 2 {
		seg := make([]complex128, n)
		for i := 0; i < n; i++ {
			w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/n)
			seg[i] = complex(float64(real(iq[off+i]))*w, float64(imag(iq[off+i]))*w)
		}
		fftRec(seg)
		for i, v := range seg {
			acc[i] += real(v)*real(v) + imag(v)*imag(v)
		}
		cnt++
	}
	for i := range acc {
		acc[i] /= float64(cnt)
	}
	return acc
}

func peakOffset(iq []complex64, rate int) float64 {
	spec := welch(iq)
	srt := append([]float64{}, spec...)
	sort.Float64s(srt)
	th := srt[len(srt)/2] * 100 // 20 dB over median
	bestLo, bestHi, cur := -1, -1, -1
	binHz := float64(rate) / float64(len(spec))
	guard := int(3000 / binHz) // ignore only the DC spur region
	for i := guard; i < len(spec)-guard; i++ {
		if spec[i] > th {
			if cur < 0 {
				cur = i
			}
		} else if cur >= 0 {
			if bestLo < 0 || i-cur > bestHi-bestLo {
				bestLo, bestHi = cur, i
			}
			cur = -1
		}
	}
	if cur >= 0 && (bestLo < 0 || len(spec)-guard-cur > bestHi-bestLo) {
		bestLo, bestHi = cur, len(spec)-guard
	}
	if bestLo < 0 {
		return 0
	}
	// Power-weighted centroid of the blob: for FM the spectral centroid
	// tracks the carrier, the geometric midpoint does not (asymmetric MPX).
	var wsum, isum float64
	for i := bestLo; i < bestHi; i++ {
		wsum += spec[i]
		isum += spec[i] * float64(i)
	}
	center := isum / wsum
	off := center * binHz
	if center > float64(len(spec))/2 {
		off -= float64(rate)
	}
	return off
}

// resample linear from inRate to 48000.
func resample(x []float64, inRate int) []float64 {
	ratio := float64(inRate) / 48000.0
	n := int(float64(len(x)) / ratio)
	out := make([]float64, n)
	for i := range out {
		pos := float64(i) * ratio
		j := int(pos)
		frac := pos - float64(j)
		if j+1 < len(x) {
			out[i] = x[j]*(1-frac) + x[j+1]*frac
		} else if j < len(x) {
			out[i] = x[j]
		}
	}
	return out
}

func writeWAV(path string, l, r []float64, rate int) error {
	n := len(l)
	if len(r) < n {
		n = len(r)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataLen := uint32(n * 2 * 2)
	hdr := []byte("RIFF")
	hdr = binary.LittleEndian.AppendUint32(hdr, 36+dataLen)
	hdr = append(hdr, []byte("WAVEfmt ")...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 16)
	hdr = binary.LittleEndian.AppendUint16(hdr, 1) // PCM
	hdr = binary.LittleEndian.AppendUint16(hdr, 2) // stereo
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(rate))
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(rate*4))
	hdr = binary.LittleEndian.AppendUint16(hdr, 4)
	hdr = binary.LittleEndian.AppendUint16(hdr, 16)
	hdr = append(hdr, []byte("data")...)
	hdr = binary.LittleEndian.AppendUint32(hdr, dataLen)
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	var b [4]byte
	// peak-normalize to 0.8 full scale
	peak := 1e-9
	for i := 0; i < n; i++ {
		if a := math.Abs(l[i]); a > peak {
			peak = a
		}
		if a := math.Abs(r[i]); a > peak {
			peak = a
		}
	}
	gain := 0.8 / peak
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[0:2], uint16(int16(math.Max(-32768, math.Min(32767, l[i]*gain*32767)))))
		binary.LittleEndian.PutUint16(b[2:4], uint16(int16(math.Max(-32768, math.Min(32767, r[i]*gain*32767)))))
		if _, err := f.Write(b[:]); err != nil {
			return err
		}
	}
	return nil
}

// SDRSTREAM_WFM_LISTEN="capture.f32,rate[,offsetHz|auto][,tauSec]" and
// SDRSTREAM_WFM_LISTEN_OUT=/path/out.wav
func TestWFMListenChain(t *testing.T) {
	spec := os.Getenv("SDRSTREAM_WFM_LISTEN")
	out := os.Getenv("SDRSTREAM_WFM_LISTEN_OUT")
	if spec == "" || out == "" {
		t.Skip("set SDRSTREAM_WFM_LISTEN and SDRSTREAM_WFM_LISTEN_OUT")
	}
	parts := strings.Split(spec, ",")
	iq, err := readF32IQ(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	rate, _ := strconv.Atoi(parts[1])
	offset := 0.0
	if len(parts) < 3 || parts[2] == "auto" {
		offset = peakOffset(iq, rate)
	} else {
		offset, _ = strconv.ParseFloat(parts[2], 64)
	}
	tau := 75e-6
	if len(parts) > 3 {
		tau, _ = strconv.ParseFloat(parts[3], 64)
	}
	d := iqdemod.NewWFM(rate)
	if d == nil {
		t.Fatalf("NewWFM nil at %d", rate)
	}
	// tune to the measured offset: use New with WFM mode and the same rate
	// selection NewWFM made
	mpxRate := rate / (rate / 192000)
	d = iqdemod.New(rate, mpxRate, offset, iqdemod.WFM)
	var mpx []float64
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		mpx = d.Process(iq[off:end], mpx)
	}
	// stereo per ~0.5s chunk
	chunk := mpxRate / 2
	var L, R []float64
	var locks, chunks int
	var snrSum float64
	phases := make([]float64, 0, 32)
	snrs := make([]float64, 0, 32)
	for off := 0; off+chunk <= len(mpx); off += chunk {
		res := decode.DecodeFMStereo(mpx[off:off+chunk], mpxRate)
		chunks++
		snrSum += res.PilotSNRdB
		phases = append(phases, res.PilotPhase)
		snrs = append(snrs, res.PilotSNRdB)
		if res.PilotLocked {
			locks++
		}
		L = append(L, res.L...)
		R = append(R, res.R...)
	}
	// Circular phase spread across chunks: high spread means the 38 kHz
	// subcarrier reference is unstable (gap/clock damage), which alone can
	// erase the stereo difference; low spread points at mono content.
	var csum, ssum float64
	for _, p := range phases {
		csum += math.Cos(p)
		ssum += math.Sin(p)
	}
	rbar := math.Hypot(csum, ssum) / float64(len(phases)) // 1 = identical phase
	t.Logf("pilot phase coherence across chunks: R=%.3f (1.0 stable, ~0 unstable); phases=%.2f snrs=%.1f", rbar, phases, snrs)
	L = iqdemod.Deemphasis(L, mpxRate, tau)
	R = iqdemod.Deemphasis(R, mpxRate, tau)
	l48, r48 := resample(L, mpxRate), resample(R, mpxRate)
	if err := writeWAV(out, l48, r48, 48000); err != nil {
		t.Fatal(err)
	}
	// stereo separation: correlation between L and R (0 = fully separated)
	var sxy, sxx, syy float64
	for i := range l48 {
		sxy += l48[i] * r48[i]
		sxx += l48[i] * l48[i]
		syy += r48[i] * r48[i]
	}
	corr := sxy / (math.Sqrt(sxx*syy) + 1e-20)
	t.Logf("offset=%.0fHz mpxRate=%d chunks=%d locked=%d avgPilotSNR=%.1fdB corr(L,R)=%.3f wav=%s (%.1fs)",
		offset, mpxRate, chunks, locks, snrSum/math.Max(1, float64(chunks)), corr, out, float64(len(l48))/48000)
}
