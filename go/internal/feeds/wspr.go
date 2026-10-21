package feeds

// WSPRnet spot database as an on-demand cross-check: given a callsign our
// digimode decoder heard, ask whether WSPRnet also spotted it recently. One
// polite HTTP query per check, no streaming, no key.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type WSPR struct {
	Base   string // default https://www.wsprnet.org
	Client *http.Client
}

func (w *WSPR) Name() string { return "wsprnet" }
func (w *WSPR) Kind() string { return "digimode" }

func (w *WSPR) base() string {
	if w.Base != "" {
		return strings.TrimRight(w.Base, "/")
	}
	return "https://www.wsprnet.org"
}

var wsprCell = regexp.MustCompile(`<td[^>]*>&nbsp;(.*?)&nbsp;</td>`)

// Recent returns up to limit recent WSPRnet spots for callsign.
func (w *WSPR) Recent(ctx context.Context, callsign string, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 20
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	query := url.Values{
		"mode":     {"html"},
		"band":     {"all"},
		"limit":    {fmt.Sprint(limit)},
		"findcall": {callsign},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.base()+"/olddb?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream cross-check (personal monitor)")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("wsprnet: %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	return ParseWSPRTable(string(body), callsign), nil
}

// ParseWSPRTable extracts rows from the WSPRnet olddb HTML table. Columns:
// timestamp, call, MHz, SNR, drift, grid, power, ?, reporter, reporter
// grid, km, ...
func ParseWSPRTable(html, want string) []Item {
	var items []Item
	want = strings.ToUpper(want)
	for _, row := range strings.Split(html, "</tr>") {
		cells := wsprCell.FindAllStringSubmatch(row, -1)
		if len(cells) < 10 {
			continue
		}
		get := func(i int) string { return strings.TrimSpace(cells[i][1]) }
		call := strings.ToUpper(get(1))
		if want != "" && call != want {
			continue
		}
		when, err := time.Parse("2006-01-02 15:04", get(0))
		if err != nil {
			continue
		}
		it := Item{
			ID:     fmt.Sprintf("wspr|%s|%s|%s", call, get(8), get(0)),
			Source: "wsprnet",
			Kind:   "digimode",
			Label:  call,
			Detail: fmt.Sprintf("WSPR %s MHz · %s dB · heard by %s (%s)", get(2), get(3), get(8), get(9)),
			Time:   when.UTC(),
		}
		if lat, lon, ok := maidenheadToLatLon(get(5)); ok {
			it.Lat, it.Lon, it.HasPos = lat, lon, true
		}
		items = append(items, it)
	}
	return items
}
