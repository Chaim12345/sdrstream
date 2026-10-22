package icecast

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// YPURL is the Xiph (Icecast) directory's bulk listing: one XML file with
// every public stream. It is about 9 MB, so it is cached on disk.
const YPURL = "https://dir.xiph.org/yp.xml"

// Entry is one stream in the directory.
type Entry struct {
	Name      string `xml:"server_name" json:"name"`
	Type      string `xml:"server_type" json:"type"`
	Bitrate   string `xml:"bitrate" json:"bitrate"`
	ListenURL string `xml:"listen_url" json:"url"`
	Song      string `xml:"current_song" json:"song,omitempty"`
	Genre     string `xml:"genre" json:"genre"`
}

// ParseYP reads the directory XML and drops entries without a listen URL.
func ParseYP(r io.Reader) ([]Entry, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	var out []Entry
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "entry" {
			var e Entry
			if err := dec.DecodeElement(&e, &se); err != nil {
				return out, err
			}
			e.ListenURL = strings.TrimSpace(e.ListenURL)
			if strings.HasPrefix(e.ListenURL, "http://") || strings.HasPrefix(e.ListenURL, "https://") {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// Filter keeps entries whose genre or name contains genre (case-insensitive)
// and, when mimePrefix is set, whose type starts with it.
func Filter(entries []Entry, genre, mimePrefix string) []Entry {
	genre = strings.ToLower(strings.TrimSpace(genre))
	var out []Entry
	for _, e := range entries {
		if genre != "" && !strings.Contains(strings.ToLower(e.Genre), genre) && !strings.Contains(strings.ToLower(e.Name), genre) {
			continue
		}
		if mimePrefix != "" && !strings.HasPrefix(strings.ToLower(e.Type), mimePrefix) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FetchDirectory loads the directory, using cacheDir (24 h) when set so the
// 9 MB file is not downloaded on every run.
func (c *Client) FetchDirectory(ctx context.Context, url, cacheDir string, maxAge time.Duration) ([]Entry, error) {
	if url == "" {
		url = YPURL
	}
	var cache string
	if cacheDir != "" {
		cache = filepath.Join(cacheDir, "xiph-yp.xml")
		if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < maxAge {
			if f, err := os.Open(cache); err == nil {
				defer f.Close()
				return ParseYP(f)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	cl := &http.Client{Timeout: 90 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icecast: directory: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if cache != "" {
		_ = os.MkdirAll(cacheDir, 0o755)
		_ = os.WriteFile(cache, body, 0o644)
	}
	return ParseYP(strings.NewReader(string(body)))
}
