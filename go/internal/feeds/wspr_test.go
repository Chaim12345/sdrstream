package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wsprSample = `<table>
<tr id="oddrow"><td align=left>&nbsp;2026-10-21 20:40&nbsp;</td><td align=left>&nbsp;4X1RF&nbsp;</td><td align=right>&nbsp;14.097169&nbsp;</td><td align=right>&nbsp;-17&nbsp;</td><td align=right>&nbsp;0&nbsp;</td><td align=left>&nbsp;KM72jb&nbsp;</td><td align=right>&nbsp;+33&nbsp;</td><td align=right>&nbsp;1.995&nbsp;</td><td align=left>&nbsp;K2TAV&nbsp;</td><td align=left>&nbsp;FN33il&nbsp;</td><td align=right>&nbsp;3773&nbsp;</td><td align=right>&nbsp;2344&nbsp;</td><td align=right>&nbsp;WSPR-2&nbsp;</td><td align=right>&nbsp;POTACAT&nbsp;</td></tr>
<tr><td colspan=8><b>No posts found!</b></td></tr>
</table>`

func TestParseWSPRTable(t *testing.T) {
	items := ParseWSPRTable(wsprSample, "4x1rf")
	if len(items) != 1 {
		t.Fatalf("want 1 spot, got %+v", items)
	}
	it := items[0]
	if it.Label != "4X1RF" || it.Source != "wsprnet" || !it.HasPos {
		t.Fatalf("bad spot: %+v", it)
	}
	if it.Time.Hour() != 20 {
		t.Fatalf("time parse failed: %v", it.Time)
	}
	if got := ParseWSPRTable(wsprSample, "OTHER"); len(got) != 0 {
		t.Fatalf("filter failed: %+v", got)
	}
}

func TestWSPRRecent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("findcall") != "4X1RF" {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(wsprSample))
	}))
	defer srv.Close()
	w := &WSPR{Base: srv.URL}
	items, err := w.Recent(context.Background(), "4X1RF", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Detail, "K2TAV") {
		t.Fatalf("unexpected spots: %+v", items)
	}
}
