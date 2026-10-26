package directory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const spyServerFixture = `{"servers":[
{"serverVersion":"2.0.1922","maxClients":3,"currentClientCount":0,
"streamingPort":5555,"ownerName":"John Doe","ownerEmail":"john@example.com",
"antennaType":"Vertical","antennaLocation":{"lat":46.2592,"long":5.6573},
"generalDescription":"no description","deviceType":"AirspyOne",
"fullControlAllowed":true,"minimumFrequency":24000000,
"maximumFrequency":1800000000,"maximumIQSampleRate":156250,
"streamingHost":"203.0.113.10","online":true},
{"streamingHost":"203.0.113.11","streamingPort":5556,"deviceType":"AirspyHF",
"online":false},
{"streamingHost":"192.168.1.20","streamingPort":5555,"deviceType":"AirspyOne",
"online":true},
{"streamingHost":"127.0.0.1","streamingPort":5555,"deviceType":"AirspyOne",
"online":true},
{"streamingHost":"169.254.1.1","streamingPort":5555,"deviceType":"AirspyOne",
"online":true},
{"streamingHost":"100.64.0.1","streamingPort":5555,"deviceType":"AirspyOne",
"online":true}
]}`

func TestParseSpyServersProjectsSafeFieldsAndDropsOffline(t *testing.T) {
	servers, err := ParseSpyServers(spyServerFixture)
	if err != nil {
		t.Fatalf("ParseSpyServers: %v", err)
	}
	// Only online entries survive: 203.0.113.10 plus the three
	// private/loopback/link-local/CGNAT entries (parse keeps them; the
	// public filter drops them).
	if len(servers) != 5 {
		t.Fatalf("want 5 online entries, got %d", len(servers))
	}
	var s SpyServer
	for _, srv := range servers {
		if srv.Host == "203.0.113.10" {
			s = srv
		}
	}
	if s.Host != "203.0.113.10" || s.Port != 5555 || s.DeviceType != "AirspyOne" ||
		s.MinFrequency != 24000000 || s.MaxFrequency != 1800000000 ||
		s.MaxClients != 3 || !s.FullControl || !s.Online ||
		s.Latitude != 46.2592 || s.Longitude != 5.6573 {
		t.Fatalf("projection wrong: %+v", s)
	}
	raw := spyServerFixture
	if strings.Contains(raw, "ownerEmail") {
		// fixture carries owner data; projection must not.
		for _, srv := range servers {
			b, _ := json.Marshal(srv)
			if strings.Contains(string(b), "john@example.com") || strings.Contains(string(b), "John Doe") {
				t.Fatalf("owner identity leaked into projection: %s", b)
			}
		}
	}
}

func TestFilterPublicSpyServersDropsNonPublic(t *testing.T) {
	servers, err := ParseSpyServers(spyServerFixture)
	if err != nil {
		t.Fatalf("ParseSpyServers: %v", err)
	}
	// NOTE: 203.0.113.0/24 is TEST-NET-3, so the fixture's "public" entry
	// is itself non-public; substitute a real-looking public literal.
	for i := range servers {
		if servers[i].Host == "203.0.113.10" {
			servers[i].Host = "8.8.8.8"
		}
	}
	out := FilterPublicSpyServers(context.Background(), servers)
	if len(out) != 1 || out[0].Host != "8.8.8.8" {
		t.Fatalf("want only the public literal, got %+v", out)
	}
}

func TestFetchSpyServersBadPayload(t *testing.T) {
	if _, err := ParseSpyServers("not json"); err == nil {
		t.Fatal("expected parse error on garbage payload")
	}
}
