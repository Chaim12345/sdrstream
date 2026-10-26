package passplan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTripAtomic0600(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}, 2: {pass(200, 10, 50)}}}
	p := newP(f, StrategyRank)
	p.Generate([]Monitored{mon("a", 1, "", 0), mon("b", 2, "", 1)})
	p.Start(p.Timeline()[0].ID)
	path := filepath.Join(t.TempDir(), "sub", "passplan_state.json")
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(path + ".tmp.*"); len(m) != 0 {
		t.Fatalf("temp left: %v", m)
	}
	q := newP(fake{}, "")
	if err := q.Load(path); err != nil {
		t.Fatal(err)
	}
	tl := q.Timeline()
	if len(tl) != 2 || tl[0].Status != StatusFailed || tl[0].Detail != "interrupted by restart" || tl[1].Status != StatusScheduled {
		t.Fatalf("%+v", tl)
	}
	// regenerate after load must not duplicate
	q.Now = p.Now
	q.finder = f
	st, _ := q.Generate([]Monitored{mon("a", 1, "", 0), mon("b", 2, "", 1)})
	if st.Generated != 0 || len(q.Timeline()) != 2 {
		t.Fatalf("dup after load %+v", st)
	}
}

func TestLoadMissingCorruptAndVersion(t *testing.T) {
	d := t.TempDir()
	p := newP(fake{}, "")
	if err := p.Load(filepath.Join(d, "none.json")); err != nil {
		t.Fatal("missing must be ok")
	}
	bad := filepath.Join(d, "bad.json")
	os.WriteFile(bad, []byte("{nope"), 0o600)
	if p.Load(bad) == nil {
		t.Fatal("corrupt must error")
	}
	ver := filepath.Join(d, "v.json")
	os.WriteFile(ver, []byte(`{"version":99}`), 0o600)
	if p.Load(ver) == nil {
		t.Fatal("version must error")
	}
}

func TestRankStrategyPriorityBeatsPeak(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 20)}, 2: {pass(62, 10, 80)}}}
	p := newP(f, StrategyRank)
	p.Generate([]Monitored{mon("prio", 1, "rx", 0), mon("tall", 2, "rx", 9)})
	for _, o := range p.Timeline() {
		if o.Satellite == "prio" && o.Status != StatusScheduled || o.Satellite == "tall" && o.Status == StatusScheduled {
			t.Fatalf("rank: %+v", p.Timeline())
		}
	}
	// equal priority: peak decides
	p = newP(f, StrategyRank)
	p.Generate([]Monitored{mon("a", 1, "rx", 3), mon("b", 2, "rx", 3)})
	for _, o := range p.Timeline() {
		if o.Satellite == "b" && o.Status != StatusScheduled {
			t.Fatalf("rank equal priority: %+v", p.Timeline())
		}
	}
}
