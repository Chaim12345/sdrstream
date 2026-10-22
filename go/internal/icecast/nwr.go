package icecast

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// NWRPageURL lists the community NOAA Weather Radio streams.
const NWRPageURL = "https://noaaweatherradio.org/"

// NWRStream is one NOAA Weather Radio relay from the page's playlist.
type NWRStream struct {
	URL       string `json:"url"`
	State     string `json:"state"`
	City      string `json:"city"`
	MHz       string `json:"mhz"`
	Callsign  string `json:"callsign,omitempty"`  // NWS transmitter call sign, e.g. WXK56
	Recording bool   `json:"recording,omitempty"` // a fixed mp3 file, not a live stream
}

var (
	nwrItem  = regexp.MustCompile(`(?s)<li\s+data-path="([^"]+)"(.*?)</li>`)
	nwrBody  = regexp.MustCompile(`(?s)<span class="pwsited">(.*?)</span>(.*?)\s+(\d{3}\.\d{3})`)
	nwrTags  = regexp.MustCompile(`<[^>]*>`)
	nwrSite  = regexp.MustCompile(`(?:^|[-/])([WK][A-Z]{1,2}\d{2,3})(?:[-_.]|$)`)
	callOnly = regexp.MustCompile(`\b([WK][A-Z]{2}\d{2,3}|[WK][A-Z]\d{3})\b`)
)

// ParseNWR extracts the playlist entries from the NWR page HTML. Inactive
// placeholders are dropped.
func ParseNWR(page string) []NWRStream {
	var out []NWRStream
	for _, m := range nwrItem.FindAllStringSubmatch(page, -1) {
		u := html.UnescapeString(m[1])
		if !strings.HasPrefix(u, "http") {
			continue // content/inactive.mp3 placeholders
		}
		s := NWRStream{URL: u}
		if b := nwrBody.FindStringSubmatch(m[2]); b != nil {
			s.State = strings.TrimSpace(html.UnescapeString(nwrTags.ReplaceAllString(b[1], "")))
			s.City = strings.TrimSpace(html.UnescapeString(nwrTags.ReplaceAllString(b[2], "")))
			s.MHz = b[3]
		}
		if c := nwrSite.FindStringSubmatch(u); c != nil {
			s.Callsign = c[1]
		}
		s.Recording = strings.HasSuffix(strings.ToLower(u), ".mp3")
		out = append(out, s)
	}
	return out
}

// Live returns only the streams that are live relays.
func Live(streams []NWRStream) []NWRStream {
	var out []NWRStream
	for _, s := range streams {
		if !s.Recording {
			out = append(out, s)
		}
	}
	return out
}

// FetchNWR downloads and parses the NWR page.
func (c *Client) FetchNWR(ctx context.Context, url string) ([]NWRStream, error) {
	if url == "" {
		url = NWRPageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icecast: nwr page: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParseNWR(string(b)), nil
}
