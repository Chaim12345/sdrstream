package decode

// dispatcher.go: one entry point that connects demodulated streams to the
// deframer primitives (AX.25 HDLC, CCSDS ASM/RS, AX100 RS-mode, BPSK/AFSK
// audio demodulators) and reports every candidate's outcome, not only the
// frames that passed. The primitive deframers (CCSDSDeframe,
// AX100RSDeframe, hdlcFrames) silently drop failing candidates; this file
// mirrors their checks with the same package internals so a caller can
// report "3 candidates, 1 accepted (RS-verified), 2 rejected (RS
// uncorrectable, truncated)" instead of "1 frame". Consistency between the
// accounting paths here and the primitives is pinned by dispatcher_test.go.
//
// Provenance honesty: an accepted frame has passed the named check
// (FrameOutcome.Verified), which proves self-consistency against the
// check, not interoperability with real over-the-air signals. No
// over-the-air vectors exist for these paths; see HANDOFF and the test
// file for what is synthetic.

import (
	"fmt"
	"strings"
)

// Verification labels for FrameOutcome.Verified.
const (
	VerifyCRC16AX25   = "crc16-ax25"
	VerifyRSCCSDS     = "rs(255,223)-ccsds-conventional"
	VerifyRSCCSDSDual = "rs(255,223)-ccsds-dual"
	VerifyRSAX100     = "rs(255,223)-shortened-ax100"
)

// Rejection reasons for FrameOutcome.Reason.
const (
	RejectShort           = "shorter than an address+control+FCS"
	RejectDestuff         = "bit de-stuffing failed"
	RejectCRC             = "crc16 residue mismatch"
	RejectTruncated       = "frame runs past the end of the stream"
	RejectBadLength       = "invalid length field"
	RejectRSUncorrectable = "reed-solomon: uncorrectable"
)

// FrameOutcome is the fate of one candidate frame found in a stream:
// accepted with the check that passed it, or rejected with the reason.
type FrameOutcome struct {
	Protocol  string `json:"protocol"`
	Offset    int    `json:"offset"` // bit offset of the candidate in the dispatched stream
	Accepted  bool   `json:"accepted"`
	Verified  string `json:"verified,omitempty"`  // check that accepted it
	Reason    string `json:"reason,omitempty"`    // why it was rejected
	Corrected int    `json:"corrected,omitempty"` // RS symbol errors fixed
	Inverted  bool   `json:"inverted,omitempty"`  // stream polarity was flipped (CCSDS)
	Payload   []byte `json:"payload,omitempty"`
}

// DispatchReport is the full accounting of one DispatchBits/DispatchSamples
// call: every candidate, accepted or rejected, in stream order.
type DispatchReport struct {
	Protocol string         `json:"protocol"`
	Bits     int            `json:"bits"`
	Frames   []FrameOutcome `json:"frames"`
	// Carrier is the BPSK carrier (Hz) the audio path locked to, 0 when
	// the input was already a bit stream.
	Carrier float64 `json:"carrier_hz,omitempty"`
}

// Accepted returns the accepted frames.
func (r DispatchReport) Accepted() []FrameOutcome {
	var out []FrameOutcome
	for _, f := range r.Frames {
		if f.Accepted {
			out = append(out, f)
		}
	}
	return out
}

// Rejected returns the rejected candidates.
func (r DispatchReport) Rejected() []FrameOutcome {
	var out []FrameOutcome
	for _, f := range r.Frames {
		if !f.Accepted {
			out = append(out, f)
		}
	}
	return out
}

// Summary renders a one-line honest accounting for logs and ledgers.
func (r DispatchReport) Summary() string {
	acc := len(r.Accepted())
	rej := r.Rejected()
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d candidates, %d accepted", r.Protocol, len(r.Frames), acc)
	if len(rej) > 0 {
		counts := map[string]int{}
		var order []string
		for _, f := range rej {
			if _, ok := counts[f.Reason]; !ok {
				order = append(order, f.Reason)
			}
			counts[f.Reason]++
		}
		fmt.Fprintf(&b, ", %d rejected (", len(rej))
		for i, reason := range order {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s x%d", reason, counts[reason])
		}
		b.WriteString(")")
	}
	return b.String()
}

// DispatchOptions tunes the per-protocol paths. Zero values pick the same
// defaults the primitive deframers use.
type DispatchOptions struct {
	// CCSDS: frame data bytes (<= 223*Interleave), interleave depth, dual
	// basis, ASM bit-error allowance.
	DataLen    int
	Interleave int
	Dual       bool
	ASMMaxErr  int
	// AX100: sync-word bit-error allowance (default 2).
	SyncErr int
	// BPSK audio path: symbol rate (default 1200) and carrier search
	// window (default 300..16000 Hz).
	Baud      float64
	CarrierLo float64
	CarrierHi float64
}

