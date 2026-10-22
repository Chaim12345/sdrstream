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
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/chaim12345/sdrstream/go/internal/rivaspeech"
)

const (
	// DefaultNIMEndpoint is the hosted NVIDIA API catalog gRPC endpoint.
	DefaultNIMEndpoint = "grpc.nvcf.nvidia.com:443"
	// DefaultNIMFunctionID is the catalog function id of
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
//	SDRSTREAM_NIM_API_KEY      NVIDIA API catalog key (required to enable)
//	SDRSTREAM_NIM_ENDPOINT     default grpc.nvcf.nvidia.com:443
//	SDRSTREAM_NIM_FUNCTION_ID  default canary-1b-asr
//	SDRSTREAM_NIM_LANG         language hint, default "multi" (auto-detect)
func DefaultNIMOptions() NIMOptions {
	return NIMOptions{
		APIKey:       os.Getenv("SDRSTREAM_NIM_API_KEY"),
		Endpoint:     envOr("SDRSTREAM_NIM_ENDPOINT", DefaultNIMEndpoint),
		FunctionID:   envOr("SDRSTREAM_NIM_FUNCTION_ID", DefaultNIMFunctionID),
		LanguageCode: envOr("SDRSTREAM_NIM_LANG", DefaultNIMLanguage),
	}
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
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := nimDial(dialCtx, options)
	cancel()
	if err != nil {
		return transcript, fmt.Errorf("voice: NIM dial %s: %w", options.Endpoint, err)
	}
	defer conn.Close()
	return nimRecognize(ctx, rivaspeech.NewRivaSpeechRecognitionClient(conn), samples, rate, options)
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
	transcript.Backend = "nvidia-nim"
	transcript.Spoken = transcript.Text != ""
	return transcript, nil
}

// TranscribeBest picks a speech backend and transcribes. The local
// whisper.cpp stays the baseline; the hosted NIM model is tried first
// when it is configured, because it is multilingual where the local
// English models are not. SDRSTREAM_VOICE_BACKEND forces a choice:
// "nim" or "whisper"; anything else is the automatic behaviour. A failed
// NIM call falls back to whisper so an outage never costs a transcript.
func TranscribeBest(ctx context.Context, samples []float64, rate int, options Options, nim NIMOptions) (Transcript, error) {
	switch strings.ToLower(os.Getenv("SDRSTREAM_VOICE_BACKEND")) {
	case "whisper":
		return Transcribe(ctx, samples, rate, options)
	case "nim":
		return TranscribeNIM(ctx, samples, rate, nim)
	}
	if NIMAvailable(nim) {
		transcript, err := TranscribeNIM(ctx, samples, rate, nim)
		if err == nil {
			return transcript, nil
		}
		if !Available(options) {
			return transcript, err
		}
		fallback, ferr := Transcribe(ctx, samples, rate, options)
		if ferr == nil {
			return fallback, nil
		}
		return fallback, fmt.Errorf("voice: NIM failed (%v); whisper fallback failed (%v)", err, ferr)
	}
	return Transcribe(ctx, samples, rate, options)
}
