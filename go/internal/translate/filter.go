package translate

import (
	"regexp"
	"strings"
	"unicode"
)

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

var englishWords = map[string]bool{"the": true, "and": true, "of": true, "to": true, "is": true, "in": true, "for": true,
	"this": true, "that": true, "with": true, "are": true, "was": true, "you": true, "from": true, "wind": true,
	"over": true, "at": true, "on": true, "be": true, "it": true, "as": true, "have": true, "not": true, "by": true,
	"all": true, "will": true, "we": true, "or": true, "an": true, "my": true, "your": true, "ship": true}

// LooksEnglish is a cheap local check so English text is never sent. It is
// conservative about claiming English: text in a non-Latin script never is, and
// Latin text needs several common English words.
func LooksEnglish(s string) bool {
	words, hits := 0, 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if w == "" {
			continue
		}
		for _, r := range w {
			if r > unicode.MaxLatin1 {
				return false
			}
		}
		words++
		if englishWords[w] {
			hits++
		}
	}
	return words >= 3 && hits*100 >= words*25 && hits >= 2
}

var (
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	rePhone  = regexp.MustCompile(`(?:\+|00)?\d[\d \-().]{7,}\d`)
	reDigits = regexp.MustCompile(`\d{9,}`)
	reIBAN   = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{10,30}\b`)
	reURLCrd = regexp.MustCompile(`(?i)https?://[^\s/@]+:[^\s/@]+@`)
	reSecret = regexp.MustCompile(`(?i)\b(password|passcode|pin code|secret|token|api[_ -]?key|cvv|ssn|passport)\b`)
	reCoord  = regexp.MustCompile(`-?\d{1,3}\.\d{4,}\s*[,;]\s*-?\d{1,3}\.\d{4,}`)
)

// Eligible reports whether a text may be sent to a third-party service whose
// unpaid terms allow content to be used for product improvement and read by
// reviewers. It refuses text that looks like it carries personal identifiers
// or credentials. It is a heuristic filter, not a guarantee: it cannot find a
// name or an address in free text, which is why the operator also has to opt in
// per provider, and why it errs toward refusing.
func Eligible(s string) (bool, string) {
	switch {
	case reEmail.MatchString(s):
		return false, "an email address"
	case reURLCrd.MatchString(s):
		return false, "credentials in a URL"
	case reSecret.MatchString(s):
		return false, "a credential keyword"
	case reIBAN.MatchString(s):
		return false, "a bank account number"
	case reDigits.MatchString(s):
		return false, "a long number"
	case rePhone.MatchString(s):
		return false, "a phone-number-like sequence"
	case reCoord.MatchString(s):
		return false, "precise coordinates"
	}
	return true, ""
}
