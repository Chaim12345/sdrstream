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
