package feeds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const aisSample = `{"MessageType":"PositionReport","MetaData":{"MMSI":212345000,"ShipName":"TEST VESSEL","latitude":32.5,"longitude":34.1,"time_utc":"2026-10-21 20:39:24.000000000 +0000 UTC"},"Message":{"PositionReport":{"Sog":12.3,"Cog":287,"TrueHeading":290}}}`

func TestParseAIS(t *testing.T) {
	it, ok := ParseAIS([]byte(aisSample), time.Now())
	if !ok {
		t.Fatal("no item")
	}
	if it.Label != "TEST VESSEL" || !it.HasPos || it.ID != "ais|212345000" {
		t.Fatalf("bad item: %+v", it)
	}
	if !strings.Contains(it.Detail, "12.3 kt") {
		t.Fatalf("missing speed: %+v", it)
	}
}

func TestAISRunSendsKey(t *testing.T) {
	up := websocket.Upgrader{}
	var received aisSubscribe
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.ReadJSON(&received); err != nil {
			t.Errorf("read subscribe: %v", err)
			return
		}
		conn.WriteMessage(websocket.TextMessage, []byte(aisSample))
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	src := &AIS{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Key: "test-key-123"}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Item, 2)
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(it Item) { got <- it }) }()
	var item Item
	select {
	case item = <-got:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for feed item")
	}
	cancel()
	<-done
	if received.APIKey != "test-key-123" {
		t.Fatalf("key not sent: %+v", received)
	}
	if len(received.BoundingBoxes) != 1 {
		t.Fatalf("bounding box missing: %+v", received)
	}
	if item.Label != "TEST VESSEL" {
		t.Fatalf("want TEST VESSEL, got %+v", item)
	}
}

func TestAISWithoutKeyFails(t *testing.T) {
	src := &AIS{}
	if err := src.Run(context.Background(), func(Item) {}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

var _ = json.Marshal // keep encoding/json import if unused later
