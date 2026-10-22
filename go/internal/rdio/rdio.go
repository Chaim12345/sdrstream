// Package rdio uploads decoded transmissions to an Rdio Scanner server
// (https://github.com/chuot/rdio-scanner) using its call-upload API.
//
// Rdio Scanner is an audio archive and scanner-style web UI: it does not
// tune receivers or decode anything, it ingests finished calls. sdrstream
// is the other half of that pipeline, so instead of copying the 14k-line
// archive server, sdrstream feeds it. A receiver capture plus its decoder
// output or transcript becomes one call in Rdio's searchable, playable
// feed.
package rdio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options configures the upload target.
type Options struct {
	// BaseURL is the Rdio Scanner server root, e.g. http://127.0.0.1:3000.
	BaseURL string
	// APIKey is an Rdio Scanner API key with access to the target systems.
	APIKey string
	// SystemLabel names the receiver in Rdio's UI. A key configured with
	// "*" access accepts calls without pre-registering systems.
	SystemLabel string
	// HTTPClient is optional; nil uses a 60 s default client.
	HTTPClient *http.Client
}

// Enabled reports whether a target URL is configured.
func (o Options) Enabled() bool { return strings.TrimSpace(o.BaseURL) != "" }

// Call is one transmission to hand to Rdio Scanner.
type Call struct {
	AudioPath      string
	AudioName      string
	AudioMime      string
	Timestamp      time.Time
	FrequencyHz    float64
	SystemLabel    string
	TalkgroupLabel string
	TalkgroupGroup string
	TalkgroupTag   string
	Site           int
	Unit           int
}

// Upload sends one call. The server answers 200 with
// "Call imported successfully."; anything else is an error carrying the
// server's body so a wrong API key is obvious in the logs.
func Upload(ctx context.Context, options Options, call Call) error {
	if !options.Enabled() {
		return fmt.Errorf("rdio: no server configured")
	}
	if call.AudioPath == "" {
		return fmt.Errorf("rdio: call has no audio file")
	}
	audio, err := os.ReadFile(call.AudioPath)
	if err != nil {
		return fmt.Errorf("rdio: read audio: %w", err)
	}
	if len(audio) <= 44 {
		return fmt.Errorf("rdio: audio file %s is empty", call.AudioPath)
	}
	name := call.AudioName
	if name == "" {
		name = filepath.Base(call.AudioPath)
	}
	mimeType := call.AudioMime
	if mimeType == "" {
		mimeType = mimeTypeFor(name)
	}
	system := call.SystemLabel
	if system == "" {
		system = options.SystemLabel
	}
	if system == "" {
		system = "sdrstream"
	}
	talkgroup := call.TalkgroupLabel
	if talkgroup == "" {
		if call.FrequencyHz > 0 {
			talkgroup = fmt.Sprintf("%.4f MHz", call.FrequencyHz/1e6)
		} else {
			talkgroup = "sdrstream"
		}
	}
	stamp := call.Timestamp
	if stamp.IsZero() {
		stamp = time.Now()
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := [][2]string{
		{"key", options.APIKey},
		{"audioName", name},
		{"audioMime", mimeType},
		{"timestamp", strconv.FormatInt(stamp.UnixMilli(), 10)},
		{"systemLabel", system},
		{"talkgroupLabel", talkgroup},
	}
	if call.FrequencyHz > 0 {
		fields = append(fields, [2]string{"frequency", strconv.FormatUint(uint64(call.FrequencyHz), 10)})
	}
	if call.TalkgroupGroup != "" {
		fields = append(fields, [2]string{"talkgroupGroup", call.TalkgroupGroup})
	}
	if call.TalkgroupTag != "" {
		fields = append(fields, [2]string{"talkgroupTag", call.TalkgroupTag})
	}
	if call.Site > 0 {
		fields = append(fields, [2]string{"site", strconv.Itoa(call.Site)})
	}
	if call.Unit > 0 {
		fields = append(fields, [2]string{"unit", strconv.Itoa(call.Unit)})
	}
	for _, field := range fields {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return fmt.Errorf("rdio: write %s: %w", field[0], err)
		}
	}
	part, err := writer.CreateFormFile("audio", name)
	if err != nil {
		return fmt.Errorf("rdio: create audio part: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return fmt.Errorf("rdio: write audio: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("rdio: close multipart: %w", err)
	}

	endpoint := strings.TrimRight(options.BaseURL, "/") + "/api/call-upload"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return fmt.Errorf("rdio: build request: %w", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("rdio: upload: %w", err)
	}
	defer response.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("rdio: server returned %s: %s", response.Status, strings.TrimSpace(string(reply)))
	}
	if !strings.Contains(string(reply), "imported successfully") {
		return fmt.Errorf("rdio: unexpected reply: %s", strings.TrimSpace(string(reply)))
	}
	return nil
}

func mimeTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wav":
		return "audio/x-wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}

// Queue uploads calls one at a time in the background, so a slow or
// unreachable archive never stalls the detector or the decoder.
type Queue struct {
	options Options
	logf    func(format string, args ...interface{})
	calls   chan Call
	done    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once

	mu       sync.Mutex
	closed   bool
	uploaded int
	failed   int
	lastErr  error
}

// NewQueue starts an upload worker. logf may be nil.
func NewQueue(options Options, logf func(format string, args ...interface{})) *Queue {
	queue := &Queue{
		options: options,
		logf:    logf,
		calls:   make(chan Call, 64),
		done:    make(chan struct{}),
	}
	if queue.logf == nil {
		queue.logf = func(string, ...interface{}) {}
	}
	if options.Enabled() {
		queue.wg.Add(1)
		go queue.run()
	}
	return queue
}

// Enqueue schedules a call. It never blocks: if the backlog is full the
// call is dropped and counted, because losing an archive copy is better
// than blocking the receiver. Both the send and the drop take q.mu, which
// is also what makes Close race-free: a plain select-default does NOT
// protect against a send on a closed channel, so Enqueue-after-Close used
// to panic exactly when the process was shutting down.
func (q *Queue) Enqueue(call Call) {
	if !q.options.Enabled() {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	select {
	case q.calls <- call:
	default:
		q.failed++
		q.lastErr = fmt.Errorf("rdio: upload queue full, dropped %s", filepath.Base(call.AudioPath))
		q.logf("%v", q.lastErr)
	}
}

// Close drains the queue and waits for outstanding uploads.
func (q *Queue) Close() {
	q.once.Do(func() {
		q.mu.Lock()
		q.closed = true
		if q.options.Enabled() {
			close(q.calls)
		}
		// Released before wg.Wait: the upload worker takes q.mu for its
		// stats, so holding the lock here would deadlock the drain.
		q.mu.Unlock()
		if q.options.Enabled() {
			q.wg.Wait()
		}
	})
}

// Stats reports how many calls reached the archive.
func (q *Queue) Stats() (uploaded, failed int, lastErr error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.uploaded, q.failed, q.lastErr
}

func (q *Queue) run() {
	defer q.wg.Done()
	for call := range q.calls {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := Upload(ctx, q.options, call)
		cancel()
		q.mu.Lock()
		if err != nil {
			q.failed++
			q.lastErr = err
		} else {
			q.uploaded++
		}
		q.mu.Unlock()
		if err != nil {
			q.logf("rdio upload %s: %v", filepath.Base(call.AudioPath), err)
		}
	}
}

// MarshalJSON is provided so a Queue can be embedded in diagnostic
// payloads without exposing the API key.
func (o Options) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"base_url": o.BaseURL,
		"enabled":  o.Enabled(),
	})
}
