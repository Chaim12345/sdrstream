package kiwisdr

import (
	"encoding/binary"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// adpcmState carries KiwiSDR's four bit IMA ADPCM decoder state across
// frames; the server seeds it through the "audio_adpcm_state" MSG packet.
type adpcmState struct {
	index int
	prev  int
}

var stepTable = [...]int{7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25,
	28, 31, 34, 37, 41, 45, 50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143,
	157, 173, 190, 209, 230, 253, 279, 307, 337, 371, 408, 449, 494, 544, 598,
	658, 724, 796, 876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
	2272, 2499, 2749, 3024}

var indexTable = [...]int{-1, -1, -1, -1, 2, 4, 6, 8}

// decodeSampleBlock turns one SND body into audio. Uncompressed samples are
// big-endian int16 unless the little-endian flag is set; compressed blocks
// run low nibble first.
func decodeSampleBlock(flags byte, data []byte, st *adpcmState) Chunk {
	var chunk Chunk
	if len(data) == 0 {
		return chunk
	}
	if flags&flagADCOverrun != 0 {
		chunk.Overrun = true
	}
	if flags&flagCompressed != 0 {
		chunk.Compressed = true
		chunk.Samples, st.index, st.prev = decodeADPCM(data, st.index, st.prev)
		return chunk
	}
	count := len(data) / 2
	samples := make([]int16, count)
	var order binary.ByteOrder = binary.BigEndian
	if flags&flagLittleEndLs != 0 {
		order = binary.LittleEndian
	}
	for i := 0; i < count; i++ {
		samples[i] = int16(order.Uint16(data[i*2:]))
	}
	chunk.Samples = samples
	return chunk
}

func decodeADPCM(data []byte, index, prev int) ([]int16, int, int) {
	out := make([]int16, len(data)*2)
	for offset, b := range data {
		for nibble := 0; nibble < 2; nibble++ {
			code := b & 0x0F
			if nibble == 1 {
				code = (b >> 4) & 0x0F
			}
			step := stepTable[index]
			diff := step >> 3
			if code&1 != 0 {
				diff += step >> 2
			}
			if code&2 != 0 {
				diff += step >> 1
			}
			if code&4 != 0 {
				diff += step
			}
			if code&8 != 0 {
				diff = -diff
			}
			next := prev + diff
			if next > 32767 {
				next = 32767
			} else if next < -32768 {
				next = -32768
			}
			prev = next
			nx := index + indexTable[code&7]
			if nx < 0 {
				nx = 0
			} else if nx > len(stepTable)-1 {
				nx = len(stepTable) - 1
			}
			index = nx
			out[offset*2+nibble] = int16(prev)
		}
	}
	return out, index, prev
}

// extractURLEncoded finds one name=… value from a MSG body.
func extractURLEncoded(body, name string) (string, bool) {
	start := strings.Index(body, name+"=")
	if start < 0 {
		return "", false
	}
	value := body[start+len(name)+1:]
	if next := strings.Index(value, " "); next >= 0 {
		value = value[:next]
	}
	return value, true
}

// parseRxGPS digs the operator position out of load_cfg; the client needs it
// for pass planning and the UI shows it next to the receiver.
func parseRxGPS(value string, info *Info) {
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		decoded = value
	}
	var config struct {
		RxGPS string `json:"rx_gps"`
	}
	if err := json.Unmarshal([]byte(decoded), &config); err != nil {
		// Some firmware wraps the JSON so heavily it arrives escaped twice.
		if decoded2, err2 := url.QueryUnescape(decoded); err2 == nil {
			if err3 := json.Unmarshal([]byte(decoded2), &config); err3 != nil {
				return
			}
		} else {
			return
		}
	}
	gps := strings.TrimSpace(config.RxGPS)
	gps = strings.Trim(gps, "[]() ")
	parts := strings.Split(gps, ",")
	if len(parts) < 2 {
		return
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return
	}
	info.GPSLat, info.GPSLon = lat, lon
}

// knownModes lists the tuning modes, read as advertised.
func knownModes() string {
	names := make([]string, 0, len(DefaultPassbands))
	for name := range DefaultPassbands {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
