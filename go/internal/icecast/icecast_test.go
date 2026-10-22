package icecast

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseStreamTitle(t *testing.T) {
	meta := append([]byte("StreamTitle='Artist - Song';StreamUrl='';"), make([]byte, 5)...)
	if got := ParseStreamTitle(meta); got != "Artist - Song" {
		t.Fatalf("got %q", got)
	}
	if ParseStreamTitle([]byte("nothing")) != "" {
		t.Fatal("expected empty")
	}
}

// icyServer streams n blocks of audio with ICY metadata inserted.
func icyServer(metaint int, payload []byte, title string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Icy-MetaData") != "1" || !strings.HasPrefix(r.UserAgent(), "sdrstream/") {
			http.Error(w, "bad headers", 400)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Icy-Name", "Test NWR")
		w.Header().Set("Icy-Br", "32")
		w.Header().Set("Icy-Metaint", fmt.Sprint(metaint))
		for off := 0; off+metaint <= len(payload); off += metaint {
			w.Write(payload[off : off+metaint])
			if off == 0 {
				m := []byte("StreamTitle='" + title + "';")
				blocks := (len(m) + 15) / 16
				w.Write([]byte{byte(blocks)})
				w.Write(append(m, make([]byte, blocks*16-len(m))...))
			} else {
				w.Write([]byte{0})
			}
		}
	}))
}

func TestOpenStripsMetadata(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 400) // 6400 bytes
	srv := icyServer(1600, payload, "WXK56 test")
	defer srv.Close()
	var titles []string
	c := NewClient()
	body, info, err := c.Open(context.Background(), srv.URL, func(s string) { titles = append(titles, s) })
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var got bytes.Buffer
	if _, err := got.ReadFrom(body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("audio corrupted: %d bytes vs %d", got.Len(), len(payload))
	}
	if info.Name != "Test NWR" || info.BitrateKbps != 32 || info.MetaInt != 1600 {
		t.Errorf("info = %+v", info)
	}
	if len(titles) != 1 || titles[0] != "WXK56 test" {
		t.Errorf("titles = %v", titles)
	}
}

func TestOpenRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>"))
	}))
	defer srv.Close()
	if _, _, err := NewClient().Open(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("html page accepted as stream")
	}
}

// TestCaptureDecodesMP3 round-trips a tone through a real MP3 encoder and
// the full Capture path. It needs ffmpeg and is skipped without it.
func TestCaptureDecodesMP3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	mp3, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=1000:duration=3:sample_rate=22050",
		"-ac", "1", "-b:a", "32k", "-f", "mp3", "pipe:1").Output()
	if err != nil {
		t.Skipf("cannot encode mp3: %v", err)
	}
	srv := icyServer(2000, mp3, "tone")
	defer srv.Close()
	cap, err := NewClient().Capture(context.Background(), srv.URL, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if secs := float64(len(cap.Samples)) / PCMRate; secs < 2 || secs > 4 {
		t.Errorf("decoded %.2f s", secs)
	}
	if RMS(cap.Samples) < 0.05 {
		t.Errorf("rms %.3f, tone lost", RMS(cap.Samples))
	}
	if len(cap.Titles) != 1 || cap.Titles[0] != "tone" {
		t.Errorf("titles = %v", cap.Titles)
	}
}

const nwrFixture = `
<li data-path="content/inactive.mp3" data-thumbpath="x"><p><span class="pwsited">Texas</span>Nowhere 162.400 </p></li>
<li data-path="https://www.weather.gov/media/mfl/nwr/MIATWOAT.mp3"
data-thumbpath="a" data-buy-url="https://www.weather.gov/nwr/sites?site=WXM32">
<p><span class="pwsited">Florida</span>Belle Glade 162.475  <span class="pwsiteb">NOAA Direct Audio</span> </p>
</li>
<li data-path="https://wxradio.org/GA-Athens-WXK56"
data-thumbpath="b" data-buy-url="http://x">
<p><span class="pwsited">Georgia</span>Athens 162.400 <small>&starf;</small> <span class="pwsiteb">Athens Weather</span> </p>
</li>
<li data-path="https://wxradio.org/MN-Winona-KGG95" data-thumbpath="c">
<p><span class="pwsited">Wisconsin</span>Winona 162.425 </p>
</li>`

func TestParseNWR(t *testing.T) {
	got := ParseNWR(nwrFixture)
	if len(got) != 3 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	if !got[0].Recording || got[1].Recording {
		t.Errorf("recording flag wrong: %+v", got)
	}
	a := got[1]
	if a.Callsign != "WXK56" || a.City != "Athens" || a.State != "Georgia" || a.MHz != "162.400" {
		t.Errorf("athens = %+v", a)
	}
	if got[2].Callsign != "KGG95" {
		t.Errorf("callsign = %q", got[2].Callsign)
	}
	if len(Live(got)) != 2 {
		t.Errorf("live = %d", len(Live(got)))
	}
}

const ypFixture = `<directory>
<entry><server_name>Amarillo, TX - WXK38</server_name><server_type>audio/mpeg</server_type><bitrate>16</bitrate><listen_url>http://radio.weatherusa.net:80/NWR/WXK38_2.mp3</listen_url><current_song> </current_song><genre>Weather</genre></entry>
<entry><server_name>No URL</server_name><server_type>audio/mpeg</server_type><listen_url></listen_url><genre>Weather</genre></entry>
<entry><server_name>Jazz &amp; Blues</server_name><server_type>audio/aac</server_type><listen_url>http://x.example/jazz</listen_url><genre>Jazz</genre></entry>
<entry><server_name>Video</server_name><server_type>video/ogg</server_type><listen_url>http://x.example/v</listen_url><genre>Weather</genre></entry>
</directory>`

func TestParseAndFilterYP(t *testing.T) {
	e, err := ParseYP(strings.NewReader(ypFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 3 {
		t.Fatalf("entries = %d", len(e))
	}
	w := Filter(e, "weather", "audio/")
	if len(w) != 1 || !strings.Contains(w[0].ListenURL, "WXK38") {
		t.Fatalf("filter = %+v", w)
	}
	if e[1].Name != "Jazz & Blues" {
		t.Errorf("entity not decoded: %q", e[1].Name)
	}
}
