// rxprobe: bounded Stage-1 handshake probes for the cross-family receiver
// availability test. UNTRACKED tooling - never committed. One attempt per
// candidate, serial, no retries, no tuning, no DSP start (OpenWebRX probe
// stops after config), Reconnect disabled everywhere. Every attempt is a
// ledger contact, including dial failures and silent endpoints.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/chaim12345/sdrstream/go/internal/iqsrc"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaim12345/sdrstream/go/cmd/rxprobe/pcmv4"
	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

type candidate struct {
	EndpointKey string `json:"endpoint_key"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	URL         string `json:"url"`
	Family      string `json:"family"`
}

type result struct {
	Family    string `json:"family"`
	Endpoint  string `json:"endpoint"`
	StartedAt string `json:"started_at"`
	DialOK    bool   `json:"dial_ok"`
	FirstData int64  `json:"first_data_ms"`
	Msgs      int    `json:"msgs"`
	Bytes     int    `json:"bytes"`
	MsgTypes  string `json:"msg_types,omitempty"`
	Facts     string `json:"facts,omitempty"`
	Outcome   string `json:"outcome"`
	Err       string `json:"err,omitempty"`
	Writes    string `json:"writes"`
}

func main() {
	var cands []candidate
	f, err := os.Open(os.Args[1])
	if err != nil {
		fatal(err)
	}
	if err := json.NewDecoder(f).Decode(&cands); err != nil {
		fatal(err)
	}
	f.Close()
	out, err := os.Create(os.Args[2])
	if err != nil {
		fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	total := 0
	for _, c := range cands {
		r := result{Family: c.Family, Endpoint: c.EndpointKey, StartedAt: time.Now().UTC().Format(time.RFC3339)}
		stage := "1"
		if len(os.Args) > 3 {
			stage = os.Args[3]
		}
		switch {
		case stage == "2" && c.Family == "SpyServer":
			probeSpyStage2(c, &r)
		case stage == "2" && c.Family == "WebSDR":
			probeWebSDRStage2(c, &r)
		case stage == "2" && c.Family == "KiwiSDR":
			probeKiwiStage2(c, &r)
		case stage == "2" && c.Family == "OpenWebRX":
			probeOpenWebRXStage2(c, &r)
		case stage == "uber1" && c.Family == "UberSDR":
			probeUber(c, &r, nil, nil)
		case stage == "a" && c.Family == "Web-888":
			probeWeb888(c, &r, nil, nil)
		case stage == "c" && c.Family == "KiwiSDR":
			probeKiwiBatchC(c, &r)
		case stage == "c" && c.Family == "OpenWebRX":
			probeOpenWebRXStage2(c, &r)
		case stage == "c" && c.Family == "WebSDR":
			probeWebSDRStage2(c, &r)
		case stage == "retune" && c.Family == "KiwiSDR":
			probeKiwiRetune(c, &r)
		case stage == "retune" && c.Family == "OpenWebRX":
			probeOpenWebRXRetune(c, &r)
		case c.Family == "SpyServer":
			probeSpy(c, &r)
		case c.Family == "KiwiSDR":
			probeKiwi(c, &r)
		case c.Family == "WebSDR":
			probeWebSDR(c, &r)
		case c.Family == "OpenWebRX":
			probeOpenWebRX(c, &r)
		default:
			r.Outcome = "harness-bug: unknown family"
		}
		if err := enc.Encode(r); err != nil {
			fatal(err)
		}
		total++
		fmt.Printf("[%d] %s %s -> %s msgs=%d bytes=%d %s\n", total, r.Family, r.Endpoint, r.Outcome, r.Msgs, r.Bytes, r.Err)
	}
	fmt.Println("TOTAL ATTEMPTS:", total)
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "FATAL (harness stop):", err); os.Exit(2) }

func isTimeout(err error) bool {
	var ne net.Error
	if ok := errorAs(err, &ne); ok && ne.Timeout() {
		return true
	}
	return false
}

func errorAs(err error, target interface{}) bool {
	type causer interface{ Unwrap() error }
	for err != nil {
		if ne, ok := err.(net.Error); ok {
			if p, ok := target.(*net.Error); ok {
				*p = ne
				return true
			}
		}
		c, ok := err.(causer)
		if !ok {
			return false
		}
		err = c.Unwrap()
	}
	return false
}

// --- SpyServer: raw TCP, read-only 2s, then one protocol HELLO, read to 5s.
func probeSpy(c candidate, r *result) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := common.DialPublic(ctx, &d, "tcp", c.EndpointKey)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		r.Err = err.Error()
		return
	}
	defer conn.Close()
	r.DialOK = true
	r.Writes = "none-then-HELLO-if-silent"
	start := time.Now()
	types := map[uint32]int{}
	sentHello := false
	for time.Since(start) < 5*time.Second {
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var hdr [20]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			if isTimeout(err) {
				if !sentHello && time.Since(start) > 2*time.Second {
					app := "sdrstream-probe"
					body := make([]byte, 4+len(app))
					binary.LittleEndian.PutUint32(body, (2<<24)|(0<<16)|1700)
					copy(body[4:], app)
					cmd := make([]byte, 8+len(body))
					binary.LittleEndian.PutUint32(cmd[0:4], 0)
					binary.LittleEndian.PutUint32(cmd[4:8], uint32(len(body)))
					copy(cmd[8:], body)
					if _, werr := conn.Write(cmd); werr != nil {
						r.Outcome = "write-fail"
						r.Err = werr.Error()
						return
					}
					sentHello = true
				}
				continue
			}
			break
		}
		size := binary.LittleEndian.Uint32(hdr[16:20])
		if size > 1<<20 {
			r.Outcome = "harness-bug: oversize header"
			r.Err = fmt.Sprintf("size=%d", size)
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(conn, body); err != nil {
			break
		}
		mt := binary.LittleEndian.Uint32(hdr[4:8]) & 0xFFFF
		if r.Msgs == 0 {
			r.FirstData = time.Since(start).Milliseconds()
		}
		r.Msgs++
		r.Bytes += 20 + int(size)
		types[mt]++
	}
	var ts []string
	for t, n := range types {
		ts = append(ts, fmt.Sprintf("%d:%d", t, n))
	}
	r.MsgTypes = strings.Join(ts, ",")
	r.Facts = fmt.Sprintf("hello_sent=%v device_info=%d client_sync=%d", sentHello, types[0], types[1])
	switch {
	case r.Msgs > 0:
		r.Outcome = "answered"
	default:
		r.Outcome = "silent"
	}
}

// --- KiwiSDR: Connect (SET auth/agc/ident only; curFreq=0 => no Tune),
// then Record up to 4s. Server streams audio on SND after Connect: this
// attempt is counted as a contact AND a short stream, honestly.
func probeKiwi(c candidate, r *result) {
	opts := kiwisdr.DefaultOptions()
	opts.Reconnect = false
	opts.OpenTimeout = 8 * time.Second
	opts.IdleTimeout = 6 * time.Second
	r.Writes = "SET auth/AR/agc/ident (no Tune: curFreq=0)"
	cl, err := kiwisdr.New(c.EndpointKey, opts)
	if err != nil {
		r.Outcome = "harness-bug: client-build"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := cl.Connect(ctx); err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer cl.Close()
	r.DialOK = true
	rctx, rcancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer rcancel()
	samples, rate, err := cl.Record(rctx, 4*time.Second)
	if len(samples) > 0 {
		r.FirstData = time.Since(start).Milliseconds()
		r.Msgs = 1
		r.Bytes = len(samples) * 2
		r.Outcome = "answered+streamed-audio"
		r.Facts = fmt.Sprintf("samples=%d rate=%d info=%+v", len(samples), rate, cl.InfoSnapshot())
	} else if err != nil {
		r.Outcome = "connected-no-data"
		r.Err = err.Error()
	} else {
		r.Outcome = "connected-no-data"
	}
}

// --- WebSDR: Connect is dial-only (no writes); server streams immediately.
func probeWebSDR(c candidate, r *result) {
	opts := websdr.DefaultOptions()
	opts.Reconnect = false
	opts.OpenTimeout = 8 * time.Second
	opts.IdleTimeout = 6 * time.Second
	r.Writes = "none (dial-only connect)"
	u := c.URL
	if u == "" {
		u = "http://" + c.EndpointKey
	}
	cl, err := websdr.New(u, opts)
	if err != nil {
		r.Outcome = "harness-bug: client-build"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := cl.Connect(ctx); err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer cl.Close()
	r.DialOK = true
	deadline := time.After(5 * time.Second)
	seen := map[string]int{}
	for {
		select {
		case ev := <-cl.Events():
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			seen[ev.Type]++
		case <-deadline:
			var ts []string
			for t, n := range seen {
				ts = append(ts, fmt.Sprintf("%s:%d", t, n))
			}
			r.MsgTypes = strings.Join(ts, ",")
			r.Facts = fmt.Sprintf("samplerate=%d status=%+v", cl.Samplerate, cl.Status)
			if r.Msgs > 0 {
				r.Outcome = "answered+streamed-audio"
			} else {
				r.Outcome = "connected-no-data"
			}
			return
		}
	}
}

// --- OpenWebRX: minimal handshake - greeting + connectionproperties, read
// until config received or a single 5s deadline; NEVER
// selectprofile/tuning/dspcontrol. One read deadline for the whole window
// and break on ANY error: gorilla poisons a websocket connection after any
// read error (incl. timeouts) and panics on a repeated read.
func probeOpenWebRX(c candidate, r *result) {
	probeOpenWebRXWithDialer(c, r, websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)})
}

func probeOpenWebRXWithDialer(c candidate, r *result, d websocket.Dialer) {
	r.Writes = "greeting+connectionproperties only (no DSP start, no profile, no tune)"
	u, err := common.WebSocketURL(c.URL, "/ws/")
	if err != nil {
		r.Outcome = "harness-bug: url"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := d.DialContext(ctx, u, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		r.Err = err.Error()
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("SERVER DE CLIENT client=sdrstream-probe type=receiver")); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	gotConfig := false
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		} // any error ends the probe: connection is poisoned after it
		if r.Msgs == 0 {
			r.FirstData = time.Since(start).Milliseconds()
		}
		r.Msgs++
		r.Bytes += len(data)
		if mt == websocket.TextMessage {
			text := string(data)
			if strings.HasPrefix(text, "CLIENT DE SERVER") {
				conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connectionproperties","params":{"output_rate":12000,"hd_output_rate":44100}}`))
			}
			if strings.Contains(text, `"type":"config"`) || strings.Contains(text, `"type": "config"`) {
				gotConfig = true
				snip := text
				if len(snip) > 300 {
					snip = snip[:300]
				}
				r.Facts = "config: " + snip
			}
		}
	}
	if gotConfig {
		r.Outcome = "handshake-complete-config"
	} else if r.Msgs > 0 {
		r.Outcome = "answered-partial"
	} else {
		r.Outcome = "connected-no-data"
	}
}

