package feeds

import (
	"testing"
	"time"
)

// The proof ledger needs every item to link back to its provider.
func TestRBNAndPSKItemsCarryProviderRef(t *testing.T) {
	r, ok := ParseRBNSpot("DX de WZ7I-#:     14074.0  K1ABC       CW    17 dB  25 WPM CQ      2001Z", time.Now())
	if !ok || r.Ref == "" {
		t.Fatalf("rbn item ok=%v ref=%q", ok, r.Ref)
	}
	p, ok := ParsePSKSpot([]byte(`{"sc":"4x1rf","rc":"F59706","sl":"KM72jb","rl":"JN47qb","f":14074888,"md":"FT8","rp":-12,"t":1792615200,"b":"20m"}`), time.Now())
	if !ok || p.Ref == "" {
		t.Fatalf("psk item ok=%v ref=%q", ok, p.Ref)
	}
}
