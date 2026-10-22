package regress

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/hfdl"
)

// Run executes one case. dir is the recording cache; root is the go/ module
// directory (for Local files). The recording must already exist.
func Run(c Case, dir, root string) (res Result) {
	res = Result{Case: c}
	defer func() {
		if p := recover(); p != nil {
			res.Status, res.Note = Fail, fmt.Sprint("decoder panic: ", p)
		}
	}()
	path := filepath.Join(dir, c.File)
	if c.Local != "" {
		path = filepath.Join(root, c.Local)
	}
	got := map[string]bool{}
	var err error
	switch c.Decoder {
	case "ft8", "ft4":
		var s []float64
		var rate int
		if s, rate, err = decode.ReadWAV(path); err == nil {
			for _, m := range decode.FTXDecode(s, rate, strings.ToUpper(c.Decoder)) {
				if m.Type == "unsupported" {
					res.Unsupported = append(res.Unsupported, m.Text)
					continue
				}
				got[Normalize(m.Text)] = true
			}
		}
	case "wspr":
		var s []float64
		var rate int
		if s, rate, err = decode.ReadWAV(path); err == nil {
			for _, m := range decode.WSPRDecode(s, rate) {
				got[Normalize(m.Text)] = true
			}
		}
	case "pocsag":
		var s []float64
		var rate int
		if s, rate, err = decode.ReadWAV(path); err == nil {
			for _, m := range decode.POCSAGDecode(s, rate, 0) {
				txt := m.Alpha
				if txt == "" {
					txt = m.Numeric
				}
				got[Normalize(fmt.Sprintf("%d|%d|%s", m.Address, m.Function, txt))] = true
			}
		}
	case "flex":
		var s []float64
		var rate int
		if s, rate, err = decode.ReadWAV(path); err == nil {
			for _, m := range decode.FlexDecode(s, rate) {
				got[Normalize(fmt.Sprintf("%d|%s", m.Capcode, m.Text))] = true
			}
		}
	case "acars":
		var chs [][]float64
		var rate int
		if chs, rate, err = decode.ReadWAVChannels(path); err == nil {
			for ch, s := range chs {
				for _, m := range decode.ACARSDecode(s, rate, ch) {
					got[fmt.Sprintf("%s|%s|%c|%c", m.Reg, m.Label, m.Mode, m.BlockID)] = true
				}
			}
		}
	case "aprs":
		var pk []decode.AX25
		if pk, err = decode.AFSKFrames(path); err == nil {
			for _, p := range pk {
				got[Normalize(p.Source+">"+p.Destination)] = true
			}
		}
	case "hfdl":
		var s []float64
		var rate int
		if s, rate, err = decode.ReadWAV(path); err == nil {
			carrier := c.Args["carrier"]
			for i, f := range hfdl.DecodeAudio(s, float64(rate), carrier, 0) {
				got[fmt.Sprintf("fcs-ok frame %d (%d bytes)", i, len(f.Frame.Data))] = true
			}
		}
	default:
		err = fmt.Errorf("unknown decoder %q", c.Decoder)
	}
	if err != nil {
		res.Status, res.Note = Fail, err.Error()
		return res
	}
	if c.Golden == "" {
		res.Got = len(got)
		res.Status = Info
		if c.MinFrames > 0 {
			res.Expected = c.MinFrames
			if res.Got < c.MinFrames {
				res.Status = Fail
			} else {
				res.Status = Pass
			}
		}
		var dec []string
		for k := range got {
			dec = append(dec, k)
		}
		sort.Strings(dec)
		res.Note = "decoded: " + strings.Join(dec, "; ")
		return res
	}
	ref, err := loadGolden(c.Golden)
	if err != nil {
		res.Status, res.Note = Fail, err.Error()
		return res
	}
	ok, _ := loadGolden("ok_" + c.Golden) // optional second-decoder set
	compare(&res, got, ref, ok)
	return res
}
