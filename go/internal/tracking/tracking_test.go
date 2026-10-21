package tracking

import (
	"math"
	"testing"
	"time"
)

// The General-Perturbations record below carries the same elements as the
// TLE text in tleISS, derived from CelesTrak's live ISS set. The numeric
// CCSDS fields are quoted as strings, the way CelesTrak serves them.
const gpISS = `[{"OBJECT_NAME":"ISS (ZARYA)","NORAD_CAT_ID":"25544","EPOCH":"2026-10-20T15:51:33.979680","MEAN_MOTION":15.48959761,"ECCENTRICITY":0.000675,"INCLINATION":51.6326,"RA_OF_ASC_NODE":39.1774,"ARG_OF_PERICENTER":287.932,"MEAN_ANOMALY":72.0932,"BSTAR":0.00016489,"MEAN_MOTION_DOT":8.635e-05,"MEAN_MOTION_DDOT":0.0,"CLASSIFICATION_TYPE":"U","EPHEMERIS_TYPE":"0","ELEMENT_SET_NO":"999","REV_AT_EPOCH":"46830"}]`

const tleISS = `ISS (ZARYA)
1 25544U 98067A   26293.66080995  .00008635  00000-0  16489-3 0  9998
2 25544  51.6326  39.1774 0006750 287.9320  72.0932 15.48959761591200`

// A record for an object that only exists in GP form: NORAD catalog numbers
// above 99,999 cannot be written as TLE text at all.
const gpSixDigit = `[{"OBJECT_NAME":"PICSAT-B","NORAD_CAT_ID":"100234","EPOCH":"2026-10-19T02:05:33.000000","MEAN_MOTION":15.30000000,"ECCENTRICITY":0.0010000,"INCLINATION":97.6000,"RA_OF_ASC_NODE":210.0000,"ARG_OF_PERICENTER":120.0000,"MEAN_ANOMALY":10.0000,"BSTAR":0.00005000,"MEAN_MOTION_DOT":0.00001200,"MEAN_MOTION_DDOT":0.0,"CLASSIFICATION_TYPE":"U","EPHEMERIS_TYPE":"0","ELEMENT_SET_NO":"1","REV_AT_EPOCH":"10"}]`

func TestFromGPTextMatchesTLEPropagation(t *testing.T) {
	gp, err := FromGPText(gpISS, "iss")
	if err != nil {
		t.Fatalf("FromGPText: %v", err)
	}
	tle, err := FromTLEText(tleISS, "ISS")
	if err != nil {
		t.Fatalf("FromTLEText: %v", err)
	}

	when := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	gpLook, err := gp.LookAngle(when, 32.79, 34.99, 0)
	if err != nil {
		t.Fatalf("GP LookAngle: %v", err)
	}
	tleLook, err := tle.LookAngle(when, 32.79, 34.99, 0)
	if err != nil {
		t.Fatalf("TLE LookAngle: %v", err)
	}

	// Same elements through the same propagator: agreement should be tight.
	if math.Abs(gpLook.Elevation-tleLook.Elevation) > 0.05 {
		t.Errorf("elevation disagreement: GP %.3f vs TLE %.3f",
			gpLook.Elevation, tleLook.Elevation)
	}
	if math.Abs(gpLook.Azimuth-tleLook.Azimuth) > 0.05 {
		t.Errorf("azimuth disagreement: GP %.3f vs TLE %.3f",
			gpLook.Azimuth, tleLook.Azimuth)
	}
}

func TestFromGPTextHandlesSixDigitCatalogNumbers(t *testing.T) {
	tracker, err := FromGPText(gpSixDigit, "picsat")
	if err != nil {
		t.Fatalf("FromGPText (six-digit ID): %v", err)
	}
	when := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	look, err := tracker.LookAngle(when, 32.79, 34.99, 0)
	if err != nil {
		t.Fatalf("LookAngle: %v", err)
	}
	if look.Elevation < -90 || look.Elevation > 90 {
		t.Errorf("implausible elevation %.2f", look.Elevation)
	}
	if tracker.ForTLE().SatelliteNumber != 100234 {
		t.Errorf("catalog number = %d, want 100234", tracker.ForTLE().SatelliteNumber)
	}
}

func TestFromGPTextNameMatching(t *testing.T) {
	// Case-insensitive substring match, like FromTLEText.
	if _, err := FromGPText(gpISS, "zarya"); err != nil {
		t.Errorf("case-insensitive match failed: %v", err)
	}
	if _, err := FromGPText(gpISS, "NO SUCH SATELLITE"); err == nil {
		t.Error("missing satellite should be an error")
	}
}

func TestFromGPTextRejectsBadJSON(t *testing.T) {
	if _, err := FromGPText("not json", "iss"); err == nil {
		t.Error("bad JSON should be an error")
	}
}
