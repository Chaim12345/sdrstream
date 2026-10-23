// Package gallery collects the artefacts ground stations attach to their
// SatNOGS observations: waterfall images, decoded payload images, text and
// raw demodulated data. The gallery keeps the originals, names files after
// the observation, and classifies them by content instead of trusting an
// extension the S3 URLs usually do not have.
package gallery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chaim12345/sdrstream/go/internal/satnogs"
)

// SatNOGS observation data is distributed freely under CC BY-SA 4.0,
// per https://network.satnogs.org/about/ - credit the network and its
// ground stations and keep the licence with anything republished.
const (
	// LicenseName and LicenseURL identify the licence SatNOGS publishes
	// observation data under.
	LicenseName = "CC BY-SA 4.0"
	LicenseURL  = "https://creativecommons.org/licenses/by-sa/4.0/"
	// Attribution is the ready-to-use credit line for gallery displays.
	Attribution = "SatNOGS network (network.satnogs.org) and its ground stations, CC BY-SA 4.0"
)

// ObservationLink is the public page backing one observation.
func ObservationLink(id int) string {
	return fmt.Sprintf("https://network.satnogs.org/observations/%d/", id)
}

// Item is one artefact from one observation.
type Item struct {
	Source         string    `json:"source,omitempty"` // "satnogs"; live decodes use "live"
	ObservationURL string    `json:"observation_url,omitempty"`
	DecodedFrom    string    `json:"decoded_from,omitempty"`  // artefact this image was decoded from by sdrstream
	DecodeStatus   string    `json:"decode_status,omitempty"` // set on audio items when frame decoding was skipped, with the reason
	ObservationID  int       `json:"observation_id"`
	Satellite      string    `json:"satellite"`
	NoradID        int       `json:"norad_id"`
	Station        string    `json:"station"`
	Start          time.Time `json:"start"`
	Mode           string    `json:"mode"`
	FrequencyHz    int64     `json:"frequency_hz"`
	Kind           string    `json:"kind"` // image, audio, text or binary
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Path           string    `json:"path,omitempty"`
	Size           int64     `json:"size,omitempty"`
	Text           string    `json:"text,omitempty"`
}

// Collect lists the artefacts of a set of observations. Waterfalls are
// images, the legacy payload field is audio, and demoddata is left
// unclassified until its bytes are sniffed.
func Collect(observations []satnogs.Observation) []Item {
	seen := map[string]bool{}
	var items []Item
	add := func(observation satnogs.Observation, url, kind string) {
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		items = append(items, Item{
			Source:         "satnogs",
			ObservationURL: ObservationLink(observation.ID),
			ObservationID:  observation.ID,
			Satellite:      observation.SatName,
			NoradID:        observation.NoradID,
			Station:        observation.Station,
			Start:          observation.Start,
			Mode:           observation.Mode,
			FrequencyHz:    observation.FrequencyHz,
			Kind:           kind,
			Name:           fmt.Sprintf("%d-%s", observation.ID, baseName(url)),
			URL:            url,
		})
	}
	for _, observation := range observations {
		add(observation, observation.WaterfallURL, "image")
		add(observation, observation.PayloadURL, "audio")
		add(observation, observation.DemodURL, "")
		for _, demod := range observation.Demoddata {
			add(observation, demod.PayloadDemod, "")
		}
	}
	return items
}

// baseName is the last path element of a URL, without a query or fragment.
func baseName(url string) string {
	url = strings.TrimRight(url, "/")
	if i := strings.IndexAny(url, "?#"); i >= 0 {
		url = url[:i]
	}
	name := filepath.Base(url)
	if name == "." || name == "/" || name == "" {
		return "payload"
	}
	return name
}

