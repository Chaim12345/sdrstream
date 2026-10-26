package sdrshare

import (
	"errors"
	"testing"
)

func cfg() map[string]any { return map[string]any{"center_freq": 100e6, "sample_rate": 2.4e6} }

func TestJoinSemantics(t *testing.T) {
	h := NewHub()
	if _, err := h.Join("a", "j1"); !errors.Is(err, ErrNotStreaming) {
		t.Fatalf("join idle: %v", err)
	}
	if err := h.Start("a", "c", cfg(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Join("a", "c"); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("controller join: %v", err)
	}
	live, err := h.Join("a", "j1")
	if err != nil || live["sample_rate"] != 2.4e6 {
		t.Fatalf("join: %v %v", live, err)
	}
	// Joiner cannot change settings or silently start over the controller.
	if err := h.SetConfig("a", "j1", map[string]any{"center_freq": 1.0}, false); !errors.Is(err, ErrJoinerSettings) {
		t.Fatalf("joiner set: %v", err)
	}
	var ce *ConflictError
	if err := h.Start("a", "j1", cfg(), false); !errors.As(err, &ce) || !ce.RequiresTakeover {
		t.Fatalf("start conflict: %v", err)
	}
	if err := h.Start("a", "x", cfg(), false); !errors.As(err, &ce) || len(ce.Others) != 2 {
		t.Fatalf("stranger start: %v", err)
	}
	if c, _ := h.Config("a"); c["center_freq"] != 100e6 {
		t.Fatal("config changed without takeover")
	}
}

func TestTakeoverKeepsJoiners(t *testing.T) {
	h := NewHub()
	h.Start("a", "c", cfg(), false)
	h.Join("a", "j1")
	n := cfg()
	n["center_freq"] = 7e6
	if err := h.Start("a", "j1", n, true); err != nil {
		t.Fatal(err)
	}
	if c, _ := h.Config("a"); c["center_freq"] != 7e6 {
		t.Fatal("takeover config not applied")
	}
	if m := h.Members("a"); len(m) != 2 { // c demoted to listener
		t.Fatalf("members %v", m)
	}
	if err := h.SetConfig("a", "c", map[string]any{"gain": 1}, false); !errors.Is(err, ErrJoinerSettings) {
		t.Fatal("ex-controller changed settings without takeover")
	}
}

func TestWorkerLifetime(t *testing.T) {
	h := NewHub()
	var stopped []string
	h.OnWorkerStop = func(id string) { stopped = append(stopped, id) }
	h.Start("a", "c", cfg(), false)
	h.Join("a", "j1")
	h.Stop("c") // controller leaves, joiner keeps worker alive, not promoted
	if len(stopped) != 0 {
		t.Fatal("worker stopped while joiner remains")
	}
	if err := h.SetConfig("a", "j1", map[string]any{"gain": 1}, false); !errors.Is(err, ErrJoinerSettings) {
		t.Fatalf("joiner promoted: %v", err)
	}
	if err := h.Leave("a", "j1"); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0] != "a" {
		t.Fatalf("stopped %v", stopped)
	}
	if err := h.Leave("a", "j1"); !errors.Is(err, ErrNoJoin) {
		t.Fatalf("double leave: %v", err)
	}
}

func TestOneJoinAtATime(t *testing.T) {
	h := NewHub()
	h.Start("a", "c1", cfg(), false)
	h.Start("b", "c2", cfg(), false)
	h.Join("a", "j")
	h.Join("b", "j")
	if m := h.Members("a"); len(m) != 1 || m[0] != "c1" {
		t.Fatalf("a members %v", m)
	}
}

func TestVFOsIndependent(t *testing.T) {
	h := NewHub()
	h.Start("a", "c", cfg(), false)
	h.Join("a", "j")
	h.Start("a", "internal:obs1", cfg(), true)
	if len(h.VFOs("c")) != 4 || len(h.VFOs("j")) != 4 || len(h.VFOs("internal:obs1")) != 10 {
		t.Fatal("bank sizes")
	}
	f, on, sel := int64(145800000), true, true
	h.UpdateVFO("j", 2, VFOUpdate{CenterHz: &f, Active: &on, Selected: &sel})
	if h.VFOs("c")[1].CenterHz != 0 || h.VFOs("j")[1].CenterHz != f || !h.VFOs("j")[1].Selected {
		t.Fatal("vfo leaked across sessions")
	}
	h.UpdateVFO("j", 0, VFOUpdate{Selected: &sel})
	if h.VFOs("j")[1].Selected {
		t.Fatal("vfo 0 should deselect all")
	}
	if err := h.UpdateVFO("j", 5, VFOUpdate{}); !errors.Is(err, ErrBadVFO) {
		t.Fatalf("range: %v", err)
	}
	h.Leave("a", "j")
	if h.UpdateVFO("j", 1, VFOUpdate{}) == nil {
		t.Fatal("vfo state kept after leave")
	}
}

func TestConcurrentChurn(t *testing.T) {
	h := NewHub()
	stops := make(chan string, 1000)
	h.OnWorkerStop = func(id string) { stops <- id }
	h.Start("a", "c", cfg(), false)
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id := "j" + string(rune('A'+i))
			for n := 0; n < 200; n++ {
				if _, err := h.Join("a", id); err == nil {
					f := int64(n)
					h.UpdateVFO(id, 1+n%4, VFOUpdate{CenterHz: &f})
					h.Config("a")
					h.Leave("a", id)
				}
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if m := h.Members("a"); len(m) != 1 || m[0] != "c" {
		t.Fatalf("members after churn: %v", m)
	}
	h.Stop("c")
	if len(h.Members("a")) != 0 || len(h.sources) != 0 || len(h.vfos) != 0 {
		t.Fatalf("leaked state: %d sources %d vfo banks", len(h.sources), len(h.vfos))
	}
	if len(stops) < 1 {
		t.Fatal("worker stop not signalled")
	}
}

func TestStopCleansEverything(t *testing.T) {
	h := NewHub()
	h.Start("a", "c", cfg(), false)
	h.Join("a", "j")
	h.Stop("j")
	h.Stop("j") // idempotent
	h.Stop("nobody")
	if len(h.vfos) != 1 {
		t.Fatalf("banks %d", len(h.vfos))
	}
}

func TestRoleAndActive(t *testing.T) {
	h := NewHub()
	h.Start("a", "c", cfg(), false)
	h.Join("a", "j")
	if s, r := h.Role("c"); s != "a" || r != "controller" {
		t.Fatal(s, r)
	}
	if s, r := h.Role("j"); s != "a" || r != "listener" {
		t.Fatal(s, r)
	}
	if s, r := h.Role("x"); s != "" || r != "" {
		t.Fatal(s, r)
	}
	if a := h.Active(); len(a) != 1 || len(a["a"]) != 2 {
		t.Fatal(a)
	}
}

func TestReleaseSkipsWorkerStop(t *testing.T) {
	h := NewHub()
	n := 0
	h.OnWorkerStop = func(string) { n++ }
	h.Start("a", "c", cfg(), false)
	h.Release("c")
	if n != 0 || len(h.Active()) != 0 {
		t.Fatalf("n=%d active=%v", n, h.Active())
	}
}