func classifyDialErr(err error) string {
	s := err.Error()
	switch {
	case isTimeout(err):
		return "dial-timeout"
	case strings.Contains(s, "connection refused"):
		return "refused"
	case strings.Contains(s, "reset by peer"):
		return "reset"
	case strings.Contains(s, "no such host"):
		return "dns-fail"
	case strings.Contains(s, "refusing non-public"):
		return "guard-rejected"
	default:
		return "dial-fail"
	}
}

// --- Stage 2 SpyServer: HELLO + DEVICE_INFO/CLIENT_SYNC readback, then ONE
// non-tuning command (STREAMING_ENABLED=1) - no frequency, format,
// decimation or gain settings. IQ at the server's CURRENT center or nothing.
func probeSpyStage2(c candidate, r *result) {
	probeSpyStage2Dial(c, r, nil)
}

// dialFn injects an unguarded dialer for local regression tests only.
func probeSpyStage2Dial(c candidate, r *result, dialFn func(ctx context.Context, addr string) (net.Conn, error)) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r.Writes = "HELLO + SETTING streaming_enabled=1 only (no frequency/format/decimation/gain)"
	var conn net.Conn
	var err error
	if dialFn != nil {
		conn, err = dialFn(ctx, c.EndpointKey)
	} else {
		d := net.Dialer{Timeout: 8 * time.Second}
		conn, err = common.DialPublic(ctx, &d, "tcp", c.EndpointKey)
	}
	if err != nil {
		r.Outcome = classifyDialErr(err)
		r.Err = err.Error()
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	app := "sdrstream-probe"
	body := make([]byte, 4+len(app))
	binary.LittleEndian.PutUint32(body, (2<<24)|(0<<16)|1700)
	copy(body[4:], app)
	cmd := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(cmd[0:4], 0)
	binary.LittleEndian.PutUint32(cmd[4:8], uint32(len(body)))
	copy(cmd[8:], body)
	if _, err := conn.Write(cmd); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	readMsg := func() (uint32, uint32, []byte, error) {
		var hdr [20]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return 0, 0, nil, err
		}
		size := binary.LittleEndian.Uint32(hdr[16:20])
		if size > 1<<20 {
			return 0, 0, nil, fmt.Errorf("oversize %d", size)
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(conn, b); err != nil {
			return 0, 0, nil, err
		}
		return binary.LittleEndian.Uint32(hdr[4:8]) & 0xFFFF, binary.LittleEndian.Uint32(hdr[12:16]), b, nil
	}
	// phase 1: handshake readback (max 5s)
	var canControl, devCenter, iqCenter uint32
	haveInfo, haveSync := false, false
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !(haveInfo && haveSync) {
		mt, _, b, err := readMsg()
		if err != nil {
			r.Outcome = "handshake-incomplete"
			r.Err = err.Error()
			r.Facts = fmt.Sprintf("device_info=%v client_sync=%v", haveInfo, haveSync)
			return
		}
		if r.Msgs == 0 {
			r.FirstData = time.Since(start).Milliseconds()
		}
		r.Msgs++
		r.Bytes += 20 + len(b)
		switch mt {
		case 0:
			haveInfo = true
		case 1:
			haveSync = true
			if len(b) >= 16 {
				canControl = binary.LittleEndian.Uint32(b[0:4])
				devCenter = binary.LittleEndian.Uint32(b[8:12])
				iqCenter = binary.LittleEndian.Uint32(b[12:16])
			}
		}
	}
	r.Facts = fmt.Sprintf("can_control=%d device_center_hz=%d iq_center_hz=%d", canControl, devCenter, iqCenter)
	// phase 2: enable streaming only (cmd 2, body {setting=1, value=1})
	sb := make([]byte, 16)
	binary.LittleEndian.PutUint32(sb[0:4], 2)
	binary.LittleEndian.PutUint32(sb[4:8], 8)
	binary.LittleEndian.PutUint32(sb[8:12], 1) // SETTING_STREAMING_ENABLED
	binary.LittleEndian.PutUint32(sb[12:16], 1)
	if _, err := conn.Write(sb); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	// phase 3: read 5s of whatever comes; count IQ msgs + seq gaps + sync moves
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	iqMsgs, gaps, iqBytes := 0, 0, 0
	lastSeq := int64(-1)
	centerMoved := false
	for {
		mt, seq, b, err := readMsg()
		if err != nil {
			break
		}
		r.Msgs++
		r.Bytes += 20 + len(b)
		switch mt {
		case 100:
			iqMsgs++
			iqBytes += len(b)
			if lastSeq >= 0 && int64(seq) != lastSeq+1 {
				gaps++
			}
			lastSeq = int64(seq)
		case 1:
			if len(b) >= 16 && binary.LittleEndian.Uint32(b[12:16]) != iqCenter {
				centerMoved = true
			}
		}
	}
	r.Facts += fmt.Sprintf(" iq_msgs=%d iq_bytes=%d seq_gaps=%d center_moved_during_stream=%v", iqMsgs, iqBytes, gaps, centerMoved)
	if iqMsgs > 0 {
		r.Outcome = "iq-samples-received"
	} else {
		r.Outcome = "no-stream-after-enable"
	}
}

