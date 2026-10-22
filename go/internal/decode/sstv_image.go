package decode

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// SSTV image decoding. The signal is FM: 1500 Hz is black, 2300 Hz is
// white, 1200 Hz is the line sync. Decoding demodulates the whole clip to
// an instantaneous-frequency trace once, then re-anchors on each line's
// 1200 Hz sync pulse, so a constant clock offset (sound card or receiver
// ppm error) cannot shear the picture and a wrong line length cannot
// accumulate.
//
// Supported modes: Martin M1/M2 and Scottie S1 (sequential RGB, green
// first), and Robot 36 (YUV 4:2:0, the ARISS ISS mode). Mode timings
// follow the G3OQD / GM3SBC / Robot Research specifications as encoded by
// the independent pysstv encoder and the QSSTV decoder tables; this file
// implements them from the published line schedules.
//
// Frequencies shared by every mode.
const (
	sstvFreqBlack = 1500.0
	sstvFreqWhite = 2300.0
	sstvFreqSync  = 1200.0
)

// sstvFamily selects the line layout.
type sstvFamily int

const (
	familyMartin sstvFamily = iota
	familyScottie
	familyRobot36
)

// sstvModeSpec is one decodable mode's line schedule.
type sstvModeSpec struct {
	name         string
	vis          int
	family       sstvFamily
	width        int
	height       int
	syncSec      float64
	gapSec       float64 // separator/porch at 1500 Hz between channels
	scanSec      float64 // per-channel scan (Martin/Scottie)
	firstSyncSec float64 // offset of the first sync pulse after the VIS header
	// Robot 36 only:
	yScanSec      float64
	cScanSec      float64
	syncPorchSec  float64
	sepSec        float64
	porchSec      float64
	sepFreqEven   float64 // separator tone naming the chroma on even lines
	sepFreqOdd    float64
	scottieSyncAt float64 // Scottie: sync offset from line start
}

var sstvModeSpecs = map[int]sstvModeSpec{
	0x2C: {name: "Martin M1", vis: 0x2C, family: familyMartin, width: 320, height: 256,
		syncSec: 4.862e-3, gapSec: 0.572e-3, scanSec: 146.432e-3},
	0x28: {name: "Martin M2", vis: 0x28, family: familyMartin, width: 320, height: 256,
		syncSec: 4.862e-3, gapSec: 0.572e-3, scanSec: 73.216e-3},
	0x3C: {name: "Scottie S1", vis: 0x3C, family: familyScottie, width: 320, height: 256,
		syncSec: 9e-3, gapSec: 1.5e-3, scanSec: 136.74e-3, firstSyncSec: 279.48e-3, scottieSyncAt: 279.48e-3},
	0x08: {name: "Robot 36", vis: 0x08, family: familyRobot36, width: 320, height: 240,
		syncSec: 9e-3, syncPorchSec: 3e-3, yScanSec: 88e-3, sepSec: 4.5e-3,
		porchSec: 1.5e-3, cScanSec: 44e-3, sepFreqEven: 1500, sepFreqOdd: 2300},
}

// lineSec returns the nominal line time.
func (m sstvModeSpec) lineSec() float64 {
	switch m.family {
	case familyMartin:
		return m.syncSec + m.gapSec + 3*(m.scanSec+m.gapSec)
	case familyScottie:
		return 3*(m.scanSec+2*m.gapSec) + m.syncSec + m.gapSec
	case familyRobot36:
		return m.syncSec + m.syncPorchSec + m.yScanSec + m.sepSec + m.porchSec + m.cScanSec
	}
	return 0
}

