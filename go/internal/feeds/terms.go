package feeds

// SourceTerms is what a viewer needs to reuse a feed item correctly:
// licence, attribution and how long we keep it. Licences are filled in
// only where the repo already documents them (or the provider states
// them); everything else says "check provider terms" instead of guessing.
type SourceTerms struct {
	License string // "" = not recorded here
	HomeURL string
	Notes   string
}

const checkTerms = "licence not recorded here - check the provider's terms before reusing"

var sourceTerms = map[string]SourceTerms{
	"adsb.lol":    {License: "ODbL", HomeURL: "https://adsb.lol/"},
	"sondehub":    {License: "CC BY-SA 2.0", HomeURL: "https://sondehub.org/"},
	"digitraffic": {License: "CC BY 4.0", HomeURL: "https://www.digitraffic.fi/en/marine-traffic/"},
	"noaa-swpc":   {License: "US government data", HomeURL: "https://www.swpc.noaa.gov/"},
	"wspr.live":   {HomeURL: "https://wspr.live/", Notes: "public query results; " + checkTerms},
	"aprs-is":     {HomeURL: "https://www.aprs-is.net/", Notes: "read-only filtered connection; " + checkTerms},
	"pskreporter": {HomeURL: "https://pskreporter.info/", Notes: checkTerms},
	"rbn":         {HomeURL: "https://www.reversebeacon.net/", Notes: checkTerms},
	"wsprnet":     {HomeURL: "https://www.wsprnet.org/", Notes: checkTerms},
	"dxcluster":   {Notes: "DX cluster telnet network; " + checkTerms},
	"pota":        {HomeURL: "https://pota.app/", Notes: checkTerms},
	"sota":        {HomeURL: "https://www.sota.org.uk/", Notes: checkTerms},
	"aisstream":   {HomeURL: "https://aisstream.io/", Notes: "needs the user's own API key; " + checkTerms},
}

// TermsFor returns the recorded terms for a source and whether any exist.
func TermsFor(name string) (SourceTerms, bool) {
	t, ok := sourceTerms[name]
	return t, ok
}
