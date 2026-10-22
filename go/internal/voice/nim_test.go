package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/chaim12345/sdrstream/go/internal/rivaspeech"
)

// fakeRiva is an in-process stand-in for the hosted NVIDIA endpoint: it
// records what the client sent and answers with a canned transcript.
type fakeRiva struct {
	rivaspeech.UnimplementedRivaSpeechRecognitionServer
	auth       string
	functionID string
	request    *rivaspeech.RecognizeRequest
	err        error
	text       string
	calls      int
}

func (f *fakeRiva) Recognize(ctx context.Context, request *rivaspeech.RecognizeRequest) (*rivaspeech.RecognizeResponse, error) {
	f.calls++
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		f.auth = strings.Join(md.Get("authorization"), ",")
		f.functionID = strings.Join(md.Get("function-id"), ",")
	}
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	return &rivaspeech.RecognizeResponse{
		Results: []*rivaspeech.SpeechRecognitionResult{{
			Alternatives: []*rivaspeech.SpeechRecognitionAlternative{{
				Transcript: f.text,
				Confidence: 0.9,
				Words: []*rivaspeech.WordInfo{
					{StartTime: 0, EndTime: 500, Word: "hola"},
					{StartTime: 600, EndTime: 1200, Word: "mundo"},
				},
			}},
		}},
	}, nil
}

// dialFake swaps the network dialer for a bufconn connection to the fake
// server and returns a restore function.
func dialFake(t *testing.T, server *fakeRiva) func() {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	rivaspeech.RegisterRivaSpeechRecognitionServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		listener.Close()
	})
	previous := nimDial
	nimDial = func(ctx context.Context, options NIMOptions) (*grpc.ClientConn, error) {
		return grpc.DialContext(ctx, "bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	return func() { nimDial = previous }
}

// toneSamples builds two seconds of speech-like modulated audio.
func toneSamples(rate int) []float64 {
	samples := make([]float64, 2*rate)
	for i := range samples {
		t := float64(i) / float64(rate)
		envelope := 0.5 + 0.5*math.Sin(2*math.Pi*3*t)
		samples[i] = envelope * math.Sin(2*math.Pi*220*t) * 0.5
	}
	return samples
}

func testNIMOptions() NIMOptions {
	return NIMOptions{
		APIKey:       "test-key",
		Endpoint:     "bufnet",
		FunctionID:   "test-function",
		LanguageCode: "es-ES",
		Insecure:     true,
	}
}

func TestNIMRecognizeFakeServer(t *testing.T) {
	server := &fakeRiva{text: "hola mundo"}
	restore := dialFake(t, server)
	defer restore()

	transcript, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, testNIMOptions())
	if err != nil {
		t.Fatalf("TranscribeNIM: %v", err)
	}
	if transcript.Text != "hola mundo" {
		t.Errorf("text = %q, want %q", transcript.Text, "hola mundo")
	}
	if !strings.HasPrefix(transcript.Backend, "nvidia-nim/") {
		t.Errorf("backend = %q, want nvidia-nim", transcript.Backend)
	}
	if !transcript.Spoken {
		t.Error("spoken should be true")
	}
	if len(transcript.Words) != 2 || transcript.Words[1].Text != "mundo" || transcript.Words[1].End != 1.2 {
		t.Errorf("words = %+v, want hola/mundo with millisecond offsets in seconds", transcript.Words)
	}
	// The catalog call must carry the key and the function id.
	if server.auth != "Bearer test-key" {
		t.Errorf("authorization metadata = %q", server.auth)
	}
	if server.functionID != "test-function" {
		t.Errorf("function-id metadata = %q", server.functionID)
	}
	// The audio must be declared as 16 kHz mono LINEAR_PCM, resampled
	// from the 8 kHz input, and the hint must pass through.
	config := server.request.GetConfig()
	if config.GetEncoding() != rivaspeech.AudioEncoding_LINEAR_PCM {
		t.Errorf("encoding = %v", config.GetEncoding())
	}
	if config.GetSampleRateHertz() != 16000 {
		t.Errorf("sample rate = %d, want 16000", config.GetSampleRateHertz())
	}
	if config.GetLanguageCode() != "es-ES" {
		t.Errorf("language = %q", config.GetLanguageCode())
	}
	if got, want := len(server.request.GetAudio()), 2*16000*2; got != want {
		t.Errorf("audio bytes = %d, want %d (2 s of 16-bit 16 kHz PCM)", got, want)
	}
}