// fmDemod converts audio to an instantaneous-frequency trace (Hz) of the
// same length: mix down around 1900 Hz, lowpass with a short boxcar, and
// differentiate the phase of the resulting I/Q pair. Per-pixel windows at
// 8 kHz are shorter than one tone cycle, so tone-by-Goertzel estimation
// cannot work there; phase differentiation plus averaging can.
func fmDemod(samples []float64, rate int) []float64 {
	n := len(samples)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	const fc = 1900.0
	w := int(0.001 * float64(rate)) // 1 ms I/Q lowpass window
	if w < 2 {
		w = 2
	}
	// I/Q with a running boxcar sum. Raw products stay in raw (the
	// running sum subtracts them); the averaged pair lands in iq.
	raw := make([][2]float64, n)
	iq := make([][2]float64, n)
	wc := 2 * math.Pi * fc / float64(rate)
	var sumI, sumQ float64
	for i, s := range samples {
		c, sn := math.Cos(wc*float64(i)), math.Sin(wc*float64(i))
		iv, qv := s*c, -s*sn
		raw[i][0], raw[i][1] = iv, qv
		sumI += iv
		sumQ += qv
		if i-w >= 0 {
			sumI -= raw[i-w][0]
			sumQ -= raw[i-w][1]
		}
		if i >= w-1 {
			iq[i][0] = sumI / float64(w)
			iq[i][1] = sumQ / float64(w)
		}
	}
	scale := float64(rate) / (2 * math.Pi * 2)
	for i := w + 1; i < n; i++ {
		i0, q0 := iq[i-2][0], iq[i-2][1]
		i1, q1 := iq[i][0], iq[i][1]
		den := i0*i1 + q0*q1
		f := fc
		if den > 1e-12 {
			// Phase increment over two samples.
			cross := i0*q1 - q0*i1
			f = fc + math.Atan2(cross, den)*scale
		}
		out[i] = math.Max(900, math.Min(2700, f))
	}
	for i := 0; i < w && i < n; i++ {
		out[i] = out[min(w, n-1)]
	}
	// A second short boxcar on the frequency trace tames single-sample
	// phase spikes without smearing 5 ms sync edges.
	w2 := int(0.0008 * float64(rate))
	if w2 < 2 {
		return out
	}
	smooth := make([]float64, n)
	var acc float64
	for i := 0; i < n; i++ {
		acc += out[i]
		if i-w2 >= 0 {
			acc -= out[i-w2]
		}
		if i >= w2-1 {
			smooth[i] = acc / float64(w2)
		} else {
			smooth[i] = out[i]
		}
	}
	return smooth
}

// findSyncs locates line-sync pulses, starting near the expected
// position and re-anchoring on every line so clock drift cannot
// accumulate. Each sync is first found coarsely on a 2 ms-hop 1200 Hz
// purity trace, then refined to sample precision by maximizing the
// sync-length window's 1200 Hz purity against the video band. Lines
// whose sync cannot be found keep their expected position (coasting).
func findSyncs(samples []float64, rate int, m sstvModeSpec, imageStartSec float64) []float64 {
	const hopSec, winSec = 0.002, 0.008
	p1200 := tonePurity(samples, rate, sstvFreqSync, winSec, hopSec)
	syncs := make([]float64, m.height)
	lineSec := m.lineSec()
	expected := imageStartSec + m.firstSyncSec
	search := 0.012 // +/-12 ms search window
	if lineSec*0.1 < search {
		search = lineSec * 0.1
	}
	for y := 0; y < m.height; y++ {
		// Line 0 with the sync at the line start follows the VIS stop
		// bit directly, and the two form one continuous 1200 Hz
		// plateau the search cannot split. The header end is known to
		// the millisecond, so anchor line 0 to it and let the search
		// re-anchor from line 1 on, where syncs are isolated.
		if y == 0 && m.firstSyncSec == 0 {
			syncs[0] = imageStartSec
			expected = imageStartSec + lineSec
			continue
		}
		coarse := -1.0
		best := 0.45
		fromH := int((expected - search) / hopSec)
		toH := int((expected + search) / hopSec)
		// The first sync cannot precede the image; this keeps the VIS
		// stop bit (30 ms of 1200 Hz ending exactly at imageStartSec)
		// out of the line-0 search window.
		if minH := int((imageStartSec - 0.001) / hopSec); fromH < minH {
			fromH = minH
		}
		if fromH < 0 {
			fromH = 0
		}
		for h := fromH; h < toH && h < len(p1200); h++ {
			if p1200[h] > best {
				best = p1200[h]
				coarse = float64(h)*hopSec + winSec/2 // centre of the hot window
			}
		}
		if coarse < 0 {
			syncs[y] = expected
			expected += lineSec
			continue
		}
		// Sample-precise refinement: slide a sync-length window and
		// maximise sync purity against the 1500/1900 Hz competition.
		winN := int(m.syncSec * float64(rate))
		if winN < 4 {
			winN = 4
		}
		fromS := int((coarse-m.syncSec/2)*float64(rate)) - int(0.004*float64(rate))
		toS := int((coarse-m.syncSec/2)*float64(rate)) + int(0.004*float64(rate))
		bestPos, bestScore := -1, -1.0
		for p := fromS; p <= toS; p++ {
			if p < 0 || p+winN > len(samples) {
				continue
			}
			seg := samples[p : p+winN]
			var power float64
			for _, v := range seg {
				power += v * v
			}
			if power <= 0 {
				continue
			}
			g := goertzel(seg, rate, sstvFreqSync)
			score := 2*g*g/(float64(winN)*power) -
				0.5*(purityOf(seg, rate, 1500)+purityOf(seg, rate, 1900))
			if score > bestScore {
				bestScore = score
				bestPos = p
			}
		}
		if bestPos < 0 || bestScore < 0.25 {
			syncs[y] = expected
			expected += lineSec
			continue
		}
		syncs[y] = float64(bestPos) / float64(rate)
		expected = syncs[y] + lineSec
	}
	return syncs
}