// --- Stage 2 WebSDR: <=5s passive audio + status (true frequency if the
// server sends 0x85 in the window; 0 means NOT observed - never invented).
func probeWebSDRStage2(c candidate, r *result) {
	opts := websdr.DefaultOptions()
	opts.Reconnect = false
	opts.OpenTimeout = 8 * time.Second
	opts.IdleTimeout = 6 * time.Second
	r.Writes = "none (dial-only connect)"
	u := c.URL
	if u == "" {
		u = "http://" + c.EndpointKey
	}
	cl, err := websdr.New(u, opts)
	if err != nil {
		r.Outcome = "harness-bug: client-build"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := cl.Connect(ctx); err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer cl.Close()
	r.DialOK = true
	deadline := time.After(5 * time.Second)
	samples := 0
	frames := 0
	for {
		select {
		case ev := <-cl.Events():
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			if ev.Type == "audio" {
				if ch, ok := ev.Value.(websdr.Chunk); ok {
					frames++
					samples += len(ch.Samples)
					r.Bytes += 2 * len(ch.Samples)
				}
			}
		case <-deadline:
			st := cl.Status
			centerNote := "center-not-observed-in-window"
			if st.TrueFrequency > 0 {
				centerNote = fmt.Sprintf("true_freq_hz=%.0f", st.TrueFrequency)
			}
			r.Facts = fmt.Sprintf("audio_frames=%d samples=%d samplerate=%d %s smeter=%d", frames, samples, st.Samplerate, centerNote, st.Smeter)
			if samples > 0 {
				r.Outcome = "audio-samples-received"
			} else if r.Msgs > 0 {
				r.Outcome = "answered-no-audio"
			} else {
				r.Outcome = "connected-no-data"
			}
			return
		}
	}
}

// wsMsg is one websocket message from the read pump.
type wsMsg struct {
	mt   int
	data []byte
	err  error
}

// wsPump reads messages until error/close, delivering them on ch. Gorilla
// poisons a connection on ANY read error (including a fired read deadline),
// so probes end phases with time.After in select, never with a firing read
// deadline mid-conversation. The single far deadline is a backstop only.
func wsPump(conn *websocket.Conn) chan wsMsg {
	ch := make(chan wsMsg, 64)
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			ch <- wsMsg{mt, data, err}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// probeKiwiBatchC: guarded GET /status preflight (recorded as part of this
// receiver's contact) - skip on ext_api=0 (operator configured no non-Kiwi-app
// channels, jks-prv/KiwiSDR ca54338) or unreadable status; then the stage-2
// passive-default + own-channel IQ flow with real frame parsing.
func probeKiwiBatchC(c candidate, r *result) {
	client := common.PublicHTTPClient(6 * time.Second)
	statusURL := "http://" + c.EndpointKey + "/status"
	req, err := http.NewRequest("GET", statusURL, nil)
	if err != nil {
		r.Outcome = "harness-bug: status url"
		r.Err = err.Error()
		return
	}
	req.Header.Set("User-Agent", "sdrstream-kiwi-status-preflight/1.0")
	resp, err := client.Do(req)
	if err != nil {
		r.Outcome = "skipped-status-unreadable"
		r.Err = "status preflight: " + err.Error()
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		r.Outcome = "skipped-status-unreadable"
		r.Err = fmt.Sprintf("status preflight: code=%d err=%v", resp.StatusCode, err)
		return
	}
	m := regexp.MustCompile(`ext_api=(\d+)`).FindSubmatch(body)
	if m == nil {
		// pre-v1.837 firmware: field absent does not deny - proceed, honestly noted
		r.Writes = "GET /status (no ext_api field, pre-v1.837); "
	} else if string(m[1]) == "0" {
		r.Outcome = "skipped-ext-api-zero"
		r.Err = "operator configured ext_api=0 (no non-Kiwi-app channels)"
		return
	} else {
		r.Writes = "GET /status ext_api=" + string(m[1]) + "; "
	}
	r.Bytes += len(body)
	r.Msgs++
	probeKiwiStage2(c, r)
	r.Writes = "preflight; " + r.Writes
	if r.Outcome == "" {
		r.Outcome = "harness-bug: empty outcome after stage2"
	}
}

// newUUID: upstream validates user_session_id with isValidUUID at BOTH
// /connection (main.go:4019) and /ws (websocket.go), so the probe must
// present a real UUID, generated once per attempt and shared by both legs.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- Batch A Web-888 (RaspSDR fork): guarded GET /status preflight then the
// same passive-default + own-channel IQ flow as the approved Kiwi leg, but
// with the STANDALONE fork-verified parser (web888.go) and fork-specific
// access guards (parent 10:24:19):
//   - the fork's /status has NO ext_api field: a missing field is EXPECTED
//     and is not a denial; a present ext_api=0 is still a hard skip.
//   - too_busy => refused-busy, stop, no retry, no backoff gaming. too_busy
//     is NOT unambiguous hardware-full (API allowance can trigger it) - the
//     ledger records it plainly without interpretation.
//   - honest identity: sdrstream UA + ident_user=sdrstream-probe; no browser
//     fingerprint spoofing, no bypass of any kind.
//   - no invented frequency: SET mod=iq only at the passively observed
//     init.freq on our own channel; no observed default => stop.
func probeWeb888(c candidate, r *result, rt http.RoundTripper, d *websocket.Dialer) {
	client := common.PublicHTTPClient(5 * time.Second)
	if rt != nil {
		client = &http.Client{Transport: rt}
	}
	statusURL := "http://" + c.EndpointKey + "/status"
	req, err := http.NewRequest(http.MethodGet, statusURL, nil)
	if err != nil {
		r.Outcome = "harness-bug: status url"
		r.Err = err.Error()
		return
	}
	req.Header.Set("User-Agent", "sdrstream-web888-status-preflight/1.0")
	resp, err := client.Do(req)
	if err != nil {
		r.Outcome = "skipped-status-unreadable"
		r.Err = "status preflight: " + err.Error()
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		r.Outcome = "skipped-status-unreadable"
		r.Err = fmt.Sprintf("status preflight: code=%d err=%v", resp.StatusCode, err)
		return
	}
	r.Msgs++
	r.Bytes += len(body)
	if m := regexp.MustCompile(`ext_api=(\d+)`).FindSubmatch(body); m != nil {
		if string(m[1]) == "0" {
			r.Outcome = "skipped-ext-api-zero"
			r.Err = "operator configured ext_api=0 (no non-app channels)"
			return
		}
		r.Writes = "GET /status ext_api=" + string(m[1]) + " (unexpected on fork); "
	} else {
		r.Writes = "GET /status (no ext_api field - expected on RaspSDR fork, not a denial); "
	}
	probeWeb888WS(c, r, d)
	r.Writes = "preflight; " + r.Writes
	if r.Outcome == "" {
		r.Outcome = "harness-bug: empty outcome after ws"
	}
}

func probeWeb888WS(c candidate, r *result, d *websocket.Dialer) {
	r.Writes += "SET auth/AR/agc/ident; SET mod=iq ONLY at passively-observed init.freq on own channel"
	uri := fmt.Sprintf("ws://%s/%d/SND", c.EndpointKey, time.Now().Unix())
	dial := websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)}
	if d != nil {
		dial = *d
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := dial.DialContext(ctx, uri, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer conn.Close()
	r.DialOK = true
	// A busy server can announce too_busy and close while we are still
	// writing the SET lines, so a write error is recorded and checked
	// against what actually arrived instead of masking a busy refusal.
	var writeErr error
	write := func(s string) bool {
		if writeErr != nil {
			return false
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
			writeErr = err
			return false
		}
		return true
	}
	for _, l := range []string{"SET auth t=kiwi p=#", "SET AR OK in=12000 out=44100", "SET agc=1 hang=0 thresh=-100 slope=6 decay=1000 manGain=50", "SET ident_user=sdrstream-probe"} {
		write(l)
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ch := wsPump(conn)
	var initFreq, freqOffset, audioRate float64
	haveInit := false
	refused := ""
	phase1 := time.After(5 * time.Second)
p1:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p1
			}
			r.Msgs++
			r.Bytes += len(m.data)
			text := ""
			if m.mt == websocket.TextMessage {
				text = string(m.data)
			} else if len(m.data) >= 4 && string(m.data[:3]) == "MSG" {
				text = "MSG " + string(m.data[4:])
			}
			if strings.HasPrefix(text, "MSG ") {
				for _, pair := range strings.Fields(text[4:]) {
					name, value, ok := strings.Cut(pair, "=")
					if !ok {
						continue
					}
					switch name {
					case "load_cfg":
						if un, err := url.QueryUnescape(value); err == nil {
							var cfg struct {
								Init struct {
									Freq float64 `json:"freq"`
									Mode string  `json:"mode"`
								} `json:"init"`
							}
							if json.Unmarshal([]byte(un), &cfg) == nil && cfg.Init.Freq > 0 {
								initFreq = cfg.Init.Freq
								haveInit = true
								r.MsgTypes = "init_mode=" + cfg.Init.Mode
							}
						}
					case "freq_offset":
						fmt.Sscanf(value, "%g", &freqOffset)
					case "audio_rate":
						fmt.Sscanf(value, "%g", &audioRate)
					case "too_busy":
						refused = value
					}
				}
			}
			if haveInit {
				break p1
			}
		case <-phase1:
			break p1
		}
	}
	if refused != "" {
		r.Outcome = "refused-busy"
		r.Facts = "too_busy=" + refused + " (not unambiguous hardware-full: API allowance can trigger it)"
		return
	}
	if writeErr != nil {
		r.Outcome = "write-fail"
		r.Err = writeErr.Error()
		return
	}
	if !haveInit {
		r.Outcome = "no-passive-default-freq"
		r.Facts = fmt.Sprintf("msgs=%d (load_cfg init.freq not observed; stopped, no frequency invented)", r.Msgs)
		return
	}
	bb := initFreq - freqOffset
	if !write(fmt.Sprintf("SET mod=iq low_cut=-5000 high_cut=5000 freq=%.3f", bb)) {
		return
	}
	iqFrames, other := 0, 0
	parsedSamples, parseErrs := 0, 0
	var flagsSeen []string
	phase2 := time.After(5 * time.Second)
p2:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p2
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt == websocket.BinaryMessage && len(m.data) >= 4 && string(m.data[:3]) == "SND" {
				iqFrames++
				if len(flagsSeen) < 4 {
					flagsSeen = append(flagsSeen, fmt.Sprintf("0x%02x", m.data[3]))
				}
				if fr, perr := parseWeb888IQFrame(m.data); perr != nil {
					parseErrs++
				} else {
					parsedSamples += len(fr.Samples)
				}
			} else {
				other++
			}
		case <-phase2:
			break p2
		}
	}
	r.Facts = fmt.Sprintf("init_freq_khz=%.3f freq_offset_khz=%.3f sent_freq_khz=%.3f audio_rate=%.0f snd_frames=%d flags=%s other_msgs=%d parsed_iq_int16=%d parse_errs=%d", initFreq, freqOffset, bb, audioRate, iqFrames, strings.Join(flagsSeen, ","), other, parsedSamples, parseErrs)
	switch {
	case parsedSamples > 0:
		r.Outcome = "iq-samples-decoded"
	case iqFrames > 0:
		r.Outcome = "iq-mode-stream-received-format-undecoded"
	default:
		r.Outcome = "no-iq-after-mode-set"
	}
}

