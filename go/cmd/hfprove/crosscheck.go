package main

// Cross-checking of live decodes against independent spot networks
// (PSKReporter for FT8, wspr.live for WSPR) and proof-ledger output.
// A decode is "verified live" only when the same sender callsign was
// spotted by the network inside the capture window; otherwise it stays
// "unverified" no matter how good the SNR looks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
	"time"
)

const crossUA = "sdrstream (https://github.com/Chaim12345/sdrstream)"

// pskrCollector gathers PSK Reporter MQTT spots during a run.
type pskrCollector struct {
	mu    sync.Mutex
	spots map[string][]feeds.PSKSpot // sender callsign -> spots seen
	n     int
}

func newPSKRCollector() *pskrCollector {
	return &pskrCollector{spots: map[string][]feeds.PSKSpot{}}
}

func (c *pskrCollector) add(sp feeds.PSKSpot) {
	c.mu.Lock()
	c.spots[sp.Sender] = append(c.spots[sp.Sender], sp)
	c.n++
	c.mu.Unlock()
}

// heardBy returns the receivers that spotted sender in the given mode
// (empty mode matches any).
func (c *pskrCollector) heardBy(sender, mode string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, sp := range c.spots[sender] {
		if mode != "" && sp.Mode != "" && sp.Mode != mode {
			continue
		}
		if sp.Receiver != "" && !seen[sp.Receiver] {
			seen[sp.Receiver] = true
			out = append(out, sp.Receiver)
		}
	}
	return out
}

var callsignRE = regexp.MustCompile(`^[A-Z0-9]{1,3}[0-9][A-Z][A-Z0-9]*(/[A-Z0-9]+)?$`)

// ft8Senders extracts plausible sender callsigns from an FT8 message:
// the two tokens after an optional CQ/QRZ/DE marker. "K1JT W9XYZ -03"
// yields both; either one may be the sender.
func ft8Senders(text string) []string {
	f := strings.Fields(text)
	var out []string
	for _, tok := range f {
		up := strings.ToUpper(tok)
		switch up {
		case "CQ", "QRZ", "DE", "DX", "RR73", "RRR", "73", "TEST":
			continue
		}
		if len(tok) < 3 || len(tok) > 13 || !callsignRE.MatchString(up) {
			continue
		}
		out = append(out, up)
		if len(out) == 2 {
			return out
		}
	}
	return out
}

// wsprSender is the first token of "CALL GRID DBM".
func wsprSender(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	return strings.ToUpper(f[0])
}

// wsprLiveSpots queries wspr.live's ClickHouse JSON API for spots of the
// given senders since `since` (UTC). One request for all callsigns.
func wsprLiveSpots(ctx context.Context, client *http.Client, calls []string, since time.Time) (map[string]int, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	quoted := make([]string, len(calls))
	for i, c := range calls {
		quoted[i] = "'" + strings.ReplaceAll(c, "'", "") + "'"
	}
	q := fmt.Sprintf("SELECT tx_sign, count() AS n FROM wspr.rx WHERE tx_sign IN (%s) AND time >= '%s' GROUP BY tx_sign FORMAT JSON",
		strings.Join(quoted, ","), since.UTC().Format("2006-01-02 15:04:05"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://db1.wspr.live/?query="+urlQueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", crossUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// The retrieval API rate-limits aggressively (and some egress IPs
		// are throttled wholesale). One polite retry, then give up.
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		if resp, err = client.Do(req); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wspr.live: HTTP %d", resp.StatusCode)
	}
	var rows struct {
		Data []struct {
			TxSign string `json:"tx_sign"`
			N      int    `json:"n"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows.Data {
		out[strings.ToUpper(r.TxSign)] = r.N
	}
	return out, nil
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "'", "%27", ",", "%2C", "(", "%28", ")", "%29", ">", "%3E", "=", "%3D", ":", "%3A")
	return r.Replace(s)
}

// ledgerItem is one proof-ledger row: feeds.LedgerEntry shape plus the
// capture audio path and the cross-check verdict.
type ledgerItem struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Label     string `json:"label"`
	Detail    string `json:"detail,omitempty"`
	ProviderT string `json:"provider_time"`
	FetchedAt string `json:"fetched_at"`
	DecodedBy string `json:"decoded_by"`
	Ref       string `json:"ref,omitempty"`
	Verified  string `json:"verified"` // "verified live" | "unverified"
	Audio     string `json:"audio,omitempty"`
}

func appendLedger(path string, items []ledgerItem) error {
	if len(items) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}