func TestNIMNotConfigured(t *testing.T) {
	if _, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, NIMOptions{}); err == nil {
		t.Fatal("expected an error with no API key")
	}
	if NIMAvailable(NIMOptions{}) {
		t.Error("NIMAvailable should be false without a key")
	}
}

func TestTranscribeBestFallbackError(t *testing.T) {
	server := &fakeRiva{err: errors.New("quota exceeded")}
	restore := dialFake(t, server)
	defer restore()

	// No whisper binary or model in the test environment: both backends
	// fail and the error must name both, so a silent fallback can never
	// hide a NIM outage.
	_, err := TranscribeBest(context.Background(), toneSamples(8000), 8000,
		Options{Binary: "/nonexistent/whisper", Model: "/nonexistent/model.bin"},
		func() NIMOptions { o := testNIMOptions(); o.FunctionID = WhisperLargeV3FunctionID; return o }())
	if err == nil {
		t.Fatal("expected an error when both backends fail")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("error %q should mention the NIM failure", err)
	}
	// Default order is whisper-large-v3 then parakeet, both reported.
	for _, want := range []string{"whisper-large-v3", "parakeet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if server.calls < 2 {
		t.Errorf("expected whisper then parakeet attempts, got %d calls", server.calls)
	}
}

func TestTranscribeBestForcedBackend(t *testing.T) {
	server := &fakeRiva{text: "forzado"}
	restore := dialFake(t, server)
	defer restore()
	t.Setenv("SDRSTREAM_VOICE_BACKEND", "nim")

	transcript, err := TranscribeBest(context.Background(), toneSamples(8000), 8000, Options{}, testNIMOptions())
	if err != nil {
		t.Fatalf("TranscribeBest: %v", err)
	}
	if transcript.Text != "forzado" {
		t.Errorf("text = %q", transcript.Text)
	}
}

func TestDefaultNIMOptionsFallsBackToNVIDIAKey(t *testing.T) {
	t.Setenv("SDRSTREAM_NIM_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY", "nvidia-key")
	if got := DefaultNIMOptions().APIKey; got != "nvidia-key" {
		t.Errorf("APIKey = %q, want NVIDIA_API_KEY fallback", got)
	}
	t.Setenv("SDRSTREAM_NIM_API_KEY", "project-key")
	if got := DefaultNIMOptions().APIKey; got != "project-key" {
		t.Errorf("APIKey = %q, want SDRSTREAM_NIM_API_KEY to win", got)
	}
}

func TestDefaultNIMOptions(t *testing.T) {
	t.Setenv("SDRSTREAM_NIM_API_KEY", "k")
	t.Setenv("SDRSTREAM_NIM_ENDPOINT", "")
	t.Setenv("SDRSTREAM_NIM_FUNCTION_ID", "")
	t.Setenv("SDRSTREAM_NIM_LANG", "")
	options := DefaultNIMOptions()
	if options.Endpoint != DefaultNIMEndpoint {
		t.Errorf("endpoint = %q, want catalog default", options.Endpoint)
	}
	if options.FunctionID != WhisperLargeV3FunctionID {
		t.Errorf("function id = %q, want whisper-large-v3 default", options.FunctionID)
	}
	if !NIMAvailable(options) {
		t.Error("NIMAvailable should be true with key and defaults")
	}
	if options.LanguageCode != DefaultNIMLanguage {
		t.Errorf("language = %q, want %q (auto-detect)", options.LanguageCode, DefaultNIMLanguage)
	}
}