// --- UberSDR leg: POST /connection precheck + /ws IQ socket, the same
// handshake shape the iqsrc adapter (862cec8) performs, with the approved
// pins: public-only dial (AllowPrivate=false), default TLS verification
// (InsecureTLS=false), empty password, one 5s total window covering BOTH
// legs, one POST + one WS dial = ONE ledgered contact, a 429 in either leg
// ends the attempt with NO retry.
//
// Frequency honesty: the /ws URL omits the frequency parameter entirely.
// Upstream (ka9q_ubersdr websocket.go:540 @ 7192bfd6) then applies its own
// server-side default of 14074000 Hz (20m FT8) - the receiver's published
// default, never an invented value. The default is recorded in Writes so
// the ledger shows exactly what was asked for.
//
// The session id is a fresh UUID per attempt: upstream validates UUID
// shape at both endpoints (main.go:4019, websocket.go) and requires the
// /ws session to have passed /connection first (UA mapping check).
func probeUber(c candidate, r *result, rt http.RoundTripper, wsDialer *websocket.Dialer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sid := newUUID()
	wide, ok := probeUberCheck(ctx, c, r, rt, sid)
	if !ok {
		return
	}
	probeUberWS(ctx, c, r, wsDialer, sid, wide)
}

func uberBase(c candidate) string {
	if c.URL != "" {
		return c.URL
	}
	return "http://" + c.EndpointKey
}

