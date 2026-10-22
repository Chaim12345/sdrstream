package voice

// nim.go is the NVIDIA NIM / Riva hosted speech backend: the same Riva
// gRPC API the self-hosted server speaks, exposed by the NVIDIA API
// catalog at grpc.nvcf.nvidia.com:443 with a Bearer API key and a
// per-model function id in the request metadata. It exists because the
// local whisper.cpp models are English-first: the multilingual catalog
// models (canary-1b, parakeet multilingual) transcribe the Spanish and
// other non-English traffic the receivers actually hear.
//
// The backend is strictly optional: with no API key configured the
// package behaves exactly as before, and a failed NIM call falls back to
// the local whisper.cpp so a network or quota problem never silences
// transcription. Every transcript still goes through the guard in
// guard.go before it reaches a user-visible feed: a hosted model can
// hallucinate on noise just like a local one.

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/chaim12345/sdrstream/go/internal/rivaspeech"
)

const (
	// DefaultNIMEndpoint is the hosted NVIDIA API catalog gRPC endpoint.
	DefaultNIMEndpoint = "grpc.nvcf.nvidia.com:443"
	// DefaultNIMFunctionID is the Parakeet (second choice) catalog function id of
	// parakeet-1.1b-rnnt-multilingual-asr (25 languages, automatic
	// language detection with the "multi" language code), documented at
	// build.nvidia.com/nvidia/parakeet-1_1b-rnnt-multilingual-asr.
	// Verified live 2026-10-22 on clean and noisy narrowband Spanish and
	// English clips. canary-1b-asr (b0e8b4a5-217c-40b7-9b96-17d84e666317)
	// is the translation-capable alternative; it has no auto-detect code
	// and a wrong language hint degrades it, so it is opt-in via
	// SDRSTREAM_NIM_FUNCTION_ID with SDRSTREAM_NIM_LANG set.
	DefaultNIMFunctionID = "71203149-d3b7-4460-8231-1be2543a1fca"
	// DefaultNIMLanguage is parakeet-multilingual's automatic language
	// detection code. The hosted models reject an empty language code.
	DefaultNIMLanguage = "multi"
	// WhisperLargeV3FunctionID is the catalog function id of
	// openai/whisper-large-v3 (build.nvidia.com/openai/whisper-large-v3/api).
	// "multi" enables automatic language detection; a known language code
	// is recommended for accuracy.
	WhisperLargeV3FunctionID = "b702f636-f60c-4a3d-a6f4-f3568c13bd7d"
	// CanaryFunctionID is canary-1b-asr.
	CanaryFunctionID = "b0e8b4a5-217c-40b7-9b96-17d84e666317"
	// nimSampleRate is the rate the audio is resampled to before upload.
	nimSampleRate = 16000
)

// NIMOptions configures the hosted NVIDIA speech backend.
type NIMOptions struct {
	// APIKey is the NVIDIA API catalog key; empty disables the backend.
	APIKey string
	// Endpoint is host:port of the Riva gRPC service.
	Endpoint string
	// FunctionID is the catalog function id of the model to call.
	FunctionID string
	// LanguageCode is the BCP-47 language hint ("es-ES"), or "multi" for
	// automatic detection on the multilingual models.
	LanguageCode string
	// Model optionally names a Riva model; empty lets the service pick.
	Model string
	// Timeout bounds one recognition call; 0 means 2 minutes.
	Timeout time.Duration
	// Insecure dials plaintext instead of TLS; for tests only.
	Insecure bool
}

