// Command nimasr transcribes one WAV file with a hosted NVIDIA NIM speech
// model and prints the text. The API key comes from SDRSTREAM_NIM_API_KEY,
// never from a flag.
//
//	nimasr -model whisper-large-v3 -lang en file.wav
//	nimasr -model parakeet file.wav
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

func main() {
	model := flag.String("model", "parakeet", "parakeet | whisper-large-v3 | canary")
	lang := flag.String("lang", "", "language code (en, es-ES, multi); default multi")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: nimasr [-model m] [-lang l] file.wav")
		os.Exit(2)
	}
	id, ok := voice.NIMModelFunctionID(*model)
	if !ok {
		fmt.Fprintf(os.Stderr, "nimasr: unknown model %q\n", *model)
		os.Exit(2)
	}
	opts := voice.DefaultNIMOptions()
	opts.FunctionID = id
	if *lang != "" {
		opts.LanguageCode = *lang
	}
	samples, rate, err := decode.ReadWAV(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "nimasr:", err)
		os.Exit(1)
	}
	t, err := voice.TranscribeNIM(context.Background(), samples, rate, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nimasr:", err)
		os.Exit(1)
	}
	fmt.Println(t.Text)
}
