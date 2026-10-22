package catalogue

// VerifiedVHF lists receivers Instinct connected to, tuned and recorded real
// audio from, band by band, on 2026-10-23 (UTC). The generated Table comes
// from the WebSDR server list and says nothing about whether a host answers;
// every entry here was proven live with the satprobe tool's -verify mode
// (profile selected, tuned, seconds of non-flat audio captured).
//
// The Verified field records when the proof happened; receivers appear and
// disappear, so callers should treat entries older than a few weeks as
// leads, not guarantees, and re-verify before scheduling around them.
var VerifiedVHF = []Receiver{
	{
		URL: "https://gids.nl:8073", Kind: "openwebrx",
		Latitude: 51.80, Longitude: 5.71,
		Bands: []Band{
			{Name: "NOAA 136-138", CenterKHz: 137000.0, SampleRateKHz: 2048.0},
			{Name: "2m", CenterKHz: 145000.0, SampleRateKHz: 2048.0},
			{Name: "70cm ISS", CenterKHz: 437000.0, SampleRateKHz: 2048.0},
			{Name: "GPS", CenterKHz: 1575000.0, SampleRateKHz: 2048.0},
		},
		SatelliteBands: []string{"137mhz", "2m", "70cm", "L-band"},
	},
	{
		URL: "https://signal.rnrd.space", Kind: "openwebrx",
		Latitude: 43.76, Longitude: 7.18,
		Bands:          []Band{{Name: "NOAA / METEOR", CenterKHz: 137000.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "http://websdr.golem.linux.it", Kind: "openwebrx",
		Latitude: 43.73, Longitude: 10.93,
		Bands:          []Band{{Name: "NOAA APT", CenterKHz: 137000.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "https://sdr.ea5kao.ampr.org", Kind: "openwebrx",
		Latitude: 38.08, Longitude: -1.25,
		Bands:          []Band{{Name: "sat|meteor", CenterKHz: 137000.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "https://ivopenwebrx.de", Kind: "openwebrx",
		Latitude: 32.79, Longitude: -115.58,
		Bands:          []Band{{Name: "Polar orbiting satellites", CenterKHz: 137000.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "https://owrxp.ng5p.com", Kind: "openwebrx",
		Latitude: 34.81, Longitude: -92.21,
		Bands:          []Band{{Name: "Meteor", CenterKHz: 137000.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "https://radio.redbanksdr.stream", Kind: "openwebrx",
		Latitude: 33.96, Longitude: -81.21,
		Bands:          []Band{{Name: "noaa_wx", CenterKHz: 137500.0, SampleRateKHz: 2000.0}},
		SatelliteBands: []string{"137mhz"},
	},
	{
		URL: "http://m0bah.ddns.net:8073", Kind: "openwebrx",
		Latitude: 50.90, Longitude: -0.41,
		Bands:          []Band{{Name: "Inmarsat", CenterKHz: 1543000.0, SampleRateKHz: 12000.0}},
		SatelliteBands: []string{"L-band"},
	},
}

// VerifiedAt is when the VerifiedVHF proofs were captured (UTC).
const VerifiedAt = "2026-10-23T00:04:00Z"

// ForFrequencyVerified returns live-verified receivers covering frequencyHz,
// in listed order (best first: gids.nl proved every satellite band we use).
func ForFrequencyVerified(frequencyHz float64) []Receiver {
	var out []Receiver
	for _, receiver := range VerifiedVHF {
		if receiver.CanHear(frequencyHz) {
			out = append(out, receiver)
		}
	}
	return out
}
