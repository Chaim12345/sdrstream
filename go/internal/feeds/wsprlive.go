package feeds

// wspr.live spot archive: the WSPR community mirrors the WSPRnet database
// into a public read-only ClickHouse at db1.wspr.live so tools can query
// spots without scraping wsprnet.org. We poll only the newest window with
// a small LIMIT, every couple of minutes.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WSPRLive struct {
	Base   string // default https://db1.wspr.live
	Limit  int    // rows per poll, default 60
	Client *http.Client
	Now    func() time.Time
}

func (w *WSPRLive) Name() string { return "wspr.live" }
func (w *WSPRLive) Kind() string { return "digimode" }

func (w *WSPRLive) base() string {
	if w.Base != "" {
		return strings.TrimRight(w.Base, "/")
	}
	return "https://db1.wspr.live"
}

func (w *WSPRLive) Interval() time.Duration { return 150 * time.Second }

type wsprLiveRow struct {
	Time      string `json:"time"` // ClickHouse "2006-01-02 15:04:05", UTC
	Band      int    `json:"band"`
	RxSign    string `json:"rx_sign"`
	RxLoc     string `json:"rx_loc"`
	TxSign    string `json:"tx_sign"`
	TxLoc     string `json:"tx_loc"`
	Distance  int    `json:"distance"`
	Frequency int64  `json:"frequency"` // Hz
	Power     int    `json:"power"`     // dBm
	SNR       int    `json:"snr"`
	Drift     int    `json:"drift"`
}

// wsprBand converts a wspr.live band code (the band in MHz, with -1/0 for
// the LF/MF bands) to the common metre label.
func wsprBand(code int) string {
	switch code {
	case -1:
		return "2200m"
	case 0:
		return "630m"
	case 1:
		return "160m"
	case 3:
		return "80m"
	case 5:
		return "60m"
	case 7:
		return "40m"
	case 10:
		return "30m"
	case 14:
		return "20m"
	case 18:
		return "17m"
	case 21:
		return "15m"
	case 24:
		return "12m"
	case 28:
		return "10m"
	case 50:
		return "6m"
	case 70:
		return "4m"
	case 144:
		return "2m"
	case 432:
		return "70cm"
	case 1296:
		return "23cm"
	}
	return ""
}

// Recent returns the newest WSPR spots visible in the archive window.
func (w *WSPRLive) Poll(ctx context.Context) ([]Item, error) {
	limit := w.Limit
	if limit <= 0 {
		limit = 60
	}
	q := fmt.Sprintf(`SELECT time, band, rx_sign, rx_loc, tx_sign, tx_loc, distance, frequency, power, snr, drift
FROM wspr.rx
WHERE time >= now() - INTERVAL 4 MINUTE
ORDER BY time DESC
LIMIT %d
FORMAT JSONEachRow`, limit)
	u := w.base() + "/?query=" + url.QueryEscape(q)
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream (https://github.com/Chaim12345/sdrstream)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wspr.live: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	items := make([]Item, 0, limit)
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r wsprLiveRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue // tolerate a truncated last line
		}
		if r.TxSign == "" {
			continue
		}
		when := now
		if t, err := time.Parse("2006-01-02 15:04:05", r.Time); err == nil {
			when = t.UTC()
		}
		band := wsprBand(r.Band)
		label := fmt.Sprintf("%s %.3f MHz", r.TxSign, float64(r.Frequency)/1e6)
		if band != "" {
			label += " (" + band + ")"
		}
		detail := fmt.Sprintf("WSPR heard by %s (%s), SNR %d dB, %d dBm, %d km",
			r.RxSign, r.RxLoc, r.SNR, r.Power, r.Distance)
		items = append(items, Item{
			ID:     fmt.Sprintf("wsprlive:%s:%s:%d:%s", r.TxSign, r.RxSign, r.Frequency, r.Time),
			Source: "wspr.live",
			Kind:   "digimode",
			Time:   when,
			Label:  label,
			Detail: detail,
			Ref:    "https://wspr.live/",
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("wspr.live: no rows in window")
	}
	return items, nil
}