// DefaultNIMOptions reads the backend configuration from the environment:
//
//	SDRSTREAM_NIM_API_KEY      NVIDIA API catalog key (preferred name)
//	NVIDIA_API_KEY             the key name the NVIDIA CLI/docs use; used
//	                           when SDRSTREAM_NIM_API_KEY is not set
//	SDRSTREAM_NIM_ENDPOINT     default grpc.nvcf.nvidia.com:443
//	SDRSTREAM_NIM_MODEL        whisper-large-v3 (default), parakeet or canary
//	SDRSTREAM_NIM_FUNCTION_ID  explicit function id, overrides the model name
//	SDRSTREAM_NIM_LANG         language hint, default "multi" (auto-detect)
func DefaultNIMOptions() NIMOptions {
	fn := WhisperLargeV3FunctionID
	if id, ok := NIMModelFunctionID(os.Getenv("SDRSTREAM_NIM_MODEL")); ok {
		fn = id
	}
	return NIMOptions{
		APIKey:       nimAPIKey(),
		Endpoint:     envOr("SDRSTREAM_NIM_ENDPOINT", DefaultNIMEndpoint),
		FunctionID:   envOr("SDRSTREAM_NIM_FUNCTION_ID", fn),
		LanguageCode: envOr("SDRSTREAM_NIM_LANG", DefaultNIMLanguage),
	}
}

// nimAPIKey accepts both the project's variable and NVIDIA's own
// NVIDIA_API_KEY, so a key exported for the NVIDIA CLI enables the hosted
// backend without a second export.
func nimAPIKey() string {
	for _, name := range []string{"SDRSTREAM_NIM_API_KEY", "NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// NIMModelFunctionID maps a model preset name to its catalog function id.
// Names: "whisper-large-v3" (default, also "whisper" or empty), "parakeet"
// (multilingual), "canary". An unknown name is returned as ok=false.
func NIMModelFunctionID(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "parakeet":
		return DefaultNIMFunctionID, true
	case "", "whisper", "whisper-large", "whisper-large-v3":
		return WhisperLargeV3FunctionID, true
	case "canary":
		return CanaryFunctionID, true
	}
	return "", false
}

func nimModelName(id string) string {
	switch id {
	case WhisperLargeV3FunctionID:
		return "whisper-large-v3"
	case DefaultNIMFunctionID:
		return "parakeet-1.1b-multilingual"
	case CanaryFunctionID:
		return "canary-1b"
	}
	return id
}

// NIMAvailable reports whether the hosted backend is configured.
func NIMAvailable(options NIMOptions) bool {
	return options.APIKey != "" && options.Endpoint != "" && options.FunctionID != ""
}

// nimDial is swappable so tests can point the client at a fake server.
var nimDial = func(ctx context.Context, options NIMOptions) (*grpc.ClientConn, error) {
	creds := credentials.NewTLS(&tls.Config{})
	if options.Insecure {
		creds = insecure.NewCredentials()
	}
	return grpc.DialContext(ctx, options.Endpoint,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64<<20)),
	)
}

// TranscribeNIM recognises speech with the hosted NVIDIA model.
func TranscribeNIM(ctx context.Context, samples []float64, rate int, options NIMOptions) (Transcript, error) {
	transcript := Transcript{Seconds: float64(len(samples)) / float64(rate)}
	if len(samples) < rate/10 {
		return transcript, fmt.Errorf("voice: too short to transcribe")
	}
	if !NIMAvailable(options) {
		return transcript, fmt.Errorf("voice: NVIDIA NIM backend not configured (set SDRSTREAM_NIM_API_KEY)")
	}
	// Quota/auth cooldown: fail fast during the window instead of paying
	// a dial + minute-long upload per clip for the same rejection.
	if wait := nimCooldownRemaining(); wait > 0 {
		return transcript, fmt.Errorf("voice: NIM cooling down after quota/auth failure, retry in %s", wait.Round(time.Second))
	}
	// Bounded concurrency against the catalog tier; a wide scan must not
	// empty the request quota in a single wave.
	select {
	case nimSlots <- struct{}{}:
		defer func() { <-nimSlots }()
	case <-ctx.Done():
		return transcript, fmt.Errorf("voice: NIM call cancelled waiting for a free slot: %w", ctx.Err())
	}
	if options.Insecure {
		// Insecure dialing is the test path: each call gets its own
		// connection so every test owns its server lifecycle.
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, err := nimDial(dialCtx, options)
		cancel()
		if err != nil {
			return transcript, fmt.Errorf("voice: NIM dial %s: %w", options.Endpoint, err)
		}
		defer conn.Close()
		return nimRecognize(ctx, rivaspeech.NewRivaSpeechRecognitionClient(conn), samples, rate, options)
	}
	conn, err := nimConnFor(ctx, options)
	if err != nil {
		return transcript, fmt.Errorf("voice: NIM dial %s: %w", options.Endpoint, err)
	}
	out, err := nimRecognize(ctx, rivaspeech.NewRivaSpeechRecognitionClient(conn), samples, rate, options)
	if err != nil {
		if cooldown := nimFailureCooldown(err); cooldown > 0 {
			nimCooldownUntil.Store(time.Now().Add(cooldown).UnixNano())
		}
		return out, err
	}
	return out, nil
}

