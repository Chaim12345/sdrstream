// Package hfschedule plans UTC HF captures. Schedules predict airtime, not reception.
package hfschedule

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

const EiBiURL = "http://eibispace.de/dx/eibi.txt"
const NOAAURL = "https://ocean.weather.gov/radiofax_update.txt"

type Entry struct {
	ID                     string
	Source                 string
	Target                 decode.HFTarget
	StartMinute, EndMinute int
	Days                   string
	ValidFrom, ValidUntil  time.Time
}
type Occurrence struct {
	Entry      Entry
	Start, End time.Time
}

func (o Occurrence) Key() string { return o.Entry.ID + "-" + o.Start.UTC().Format("20060102T1504Z") }
func id(s string) string         { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:8]) }
func minute(s string, end bool) (int, error) {
	if len(s) != 4 || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, fmt.Errorf("bad UTC time %q", s)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	h, m := n/100, n%100
	if h == 24 && m == 0 && end {
		return 1440, nil
	}
	if h > 23 || m > 59 {
		return 0, fmt.Errorf("invalid UTC %q", s)
	}
	return h*60 + m, nil
}

var timeRE = regexp.MustCompile(`^\d{4}-\d{4}$`)
var validityRE = regexp.MustCompile(`Valid (.+?) - (.+)$`)

// ParseEiBi reads the frequency-sorted fixed-width eibi.txt format, not CSV.
// Unknown/irregular day codes are retained but never automatically scheduled.
func ParseEiBi(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 1024*1024)
	var from, until time.Time
	for sc.Scan() {
		line := sc.Text()
		if m := validityRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			var err error
			from, err = time.Parse("January 2, 2006", m[1])
			if err != nil {
				return nil, err
			}
			until, err = time.Parse("January 2, 2006", m[2])
			if err != nil {
				return nil, err
			}
			until = until.AddDate(0, 0, 1)
		}
		if len(line) < 57 || !timeRE.MatchString(strings.TrimSpace(line[14:23])) {
			continue
		}
		freq, err := strconv.ParseFloat(strings.TrimSpace(line[:14]), 64)
		if err != nil || freq < 10 || freq > 30000 {
			continue
		}
		start, err := minute(line[14:18], false)
		if err != nil {
			return nil, err
		}
		end, err := minute(line[19:23], true)
		if err != nil {
			return nil, err
		}
		name := latin1(strings.TrimSpace(line[34:57]))
		days := strings.TrimSpace(line[24:29])
		t := decode.HFTarget{Name: name, Kind: "broadcast", CarrierKHz: freq, DialKHz: freq, Mode: "am"}
		upper := strings.ToUpper(name)
		switch {
		case strings.Contains(upper, "NAVTEX"):
			t.Kind = "navtex"
			t.Mode = "usb"
			t.DialKHz = freq - 1.7
			t.Baud = 100
			t.MarkHz = 1785
			t.SpaceHz = 1615
		case strings.Contains(upper, "RTTY") && strings.Contains(upper, "DWD"):
			t.Kind = "rtty"
			t.Mode = "usb"
			t.DialKHz = freq - 1.9
			t.Baud = 50
			t.MarkHz = 1675
			t.SpaceHz = 2125
			t.Inverted = true
			switch freq {
			case 4583:
				t.Expect = "DDK2"
			case 7646:
				t.Expect = "DDH7"
			case 10100.8:
				t.Expect = "DDK9"
			}
		case strings.Contains(upper, "FAX"):
			t.Kind = "wefax"
			t.Mode = "usb"
			t.DialKHz = freq - 1.9
			t.LinesPerMin = 120
			t.IOC = 576
		case days == "USB":
			t.Mode = "usb"
		case days == "LSB":
			t.Mode = "lsb"
		}
		// Non-broadcast utility codes are not AM voice services. Do not auto-record them as broadcasts.
		lang := ""
		if len(line) >= 62 {
			lang = strings.TrimSpace(line[57:62])
		}
		if strings.HasPrefix(lang, "-") && t.Kind == "broadcast" {
			continue
		}
		entries = append(entries, Entry{ID: id(line), Source: EiBiURL, Target: t, StartMinute: start, EndMinute: end, Days: days, ValidFrom: from, ValidUntil: until})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no EiBi schedule rows")
	}
	if from.IsZero() {
		return nil, fmt.Errorf("EiBi validity header missing")
	}
	return entries, nil
}
func latin1(s string) string {
	var b strings.Builder
	for _, v := range []byte(s) {
		b.WriteRune(rune(v))
	}
	return b.String()
}

var noaaRow = regexp.MustCompile(`^(\d{4}|----)/(\d{4}|----)\s+(.+?)\s+(\d+)/(\d+)(?:\s|$)`)
var noaaFreq = regexp.MustCompile(`^\s*(?:NMF\s+)?([0-9.]+)\s+kHz\s+(.+?)\s+F3C`)