// Classify identifies an artefact from its first bytes and returns a kind
// and a matching file extension.
func Classify(data []byte) (kind, ext string) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image", ".png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image", ".jpg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image", ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "audio", ".wav"
	case len(data) >= 4 && string(data[:4]) == "OggS":
		return "audio", ".ogg"
	case len(data) >= 6 && string(data[:6]) == "#!AMR\n":
		return "audio", ".amr"
	case len(data) >= 3 && string(data[:3]) == "ID3":
		return "audio", ".mp3"
	}
	if looksText(data) {
		return "text", ".txt"
	}
	return "binary", ".bin"
}

// looksText accepts UTF-8 with a little whitespace and no NUL bytes. Raw
// demodulated bitstreams fail this test almost immediately.
func looksText(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	probe := data
	if len(probe) > 4096 {
		probe = probe[:4096]
	}
	if bytes.IndexByte(probe, 0) >= 0 || !utf8.Valid(probe) {
		return false
	}
	printable := 0
	for _, b := range probe {
		if b == '\n' || b == '\r' || b == '\t' || (b >= 0x20 && b < 0x7f) || b >= 0x80 {
			printable++
		}
	}
	return printable*100 >= len(probe)*90
}

// Fetch downloads every item into dir and reclassifies it from its bytes.
// Items larger than maxBytes are skipped. Binary artefacts are removed
// again unless keepBinary is set. The returned slice only contains items
// that were saved and kept.
func Fetch(items []Item, dir string, timeout time.Duration, maxBytes int64, keepBinary bool) ([]Item, error) {
	return FetchContext(context.Background(), items, dir, timeout, maxBytes, keepBinary)
}

// FetchContext is Fetch cancellable by ctx: a cancelled caller stops a
// stalled download and skips the remaining artefacts instead of fetching
// the full list.
func FetchContext(ctx context.Context, items []Item, dir string, timeout time.Duration, maxBytes int64, keepBinary bool) ([]Item, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	var saved []Item
	var firstErr error
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return saved, err
		}
		got, err := fetchOne(ctx, client, item, dir, maxBytes)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if got.Kind == "binary" && !keepBinary {
			_ = os.Remove(got.Path)
			continue
		}
		saved = append(saved, got)
	}
	return saved, firstErr
}

// WriteManifest saves items as index.json in dir, with absolute paths
// stripped so the directory can be moved. The web server loads the same
// file, so a CLI-collected gallery opens in the UI unchanged.
func WriteManifest(items []Item, dir string) error {
	out := make([]Item, len(items))
	copy(out, items)
	for i := range out {
		out[i].Path = ""
	}
	data, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "index.json"), data, 0o644)
}

// Decode bounds, kept tight so a small compressed payload cannot expand
// into work that monopolises the single gallery refresh pass. Conversion
// already carries a 2-minute ffmpeg timeout; these caps reject oversize
// input BEFORE decoding starts, and the decoders honour context
// cancellation mid-decode, so the caps bound memory while the deadline
// and cancellation bound wall time.
var (
	// maxWAVBytes caps a converted or source WAV for frame decoding.
	maxWAVBytes int64 = 64 << 20
	// maxDecodeWallClock bounds one item's pure-Go decode as a backstop to
	// the input caps, via context cancellation inside the decoders.
	maxDecodeWallClock = 2 * time.Minute
	// maxDecodeSamples caps the estimated decoded mono sample count before
	// allocation: the decoder materialises one float64 per sample, so this
	// bounds the transient allocation to ~128 MB, a conservative budget
	// for Android-class devices. Var so tests can shrink it.
	maxDecodeSamples int64 = 16 << 20
	// maxDecodeDurationSec caps the audio length handed to a decoder;
	// decode time is linear in samples, so this bounds CPU per artefact.
	maxDecodeDurationSec = 480.0
)