// nimRecognize runs one offline Recognize call through an existing client,
// split out so tests can drive it with an in-process fake server.
func nimRecognize(ctx context.Context, client rivaspeech.RivaSpeechRecognitionClient, samples []float64, rate int, options NIMOptions) (Transcript, error) {
	transcript := Transcript{Seconds: float64(len(samples)) / float64(rate)}
	pcm := Resample(samples, rate, nimSampleRate)
	audio := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		value := int16(s * 32767)
		audio[2*i] = byte(value)
		audio[2*i+1] = byte(value >> 8)
	}
	config := &rivaspeech.RecognitionConfig{
		Encoding:                   rivaspeech.AudioEncoding_LINEAR_PCM,
		SampleRateHertz:            nimSampleRate,
		LanguageCode:               options.LanguageCode,
		MaxAlternatives:            1,
		AudioChannelCount:          1,
		EnableWordTimeOffsets:      true,
		EnableAutomaticPunctuation: true,
		Model:                      options.Model,
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if options.APIKey != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx,
			"authorization", "Bearer "+options.APIKey,
			"function-id", options.FunctionID,
		)
	}
	response, err := client.Recognize(callCtx, &rivaspeech.RecognizeRequest{Config: config, Audio: audio})
	if err != nil {
		return transcript, fmt.Errorf("voice: NIM recognize: %w", err)
	}
	var texts []string
	for _, result := range response.GetResults() {
		for _, alternative := range result.GetAlternatives() {
			text := strings.TrimSpace(alternative.GetTranscript())
			if text == "" {
				continue
			}
			texts = append(texts, text)
			for _, word := range alternative.GetWords() {
				transcript.Words = append(transcript.Words, Word{
					Start: float64(word.GetStartTime()) / 1000, // milliseconds
					End:   float64(word.GetEndTime()) / 1000,
					Text:  word.GetWord(),
				})
			}
		}
	}
	transcript.Text = strings.Join(texts, " ")
	transcript.Backend = "nvidia-nim/" + nimModelName(options.FunctionID)
	transcript.Spoken = transcript.Text != ""
	return transcript, nil
}

