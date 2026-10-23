// Package tracking propagates satellite orbits, predicts passes and computes
// Doppler correction.
package tracking

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/chaim12345/sdrstream/go/internal/tle"
)

// Speed of light in m/s.
const C = 299792458.0

// LookAngle describes where a satellite is, as seen from one place on the ground.
type LookAngle struct {
	Time         time.Time
	Azimuth      float64
	Elevation    float64
	RangeKm      float64
	RangeRateKmS float64
	Latitude     float64
	Longitude    float64
	AltitudeKm   float64
}

// Visible reports whether the satellite is above the horizon.
func (l LookAngle) Visible() bool { return l.Elevation > 0 }

// DopplerShift returns the received-frequency offset in Hz for a signal at
// frequencyHz. Positive means the satellite is approaching.
func (l LookAngle) DopplerShift(frequencyHz float64) float64 {
	return -l.RangeRateKmS * 1000.0 / C * frequencyHz
}

// CorrectedFrequency returns where to tune to hear a signal transmitted at
// frequencyHz.
func (l LookAngle) CorrectedFrequency(frequencyHz float64) float64 {
	return frequencyHz + l.DopplerShift(frequencyHz)
}

// Pass is a single pass of a satellite over an observer.
type Pass struct {
	Rise        time.Time
	Peak        LookAngle
	Set         time.Time
	DurationSec float64
}

// MaxElevation is the peak elevation of the pass, in degrees.
func (p Pass) MaxElevation() float64 { return p.Peak.Elevation }

// Summary renders the pass the way the Python CLI does, with the date on
// every time: a clock-only "02:07:16 rise" reads as a past pass when the
// plan is printed mid-morning for a pass after midnight tomorrow.
func (p Pass) Summary() string {
	return fmt.Sprintf("%s rise  peak %5.1f deg at %s  set  duration %4.1f min",
		p.Rise.Format("2006-01-02 15:04:05"), p.MaxElevation(), p.Peak.Time.Format("15:04:05"),
		p.DurationSec/60)
}

// Tracker propagates one satellite and predicts passes for observers.
type Tracker struct {
	tle *sgp4.TLE
	// The library recomputes internals per call; a mutex keeps concurrent
	// sweeps safe.
	mu sync.Mutex
}

// New builds a tracker from a TLE's two element lines.
func New(line1, line2 string) (*Tracker, error) {
	parsed, err := sgp4.ParseTLELines([]string{line1, line2})
	if err != nil {
		return nil, err
	}
	return &Tracker{tle: parsed}, nil
}

// FromTLEText builds a tracker from a TLE block, matching name
// case-insensitively. text may hold one TLE or a whole file of them.
func FromTLEText(text, name string) (*Tracker, error) {
	lines := nonEmptyLines(text)
	want := strings.ToUpper(strings.TrimSpace(name))
	for i := 0; i+2 < len(lines); i++ {
		if strings.Contains(strings.ToUpper(lines[i]), want) && strings.HasPrefix(lines[i+1], "1 ") {
			return New(lines[i+1], lines[i+2])
		}
	}
	return nil, fmt.Errorf("no TLE found for %q", name)
}

func nonEmptyLines(text string) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// lookAngleAt computes the look angle at t for an observer.
func (tr *Tracker) lookAngleAt(t time.Time, lat, lon, altKm float64) (LookAngle, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	// The sgp4 library reads a time's civil fields as if they were UTC and
	// ignores the zone offset, so any non-UTC tag silently shifts the
	// propagation. Normalise at the boundary.
	t = t.UTC()
	eci, err := tr.tle.FindPositionAtTime(t)
	if err != nil {
		return LookAngle{}, err
	}
	sv := &sgp4.StateVector{
		X: eci.Position.X, Y: eci.Position.Y, Z: eci.Position.Z,
		VX: eci.Velocity.X, VY: eci.Velocity.Y, VZ: eci.Velocity.Z,
	}
	loc := &sgp4.Location{Latitude: lat, Longitude: lon, Altitude: altKm * 1000}
	observation, err := sv.GetLookAngle(loc, t)
	if err != nil {
		return LookAngle{}, err
	}
	lat2, lon2, alt2 := eci.ToGeodetic()
	return LookAngle{
		Time:      t,
		Azimuth:   observation.LookAngles.Azimuth,
		Elevation: observation.LookAngles.Elevation,
		RangeKm:   observation.LookAngles.Range,
		// The library's RangeRate field is unreliable (verified off by two
		// orders of magnitude against a finite-difference of its own range),
		// so it is computed here from the ECI vectors.
		RangeRateKmS: rangeRate(tr.tle, eci, lat, lon, altKm, t),
		Latitude:     lat2,
		Longitude:    lon2,
		AltitudeKm:   alt2,
	}, nil
}

