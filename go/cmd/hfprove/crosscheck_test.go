package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFT8Senders(t *testing.T) {
	cases := map[string][]string{
		"CQ DX R6WA LN32":    {"R6WA"},
		"OH3NIV ZS6S -03":    {"OH3NIV", "ZS6S"},
		"GJ0KYZ RK9AX MO05":  {"GJ0KYZ", "RK9AX"},
		"CQ K1JT FN20":       {"K1JT"},
		"<...> 4Z5KO R RR73": {"4Z5KO"},
		"73":                 nil,
	}
	for in, want := range cases {
		got := ft8Senders(in)
		if len(got) != len(want) {
			t.Errorf("ft8Senders(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("ft8Senders(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

func TestWSPRSender(t *testing.T) {
	if got := wsprSender("F4BAD JN25 20"); got != "F4BAD" {
		t.Errorf("got %q", got)
	}
	if got := wsprSender(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestAppendLedger(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ledger.jsonl")
	items := []ledgerItem{
		{Source: "sdrstream-hfprove", Kind: "digimode", ID: "a", Label: "CQ K1JT FN20", Verified: "unverified", Audio: "/x.wav"},
		{Source: "sdrstream-hfprove", Kind: "digimode", ID: "b", Label: "F4BAD JN25 20", Verified: "verified live", Ref: "wspr.live: 3 spot(s)"},
	}
	if err := appendLedger(p, items); err != nil {
		t.Fatal(err)
	}
	if err := appendLedger(p, items[:1]); err != nil { // appends, not truncates
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Fatalf("want 3 lines, got %d", lines)
	}
	var first ledgerItem
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Verified != "unverified" || first.Audio != "/x.wav" {
		t.Errorf("roundtrip: %+v", first)
	}
}