func (o *DispatchOptions) defaults() {
	if o.Interleave < 1 {
		o.Interleave = 1
	}
	if o.ASMMaxErr == 0 {
		o.ASMMaxErr = 2
	}
	if o.SyncErr == 0 {
		o.SyncErr = 2
	}
	if o.Baud == 0 {
		o.Baud = 1200
	}
	if o.CarrierLo == 0 && o.CarrierHi == 0 {
		o.CarrierLo, o.CarrierHi = 300, 16000
	}
}

// DispatchBits routes one hard-bit stream to the deframer for protocol
// ("ax25", "ccsds", "ax100") and returns every candidate's outcome.
func DispatchBits(protocol string, bits []bool, opts DispatchOptions) (DispatchReport, error) {
	opts.defaults()
	rep := DispatchReport{Protocol: protocol, Bits: len(bits)}
	switch strings.ToLower(protocol) {
	case "ax25":
		rep.Frames = hdlcAccounted(bits)
	case "ccsds":
		if opts.DataLen <= 0 || opts.DataLen > 223*opts.Interleave {
			return rep, fmt.Errorf("ccsds: DataLen must be 1..%d for interleave %d", 223*opts.Interleave, opts.Interleave)
		}
		rep.Frames = ccsdsAccounted(bits, opts)
	case "ax100":
		rep.Frames = ax100Accounted(bits, opts.SyncErr)
	default:
		return rep, fmt.Errorf("unknown bit-stream protocol %q (have: ax25, ccsds, ax100)", protocol)
	}
	return rep, nil
}

// DispatchSamples routes one audio buffer to the demodulator and deframer
// for protocol ("afsk-ax25", "bpsk-ax25", "bpsk-ccsds", "bpsk-ax100") and
// returns every candidate's outcome. Only demodulators that exist in this
// package are wired; anything else is an error, not a silent empty result.
func DispatchSamples(protocol string, samples []float64, rate int, opts DispatchOptions) (DispatchReport, error) {
	opts.defaults()
	switch strings.ToLower(protocol) {
	case "afsk-ax25":
		rep, err := DispatchBits("ax25", fskDemod(samples, rate), opts)
		rep.Protocol = protocol
		return rep, err
	case "bpsk-ax25", "bpsk-ccsds", "bpsk-ax100":
		bits, carrier, ok := BPSKBitStream(samples, rate, opts.Baud, [2]float64{opts.CarrierLo, opts.CarrierHi})
		if !ok {
			return DispatchReport{Protocol: protocol}, fmt.Errorf("%s: no BPSK carrier locked in %.0f..%.0f Hz at %.0f baud", protocol, opts.CarrierLo, opts.CarrierHi, opts.Baud)
		}
		rep, err := DispatchBits(strings.TrimPrefix(strings.ToLower(protocol), "bpsk-"), bits, opts)
		rep.Protocol = protocol
		rep.Carrier = carrier
		return rep, err
	default:
		return DispatchReport{Protocol: protocol}, fmt.Errorf("unknown audio protocol %q (have: afsk-ax25, bpsk-ax25, bpsk-ccsds, bpsk-ax100)", protocol)
	}
}

// BPSKBitStream demodulates a BPSK audio-IF buffer to a hard-bit stream:
// the carrier is measured from the signal over loHi Hz and both
// constellation polarities are tried (the downstream CRC/RS check picks
// the winner; NRZI decoding inside bpskBits makes polarities equivalent
// for NRZI links). Returns the locked carrier and false when no candidate
// carrier produced bits above the energy gate.
func BPSKBitStream(samples []float64, rate int, baud float64, loHi [2]float64) (bits []bool, carrier float64, ok bool) {
	if loHi == [2]float64{} {
		loHi = [2]float64{300, 16000}
	}
	step := (loHi[1] - loHi[0]) / 1500
	if step < 4 {
		step = 4
	}
	freqs, powers := squaredProfile(samples, rate, 2*loHi[0], 2*loHi[1], step)
	if len(freqs) == 0 {
		return nil, 0, false
	}
	for _, c := range carrierCandidates(freqs, powers, 6) {
		if c+baud/2+100 >= float64(rate)/2 {
			continue
		}
		for _, invert := range []bool{false, true} {
			if b := bpskBits(samples, rate, c, baud, invert); len(b) > 0 {
				return b, c, true
			}
		}
	}
	return nil, 0, false
}