// rangeRate computes d|r|/dt for the satellite relative to a rotating
// observer, in km/s. Positive means the range is growing.
func rangeRate(tle *sgp4.TLE, eci sgp4.Eci, lat, lon, altKm float64, t time.Time) float64 {
	obsVX, obsVY, obsVZ := observerVelocityECI(lat, lon, altKm, t)
	obsX, obsY, obsZ := observerPositionECI(lat, lon, altKm, t)
	rx := eci.Position.X - obsX
	ry := eci.Position.Y - obsY
	rz := eci.Position.Z - obsZ
	r := math.Sqrt(rx*rx + ry*ry + rz*rz)
	if r == 0 {
		return 0
	}
	dvx := eci.Velocity.X - obsVX
	dvy := eci.Velocity.Y - obsVY
	dvz := eci.Velocity.Z - obsVZ
	return (rx*dvx + ry*dvy + rz*dvz) / r
}

// observerPositionECI is the observer's ECI position (km) at time t.
func observerPositionECI(lat, lon, altKm float64, t time.Time) (float64, float64, float64) {
	const reWGS84 = 6378.137
	const fWGS84 = 1.0 / 298.257223563
	deg2rad := math.Pi / 180.0
	latRad := lat * deg2rad
	lonRad := lon * deg2rad
	e2 := fWGS84 * (2 - fWGS84)
	sinLat := math.Sin(latRad)
	N := reWGS84 / math.Sqrt(1-e2*sinLat*sinLat)
	xe := (N + altKm) * math.Cos(latRad) * math.Cos(lonRad)
	ye := (N + altKm) * math.Cos(latRad) * math.Sin(lonRad)
	ze := (N*(1-e2) + altKm) * sinLat
	gmst := (&sgp4.Eci{DateTime: t.UTC()}).GreenwichSiderealTime()
	cosG, sinG := math.Cos(gmst), math.Sin(gmst)
	return xe*cosG - ye*sinG, xe*sinG + ye*cosG, ze
}

// observerVelocityECI is the observer's ECI velocity (km/s) from Earth
// rotation: v = omega x r, with r in ECI.
func observerVelocityECI(lat, lon, altKm float64, t time.Time) (float64, float64, float64) {
	const omegaEarth = 7.2921150e-5 // rad/s
	x, y, _ := observerPositionECI(lat, lon, altKm, t)
	return -omegaEarth * y, omegaEarth * x, 0
}

// LookAngle returns the look angle at t for an observer.
func (tr *Tracker) LookAngle(t time.Time, lat, lon, altKm float64) (LookAngle, error) {
	return tr.lookAngleAt(t, lat, lon, altKm)
}

// FindPasses finds passes above minElevation over the next hours, using the
// same coarse-then-refine scan as the Python original.
func (tr *Tracker) FindPasses(lat, lon float64, start time.Time, hours, minElevation float64) ([]Pass, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	// Same zone-offset hazard as lookAngleAt: the library reads civil fields
	// as UTC, and tags the returned pass times with the start's location.
	start = start.UTC()
	passes, err := tr.tle.GeneratePasses(lat, lon, 0, start, start.Add(time.Duration(hours*float64(time.Hour))), 30)
	if err != nil {
		return nil, err
	}
	var out []Pass
	for _, p := range passes {
		if p.MaxElevation < minElevation {
			continue
		}
		out = append(out, Pass{
			Rise:        p.AOS.UTC(),
			Peak:        LookAngle{Time: p.MaxElevationTime.UTC(), Elevation: p.MaxElevation, Azimuth: p.MaxElevationAz},
			Set:         p.LOS.UTC(),
			DurationSec: p.Duration.Seconds(),
		})
	}
	return out, nil
}

// ForTLE gives callers direct access to the parsed TLE, for Doppler sweeps.
func (tr *Tracker) ForTLE() *sgp4.TLE { return tr.tle }

// FromGPText builds a tracker from General-Perturbations records, matching
// name case-insensitively. GP records are the preferred element source:
// since the NORAD catalog passed 99,999, objects with six-digit catalog
// numbers are published only in the GP formats, never as TLE text.
func FromGPText(text, name string) (*Tracker, error) {
	normalized, err := tle.NormalizeGPJSON(text)
	if err != nil {
		return nil, fmt.Errorf("parsing GP records: %w", err)
	}
	records, err := sgp4.ParseOMMs([]byte(normalized))
	if err != nil {
		return nil, fmt.Errorf("parsing GP records: %w", err)
	}
	want := strings.ToUpper(strings.TrimSpace(name))
	for i := range records {
		if strings.Contains(strings.ToUpper(records[i].ObjectName), want) {
			tle, err := records[i].ToTLE()
			if err != nil {
				return nil, err
			}
			return &Tracker{tle: tle}, nil
		}
	}
	return nil, fmt.Errorf("no GP record found for %q", name)
}
