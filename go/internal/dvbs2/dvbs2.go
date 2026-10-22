// Package dvbs2 carves TFTP DATA packets out of a raw capture and
// reassembles them, as used by the "Don't Look Up" high-rate data feed.
//
// The two documented layouts carry 512 or 1024 data bytes in 516- or
// 1028-byte frames. Sequence numbers are unsigned 16-bit and wrap.
package dvbs2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// Payload sizes of the two documented layouts. The constants are named after
// the total frame sizes, which is how the feed labels them, so note the
// four-byte header: BLOCK516 frames are 512 bytes of data inside 516 bytes.
const (
	Block516  = 512
	Block1028 = 1024

	Frame516  = Block516 + 4
	Frame1028 = Block1028 + 4

	// Sequence numbers are unsigned 16-bit, so they wrap.
	Modulus = 1 << 16

	HeaderSize = 4
)

// TftpData is one TFTP DATA packet: opcode, block number and payload.
type TftpData struct {
	Block  int
	Data   []byte
	Opcode int
}

// ParseTftpData reads a DATA packet, optionally insisting on an exact length.
func ParseTftpData(frame []byte, payloadSize int) (TftpData, error) {
	if len(frame) < HeaderSize {
		return TftpData{}, fmt.Errorf("frame shorter than a TFTP header")
	}
	opcode := int(binary.BigEndian.Uint16(frame[0:2]))
	block := int(binary.BigEndian.Uint16(frame[2:4]))
	if opcode != 3 {
		return TftpData{}, fmt.Errorf("not a TFTP DATA packet (opcode %d)", opcode)
	}
	data := frame[HeaderSize:]
	if payloadSize > 0 && len(data) != payloadSize {
		return TftpData{}, fmt.Errorf("expected %d data bytes, found %d",
			payloadSize, len(data))
	}
	return TftpData{Block: block, Data: data, Opcode: opcode}, nil
}

// Frame is a DATA packet found at a byte offset in the capture.
type Frame struct {
	Offset      int
	Block       int
	Data        []byte
	PayloadSize int
}

// Raw is the frame as it sits in the capture, header included.
func (f Frame) Raw() []byte {
	out := make([]byte, HeaderSize+len(f.Data))
	binary.BigEndian.PutUint16(out[0:2], 3)
	binary.BigEndian.PutUint16(out[2:4], uint16(f.Block))
	copy(out[4:], f.Data)
	return out
}

// End is the offset just past the frame.
func (f Frame) End() int { return f.Offset + HeaderSize + len(f.Data) }

// Summary renders one line.
func (f Frame) Summary() string {
	return fmt.Sprintf("offset %-9d block %-6d %d bytes", f.Offset, f.Block, len(f.Data))
}

// Carved is every DATA packet found in one capture, plus what was skipped.
type Carved struct {
	PayloadSize int
	Frames      []Frame
	// Gaps are byte ranges that matched nothing, as (start, end) pairs.
	Gaps [][2]int
	Size int
}

// Payload returns the payloads in capture order.
func (c Carved) Payload() []byte {
	var out bytes.Buffer
	for _, frame := range c.Frames {
		out.Write(frame.Data)
	}
	return out.Bytes()
}

// Ordered returns frames in block order, tolerating one wrap of the 16-bit
// counter. The wrap sits at the largest circular gap between consecutive
// block numbers, so ordering starts just after it; this works whatever order
// the frames arrived in.
func (c Carved) Ordered() []Frame {
	if len(c.Frames) == 0 {
		return nil
	}
	seen := map[int][]Frame{}
	blocks := []int{}
	for _, frame := range c.Frames {
		if _, ok := seen[frame.Block]; !ok {
			blocks = append(blocks, frame.Block)
		}
		seen[frame.Block] = append(seen[frame.Block], frame)
	}
	sort.Ints(blocks)
	if len(blocks) == 1 {
		return append([]Frame{}, seen[blocks[0]]...)
	}
	// Find the largest circular gap; the wrap sits within it.
	wrapAt := -1
	bestGap := -1
	for i := 0; i < len(blocks); i++ {
		next := blocks[(i+1)%len(blocks)]
		gap := (next - blocks[i] + Modulus) % Modulus
		if i == len(blocks)-1 {
			// The last-to-first gap crosses zero only if there is a wrap.
			if blocks[0] > blocks[len(blocks)-1] {
				gap = (blocks[0] - blocks[len(blocks)-1] + Modulus) % Modulus
			}
		}
		if gap > bestGap {
			bestGap = gap
			wrapAt = i
		}
	}
	// Order starting just after the largest gap.
	ordered := make([]Frame, 0, len(c.Frames))
	for i := 1; i <= len(blocks); i++ {
		block := blocks[(wrapAt+i)%len(blocks)]
		ordered = append(ordered, seen[block]...)
	}
	return ordered
}

// Reassembled returns the payload in block order.
func (c Carved) Reassembled() []byte {
	var out bytes.Buffer
	for _, frame := range c.Ordered() {
		out.Write(frame.Data)
	}
	return out.Bytes()
}

// IterFrames scans data for DATA packets of the given payload size. strict
// requires an exact payload length; otherwise a short final frame is kept.
func IterFrames(data []byte, payloadSize int, strict bool) Carved {
	carved := Carved{PayloadSize: payloadSize, Size: len(data)}
	offset := 0
	lastEnd := 0
	for offset+HeaderSize <= len(data) {
		opcode := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if opcode != 3 {
			offset++
			continue
		}
		block := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		end := offset + HeaderSize + payloadSize
		if end > len(data) {
			if strict {
				offset++
				continue
			}
			end = len(data)
		}
		payload := data[offset+HeaderSize : end]
		if len(payload) < payloadSize && strict {
			offset++
			continue
		}
		if offset > lastEnd {
			carved.Gaps = append(carved.Gaps, [2]int{lastEnd, offset})
		}
		carved.Frames = append(carved.Frames, Frame{
			Offset: offset, Block: block, Data: payload, PayloadSize: payloadSize})
		lastEnd = end
		offset = end
	}
	if lastEnd < len(data) {
		carved.Gaps = append(carved.Gaps, [2]int{lastEnd, len(data)})
	}
	return carved
}

// carve scans for frames of one payload size.
func carve(data []byte, payloadSize int) Carved { return IterFrames(data, payloadSize, false) }

// Carve516 and Carve1028 use the two documented layouts.
func Carve516(data []byte) Carved  { return carve(data, Block516) }
func Carve1028(data []byte) Carved { return carve(data, Block1028) }

// Carve scans for frames of one payload size (exported form).
func Carve(data []byte, payloadSize int) Carved { return carve(data, payloadSize) }

// BestCarve returns whichever payload size yields more frames.
func BestCarve(data []byte, payloadSizes ...int) Carved {
	if len(payloadSizes) == 0 {
		payloadSizes = []int{Block516, Block1028}
	}
	var best Carved
	for _, size := range payloadSizes {
		carved := carve(data, size)
		if len(carved.Frames) > len(best.Frames) {
			best = carved
		}
	}
	return best
}
