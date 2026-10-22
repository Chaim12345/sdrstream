# Third-party notices

This repository vendors or adapts third-party code. Copyright stays with
the original authors; each entry names the source and its license.

## go/internal/decode/ft8.go and ftx_tables.go

Go port of the decode path of kgoba/ft8_lib (MIT License, Copyright (c)
2018 Karlis Goba); the LDPC tables come from WSJT-X via ft8_lib. License
text: go/internal/decode/testdata/ft8_lib.LICENSE. Test recordings under
go/internal/decode/testdata/ft8 come from the ft8_lib test suite.

## go/internal/rivaspeech

Go bindings generated from NVIDIA's Riva speech proto files
(`riva_asr.proto`, `riva_audio.proto`, `riva_common.proto`), fetched from
github.com/nvidia-riva/common (riva/proto, main branch, 2026-10-22) and
compiled with protoc 29.3, protoc-gen-go and protoc-gen-go-grpc.

Copyright NVIDIA CORPORATION and Google LLC. License: MIT / Apache-2.0,
as stated in the SPDX and license headers preserved at the top of each
generated file.
