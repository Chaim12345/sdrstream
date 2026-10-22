// Package rivaspeech contains the Go bindings generated from NVIDIA's Riva
// speech proto files (github.com/nvidia-riva/common, riva/proto, main branch,
// fetched 2026-10-22): riva_asr.proto, riva_audio.proto and riva_common.proto,
// compiled with protoc 29.3, protoc-gen-go and protoc-gen-go-grpc.
//
// The files are vendored so the build does not depend on an external proto
// toolchain; regenerate them by repeating the fetch and protoc invocation
// above. The RivaSpeechRecognition service is what the hosted NVIDIA API
// catalog endpoints (grpc.nvcf.nvidia.com:443) speak.
package rivaspeech
