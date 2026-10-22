package decode

import (
	"math"
	"sync"
	"testing"
)

// The sample sentences are real AIS traffic captured from the openwaters.io
// community feed (upstream source: AISHub) on 2026-10-22, kept with the
// decoded fields that feed reported for cross-checking.

func TestAISPositionReport(t *testing.T) {
	asm := NewAISAssembler()
	br, raw, err := asm.Feed("!AIVDM,1,1,,A,16H;3L00002Ocn0BU;b=78n20000,0*4D")
	if err != nil {
		t.Fatal(err)
	}
	if br == nil {
		t.Fatal("single-fragment message did not complete")
	}
	m, err := DecodeAIS(br, raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != 1 {
		t.Errorf("type = %d, want 1", m.Type)
	}
	if m.MMSI != 428000112 {
		t.Errorf("mmsi = %d, want 428000112", m.MMSI)
	}
	if !m.HasPos {
		t.Fatal("no position")
	}
	if math.Abs(m.Lat-32.4726) > 1e-4 {
		t.Errorf("lat = %f, want ~32.4726", m.Lat)
	}
	if math.Abs(m.Lon-34.883733) > 1e-4 {
		t.Errorf("lon = %f, want ~34.883733", m.Lon)
	}
	if math.Abs(m.COG-335.6) > 0.05 {
		t.Errorf("cog = %f, want 335.6", m.COG)
	}
	if m.Heading != 283 {
		t.Errorf("heading = %d, want 283", m.Heading)
	}
}

func TestAISPositionReportSentinels(t *testing.T) {
	asm := NewAISAssembler()
	// ROT = -128 (no turn information) and heading 511 (not available).
	br, raw, err := asm.Feed("!AIVDM,1,1,,A,13R1E@OP00RP;`TBjA6c3wwD0000,0*1B")
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeAIS(br, raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.MMSI != 237000001 {
		t.Errorf("mmsi = %d, want 237000001", m.MMSI)
	}
	if math.Abs(m.Lat-32.82991) > 1e-4 || math.Abs(m.Lon-34.992243) > 1e-4 {
		t.Errorf("pos = %f,%f, want ~32.82991,34.992243", m.Lat, m.Lon)
	}
	if math.Abs(m.COG-283.1) > 0.05 {
		t.Errorf("cog = %f, want 283.1", m.COG)
	}
	if m.Heading != 511 {
		t.Errorf("heading = %d, want sentinel 511", m.Heading)
	}
}

func TestAISChecksumReject(t *testing.T) {
	asm := NewAISAssembler()
	if _, _, err := asm.Feed("!AIVDM,1,1,,A,16H;3L00002Ocn0BU;b=78n20000,0*FF"); err == nil {
		t.Fatal("corrupted checksum accepted")
	}
}

func TestAISMultiFragment(t *testing.T) {
	asm := NewAISAssembler()
	// Real type 5 static report for MMSI 636015092 ("TORM ALEXANDRA"),
	// captured from the community feed in two fragments.
	br, _, err := asm.Feed("!AIVDM,2,1,5,A,59NSAu42CqpGD8<CL01@u8n04hEP4pA8400000162`434565ND@Dj13i0000,0*04")
	if err != nil {
		t.Fatal(err)
	}
	if br != nil {
		t.Fatal("first fragment completed the message")
	}
	br, raw, err := asm.Feed("!AIVDM,2,2,5,A,00000000000,2*21")
	if err != nil {
		t.Fatal(err)
	}
	if br == nil {
		t.Fatal("message did not complete after the second fragment")
	}
	m, err := DecodeAIS(br, raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != 5 {
		t.Errorf("type = %d, want 5", m.Type)
	}
	if m.MMSI != 636015092 {
		t.Errorf("mmsi = %d, want 636015092", m.MMSI)
	}
	if m.Name != "TORM ALEXANDRA" {
		t.Errorf("name = %q, want TORM ALEXANDRA", m.Name)
	}
	if m.Callsign != "5BCD7" {
		t.Errorf("callsign = %q, want 5BCD7", m.Callsign)
	}
	if m.Destination != "ASHDOD" {
		t.Errorf("destination = %q, want ASHDOD", m.Destination)
	}
	t.Logf("type5: %s", m.Describe())
}

func TestAISConcurrentFeed(t *testing.T) {
	asm := NewAISAssembler()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, _, err := asm.Feed("!AIVDM,1,1,,A,16H;3L00002Ocn0BU;b=78n20000,0*4D"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
