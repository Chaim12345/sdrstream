package directory

import "testing"

const directoryFixture = `<!doctype html><script>
var receivers = [
 {"label":"Site \"A\"; odd","location":{"coordinates":[-8.745792,39.293961],"type":"Point"},"receivers":[
   {"label":"OWRX","url":"http://a.example:8073/","type":"OpenWebRX"},
   {"label":"Kiwi","url":"http://a.example:8073/kiwi","type":"KiwiSDR"}]},
 {"label":"B","location":{"coordinates":[5.0,52.0]},"receivers":[
   {"label":"WS","url":"websdr.example:8901","type":"WebSDR"}]}
];
</script>`

func TestParseStations(t *testing.T) {
	stations, err := ParseStations(directoryFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 3 {
		t.Fatalf("got %d stations, want 3", len(stations))
	}
	counts := CountByType(stations)
	if counts["OpenWebRX"] != 1 || counts["KiwiSDR"] != 1 || counts["WebSDR"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	first := stations[0]
	if first.Name != "Kiwi" || first.URL != "http://a.example:8073/kiwi" {
		t.Fatalf("first station = %+v", first)
	}
	if first.Latitude != 39.293961 || first.Longitude != -8.745792 {
		t.Fatalf("coordinate order: %+v", first)
	}
	last := stations[len(stations)-1]
	if last.Type != "WebSDR" || last.Latitude != 52.0 || last.Longitude != 5.0 {
		t.Fatalf("last station = %+v", last)
	}
}

func TestParseStationsMissingArray(t *testing.T) {
	if _, err := ParseStations("<html>nothing here</html>"); err == nil {
		t.Fatal("expected an error for a page without a receiver array")
	}
}