// hdlcAccounted mirrors hdlcFrames (afsk.go) but reports every
// flag-delimited candidate: accepted frames passed the AX.25 FCS residue
// check; rejected ones carry the stage that failed. The checks are the
// same package internals (destuff, crc16, 0xF0B8 residue).
func hdlcAccounted(bits []bool) []FrameOutcome {
	var out []FrameOutcome
	var win uint16
	var data []bool
	start := -1
	seenFlag := false

	flush := func(end int) {
		if start < 0 {
			return
		}
		cand := FrameOutcome{Protocol: "ax25", Offset: start}
		switch {
		case len(data) < 24:
			cand.Reason = RejectShort
		default:
			f := destuff(data)
			switch {
			case f == nil || len(f) < 3:
				cand.Reason = RejectDestuff
			case crc16(f) != 0xF0B8:
				cand.Reason = RejectCRC
			default:
				cand.Accepted = true
				cand.Verified = VerifyCRC16AX25
				cand.Payload = f
			}
		}
		out = append(out, cand)
		data = data[:0]
		_ = end
	}

	for i, bit := range bits {
		win = ((win << 1) | uint16(bitValue(bit))) & 0xFF
		if win == 0x7E {
			if seenFlag {
				if len(data) >= 7 {
					data = data[:len(data)-7]
				} else {
					data = data[:0]
				}
				flush(i)
			}
			seenFlag = true
			start = i + 1
			continue
		}
		if seenFlag {
			data = append(data, bit)
		}
	}
	return out
}

// ccsdsAccounted mirrors CCSDSDeframe (deframe.go) with per-candidate
// accounting: each ASM hit is accepted (every interleaved RS codeword
// decoded) or rejected (frame truncated, or a codeword uncorrectable).
func ccsdsAccounted(bits []bool, opts DispatchOptions) []FrameOutcome {
	per := opts.DataLen / opts.Interleave
	cwLen := per + rsNroots
	total := cwLen * opts.Interleave
	verify := VerifyRSCCSDS
	if opts.Dual {
		verify = VerifyRSCCSDSDual
	}
	var out []FrameOutcome
	for _, h := range FindASM(bits, opts.ASMMaxErr) {
		cand := FrameOutcome{Protocol: "ccsds", Offset: h.Offset, Inverted: h.Inverted}
		start := h.Offset + 32
		if start+total*8 > len(bits) {
			cand.Reason = RejectTruncated
			out = append(out, cand)
			continue
		}
		raw := make([]byte, total)
		for i := 0; i < total*8; i++ {
			v := bits[start+i]
			if h.Inverted {
				v = !v
			}
			if v {
				raw[i/8] |= 0x80 >> uint(i%8)
			}
		}
		CCSDSDerandomize(raw)
		cws := make([][]byte, opts.Interleave)
		for i := range cws {
			cws[i] = make([]byte, cwLen)
		}
		for i, b := range raw {
			cws[i%opts.Interleave][i/opts.Interleave] = b
		}
		ok := true
		for _, cw := range cws {
			if opts.Dual {
				for i := range cw {
					cw[i] = dualToConv[cw[i]]
				}
			}
			n, err := RSDecode(cw)
			if err != nil {
				cand.Reason = RejectRSUncorrectable
				ok = false
				break
			}
			cand.Corrected += n
		}
		if !ok {
			out = append(out, cand)
			continue
		}
		data := make([]byte, 0, opts.DataLen)
		for j := 0; j < per; j++ {
			for i := 0; i < opts.Interleave; i++ {
				data = append(data, cws[i][j])
			}
		}
		if opts.Dual {
			for i := range data {
				data[i] = convToDual[data[i]]
			}
		}
		cand.Accepted = true
		cand.Verified = verify
		cand.Payload = data
		out = append(out, cand)
	}
	return out
}

// ax100Accounted mirrors AX100RSDeframe (ax100.go) with per-candidate
// accounting: each sync-word match is accepted (shortened RS decoded) or
// rejected (bad length, truncated, or RS uncorrectable).
func ax100Accounted(bits []bool, syncErr int) []FrameOutcome {
	d := G3RUHDescramble(bits)
	var out []FrameOutcome
	for off := 0; off+40 <= len(d); off++ {
		var w uint32
		for i := 0; i < 32; i++ {
			w <<= 1
			if d[off+i] {
				w |= 1
			}
		}
		if popcount32(w^AX100Sync) > syncErr {
			continue
		}
		cand := FrameOutcome{Protocol: "ax100", Offset: off}
		p := off + 32
		var L int
		for i := 0; i < 8; i++ {
			L <<= 1
			if d[p+i] {
				L |= 1
			}
		}
		switch {
		case L < rsNroots+2 || L > 255:
			cand.Reason = RejectBadLength
		case p+8+(L-1)*8 > len(d):
			cand.Reason = RejectTruncated
		default:
			cw := make([]byte, L-1)
			for i := range cw {
				for b := 0; b < 8; b++ {
					cw[i] <<= 1
					if d[p+8+i*8+b] {
						cw[i] |= 1
					}
				}
			}
			n, err := RSDecode(cw)
			if err != nil {
				cand.Reason = RejectRSUncorrectable
			} else {
				cand.Accepted = true
				cand.Verified = VerifyRSAX100
				cand.Corrected = n
				cand.Payload = cw[:len(cw)-rsNroots]
			}
		}
		out = append(out, cand)
	}
	return out
}
