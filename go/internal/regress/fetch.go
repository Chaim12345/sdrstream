package regress

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// UserAgent identifies the harness to the public hosts it downloads from.
const UserAgent = "sdrstream-decoder-regress/1.0 (+https://github.com/Chaim12345/sdrstream)"

// Fetch downloads the recording of c into dir unless it is already cached
// and matches the pinned digest. It sleeps delay after each download so the
// public hosts see at most one request per delay. FLAC sources are converted
// to 16-bit WAV with ffmpeg when it is installed.
func Fetch(c Case, dir string, delay time.Duration) error {
	if c.Local != "" || c.URL == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, c.File)
	if _, err := os.Stat(dst); err == nil {
		return verify(c, dst)
	}
	raw := dst
	isFlac := strings.HasSuffix(strings.ToLower(c.URL), ".flac")
	if isFlac {
		raw = strings.TrimSuffix(dst, ".wav") + ".flac"
	}
	if _, err := os.Stat(raw); err != nil {
		if err := download(c.URL, raw); err != nil {
			return err
		}
		time.Sleep(delay)
	}
	if err := verify(c, raw); err != nil {
		os.Remove(raw)
		return err
	}
	if isFlac {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return fmt.Errorf("%s: ffmpeg needed to convert FLAC", c.Name)
		}
		out, err := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-i", raw, "-ac", "1", "-c:a", "pcm_s16le", dst).CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffmpeg: %v: %s", err, out)
		}
	}
	return nil
}

func verify(c Case, path string) error {
	if c.SHA256 == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != c.SHA256 {
		return fmt.Errorf("%s: sha256 %s, pinned %s (upstream file changed?)", c.Name, got, c.SHA256)
	}
	return nil
}

func download(url, dst string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, dst)
}