func purityOf(seg []float64, rate int, freq float64) float64 {
	var power float64
	for _, v := range seg {
		power += v * v
	}
	if power <= 0 {
		return 0
	}
	g := goertzel(seg, rate, freq)
	return 2 * g * g / (float64(len(seg)) * power)
}

// sampleChannel averages the demodulated frequency over width pixel
// windows starting at startSec and maps 1500-2300 Hz to 0-255.
func sampleChannel(freq []float64, rate int, startSec, scanSec float64, width int, dst []uint8) bool {
	step := scanSec / float64(width)
	// Fast modes give a pixel well under 1 ms of audio, where a lone
	// demodulated sample is noisy; average over at least ~0.8 ms.
	half := step / 2
	if half < 0.0004 {
		half = 0.0004
	}
	complete := true
	for x := 0; x < width; x++ {
		center := startSec + (float64(x)+0.5)*step
		from := int((center - half) * float64(rate))
		to := int((center+half)*float64(rate) + 0.5)
		if from >= len(freq) {
			complete = false
			break
		}
		if from < 0 {
			from = 0
		}
		if to > len(freq) {
			to = len(freq)
			complete = false
		}
		if to <= from {
			to = from + 1
		}
		var sum float64
		for k := from; k < to; k++ {
			sum += freq[k]
		}
		f := sum / float64(to-from)
		v := math.Round((f - sstvFreqBlack) * 255 / (sstvFreqWhite - sstvFreqBlack))
		dst[x] = uint8(math.Max(0, math.Min(255, v)))
	}
	return complete
}

// SSTVImage decodes the first complete SSTV frame in samples into a PNG
// image. The VIS header picks the mode; unsupported modes and frames
// with under 80% of their lines present in the audio return an error.
func SSTVImage(samples []float64, rate int) ([]byte, error) {
	if rate < 8000 {
		return nil, fmt.Errorf("SSTV sample rate %d is too low", rate)
	}
	vis := SSTVDetect(samples, rate)
	if len(vis) == 0 {
		return nil, fmt.Errorf("no SSTV VIS header found")
	}
	spec, ok := sstvModeSpecs[vis[0].VIS]
	if !ok {
		return nil, fmt.Errorf("unsupported SSTV mode %s (VIS 0x%02X)", vis[0].Mode, vis[0].VIS)
	}
	// StartSec names the 1200 Hz break inside the header; image data
	// follows the break, the second leader and ten 30 ms VIS bits.
	imageStart := vis[0].StartSec + 0.010 + 0.300 + 10*sstvBitLen
	freq := fmDemod(samples, rate)
	syncs := findSyncs(samples, rate, spec, imageStart)

	img := image.NewNRGBA(image.Rect(0, 0, spec.width, spec.height))
	decoded := 0
	switch spec.family {
	case familyMartin:
		decoded = decodeMartin(freq, rate, spec, syncs, img)
	case familyScottie:
		decoded = decodeScottie(freq, rate, spec, syncs, img)
	case familyRobot36:
		decoded = decodeRobot36(freq, rate, spec, syncs, img)
	}
	if float64(decoded) < 0.8*float64(spec.height) {
		return nil, fmt.Errorf("truncated %s frame: %d of %d lines present (need 80%%)",
			spec.name, decoded, spec.height)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodeMartin(freq []float64, rate int, m sstvModeSpec, syncs []float64, img *image.NRGBA) int {
	g := make([]uint8, m.width)
	b := make([]uint8, m.width)
	r := make([]uint8, m.width)
	decoded := 0
	for y := 0; y < m.height; y++ {
		s := syncs[y]
		gStart := s + m.syncSec + m.gapSec
		bStart := gStart + m.scanSec + m.gapSec
		rStart := bStart + m.scanSec + m.gapSec
		okG := sampleChannel(freq, rate, gStart, m.scanSec, m.width, g)
		okB := sampleChannel(freq, rate, bStart, m.scanSec, m.width, b)
		okR := sampleChannel(freq, rate, rStart, m.scanSec, m.width, r)
		for x := 0; x < m.width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: r[x], G: g[x], B: b[x], A: 255})
		}
		if okG && okB && okR {
			decoded++
		}
	}
	return decoded
}