const uberProbeUA = "sdrstream-uber-precheck/1.0"

func probeUberCheck(ctx context.Context, c candidate, r *result, rt http.RoundTripper, sid string) ([]string, bool) {
	r.Writes = "POST /connection (empty password, UUID session)"
	client := common.PublicHTTPClient(5 * time.Second)
	if rt != nil {
		client = &http.Client{Transport: rt}
	}
	body, _ := json.Marshal(map[string]string{"user_session_id": sid, "password": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uberBase(c)+"/connection", bytes.NewReader(body))
	if err != nil {
		r.Outcome = "harness-bug: connection url"
		r.Err = err.Error()
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", uberProbeUA)
	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			r.Outcome = "silent"
		} else {
			r.Outcome = "dial-fail"
		}
		r.Err = err.Error()
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	r.Msgs++
	r.Bytes += len(raw)
	r.DialOK = true
	if resp.StatusCode == http.StatusTooManyRequests {
		r.Outcome = "answered-rate-limited"
		r.Err = "HTTP 429 on /connection; no retry per policy"
		return nil, false
	}
	if err != nil || resp.StatusCode != 200 {
		r.Outcome = "answered-error"
		r.Err = fmt.Sprintf("HTTP %d err=%v", resp.StatusCode, err)
		return nil, false
	}
	var payload struct {
		Allowed        bool     `json:"allowed"`
		Reason         string   `json:"reason"`
		AllowedIQModes []string `json:"allowed_iq_modes"`
	}
	if jerr := json.Unmarshal(raw, &payload); jerr != nil {
		r.Outcome = "answered-unparseable"
		r.Err = "bad /connection body: " + jerr.Error()
		return nil, false
	}
	wide := []string{}
	for _, m := range payload.AllowedIQModes {
		switch m {
		case "iq48", "iq96", "iq192", "iq384":
			wide = append(wide, m)
		}
	}
	switch {
	case !payload.Allowed:
		r.Outcome = "answered-not-allowed"
		r.Err = payload.Reason
		return nil, false
	case len(wide) == 0:
		r.Outcome = "answered-no-public-wide-mode"
		return nil, false
	}
	return wide, true
}

// probeUberWS opens the IQ socket at the smallest allowed wide mode (least
// bandwidth) and decodes binary PCM4 frames with the reviewed bounded
// decoder (vendored from 862cec8) inside the same shared 5s window: real IQ
// samples verified, no extra contact, retry, or time.
func probeUberWS(ctx context.Context, c candidate, r *result, wsDialer *websocket.Dialer, sid string, wide []string) {
	mode := wide[0]
	for _, m := range wide {
		if m == "iq48" {
			mode = m
		}
	}
	base := uberBase(c)
	wsBase := "ws://" + strings.TrimPrefix(base, "http://")
	if strings.HasPrefix(base, "https://") {
		wsBase = "wss://" + strings.TrimPrefix(base, "https://")
	}
	q := url.Values{}
	q.Set("mode", mode)
	q.Set("user_session_id", sid)
	q.Set("format", "pcm-zstd")
	q.Set("version", "4")
	// NO frequency parameter: the server applies its own default
	// (14074000 Hz, upstream websocket.go:540). Nothing invented.
	u := wsBase + "/ws?" + q.Encode()
	r.Writes += "; WS /ws mode=" + mode + " format=pcm-zstd v4, frequency omitted (server default 14074000 Hz)"
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDialContext: common.PublicDialContext(5 * time.Second)}
	if wsDialer != nil {
		d = *wsDialer
	}
	h := http.Header{}
	h.Set("User-Agent", uberProbeUA)
	conn, resp, err := d.DialContext(ctx, u, h)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			r.Outcome = "answered-rate-limited"
			r.Err = "HTTP 429 on /ws; no retry per policy"
			return
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		r.Outcome = "ws-fail"
		r.Err = fmt.Sprintf("ws dial: %v (HTTP %d)", err, code)
		return
	}
	defer conn.Close()
	frames, decodeErrs, totalSamples := 0, 0, 0
	rate, channels := 0, 0
	dec := pcmv4.NewPCMv4StreamDecoder() // reviewed bounded decoder, vendored from 862cec8; one per connection
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		r.Msgs++
		r.Bytes += len(data)
		if mt == websocket.BinaryMessage {
			frames++
			if h, samples, derr := dec.DecodePacket(data); derr != nil {
				decodeErrs++
			} else {
				totalSamples += len(samples)
				rate, channels = h.SampleRate, h.Channels
			}
		}
		if ctx.Err() != nil || frames >= 32 {
			break
		}
	}
	switch {
	case totalSamples > 0:
		r.Outcome = "iq-samples-decoded"
		r.MsgTypes = fmt.Sprintf("mode=%s binary-frames=%d samples=%d rate=%d channels=%d", mode, frames, totalSamples, rate, channels)
		r.Err = "decoded with reviewed pcmv4 (vendored 862cec8); frequency = server default 14074000 Hz"
	case frames > 0:
		r.Outcome = "iq-frames-received"
		r.MsgTypes = fmt.Sprintf("mode=%s binary-frames=%d", mode, frames)
		r.Err = fmt.Sprintf("frames only: %d/%d packets failed pcmv4 decode; frequency = server default 14074000 Hz", decodeErrs, frames)
	case r.Outcome == "":
		r.Outcome = "answered-no-frames"
		r.Err = "ws connected at server default frequency; no binary frames inside the window"
	}
}