// runFFmpeg executes one bounded conversion command.
func runFFmpeg(ctx context.Context, binary string, args ...string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// probeWAV reads a WAV header and returns the payload duration in seconds
// and the estimated decoded mono sample count, without loading samples.
// Validation mirrors decode.ReadWAV arithmetic: 16-bit PCM only, rate > 0,
// channels < 1 coerced to 1, first data chunk only, data clamped to the
// bytes actually present.
func probeWAV(path string) (float64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return 0, 0, fmt.Errorf("short file")
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	var rate, channels, bits int64
	var dataBytes int64
	dataSeen := false
	for {
		chunk := make([]byte, 8)
		if _, err := io.ReadFull(f, chunk); err != nil {
			break
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		pos, _ := f.Seek(0, io.SeekCurrent)
		switch string(chunk[0:4]) {
		case "fmt ":
			if size < 16 {
				return 0, 0, fmt.Errorf("short fmt chunk")
			}
			fmtbuf := make([]byte, 16)
			if _, err := io.ReadFull(f, fmtbuf); err != nil {
				return 0, 0, err
			}
			channels = int64(binary.LittleEndian.Uint16(fmtbuf[2:4]))
			rate = int64(binary.LittleEndian.Uint32(fmtbuf[4:8]))
			bits = int64(binary.LittleEndian.Uint16(fmtbuf[14:16]))
			if _, err := f.Seek(pos+size+(size&1), io.SeekStart); err != nil {
				return 0, 0, err
			}
		case "data":
			// The decoder reads the FIRST data chunk and stops; the
			// probe measures exactly that chunk, even when it is empty
			// and a later data chunk holds more audio.
			if !dataSeen {
				dataSeen = true
				dataBytes = size
				if actual := fi.Size() - pos; dataBytes > actual {
					dataBytes = actual
				}
			}
			if _, err := f.Seek(pos+size+(size&1), io.SeekStart); err != nil {
				break
			}
		default:
			if _, err := f.Seek(pos+size+(size&1), io.SeekStart); err != nil {
				return 0, 0, err
			}
		}
		if dataSeen && rate > 0 {
			break
		}
	}
	if rate <= 0 {
		return 0, 0, fmt.Errorf("missing or invalid fmt chunk")
	}
	if bits != 16 {
		// decode.ReadWAV only accepts 16-bit PCM.
		return 0, 0, fmt.Errorf("only 16-bit PCM is supported")
	}
	if channels < 1 {
		// decode.ReadWAV coerces missing channel counts to mono.
		channels = 1
	}
	// int64 throughout: rate*channels*2 stays far below overflow even for
	// header-maximum rate (2^32-1) and channels (65535).
	bytesPerSec := rate * channels * 2
	estSamples := dataBytes / 2 / channels
	return float64(dataBytes) / float64(bytesPerSec), estSamples, nil
}

// DecodeFrames runs the project's own decoders over downloaded audio
// artefacts and returns new image items for the frames they extract.
// Mode-matched: SSTV and APT observations are attempted; APT pictures
// ship only when the decode verifies. OGG/MP3 payloads convert through
// ffmpeg; WAV decodes directly. Every attempted artefact that does not
// yield a frame gets its DecodeStatus set to the concrete skip reason,
// so a missing tool or an oversized input is reported, not silent.
func DecodeFrames(saved []Item, dir string) []Item {
	return DecodeFramesContext(context.Background(), saved, dir)
}

// DecodeFramesContext is DecodeFrames with cancellation: a cancelled
// context stops in-flight decode work and is reported per artefact, and
// no partial frame file is written.
func DecodeFramesContext(ctx context.Context, saved []Item, dir string) []Item {
	var frames []Item
	for i := range saved {
		item := &saved[i]
		// Once cancelled, mark remaining decodable artefacts without
		// loading their audio: the decode loops are not the only work
		// worth skipping, ReadWAV would still read every file.
		if ctx.Err() != nil {
			if item.Kind == "audio" {
				m := strings.ToUpper(item.Mode)
				if strings.Contains(m, "SSTV") || strings.Contains(m, "APT") {
					item.DecodeStatus = "frame decode skipped: decode cancelled"
				}
			}
			continue
		}
		if item.Kind != "audio" {
			continue
		}
		mode := strings.ToUpper(item.Mode)
		wantSSTV := strings.Contains(mode, "SSTV")
		wantAPT := strings.Contains(mode, "APT")
		wantAFSK := strings.Contains(mode, "AFSK") || strings.Contains(mode, "APRS") || strings.Contains(mode, "AX25")
		wantCW := strings.Contains(mode, "CW")
		if !wantSSTV && !wantAPT && !wantAFSK && !wantCW {
			continue // not an image-payload mode; nothing to report
		}
		path := item.Path
		if !strings.HasSuffix(strings.ToLower(path), ".wav") {
			if !decode.FFmpegAvailable() {
				item.DecodeStatus = "frame decode skipped: ffmpeg not installed, cannot convert " + filepath.Ext(path)
				continue
			}
			tmp, err := os.CreateTemp("", "sdrstream-gallery-*.wav")
			if err != nil {
				item.DecodeStatus = "frame decode skipped: " + err.Error()
				continue
			}
			tmp.Close()
			// -fs caps the output file DURING conversion: a small
			// compressed payload cannot expand past the cap on disk.
			// The 1MB slack lets the post-check tell "hit the cap" from
			// "legitimately small"; over-cap output is skipped, not decoded.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			err = runFFmpeg(ctx, decode.FFmpegBinary(""), "-y", "-v", "error", "-i", path, "-ac", "1",
				"-fs", fmt.Sprintf("%d", maxWAVBytes+(1<<20)), tmp.Name())
			cancel()
			if err != nil {
				os.Remove(tmp.Name())
				item.DecodeStatus = "frame decode skipped: conversion failed: " + err.Error()
				continue
			}
			if fi, err := os.Stat(tmp.Name()); err != nil || fi.Size() > maxWAVBytes {
				os.Remove(tmp.Name())
				item.DecodeStatus = fmt.Sprintf("frame decode skipped: converted WAV over the %d MB limit", maxWAVBytes>>20)
				continue
			}
			defer os.Remove(tmp.Name())
			path = tmp.Name()
		} else if fi, err := os.Stat(path); err == nil && fi.Size() > maxWAVBytes {
			item.DecodeStatus = fmt.Sprintf("frame decode skipped: WAV over the %d MB limit", maxWAVBytes>>20)
			continue
		}
		// Duration check probes the header first, with the data chunk
		// clamped to the bytes actually present: a padded or truncated
		// file is measured by its real payload, not its declaration, and
		// over-limit audio is rejected before the full sample load.
		duration, estSamples, err := probeWAV(path)
		if err != nil {
			item.DecodeStatus = "frame decode skipped: unreadable audio: " + err.Error()
			continue
		}
		if duration > maxDecodeDurationSec {
			item.DecodeStatus = fmt.Sprintf("frame decode skipped: audio longer than the %d s decode limit", int(maxDecodeDurationSec))
			continue
		}
		// Preflight the allocation: the decoder materialises one float64
		// per decoded sample, so reject over-budget files before any large
		// slice is allocated (rate does not reduce the count; channels do).
		if estSamples > maxDecodeSamples {
			item.DecodeStatus = fmt.Sprintf("frame decode skipped: estimated %d decoded samples over the %d M sample memory budget", estSamples, maxDecodeSamples>>20)
			continue
		}
		samples, rate, err := decode.ReadWAV(path)
		if err != nil {
			item.DecodeStatus = "frame decode skipped: unreadable audio: " + err.Error()
			continue
		}
		// One deadline per artefact, honouring the caller's context: the
		// decoders check it inside their sample loops, so an over-long
		// or cancelled decode stops instead of monopolising a refresh.
		dctx, cancel := context.WithTimeout(ctx, maxDecodeWallClock)
		if wantAFSK {
			// Packet audio: the HDLC CRC is the binary ground-truth gate,
			// same honesty bar as the APT sync lock. Decoded AX.25 frames
			// ship as a text artefact.
			packets, ferr := decode.AFSKFramesContext(dctx, path)
			cancel()
			if ferr != nil {
				if errors.Is(ferr, context.DeadlineExceeded) {
					item.DecodeStatus = fmt.Sprintf("frame decode skipped: decode exceeded the %d min limit", int(maxDecodeWallClock.Minutes()))
				} else if errors.Is(ferr, context.Canceled) {
					item.DecodeStatus = "frame decode skipped: decode cancelled"
				} else {
					item.DecodeStatus = "frame decode skipped: packet decode failed: " + ferr.Error()
				}
				continue
			}
			if len(packets) == 0 {
				item.DecodeStatus = "frame decode skipped: no valid AX.25 frames found"
				continue
			}
			var text bytes.Buffer
			fmt.Fprintf(&text, "%d AX.25 packet(s) decoded from %s by sdrstream\n\n", len(packets), item.Name)
			for _, f := range packets {
				text.WriteString(f.String())
				text.WriteByte('\n')
			}
			ext := filepath.Ext(item.Name)
			name := strings.TrimSuffix(item.Name, ext) + "-sdrstream-decoded.txt"
			out := filepath.Join(dir, name)
			if err := os.WriteFile(out, text.Bytes(), 0o644); err != nil {
				item.DecodeStatus = "packets decoded but could not be saved: " + err.Error()
				continue
			}
			frames = append(frames, Item{
				Source:         item.Source,
				ObservationURL: item.ObservationURL,
				ObservationID:  item.ObservationID,
				Satellite:      item.Satellite,
				NoradID:        item.NoradID,
				Station:        item.Station,
				Start:          item.Start,
				Mode:           item.Mode,
				FrequencyHz:    item.FrequencyHz,
				Kind:           "text",
				Name:           name,
				Path:           out,
				Size:           int64(text.Len()),
				Text:           preview(text.Bytes()),
				DecodedFrom:    item.Name,
			})
			continue
		}
		if wantCW {
			// Beacon audio: the Morse decoder returns its best read even
			// when it looks like noise, so the plausibility gate (element
			// timing + decodable character mix) decides whether anything
			// ships. The transcript is an automatic decode, not verified
			// text.
			res, merr := decode.MorseDecodeContext(dctx, path)
			cancel()
			if merr != nil {
				if errors.Is(merr, context.DeadlineExceeded) {
					item.DecodeStatus = fmt.Sprintf("frame decode skipped: decode exceeded the %d min limit", int(maxDecodeWallClock.Minutes()))
				} else if errors.Is(merr, context.Canceled) {
					item.DecodeStatus = "frame decode skipped: decode cancelled"
				} else {
					item.DecodeStatus = "frame decode skipped: morse decode failed: " + merr.Error()
				}
				continue
			}
			if res.Text == "" || !res.Plausible() {
				item.DecodeStatus = "frame decode skipped: no plausible Morse found"
				continue
			}
			wpm := 0.0
			if res.DotMs > 0 {
				wpm = 1200 / res.DotMs
			}
			var text bytes.Buffer
			fmt.Fprintf(&text, "Automatic Morse (CW) decode of %s by sdrstream - plausibility-gated, transcript not independently verified\n\ntranscript: %s\n\ntone %.0f Hz, %.1f WPM, SNR %.1f dB\n", item.Name, res.Text, res.ToneHz, wpm, res.SnrdB)
			ext := filepath.Ext(item.Name)
			name := strings.TrimSuffix(item.Name, ext) + "-sdrstream-decoded.txt"
			out := filepath.Join(dir, name)
			if err := os.WriteFile(out, text.Bytes(), 0o644); err != nil {
				item.DecodeStatus = "morse decoded but could not be saved: " + err.Error()
				continue
			}
			frames = append(frames, Item{
				Source:         item.Source,
				ObservationURL: item.ObservationURL,
				ObservationID:  item.ObservationID,
				Satellite:      item.Satellite,
				NoradID:        item.NoradID,
				Station:        item.Station,
				Start:          item.Start,
				Mode:           item.Mode,
				FrequencyHz:    item.FrequencyHz,
				Kind:           "text",
				Name:           name,
				Path:           out,
				Size:           int64(text.Len()),
				Text:           preview(text.Bytes()),
				DecodedFrom:    item.Name,
			})
			continue
		}
		var png []byte
		if wantSSTV {
			png, err = decode.SSTVImageContext(dctx, samples, rate)
		} else {
			// APT pictures ship only when the sync-lock gate verifies the
			// decode, same honesty bar as the decode command.
			var r *decode.APTResult
			r, err = decode.APTDecodeContext(dctx, samples, rate)
			if err == nil && r == nil {
				item.DecodeStatus = "frame decode skipped: no APT sync found"
				cancel()
				continue
			}
			if err == nil {
				ok, why := r.Verified()
				if !ok {
					item.DecodeStatus = "frame decode skipped: APT decode not verified (" + why + ")"
					cancel()
					continue
				}
				png, err = r.PNG()
			}
		}
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				item.DecodeStatus = fmt.Sprintf("frame decode skipped: decode exceeded the %d min limit", int(maxDecodeWallClock.Minutes()))
			} else if errors.Is(err, context.Canceled) {
				item.DecodeStatus = "frame decode skipped: decode cancelled"
			} else {
				item.DecodeStatus = "frame decode skipped: " + err.Error()
			}
			continue
		}
		ext := filepath.Ext(item.Name)
		name := strings.TrimSuffix(item.Name, ext) + "-sdrstream-decoded.png"
		out := filepath.Join(dir, name)
		if err := os.WriteFile(out, png, 0o644); err != nil {
			item.DecodeStatus = "frame decoded but could not be saved: " + err.Error()
			continue
		}
		frames = append(frames, Item{
			Source:         item.Source,
			ObservationURL: item.ObservationURL,
			ObservationID:  item.ObservationID,
			Satellite:      item.Satellite,
			NoradID:        item.NoradID,
			Station:        item.Station,
			Start:          item.Start,
			Mode:           item.Mode,
			FrequencyHz:    item.FrequencyHz,
			Kind:           "image",
			Name:           name,
			Path:           out,
			Size:           int64(len(png)),
			DecodedFrom:    item.Name,
		})
	}
	return frames
}

