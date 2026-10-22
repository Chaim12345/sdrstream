// Package icecast ingests public Icecast and Shoutcast internet radio
// streams (including NOAA Weather Radio relays) and turns them into the same
// mono PCM the receiver clients produce, so the existing ASR and decoders can
// run on them unchanged.
//
// Everything here is receive-only and polite: one connection per stream, a
// descriptive User-Agent, bounded reads, and a disk cache for directory
// listings.
package icecast

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// UserAgent identifies this tool to stream hosts.
const UserAgent = "sdrstream/1.0 (+https://github.com/Chaim12345/sdrstream; receive-only)"

// PCMRate is the sample rate that Capture returns.
const PCMRate = 16000

// Info is what a stream announces in its response headers.
type Info struct {
	Name        string
	Genre       string
	ContentType string
	BitrateKbps int
	MetaInt     int // bytes of audio between ICY metadata blocks, 0 = none
}

// Capture is audio plus what the stream said about itself.
type Capture struct {
	Info    Info
	Samples []float64 // mono, PCMRate
	Titles  []string  // ICY StreamTitle values seen while recording
	Bytes   int       // compressed audio bytes read
}

// Client fetches streams.
type Client struct {
	HTTP   *http.Client
	FFmpeg string // ffmpeg binary, default "ffmpeg"
}

// NewClient returns a client without a global timeout: streams are endless,
// so Capture bounds each read with its own context.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Transport: &http.Transport{
		ResponseHeaderTimeout: 15 * time.Second,
		DisableCompression:    true,
	}}}
}

func (c *Client) ffmpeg() string {
	if c.FFmpeg != "" {
		return c.FFmpeg
	}
	return "ffmpeg"
}

// Open connects to a stream and returns its audio bytes with the ICY
// metadata blocks removed. titles receives each new StreamTitle.
func (c *Client) Open(ctx context.Context, url string, titles func(string)) (io.ReadCloser, Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, Info{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("Accept", "audio/*, */*;q=0.5")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, Info{}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, Info{}, fmt.Errorf("icecast: %s: HTTP %d", url, resp.StatusCode)
	}
	info := infoFromHeaders(resp.Header)
	ct := strings.ToLower(info.ContentType)
	if strings.HasPrefix(ct, "text/") {
		resp.Body.Close()
		return nil, info, fmt.Errorf("icecast: %s: not an audio stream (%s)", url, info.ContentType)
	}
	if info.MetaInt <= 0 {
		return resp.Body, info, nil
	}
	return &icyReader{r: bufio.NewReaderSize(resp.Body, 32<<10), c: resp.Body, every: info.MetaInt, left: info.MetaInt, onTitle: titles}, info, nil
}

func infoFromHeaders(h http.Header) Info {
	in := Info{
		Name:        h.Get("Icy-Name"),
		Genre:       h.Get("Icy-Genre"),
		ContentType: h.Get("Content-Type"),
	}
	in.BitrateKbps, _ = strconv.Atoi(strings.TrimSpace(h.Get("Icy-Br")))
	in.MetaInt, _ = strconv.Atoi(strings.TrimSpace(h.Get("Icy-Metaint")))
	return in
}

// icyReader removes the in-band metadata blocks: after every `every` audio
// bytes comes one length byte (units of 16 bytes) and that many bytes of
// "StreamTitle='...';" text.
type icyReader struct {
	r       *bufio.Reader
	c       io.Closer
	every   int
	left    int
	onTitle func(string)
	last    string
}

func (i *icyReader) Close() error { return i.c.Close() }

func (i *icyReader) Read(p []byte) (int, error) {
	if i.left == 0 {
		n, err := i.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if n > 0 {
			meta := make([]byte, int(n)*16)
			if _, err := io.ReadFull(i.r, meta); err != nil {
				return 0, err
			}
			if t := ParseStreamTitle(meta); t != "" && t != i.last {
				i.last = t
				if i.onTitle != nil {
					i.onTitle(t)
				}
			}
		}
		i.left = i.every
	}
	if len(p) > i.left {
		p = p[:i.left]
	}
	n, err := i.r.Read(p)
	i.left -= n
	return n, err
}

// ParseStreamTitle extracts StreamTitle from an ICY metadata block.
func ParseStreamTitle(meta []byte) string {
	s := string(bytes.TrimRight(meta, "\x00"))
	const key = "StreamTitle='"
	a := strings.Index(s, key)
	if a < 0 {
		return ""
	}
	s = s[a+len(key):]
	if b := strings.Index(s, "';"); b >= 0 {
		return strings.TrimSpace(s[:b])
	}
	return strings.TrimSpace(strings.TrimSuffix(s, "'"))
}

// Capture records dur of audio from url and decodes it to mono PCMRate
// samples with ffmpeg. Any codec ffmpeg knows (MP3, AAC, Ogg) works.
func (c *Client) Capture(ctx context.Context, url string, dur time.Duration) (*Capture, error) {
	// Streams deliver in real time, so the wall clock is the recording
	// length; allow some slack for connect and buffering bursts.
	rctx, cancel := context.WithTimeout(ctx, dur+20*time.Second)
	defer cancel()
	cap := &Capture{}
	body, info, err := c.Open(rctx, url, func(t string) { cap.Titles = append(cap.Titles, t) })
	if err != nil {
		return nil, err
	}
	defer body.Close()
	cap.Info = info

	// Read only as much as dur of audio. Bitrate gives the byte budget;
	// without it fall back to the clock alone.
	var budget int64 = 1 << 62
	if info.BitrateKbps > 0 {
		budget = int64(info.BitrateKbps) * 1000 / 8 * int64(dur/time.Second)
	}
	deadline := time.Now().Add(dur)
	var raw bytes.Buffer
	buf := make([]byte, 16<<10)
	for int64(raw.Len()) < budget && time.Now().Before(deadline) {
		n, err := body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) || raw.Len() > 0 {
				break
			}
			return nil, err
		}
	}
	cap.Bytes = raw.Len()
	if raw.Len() == 0 {
		return nil, fmt.Errorf("icecast: %s: no audio received", url)
	}
	pcm, err := c.decode(rctx, raw.Bytes())
	if err != nil {
		return nil, err
	}
	cap.Samples = pcm
	return cap, nil
}

// decode converts compressed audio bytes to mono float samples.
func (c *Client) decode(ctx context.Context, data []byte) ([]float64, error) {
	cmd := exec.CommandContext(ctx, c.ffmpeg(), "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(PCMRate), "-f", "s16le", "pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("icecast: ffmpeg: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return PCM16(out.Bytes()), nil
}

// PCM16 converts little-endian signed 16-bit PCM to floats in [-1, 1).
func PCM16(b []byte) []float64 {
	n := len(b) / 2
	s := make([]float64, n)
	for i := 0; i < n; i++ {
		s[i] = float64(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
	}
	return s
}

// RMS is the root-mean-square level, used to tell dead air from audio.
func RMS(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(s)))
}
