package satnogs

import (
	"encoding/json"
	"testing"
	"time"
)

func TestObservationCurrentSchema(t *testing.T) {
	const raw = `{
		"id": 15254898,
		"start": "2026-10-21T05:47:52Z",
		"end": "2026-10-21T05:56:48Z",
		"norad_cat_id": 25397,
		"tle0": "0 TECHSAT 1B",
		"station_name": "PE0SAT-21",
		"station_lat": 51.721268,
		"station_lng": 5.029968,
		"observation_frequency": 435225000,
		"transmitter_mode": "FSK",
		"status": "unknown",
		"max_altitude": 85.0,
		"waterfall": "https://example.test/waterfall.png",
		"demoddata": [
			{"payload_demod": "https://example.test/data_15254898"}
		]
	}`
	var observation Observation
	if err := json.Unmarshal([]byte(raw), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.NoradID != 25397 {
		t.Errorf("NoradID = %d", observation.NoradID)
	}
	if observation.SatName != "TECHSAT 1B" {
		t.Errorf("SatName = %q", observation.SatName)
	}
	if observation.FrequencyHz != 435225000 {
		t.Errorf("FrequencyHz = %d", observation.FrequencyHz)
	}
	if observation.Mode != "FSK" {
		t.Errorf("Mode = %q", observation.Mode)
	}
	if observation.Station != "PE0SAT-21" || observation.Latitude != 51.721268 {
		t.Errorf("station = %q at %f", observation.Station, observation.Latitude)
	}
	if observation.WaterfallURL != "https://example.test/waterfall.png" {
		t.Errorf("WaterfallURL = %q", observation.WaterfallURL)
	}
	if observation.DemodURL != "https://example.test/data_15254898" {
		t.Errorf("DemodURL = %q", observation.DemodURL)
	}
	if !observation.Start.Equal(time.Date(2026, 10, 21, 5, 47, 52, 0, time.UTC)) {
		t.Errorf("Start = %s", observation.Start)
	}
}

func TestObservationLegacySchema(t *testing.T) {
	const raw = `{
		"id": 1234,
		"satellite": 40908,
		"satellite_name": "LILACSAT 2",
		"station_name": "legacy",
		"ground_station_latitude": 1.5,
		"ground_station_longitude": 2.5,
		"frequency": 437200000,
		"mode": "AFSK",
		"max_elevation": 40,
		"waterfall_download": "https://example.test/old.png",
		"payload_download": "https://example.test/old.bin"
	}`
	var observation Observation
	if err := json.Unmarshal([]byte(raw), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.NoradID != 40908 || observation.SatName != "LILACSAT 2" {
		t.Errorf("identity = %d/%q", observation.NoradID, observation.SatName)
	}
	if observation.FrequencyHz != 437200000 || observation.Mode != "AFSK" {
		t.Errorf("frequency/mode = %d/%q", observation.FrequencyHz, observation.Mode)
	}
	if observation.WaterfallURL != "https://example.test/old.png" {
		t.Errorf("WaterfallURL = %q", observation.WaterfallURL)
	}
	if observation.DemodURL != "https://example.test/old.bin" {
		t.Errorf("DemodURL = %q", observation.DemodURL)
	}
	if observation.MaxElevation != 40 {
		t.Errorf("MaxElevation = %f", observation.MaxElevation)
	}
}

func TestOnlyNoradFiltersMixedFeed(t *testing.T) {
	// The network API's satellite= parameter returned a mixed-satellite
	// feed (observed live 2026-10-23), so Observations filters client-side.
	observations := []Observation{
		{ID: 1, NoradID: 25544},
		{ID: 2, NoradID: 33591},
		{ID: 3, NoradID: 25544},
		{ID: 4, NoradID: 0}, // unlabelled rows are kept
	}
	got := onlyNorad(observations, 25544)
	if len(got) != 3 || got[0].ID != 1 || got[1].ID != 3 || got[2].ID != 4 {
		t.Fatalf("onlyNorad: got %+v", got)
	}
	if all := onlyNorad(observations, 0); len(all) != 4 {
		t.Fatalf("onlyNorad(0) must pass everything through: %+v", all)
	}
}