// decodeScottie handles the Scottie layout, where the sync pulse sits
// between the blue and red scans: for display line y the red scan follows
// sync y while green and blue precede it.
func decodeScottie(freq []float64, rate int, m sstvModeSpec, syncs []float64, img *image.NRGBA) int {
	g := make([]uint8, m.width)
	b := make([]uint8, m.width)
	r := make([]uint8, m.width)
	decoded := 0
	for y := 0; y < m.height; y++ {
		s := syncs[y]
		rStart := s + m.syncSec + m.gapSec
		// Line start is one gap before green; green then blue, each
		// followed by a gap, with a further gap before the sync.
		gStart := s - m.scottieSyncAt + m.gapSec
		bStart := gStart + m.scanSec + 2*m.gapSec
		okR := sampleChannel(freq, rate, rStart, m.scanSec, m.width, r)
		okG := sampleChannel(freq, rate, gStart, m.scanSec, m.width, g)
		okB := sampleChannel(freq, rate, bStart, m.scanSec, m.width, b)
		for x := 0; x < m.width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: r[x], G: g[x], B: b[x], A: 255})
		}
		if okG && okB && okR {
			decoded++
		}
	}
	return decoded
}

// decodeRobot36 handles the YUV 4:2:0 Robot 36 layout: per line a luma
// scan, then a half-length chroma scan alternating Cr (even lines, 1500 Hz
// separator) and Cb (odd lines, 2300 Hz separator).
func decodeRobot36(freq []float64, rate int, m sstvModeSpec, syncs []float64, img *image.NRGBA) int {
	yPlane := make([][]uint8, m.height)
	cPlane := make([][]uint8, m.height)
	decoded := 0
	for y := 0; y < m.height; y++ {
		s := syncs[y]
		yScan := make([]uint8, m.width)
		cScan := make([]uint8, m.width)
		yStart := s + m.syncSec + m.syncPorchSec
		cStart := yStart + m.yScanSec + m.sepSec + m.porchSec
		okY := sampleChannel(freq, rate, yStart, m.yScanSec, m.width, yScan)
		okC := sampleChannel(freq, rate, cStart, m.cScanSec, m.width, cScan)
		yPlane[y] = yScan
		cPlane[y] = cScan
		if okY && okC {
			decoded++
		}
	}
	for y := 0; y < m.height; y++ {
		crLine, cbLine := y, y
		if y%2 == 0 {
			cbLine = min(y+1, m.height-1)
		} else {
			crLine = y - 1
		}
		for x := 0; x < m.width; x++ {
			yv := float64(yPlane[y][x])
			cb := float64(cPlane[cbLine][x]) - 128
			cr := float64(cPlane[crLine][x]) - 128
			r := yv + 1.402*cr
			g := yv - 0.344136*cb - 0.714136*cr
			b := yv + 1.772*cb
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Max(0, math.Min(255, math.Round(r)))),
				G: uint8(math.Max(0, math.Min(255, math.Round(g)))),
				B: uint8(math.Max(0, math.Min(255, math.Round(b)))),
				A: 255,
			})
		}
	}
	return decoded
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