// ParseNOAA reads NOAA's Boston NMF radiofax_update.txt. Unsupported station
// formats fail closed. Each chart ends at the next listed chart, capped at 20m.
func ParseNOAA(r io.Reader) ([]Entry, error) {
	type freqWindow struct {
		f          float64
		start, end int
	}
	var freqs []freqWindow
	var rows []Entry
	boston := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "BOSTON, MASSACHUSETTS") {
			boston = true
		}
		if !boston {
			continue
		}
		if m := noaaFreq.FindStringSubmatch(line); m != nil {
			f, _ := strconv.ParseFloat(m[1], 64)
			start, end := 0, 1440
			if m[2] != "ALL BROADCAST TIMES" {
				parts := strings.Split(strings.ReplaceAll(m[2], "z", ""), "-")
				if len(parts) != 2 {
					return nil, fmt.Errorf("unknown NOAA frequency window")
				}
				var err error
				start, err = minute(parts[0], false)
				if err != nil {
					return nil, err
				}
				end, err = minute(parts[1], true)
				if err != nil {
					return nil, err
				}
			}
			freqs = append(freqs, freqWindow{f, start, end})
		}
		if m := noaaRow.FindStringSubmatch(line); m != nil {
			rpm, _ := strconv.ParseFloat(m[4], 64)
			ioc, _ := strconv.Atoi(m[5])
			for _, s := range m[1:3] {
				if s == "----" {
					continue
				}
				start, err := minute(s, false)
				if err != nil {
					return nil, err
				}
				for _, f := range freqs {
					if start < f.start || start >= f.end {
						continue
					}
					t := decode.HFTarget{Name: "NMF Boston " + m[3], Kind: "wefax", CarrierKHz: f.f, DialKHz: f.f - 1.9, Mode: "usb", LinesPerMin: rpm, IOC: ioc}
					rows = append(rows, Entry{ID: id(fmt.Sprintf("NMF:%g:%d:%s", f.f, start, m[3])), Source: NOAAURL, Target: t, StartMinute: start, EndMinute: min(start+20, 1440)})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no NOAA Boston radiofax rows")
	}
	for i := range rows {
		for j := range rows {
			if rows[j].Target.CarrierKHz == rows[i].Target.CarrierKHz && rows[j].StartMinute > rows[i].StartMinute && rows[j].StartMinute < rows[i].EndMinute {
				rows[i].EndMinute = rows[j].StartMinute
			}
		}
	}
	return rows, nil
}
func dayMatches(code string, d time.Time) bool {
	if code == "" || code == "USB" || code == "LSB" {
		return true
	}
	n := int(d.Weekday())
	if n == 0 {
		n = 7
	}
	if regexp.MustCompile(`^[1-7]+$`).MatchString(code) {
		return strings.Contains(code, strconv.Itoa(n))
	}
	names := []string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}
	rest := code
	for _, s := range names {
		rest = strings.ReplaceAll(rest, s, "")
	}
	rest = strings.ReplaceAll(rest, ",", "")
	if rest == "" {
		return strings.Contains(code, names[n-1])
	}
	if dt, err := time.Parse("2Jan", code); err == nil {
		return dt.Month() == d.Month() && dt.Day() == d.Day()
	}
	return false
}

// Plan includes active broadcasts and future starts, anchored to the start day
// (including for transmissions crossing midnight). End instants are exclusive.
func Plan(entries []Entry, now time.Time, horizon time.Duration) []Occurrence {
	now = now.UTC()
	mid := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var out []Occurrence
	for d := mid.AddDate(0, 0, -1); !d.After(now.Add(horizon)); d = d.AddDate(0, 0, 1) {
		for _, e := range entries {
			if !dayMatches(e.Days, d) {
				continue
			}
			start := d.Add(time.Duration(e.StartMinute) * time.Minute)
			end := d.Add(time.Duration(e.EndMinute) * time.Minute)
			if !end.After(start) {
				end = end.AddDate(0, 0, 1)
			}
			if (!e.ValidFrom.IsZero() && start.Before(e.ValidFrom)) || (!e.ValidUntil.IsZero() && !start.Before(e.ValidUntil)) {
				continue
			}
			if !end.After(now) || start.After(now.Add(horizon)) {
				continue
			}
			out = append(out, Occurrence{e, start, end})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].Key() < out[j].Key()
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out
}

var noaaValidRE = regexp.MustCompile(`^Valid\s+(.+?)\s*$`)
var noaaUpdatedRE = regexp.MustCompile(`^Updated\s+(.+?)\s*$`)

// NOAAValidity reads the "Valid <date>" and "Updated <date>" header lines of
// the hosted Boston NMF timetable so the operator sees the real header date
// instead of a hardcoded year. Missing lines return empty strings.
func NOAAValidity(r io.Reader) (valid, updated string) {
	sc := bufio.NewScanner(r)
	for i := 0; i < 20 && sc.Scan(); i++ {
		line := sc.Text()
		if m := noaaValidRE.FindStringSubmatch(line); m != nil && valid == "" {
			valid = m[1]
		}
		if m := noaaUpdatedRE.FindStringSubmatch(line); m != nil && updated == "" {
			updated = m[1]
		}
	}
	return valid, updated
}