// TranscribeBest picks a speech backend and transcribes. Order when a NIM
// key exists: NIM Whisper large-v3, then NIM Parakeet, then local
// whisper.cpp (the offline fallback). On 28 LibriSpeech clips through a
// simulated 8 dB radio channel these scored 7.0%, 13.3% and 24.2% WER.
// Setting SDRSTREAM_NIM_MODEL or SDRSTREAM_NIM_FUNCTION_ID pins the NIM
// model and drops the Parakeet step. SDRSTREAM_VOICE_BACKEND forces a
// choice: "nim" or "whisper" (local). A failed call falls through to the
// next backend so an outage never costs a transcript, and when every
// backend fails the error names each one.
func TranscribeBest(ctx context.Context, samples []float64, rate int, options Options, nim NIMOptions) (Transcript, error) {
	switch strings.ToLower(os.Getenv("SDRSTREAM_VOICE_BACKEND")) {
	case "whisper":
		return Transcribe(ctx, samples, rate, options)
	case "nim":
		return TranscribeNIM(ctx, samples, rate, nim)
	}
	if !NIMAvailable(nim) {
		return Transcribe(ctx, samples, rate, options)
	}
	chain := []NIMOptions{nim}
	pinned := os.Getenv("SDRSTREAM_NIM_MODEL") != "" || os.Getenv("SDRSTREAM_NIM_FUNCTION_ID") != ""
	if !pinned && nim.FunctionID == WhisperLargeV3FunctionID {
		second := nim
		second.FunctionID = DefaultNIMFunctionID
		second.LanguageCode = DefaultNIMLanguage // parakeet rejects specific codes it lacks
		chain = append(chain, second)
	}
	var failures []string
	for _, o := range chain {
		transcript, err := TranscribeNIM(ctx, samples, rate, o)
		if err == nil {
			return transcript, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", nimModelName(o.FunctionID), err))
	}
	if !Available(options) {
		return Transcript{}, fmt.Errorf("voice: NIM failed (%s); no local whisper available", strings.Join(failures, "; "))
	}
	fallback, ferr := Transcribe(ctx, samples, rate, options)
	if ferr == nil {
		return fallback, nil
	}
	return fallback, fmt.Errorf("voice: NIM failed (%s); whisper fallback failed (%v)", strings.Join(failures, "; "), ferr)
}

var (
	// nimConns pools one client connection per TLS endpoint. Dialing
	// cost a full TLS + HTTP2 handshake per clip before; a fleet scan
	// transcribing hundreds of clips paid it repeatedly against the
	// same grpc.nvcf.nvidia.com endpoint. gRPC reconnects internally,
	// so the pooled conn survives server-side blips.
	nimConnsMu sync.Mutex
	nimConns   = map[string]*grpc.ClientConn{}
	// nimSlots caps concurrent recognize calls. The free catalog tier
	// throttles by request rate; a wide parallel scan that jams every
	// clip through at once burns the whole quota in one wave and then
	// poisons every recorder with quota errors.
	nimSlots = make(chan struct{}, nimMaxConcurrent)
)

const nimMaxConcurrent = 2

// Cooldowns after a systemic failure: during the window TranscribeNIM
// fails fast instead of paying a dial + upload per clip against a backend
// that will return the same error.
const (
	// Quota and auth failures will not recover next call; give the
	// window real room.
	nimQuotaCooldown = 10 * time.Minute
	// Unavailable is usually a brief flap; a full minute is enough.
	nimOutageCooldown = time.Minute
)

// nimCooldownUntil is unix-nanoseconds of the cooldown expiry; 0 = none.
var nimCooldownUntil atomic.Int64

func nimFailureCooldown(err error) time.Duration {
	switch status.Code(err) {
	case codes.ResourceExhausted, codes.Unauthenticated, codes.PermissionDenied:
		return nimQuotaCooldown
	case codes.Unavailable:
		return nimOutageCooldown
	}
	return 0
}

func nimCooldownRemaining() time.Duration {
	return time.Until(time.Unix(0, nimCooldownUntil.Load()))
}

// nimConnFor returns the pooled connection for an endpoint, dialing on
// first use. Concurrent first users queue on the mutex and reuse the
// winner's connection.
func nimConnFor(ctx context.Context, options NIMOptions) (*grpc.ClientConn, error) {
	nimConnsMu.Lock()
	defer nimConnsMu.Unlock()
	if conn, ok := nimConns[options.Endpoint]; ok {
		return conn, nil
	}
	conn, err := nimDial(ctx, options)
	if err != nil {
		return nil, err
	}
	nimConns[options.Endpoint] = conn
	return conn, nil
}

// nimResetForTests drops pooled connections and any cooldown; tests that
// swap nimDial also need a clean pool, since a cached bufconn would
// outlive its fake server.
func nimResetForTests() {
	nimConnsMu.Lock()
	for _, conn := range nimConns {
		_ = conn.Close()
	}
	nimConns = map[string]*grpc.ClientConn{}
	nimConnsMu.Unlock()
	nimCooldownUntil.Store(0)
}
