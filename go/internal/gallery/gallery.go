// Package gallery collects the artefacts ground stations attach to their
// SatNOGS observations: waterfall images, decoded payload images, text and
// raw demodulated data. The gallery keeps the originals, names files after
// the observation, and classifies them by content instead of trusting an
// extension the S3 URLs usually do not have.
package gallery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"io"
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
	Source        string    `json:"source,omitempty"` // "satnogs"; live decodes use "live"
	ObservationURL string   `json:"observation_url,omitempty"`
	DecodedFrom   string    `json:"decoded_from,omitempty"` // artefact this image was decoded from by sdrstream
	ObservationID int       `json:"observation_id"`
	Satellite     string    `json:"satellite"`
	NoradID       int       `json:"norad_id"`
	Station       string    `json:"station"`
	Start         time.Time `json:"start"`
	Mode          string    `json:"mode"`
	FrequencyHz   int64     `json:"frequency_hz"`
	Kind          string    `json:"kind"` // image, audio, text or binary
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	Path          string    `json:"path,omitempty"`
	Size          int64     `json:"size,omitempty"`
	Text          string    `json:"text,omitempty"`
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
			Source:        "satnogs",
			ObservationURL: ObservationLink(observation.ID),
			ObservationID: observation.ID,
			Satellite:     observation.SatName,
			NoradID:       observation.NoradID,
			Station:       observation.Station,
			Start:         observation.Start,
			Mode:          observation.Mode,
			FrequencyHz:   observation.FrequencyHz,
			Kind:          kind,
			Name:          fmt.Sprintf("%d-%s", observation.ID, baseName(url)),
			URL:           url,
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	var saved []Item
	var firstErr error
	for _, item := range items {
		got, err := fetchOne(client, item, dir, maxBytes)
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

// DecodeFrames runs the project's own decoders over downloaded audio
// artefacts and returns new image items for the frames they extract.
// Mode-matched: only observations whose mode names SSTV are attempted.
// OGG/MP3 payloads convert through ffmpeg; WAV decodes directly. An
// audio item that yields no frame is left as it is, without a placeholder.
func DecodeFrames(saved []Item, dir string) []Item {
	var frames []Item
	for _, item := range saved {
		if item.Kind != "audio" || !strings.Contains(strings.ToUpper(item.Mode), "SSTV") {
			continue
		}
		path := item.Path
		if !strings.HasSuffix(strings.ToLower(path), ".wav") {
			if !decode.FFmpegAvailable() {
				continue
			}
			tmp, err := os.CreateTemp("", "sdrstream-gallery-*.wav")
			if err != nil {
				continue
			}
			tmp.Close()
			defer os.Remove(tmp.Name())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := decode.ConvertToWAV(ctx, "", path, tmp.Name()); err != nil {
				continue
			}
			path = tmp.Name()
		}
		samples, rate, err := decode.ReadWAV(path)
		if err != nil {
			continue
		}
		png, err := decode.SSTVImage(samples, rate)
		if err != nil {
			continue
		}
		ext := filepath.Ext(item.Name)
		name := strings.TrimSuffix(item.Name, ext) + "-sdrstream-decoded.png"
		out := filepath.Join(dir, name)
		if err := os.WriteFile(out, png, 0o644); err != nil {
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

func fetchOne(client *http.Client, item Item, dir string, maxBytes int64) (Item, error) {
	request, err := http.NewRequest("GET", item.URL, nil)
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
