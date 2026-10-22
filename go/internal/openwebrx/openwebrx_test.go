package openwebrx

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startMockServer speaks just enough OpenWebRX to complete the handshake. It
// records every client message so a test can pin the exact sequence.
func startMockServer(t *testing.T) (*httptest.Server, <-chan map[string]interface{}) {
	t.Helper()
	received := make(chan map[string]interface{}, 16)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage,
			[]byte("CLIENT DE SERVER client=test type=receiver")); err != nil {
			return
		}
		configured := false
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var message map[string]interface{}
			if json.Unmarshal(data, &message) != nil {
				continue
			}
			received <- message
			if message["type"] != "connectionproperties" || configured {
				continue
			}
			configured = true
			// The last config message carries the centre frequency, and
			// the profiles list lets the handshake finish at once.
			conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"type":"config","value":{"center_freq":145000000,"audio_compression":"adpcm"}}`))
			conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"type":"profiles","value":[{"id":"sdr|p","name":"test"}]}`))
		}
	}))
	t.Cleanup(server.Close)
	return server, received
}

// testOptions allows loopback mock servers, which the SSRF guard refuses
// by default.
func testOptions() Options {
	o := DefaultOptions()
	o.AllowPrivate = true
	return o
}

// next waits for the next client message, failing the test if none arrives.
func next(t *testing.T, received <-chan map[string]interface{}) map[string]interface{} {
	t.Helper()
	select {
	case message := <-received:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("the client sent no message")
		return nil
	}
}

// TestTuneMessageSequence pins the wire sequence around a retune. Some
// OpenWebRX servers stop the demodulator when the tuning changes and stay
// silent until they are told to start again, so Tune must send the new
// tuning and then dspcontrol action=start, in that order. This test exists
// because that second message is easy to remove without noticing: a real
// receiver just goes quiet.
func TestTuneMessageSequence(t *testing.T) {
	server, received := startMockServer(t)
	client, err := New(server.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// Handshake: connection properties, then the DSP start. Connect has no
	// pending tuning, so nothing else may precede the start.
	if got := next(t, received); got["type"] != "connectionproperties" {
		t.Fatalf("first message = %v, want connectionproperties", got)
	}
	if got := next(t, received); got["type"] != "dspcontrol" || got["action"] != "start" {
		t.Fatalf("second message = %v, want dspcontrol action=start", got)
	}

	frequency, bandwidth := int64(145_500_000), int64(2_700)
	if err := client.Tune(ctx, &frequency, "usb", &bandwidth); err != nil {
		t.Fatalf("tune: %v", err)
	}

	tuning := next(t, received)
	if tuning["type"] != "dspcontrol" {
		t.Fatalf("tuning message = %v, want dspcontrol", tuning)
	}
	if tuning["frequency"] != float64(frequency) {
		t.Errorf("frequency = %v, want %d", tuning["frequency"], frequency)
	}
	if tuning["modulation"] != "usb" {
		t.Errorf("modulation = %v, want usb", tuning["modulation"])
	}
	if tuning["bandwidth"] != float64(bandwidth) {
		t.Errorf("bandwidth = %v, want %d", tuning["bandwidth"], bandwidth)
	}
	restart := next(t, received)
	if restart["type"] != "dspcontrol" || restart["action"] != "start" {
		t.Fatalf("message after tuning = %v, want dspcontrol action=start", restart)
	}
}

// TestDecodeFloat32 checks that an uncompressed FFT frame (raw little-endian
// float32 dB values) is read as floats, not as int16. Reading it as int16 was
// the original bug: the high half of each float looked like a constant.
func TestDecodeFloat32(t *testing.T) {
	values := []float32{-48.5, -17.75, 3.25, 0}
	payload := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(payload[i*4:], math.Float32bits(v))
	}
	got := decodeFloat32(payload)
	if len(got) != len(values) {
		t.Fatalf("len = %d, want %d", len(got), len(values))
	}
	for i, v := range values {
		if got[i] != float64(v) {
			t.Errorf("bin %d = %v, want %v", i, got[i], v)
		}
	}
}

func TestAverageSpectra(t *testing.T) {
	avg := AverageSpectra([][]float64{{1, 2, 3}, {3, 4, 5}})
	want := []float64{2, 3, 4}
	for i := range want {
		if avg[i] != want[i] {
			t.Errorf("bin %d = %v, want %v", i, avg[i], want[i])
		}
	}
	// Frames longer than the shortest are truncated, not panicked on.
	avg = AverageSpectra([][]float64{{1, 2}, {3, 4, 5}})
	if len(avg) != 2 || avg[0] != 2 || avg[1] != 3 {
		t.Errorf("truncated average = %v, want [2 3]", avg)
	}
}