// --- Stage 2 KiwiSDR: raw SND connection, auth only, PASSIVELY read MSG
// lines for the server-configured default (load_cfg init.freq, plus
// freq_offset). Only if a default is observed: SET mod=iq at exactly that
// frequency on OUR OWN channel (Kiwi channels are independent per
// connection - official docs), then count IQ SND frames <=5s. No observed
// default => stop, no frequency is ever invented.
func probeKiwiStage2(c candidate, r *result) {
	probeKiwiStage2Dial(c, r, websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)})
}

func probeKiwiStage2Dial(c candidate, r *result, d websocket.Dialer) {
	r.Writes = "SET auth/AR/agc/ident; SET mod=iq ONLY at passively-observed init.freq on own channel"
	uri := fmt.Sprintf("ws://%s/%d/SND", c.EndpointKey, time.Now().Unix())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := d.DialContext(ctx, uri, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	write := func(s string) bool {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
			r.Outcome = "write-fail"
			r.Err = err.Error()
			return false
		}
		return true
	}
	for _, l := range []string{"SET auth t=kiwi p=#", "SET AR OK in=12000 out=44100", "SET agc=1 hang=0 thresh=-100 slope=6 decay=1000 manGain=50", "SET ident_user=sdrstream-probe"} {
		if !write(l) {
			return
		}
	}
	// phase 1: passive MSG capture (message/timer-driven; far backstop deadline)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ch := wsPump(conn)
	var initFreq, freqOffset float64
	haveInit := false
	refused := ""
	audioRate := 0.0
	phase1 := time.After(5 * time.Second)
p1:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p1
			}
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			r.Bytes += len(m.data)
			text := ""
			if m.mt == websocket.TextMessage {
				text = string(m.data)
			} else if len(m.data) >= 4 && string(m.data[:3]) == "MSG" {
				text = "MSG " + string(m.data[4:])
			}
			if strings.HasPrefix(text, "MSG ") {
				for _, pair := range strings.Fields(text[4:]) {
					name, value, ok := strings.Cut(pair, "=")
					if !ok {
						continue
					}
					switch name {
					case "load_cfg":
						if un, err := url.QueryUnescape(value); err == nil {
							var cfg struct {
								Init struct {
									Freq float64 `json:"freq"`
									Mode string  `json:"mode"`
								} `json:"init"`
							}
							if json.Unmarshal([]byte(un), &cfg) == nil && cfg.Init.Freq > 0 {
								initFreq = cfg.Init.Freq
								haveInit = true
								r.MsgTypes = "init_mode=" + cfg.Init.Mode
							}
						}
					case "freq_offset":
						fmt.Sscanf(value, "%g", &freqOffset)
					case "audio_rate":
						fmt.Sscanf(value, "%g", &audioRate)
					case "too_busy":
						refused = value
					}
				}
			}
			if haveInit {
				break p1
			} // got what we came for; don't burn the window
		case <-phase1:
			break p1
		}
	}
	if refused != "" {
		r.Outcome = "refused-busy"
		r.Facts = "too_busy=" + refused
		return
	}
	if !haveInit {
		r.Outcome = "no-passive-default-freq"
		r.Facts = fmt.Sprintf("msgs=%d (load_cfg init.freq not observed; stopped, no frequency invented)", r.Msgs)
		return
	}
	// phase 2: own-channel IQ mode at the observed default (baseband = freq - offset)
	bb := initFreq - freqOffset
	if !write(fmt.Sprintf("SET mod=iq low_cut=-5000 high_cut=5000 freq=%.3f", bb)) {
		return
	}
	iqFrames, iqBytes, other := 0, 0, 0
	var flagsSeen []string
	var captured [][]byte
	phase2 := time.After(5 * time.Second)
p2:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p2
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt == websocket.BinaryMessage && len(m.data) >= 10 && string(m.data[:3]) == "SND" {
				iqFrames++
				iqBytes += len(m.data) - 10
				if len(captured) < 64 {
					fr := make([]byte, len(m.data))
					copy(fr, m.data)
					captured = append(captured, fr)
				}
				if len(flagsSeen) < 4 {
					flagsSeen = append(flagsSeen, fmt.Sprintf("0x%02x", m.data[3]))
				}
			} else {
				other++
			}
		case <-phase2:
			break p2
		}
	}
	parsedFrames, parsedSamples := 0, 0
	pbuf := make([]complex64, 8192)
	for _, fr := range captured {
		_, n, err := iqsrc.ParseKiwiIQFrame(fr, pbuf)
		if err == nil {
			parsedFrames++
			parsedSamples += n
		}
	}
	r.Facts = fmt.Sprintf("init_freq_khz=%.3f freq_offset_khz=%.3f sent_freq_khz=%.3f audio_rate=%.0f snd_frames=%d snd_payload_bytes=%d flags=%s other_msgs=%d parsed_iq_frames=%d parsed_iq_samples=%d", initFreq, freqOffset, bb, audioRate, iqFrames, iqBytes, strings.Join(flagsSeen, ","), other, parsedFrames, parsedSamples)
	if parsedFrames > 0 {
		r.Outcome = "iq-samples-decoded"
	} else if iqFrames > 0 {
		r.Outcome = "iq-mode-stream-received-format-undecoded"
	} else {
		r.Outcome = "no-iq-after-mode-set"
	}
}

