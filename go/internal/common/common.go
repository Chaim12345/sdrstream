// Package common holds shared helpers: stream URLs, tuning parameters and
// WAV output.
package common

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WebSDRModeCodes maps modulation names to WebSDR mode codes.
var WebSDRModeCodes = map[string]int{
	"USB": 0, "LSB": 0, "CW": 0, "AM": 1, "AMSYNC": 2, "FM": 4,
}

// OpenWebRXModes maps modulation names to OpenWebRX names.
var OpenWebRXModes = map[string]string{
	"USB": "usb", "LSB": "lsb", "CW": "cw", "AM": "am",
	"AMSYNC": "sam", "FM": "nfm", "WFM": "wfm", "NFM": "nfm",
}

// NormalizeHTTPURL returns url with an explicit scheme, defaulting to HTTP.
func NormalizeHTTPURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("server URL must not be empty")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	return value, nil
}

// WebSocketURL builds a WebSocket URL for raw pointing at path. http/ws map
// to ws, https/wss map to wss.
func WebSocketURL(raw, path string) (string, error) {
	base, err := NormalizeHTTPURL(raw)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	scheme := "ws"
	if parsed.Scheme == "https" || parsed.Scheme == "wss" {
		scheme = "wss"
	}
	joined := strings.TrimRight(parsed.Path, "/") + path
	return scheme + "://" + parsed.Host + joined, nil
}

// WebSDRTuningParams builds the WebSDR /~~param query string that retunes the
// receiver. frequencyKHz is the suppressed-carrier frequency. For SSB/CW
// modes the passband is expressed as offsets from that carrier via lowKHz and
// highKHz; when only bandwidthHz is given the passband is centred on the
// carrier.
func WebSDRTuningParams(frequencyKHz float64, mode string, bandwidthHz *float64,
	lowKHz, highKHz *float64, band int, name string) (string, error) {
	upper := strings.ToUpper(mode)
	if _, ok := WebSDRModeCodes[upper]; !ok {
		keys := make([]string, 0, len(WebSDRModeCodes))
		for key := range WebSDRModeCodes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("unsupported WebSDR mode %q; expected one of %s",
			mode, strings.Join(keys, ", "))
	}

	if lowKHz == nil || highKHz == nil {
		width := 2700.0 / 1000.0
		if bandwidthHz != nil {
			width = *bandwidthHz / 1000.0
		}
		switch upper {
		case "USB":
			lowKHZ := 0.0
			high := width
			lowKHz, highKHz = &lowKHZ, &high
		case "LSB":
			low := -width
			zero := 0.0
			lowKHz, highKHz = &low, &zero
		default: // CW, AM, AMSYNC, FM
			low := -width / 2
			high := width / 2
			lowKHz, highKHz = &low, &high
		}
	}

	params := url.Values{}
	params.Set("f", fmt.Sprintf("%.3f", frequencyKHz))
	params.Set("band", fmt.Sprintf("%d", band))
	params.Set("lo", fmt.Sprintf("%.3f", *lowKHz))
	params.Set("hi", fmt.Sprintf("%.3f", *highKHz))
	params.Set("mode", fmt.Sprintf("%d", WebSDRModeCodes[upper]))
	params.Set("name", name)
	return params.Encode(), nil
}

// PCMBytes returns 16-bit little-endian PCM bytes for samples.
func PCMBytes(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
	}
	return out
}

// WriteWAV writes 16-bit PCM samples as a RIFF/WAVE file. Returns the number
// of frames written. A partial frame at the end is dropped, matching the
// Python original.
func WriteWAV(w io.Writer, samples []int16, samplerate, channels int) (int, error) {
	frames := len(samples) / channels
	payload := PCMBytes(samples[:frames*channels])

	dataLen := len(payload)
	byteRate := samplerate * channels * 2
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+dataLen))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:], uint32(samplerate))
	binary.LittleEndian.PutUint32(header[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(header[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(dataLen))

	if _, err := w.Write(header); err != nil {
		return 0, err
	}
	if _, err := w.Write(payload); err != nil {
		return 0, err
	}
	return frames, nil
}

// WAVBytes returns 16-bit PCM samples encoded as an in-memory WAV file.
func WAVBytes(samples []int16, samplerate, channels int) ([]byte, error) {
	var buffer bytes.Buffer
	if _, err := WriteWAV(&buffer, samples, samplerate, channels); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// WriteWAVFile writes 16-bit PCM samples to a WAV file on disk. The write
// goes to a sibling temp file first and is renamed into place, so a scanner
// or web server reading the directory never sees a half-written WAV.
func WriteWAVFile(path string, samples []int16, samplerate int) error {
	var buffer bytes.Buffer
	if _, err := WriteWAV(&buffer, samples, samplerate, 1); err != nil {
		return err
	}
	return WriteFileAtomic(path, buffer.Bytes(), 0o644)
}

// WriteFileAtomic writes data to path via a sibling temp file and a
// rename, so concurrent readers see either the old file or the new one,
// never a mixture. The temp name is unique per call (".tmp-<base>-*"):
// concurrent writers used to share "<path>.tmp", and one writer could
// rename another's temp out from under it (rename: no such file or
// directory) or publish a mixture of two payloads. The last rename wins.
// Directories listing for a specific extension must skip ".tmp*" names
// while a write is in flight.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename lands
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
