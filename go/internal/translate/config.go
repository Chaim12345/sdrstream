package translate

import (
	"strings"
)

// FromEnv builds a Translator from environment-style settings. Nothing is sent
// unless ALL of these hold:
//
//	SDRSTREAM_TRANSLATE=on
//	SDRSTREAM_TRANSLATE_PROVIDER=gemini
//	SDRSTREAM_TRANSLATE_CONSENT=gemini     (must name the provider)
//	GEMINI_API_KEY (or SDRSTREAM_GEMINI_API_KEY) is set
//
// A key on its own does nothing. The returned note says why it is off, for a
// status line; it never contains the key.
func FromEnv(getenv func(string) string) (*Translator, string) {
	if !strings.EqualFold(strings.TrimSpace(getenv("SDRSTREAM_TRANSLATE")), "on") {
		return New(Config{}, nil), "translation off (SDRSTREAM_TRANSLATE is not \"on\")"
	}
	prov := strings.ToLower(strings.TrimSpace(getenv("SDRSTREAM_TRANSLATE_PROVIDER")))
	if prov != "gemini" {
		return New(Config{}, nil), "translation off (SDRSTREAM_TRANSLATE_PROVIDER must be \"gemini\")"
	}
	consent := strings.ToLower(strings.TrimSpace(getenv("SDRSTREAM_TRANSLATE_CONSENT")))
	if consent != prov {
		return New(Config{}, nil), "translation off (SDRSTREAM_TRANSLATE_CONSENT must name the provider)"
	}
	key := getenv("SDRSTREAM_GEMINI_API_KEY")
	if key == "" {
		key = getenv("GEMINI_API_KEY")
	}
	if key == "" {
		return New(Config{}, nil), "translation off (no Gemini API key)"
	}
	g := &Gemini{APIKey: key, ModelID: getenv("SDRSTREAM_GEMINI_MODEL")}
	return New(Config{Enabled: true, ConsentedProvider: consent}, g), "translation on: gemini " + g.Model()
}
