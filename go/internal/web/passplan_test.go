package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type fakePlan struct{}

func (fakePlan) TimelineJSON() interface{} { return []map[string]string{{"satellite": "ISS"}} }

func get(t *testing.T, s *Server) map[string]interface{} {
	rec := httptest.NewRecorder()
	s.handlePassplan(rec, httptest.NewRequest("GET", "/api/passplan.json", nil))
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	return out
}

func TestPassplanHandler(t *testing.T) {
	s := &Server{}
	if get(t, s)["enabled"] != false {
		t.Fatal("expected disabled")
	}
	s.SetPassplan(fakePlan{})
	out := get(t, s)
	obs, _ := out["observations"].([]interface{})
	if out["enabled"] != true || len(obs) != 1 {
		t.Fatalf("%v", out)
	}
}

type fakePlanInfo struct{ fakePlan }

func (fakePlanInfo) InfoJSON() interface{} { return map[string]string{"strategy": "priority"} }

func TestPassplanHandlerServesOptionalInfo(t *testing.T) {
	s := &Server{}
	s.SetPassplan(fakePlanInfo{})
	out := get(t, s)
	info, _ := out["info"].(map[string]interface{})
	if info["strategy"] != "priority" {
		t.Fatalf("%v", out)
	}
	s.SetPassplan(fakePlan{})
	if _, has := get(t, s)["info"]; has {
		t.Fatal("info must be absent for providers without it")
	}
}