func TestFindSpectrumPeaks(t *testing.T) {
	// Noise floor at -60 dB with a single spike at bin 10.
	bins := make([]float64, 64)
	for i := range bins {
		bins[i] = -60
	}
	bins[10] = -20
	centre, span := 100e6, 2e6
	peaks := FindSpectrumPeaks(bins, centre, span, 8)
	if len(peaks) != 1 {
		t.Fatalf("got %d peaks, want 1: %+v", len(peaks), peaks)
	}
	binHz := span / float64(len(bins))
	wantHz := centre - span/2 + (10+0.5)*binHz
	if math.Abs(peaks[0].FreqHz-wantHz) > 1 {
		t.Errorf("peak at %v Hz, want %v Hz", peaks[0].FreqHz, wantHz)
	}
	if peaks[0].LevelDB != -20 {
		t.Errorf("peak level = %v, want -20", peaks[0].LevelDB)
	}
}

// TestSelectProfileSendsRawKeyAndRestartsDemod pins two OpenWebRX facts:
// selectprofile needs the raw "sdr_id|profile_id" key, and the demodulator
// must be started again afterwards or the server sends no audio.
func TestSelectProfileSendsRawKeyAndRestartsDemod(t *testing.T) {
	server, received := startMockServer(t)
	client, err := New(server.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	next(t, received)
	next(t, received)
	profiles := client.ProfileList()
	if len(profiles) != 1 || profiles[0].Key != "sdr|p" {
		t.Fatalf("profiles = %+v, want one with raw key sdr|p", profiles)
	}
	if err := client.SelectProfile(ctx, profiles[0].Key); err != nil {
		t.Fatal(err)
	}
	sel := next(t, received)
	params, _ := sel["params"].(map[string]interface{})
	if sel["type"] != "selectprofile" || params["profile"] != "sdr|p" {
		t.Fatalf("selectprofile message = %v", sel)
	}
	restart := next(t, received)
	if restart["type"] != "dspcontrol" || restart["action"] != "start" {
		t.Fatalf("message after selectprofile = %v, want dspcontrol action=start", restart)
	}
}

// A dead reader must reach the recorder as an error, not as a clean,
// silent recording.
func TestRecordReturnsReaderError(t *testing.T) {
	// A server that completes the handshake, then drops the connection
	// without sending any audio.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("CLIENT DE SERVER client=test type=receiver"))
		for {
			if _, data, err := conn.ReadMessage(); err != nil {
				return
			} else {
				var message map[string]interface{}
				if json.Unmarshal(data, &message) != nil {
					continue
				}
				if message["type"] == "connectionproperties" {
					conn.WriteMessage(websocket.TextMessage,
						[]byte(`{"type":"config","value":{"center_freq":145000000}}`))
					conn.WriteMessage(websocket.TextMessage,
						[]byte(`{"type":"profiles","value":[{"id":"sdr|p","name":"test"}]}`))
					// Handshake completes on the client's next message
					// (dspcontrol start); then the stream dies.
					if _, _, err := conn.ReadMessage(); err == nil {
						_ = conn.WriteMessage(websocket.CloseMessage,
							websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"))
					}
					return
				}
			}
		}
	}))
	defer server.Close()
	client, err := New(server.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	_, _, err = client.RecordSamples(context.Background(), 5*time.Second)
	if err == nil {
		t.Fatal("a dead stream must return the reader error, not clean silence")
	}
}

// After a retune, buffered audio from the old frequency must not leak into
// the next recording.
func TestDrainAudio(t *testing.T) {
	client, err := New("http://openwebrx.invalid/", testOptions())
	if err != nil {
		t.Fatal(err)
	}
	client.audioEvents <- Event{Type: "audio", Value: AudioChunk{Samples: []int16{1, 2}}}
	client.audioEvents <- Event{Type: "audio", Value: AudioChunk{Samples: []int16{3}}}
	client.DrainAudio()
	select {
	case leftover := <-client.audioEvents:
		t.Fatalf("audio survived the drain: %v", leftover.Type)
	default:
	}
}

// The guarded default refuses loopback before any network traffic.
func TestDialRefusesPrivateHost(t *testing.T) {
	client, err := New("http://127.0.0.1:8073/", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Connect(context.Background()); err == nil {
		t.Fatal("loopback target must be refused")
	}
}