func ExampleTranscribeBest() {
	fmt.Println("the hosted backend is tried first, whisper.cpp is the fallback")
	// Output: the hosted backend is tried first, whisper.cpp is the fallback
}

func TestNIMModelPresets(t *testing.T) {
	for name, want := range map[string]string{
		"":                 WhisperLargeV3FunctionID,
		"parakeet":         DefaultNIMFunctionID,
		"Whisper-Large-V3": WhisperLargeV3FunctionID,
		"whisper":          WhisperLargeV3FunctionID,
		"canary":           CanaryFunctionID,
	} {
		if got, ok := NIMModelFunctionID(name); !ok || got != want {
			t.Errorf("%q: got %q ok=%v", name, got, ok)
		}
	}
	if _, ok := NIMModelFunctionID("nope"); ok {
		t.Error("unknown preset accepted")
	}
	t.Setenv("SDRSTREAM_NIM_MODEL", "whisper-large-v3")
	t.Setenv("SDRSTREAM_NIM_FUNCTION_ID", "")
	if got := DefaultNIMOptions().FunctionID; got != WhisperLargeV3FunctionID {
		t.Errorf("env preset: %q", got)
	}
}

// dialPooledTLS points the pool path (Insecure=false) at a bufconn fake
// while counting dial attempts; the client credentials do not matter to
// the pooling logic, only the branch switch does.
func dialPooledTLS(t *testing.T, server *fakeRiva) (dialed *int, restore func()) {
	t.Helper()
	nimResetForTests()
	t.Cleanup(nimResetForTests)
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	rivaspeech.RegisterRivaSpeechRecognitionServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		listener.Close()
	})
	previous := nimDial
	count := 0
	nimDial = func(ctx context.Context, options NIMOptions) (*grpc.ClientConn, error) {
		count++
		return grpc.DialContext(ctx, "bufnet",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}
	return &count, func() { nimDial = previous }
}

// The TLS path must reuse one connection per endpoint: a fleet scan
// transcribing hundreds of clips through a fresh TLS handshake per clip
// multiplied latency and handshake failures.
func TestNIMPoolsTLSConnection(t *testing.T) {
	server := &fakeRiva{text: "hola mundo"}
	dials, restore := dialPooledTLS(t, server)
	defer restore()
	options := NIMOptions{APIKey: "k", Endpoint: "bufnet", FunctionID: "f", LanguageCode: "multi"}
	for range 3 {
		if _, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, options); err != nil {
			t.Fatalf("TranscribeNIM: %v", err)
		}
	}
	if *dials != 1 {
		t.Errorf("dialed %d times for 3 calls, want 1 pooled connection", *dials)
	}
}

// A quota or auth rejection must trip the cooldown: subsequent clips fail
// fast without dialing, uploading audio or burning further quota, until
// the window expires.
func TestNIMCooldownAfterQuotaFailure(t *testing.T) {
	server := &fakeRiva{err: status.Error(codes.ResourceExhausted, "rate limited")}
	_, restore := dialPooledTLS(t, server)
	defer restore()
	options := NIMOptions{APIKey: "k", Endpoint: "bufnet", FunctionID: "f", LanguageCode: "multi"}
	if _, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, options); err == nil {
		t.Fatal("expected quota failure")
	}
	if wait := nimCooldownRemaining(); wait <= 0 {
		t.Fatalf("cooldown not armed after quota failure (wait %v)", wait)
	}
	calls := server.calls
	_, err := TranscribeNIM(context.Background(), toneSamples(8000), 8000, options)
	if err == nil || !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("second call = %v, want a fast cooldown rejection", err)
	}
	if server.calls != calls {
		t.Errorf("backend hit %d more times during cooldown", server.calls-calls)
	}
}