func fetchOne(ctx context.Context, client *http.Client, item Item, dir string, maxBytes int64) (Item, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", item.URL, nil)
	if err != nil {
		return item, err
	}
	request.Header.Set("User-Agent", "sdrstream/0.1 (+gallery)")
	response, err := client.Do(request)
	if err != nil {
		return item, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return item, fmt.Errorf("%s: HTTP %d", item.URL, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return item, err
	}
	if int64(len(data)) > maxBytes {
		return item, fmt.Errorf("%s: larger than %d bytes", item.URL, maxBytes)
	}
	kind, ext := Classify(data)
	name := strings.TrimSuffix(sanitize(item.Name), filepath.Ext(item.Name)) + ext
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return item, err
	}
	item.Kind = kind
	item.Name = name
	item.Path = path
	item.Size = int64(len(data))
	if kind == "text" {
		item.Text = preview(data)
	}
	return item, nil
}

// sanitize keeps generated file names inside dir on every platform.
func sanitize(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteByte('_')
		}
	}
	out := strings.Trim(sb.String(), "._")
	if out == "" {
		return "payload"
	}
	if len(out) > 120 {
		out = out[len(out)-120:]
	}
	return out
}

// preview returns the first few readable lines of a text artefact.
func preview(data []byte) string {
	text := strings.TrimSpace(string(bytes.ToValidUTF8(data, []byte("?"))))
	if len(text) > 600 {
		text = text[:600] + "…"
	}
	return text
}