// --- Stage 2 OpenWebRX: minimal handshake, config announces the server's
// active profile; then ONLY {"type":"dspcontrol","action":"start"} - no
// selectprofile, no setparams, no tuning (a profile switch is the shared
// action; per-session DSP start on the current profile is what every
// browser does on page load). Count binary frames by opcode <=5s.
func probeOpenWebRXStage2(c candidate, r *result) {
	probeOpenWebRXStage2Dial(c, r, websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)})
}

func probeOpenWebRXStage2Dial(c candidate, r *result, d websocket.Dialer) {
	r.Writes = "greeting+connectionproperties+dspcontrol start ONLY (no selectprofile/setparams/tuning)"
	u, err := common.WebSocketURL(c.URL, "/ws/")
	if err != nil {
		r.Outcome = "harness-bug: url"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := d.DialContext(ctx, u, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		r.Err = err.Error()
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("SERVER DE CLIENT client=sdrstream-probe type=receiver")); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second)) // backstop only; phases end on timers
	ch := wsPump(conn)
	profile, center, sfreq, smod := "", int64(0), int64(0), ""
	phase1 := time.After(5 * time.Second)
p1:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p1
			}
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt != websocket.TextMessage {
				continue
			}
			text := string(m.data)
			if strings.HasPrefix(text, "CLIENT DE SERVER") {
				conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connectionproperties","params":{"output_rate":12000,"hd_output_rate":44100}}`))
			}
			if strings.Contains(text, `"type": "config"`) || strings.Contains(text, `"type":"config"`) {
				var msg struct {
					Value struct {
						ProfileID string `json:"profile_id"`
						SdrID     string `json:"sdr_id"`
						Center    int64  `json:"center_freq"`
						StartFreq int64  `json:"start_freq"`
						StartMod  string `json:"start_mod"`
					} `json:"value"`
				}
				if json.Unmarshal(m.data, &msg) == nil {
					profile, center, sfreq, smod = msg.Value.ProfileID, msg.Value.Center, msg.Value.StartFreq, msg.Value.StartMod
				}
			}
			if profile != "" {
				break p1
			} // config announced: proceed without burning the window
		case <-phase1:
			break p1
		}
	}
	if profile == "" {
		r.Outcome = "no-config-profile"
		r.Facts = fmt.Sprintf("msgs=%d (config not observed; stopped)", r.Msgs)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"dspcontrol","action":"start"}`)); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	ops := map[int]int{}
	opBytes := map[int]int{}
	phase2 := time.After(5 * time.Second)
p2:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p2
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt == websocket.BinaryMessage && len(m.data) > 0 {
				ops[int(m.data[0])]++
				opBytes[int(m.data[0])] += len(m.data) - 1
			}
		case <-phase2:
			break p2
		}
	}
	audioFrames := ops[0x02] + ops[0x04]
	audioBytes := opBytes[0x02] + opBytes[0x04]
	r.Facts = fmt.Sprintf("profile=%s center_hz=%d start_freq_hz=%d start_mod=%s audio_frames=%d audio_bytes=%d fft_frames=%d fft_bytes=%d", profile, center, sfreq, smod, audioFrames, audioBytes, ops[0x01], opBytes[0x01])
	if audioFrames > 0 {
		r.Outcome = "audio-frames-received"
	} else if r.Msgs > 0 {
		r.Outcome = "answered-no-audio"
	} else {
		r.Outcome = "connected-no-data"
	}
}

// --- Retune probes (own-session only). Kiwi: own independent channel.
// OpenWebRX: per-session demod WITHIN the profile span the server already
// serves (device center only changes on profile switch, which we never
// send). Neither protocol offers a frequency readback: outcomes are
// classified as accepted-samples-both / changed-but-no-samples / rejected,
// with s-meter and payload-difference evidence, never "RF-verified".
func probeKiwiRetune(c candidate, r *result) {
	probeKiwiRetuneDial(c, r, websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)})
}

func probeKiwiRetuneDial(c candidate, r *result, d websocket.Dialer) {
	r.Writes = "SET auth/AR/agc/ident; SET mod=iq freq=<observed init.freq>; ONE retune SET freq=<+500kHz within 0-30MHz> on own channel"
	uri := fmt.Sprintf("ws://%s/%d/SND", c.EndpointKey, time.Now().Unix())
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	conn, _, err := d.DialContext(ctx, uri, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		r.Err = err.Error()
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	write := func(s string) bool {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
			r.Outcome = "write-fail"
			r.Err = err.Error()
			return false
		}
		return true
	}
	for _, l := range []string{"SET auth t=kiwi p=#", "SET AR OK in=12000 out=44100", "SET agc=1 hang=0 thresh=-100 slope=6 decay=1000 manGain=50", "SET ident_user=sdrstream-probe"} {
		if !write(l) {
			return
		}
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ch := wsPump(conn)
	var initFreq, freqOffset float64
	haveInit := false
	phase1 := time.After(5 * time.Second)
p1:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p1
			}
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			r.Bytes += len(m.data)
			text := ""
			if m.mt == websocket.TextMessage {
				text = string(m.data)
			} else if len(m.data) >= 4 && string(m.data[:3]) == "MSG" {
				text = "MSG " + string(m.data[4:])
			}
			if strings.HasPrefix(text, "MSG ") {
				for _, pair := range strings.Fields(text[4:]) {
					name, value, ok := strings.Cut(pair, "=")
					if !ok {
						continue
					}
					switch name {
					case "load_cfg":
						if un, err := url.QueryUnescape(value); err == nil {
							var cfg struct {
								Init struct {
									Freq float64 `json:"freq"`
								} `json:"init"`
							}
							if json.Unmarshal([]byte(un), &cfg) == nil && cfg.Init.Freq > 0 {
								initFreq = cfg.Init.Freq
								haveInit = true
							}
						}
					case "freq_offset":
						fmt.Sscanf(value, "%g", &freqOffset)
					}
				}
			}
			if haveInit {
				break p1
			}
		case <-phase1:
			break p1
		}
	}
	if !haveInit {
		r.Outcome = "no-passive-default-freq"
		r.Facts = fmt.Sprintf("msgs=%d", r.Msgs)
		return
	}
	f0 := initFreq - freqOffset
	f1 := f0 + 500
	if f1 > 30000 {
		f1 = f0 - 500
	}
	if !write(fmt.Sprintf("SET mod=iq low_cut=-5000 high_cut=5000 freq=%.3f", f0)) {
		return
	}
	frames0, sm0, h0 := kiwiCollect(ch, r, 3*time.Second)
	if frames0 == 0 {
		r.Outcome = "no-iq-after-mode-set"
		r.Facts = fmt.Sprintf("f0_khz=%.3f frames=0", f0)
		return
	}
	if !write(fmt.Sprintf("SET freq=%.3f", f1)) {
		return
	}
	frames1, sm1, h1 := kiwiCollect(ch, r, 3*time.Second)
	differ := h0 != h1
	r.Facts = fmt.Sprintf("f0_khz=%.3f f1_khz=%.3f frames_f0=%d frames_f1=%d smeter_f0=%d smeter_f1=%d payload_differs=%v readback=none-in-protocol", f0, f1, frames0, frames1, sm0, sm1, differ)
	switch {
	case frames1 > 0 && differ:
		r.Outcome = "retune-accepted-samples-both"
	case frames1 > 0:
		r.Outcome = "retune-accepted-samples-both-identical-payloads-suspicious"
	case frames0 > 0:
		r.Outcome = "changed-but-no-samples"
	default:
		r.Outcome = "rejected"
	}
}

// kiwiCollect counts SND frames for window, returns frames, last smeter
// (bytes 8:10 BE unsigned) and an XOR payload fingerprint.
func kiwiCollect(ch chan wsMsg, r *result, window time.Duration) (int, int, uint64) {
	frames := 0
	sm := 0
	var hash uint64
	t := time.After(window)
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				return frames, sm, hash
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt == websocket.BinaryMessage && len(m.data) >= 10 && string(m.data[:3]) == "SND" {
				frames++
				sm = int(binary.BigEndian.Uint16(m.data[8:10]))
				for i := 10; i < len(m.data); i++ {
					hash = hash*33 + uint64(m.data[i])
				}
			}
		case <-t:
			return frames, sm, hash
		}
	}
}

func probeOpenWebRXRetune(c candidate, r *result) {
	probeOpenWebRXRetuneDial(c, r, websocket.Dialer{HandshakeTimeout: 8 * time.Second, NetDialContext: common.PublicDialContext(8 * time.Second)})
}

func probeOpenWebRXRetuneDial(c candidate, r *result, d websocket.Dialer) {
	r.Writes = "greeting+connectionproperties+dspcontrol start; ONE dspcontrol frequency WITHIN the served profile span (per-session demod, no profile switch)"
	u, err := common.WebSocketURL(c.URL, "/ws/")
	if err != nil {
		r.Outcome = "harness-bug: url"
		r.Err = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	conn, _, err := d.DialContext(ctx, u, nil)
	if err != nil {
		r.Outcome = classifyDialErr(err)
		r.Err = err.Error()
		if strings.Contains(err.Error(), "websocket") {
			r.Outcome = "ws-fail"
		}
		return
	}
	defer conn.Close()
	r.DialOK = true
	start := time.Now()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("SERVER DE CLIENT client=sdrstream-probe type=receiver")); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	ch := wsPump(conn)
	var center, sfreq, sampRate int64
	var profile string
	phase1 := time.After(5 * time.Second)
p1:
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				break p1
			}
			if r.Msgs == 0 {
				r.FirstData = time.Since(start).Milliseconds()
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt != websocket.TextMessage {
				continue
			}
			text := string(m.data)
			if strings.HasPrefix(text, "CLIENT DE SERVER") {
				conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connectionproperties","params":{"output_rate":12000,"hd_output_rate":44100}}`))
			}
			if strings.Contains(text, `"type": "config"`) || strings.Contains(text, `"type":"config"`) {
				var msg struct {
					Value struct {
						ProfileID string `json:"profile_id"`
						Center    int64  `json:"center_freq"`
						StartFreq int64  `json:"start_freq"`
						SampRate  int64  `json:"samp_rate"`
					} `json:"value"`
				}
				if json.Unmarshal(m.data, &msg) == nil {
					profile, center, sfreq, sampRate = msg.Value.ProfileID, msg.Value.Center, msg.Value.StartFreq, msg.Value.SampRate
				}
			}
			if profile != "" {
				break p1
			}
		case <-phase1:
			break p1
		}
	}
	if profile == "" || sampRate == 0 {
		r.Outcome = "no-config-profile"
		r.Facts = fmt.Sprintf("msgs=%d", r.Msgs)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"dspcontrol","action":"start"}`)); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	f0 := sfreq
	f1 := sfreq + sampRate/10
	lo, hi := center-sampRate/2+sampRate/20, center+sampRate/2-sampRate/20
	if f1 > hi || f1 < lo {
		f1 = sfreq - sampRate/10
	}
	if f1 < lo || f1 > hi || f1 == f0 {
		r.Outcome = "harness-bug: no in-span retune target"
		r.Facts = fmt.Sprintf("center=%d span=%d start=%d", center, sampRate, sfreq)
		return
	}
	audio0 := owrxCollect(ch, r, 3*time.Second)
	if audio0 == 0 {
		r.Outcome = "no-audio-at-start-freq"
		r.Facts = fmt.Sprintf("profile=%s f0=%d", profile, f0)
		return
	}
	tune, _ := json.Marshal(map[string]interface{}{"type": "dspcontrol", "frequency": f1})
	if err := conn.WriteMessage(websocket.TextMessage, tune); err != nil {
		r.Outcome = "write-fail"
		r.Err = err.Error()
		return
	}
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"dspcontrol","action":"start"}`))
	audio1 := owrxCollect(ch, r, 3*time.Second)
	r.Facts = fmt.Sprintf("profile=%s center_hz=%d span_hz=%d f0_hz=%d f1_hz=%d audio_frames_f0=%d audio_frames_f1=%d readback=none-in-protocol", profile, center, sampRate, f0, f1, audio0, audio1)
	switch {
	case audio1 > 0:
		r.Outcome = "retune-accepted-samples-both"
	case audio0 > 0:
		r.Outcome = "changed-but-no-samples"
	default:
		r.Outcome = "rejected"
	}
}

func owrxCollect(ch chan wsMsg, r *result, window time.Duration) int {
	frames := 0
	t := time.After(window)
	for {
		select {
		case m := <-ch:
			if m.err != nil {
				return frames
			}
			r.Msgs++
			r.Bytes += len(m.data)
			if m.mt == websocket.BinaryMessage && len(m.data) > 0 && (m.data[0] == 0x02 || m.data[0] == 0x04) {
				frames++
			}
		case <-t:
			return frames
		}
	}
}
