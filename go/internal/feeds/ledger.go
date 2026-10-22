package feeds

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Decoded-by labels for Item.DecodedBy.
const (
	DecodedByProvider = "provider" // the upstream service decoded it; we relay it
	DecodedByUs       = "sdrstream"
)

// LedgerEntry is one proof record: where an item came from, when the
// provider stamped it, when we fetched it, and who decoded it. The item
// is sdrstream's normalised view of the provider data, not the raw bytes.
type LedgerEntry struct {
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Detail    string    `json:"detail,omitempty"`
	ProviderT time.Time `json:"provider_time"`
	FetchedAt time.Time `json:"fetched_at"`
	DecodedBy string    `json:"decoded_by"`
	Ref       string    `json:"ref,omitempty"`
	Lat       float64   `json:"lat,omitempty"`
	Lon       float64   `json:"lon,omitempty"`
}

// Ledger is an append-only JSONL file of first sightings. It rotates to
// path+".1" at maxBytes so disk use stays bounded (about 2*maxBytes).
// The default is 64 MiB per file (about 128 MiB total, roughly 14 hours
// at the ~9 MB/hour measured with all feeds on); set SDRSTREAM_LEDGER_MAX_MB
// to change it.
type Ledger struct {
	path     string
	maxBytes int64
	ch       chan LedgerEntry
	done     chan struct{}
	once     sync.Once
}

// NewLedger opens (creating) the ledger and starts its writer. Close it
// to flush. maxBytes <= 0 means DefaultLedgerBytes.
func NewLedger(path string, maxBytes int64) *Ledger {
	if maxBytes <= 0 {
		maxBytes = DefaultLedgerBytes
	}
	l := &Ledger{path: path, maxBytes: maxBytes, ch: make(chan LedgerEntry, 4096), done: make(chan struct{})}
	go l.run()
	return l
}

func (l *Ledger) run() {
	defer close(l.done)
	var f *os.File
	var size int64
	open := func() {
		if f != nil {
			return
		}
		nf, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		f = nf
		if st, err := nf.Stat(); err == nil {
			size = st.Size()
		}
	}
	for e := range l.ch {
		open()
		if f == nil {
			continue
		}
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		b = append(b, '\n')
		if size+int64(len(b)) > l.maxBytes {
			_ = f.Close()
			f = nil
			_ = os.Rename(l.path, l.path+".1")
			open()
			if f == nil {
				continue
			}
		}
		n, _ := f.Write(b)
		size += int64(n)
	}
	if f != nil {
		_ = f.Close()
	}
}

// Append queues an entry; it never blocks (a full queue drops the entry,
// which only loses history, never live data).
func (l *Ledger) Append(e LedgerEntry) {
	select {
	case l.ch <- e:
	default:
	}
}

// Close flushes queued entries and stops the writer.
func (l *Ledger) Close() {
	l.once.Do(func() { close(l.ch) })
	<-l.done
}

// Query returns up to limit newest entries for source ("" = all), newest
// first. It reads the rotated file then the live file.
func (l *Ledger) Query(source string, limit int) []LedgerEntry {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	ring := make([]LedgerEntry, 0, limit)
	for _, p := range []string{l.path + ".1", l.path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e LedgerEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue // a torn last line after a crash is skipped
			}
			if source != "" && !strings.EqualFold(e.Source, source) {
				continue
			}
			if len(ring) == limit {
				copy(ring, ring[1:])
				ring = ring[:limit-1]
			}
			ring = append(ring, e)
		}
		_ = f.Close()
	}
	for i, j := 0, len(ring)-1; i < j; i, j = i+1, j-1 {
		ring[i], ring[j] = ring[j], ring[i]
	}
	return ring
}

// SetLedger attaches a ledger; every first sighting is appended to it.
func (m *Manager) SetLedger(l *Ledger) { m.mu.Lock(); m.ledger = l; m.mu.Unlock() }

// Ledger returns the attached ledger or nil.
func (m *Manager) Ledger() *Ledger { m.mu.Lock(); defer m.mu.Unlock(); return m.ledger }

// DefaultLedgerBytes is the per-file rotation size.
const DefaultLedgerBytes int64 = 64 << 20

// MaxLedgerMB caps the configurable size (1 GiB per file) so a typo cannot
// fill the disk.
const MaxLedgerMB = 1024

// LedgerBytesFromEnv reads SDRSTREAM_LEDGER_MAX_MB (per-file size in MiB).
// Unset, invalid or out-of-range (<1 or >MaxLedgerMB) values give the
// default; ok reports whether the value was valid or unset.
func LedgerBytesFromEnv(getenv func(string) string) (bytes int64, ok bool) {
	raw := strings.TrimSpace(getenv("SDRSTREAM_LEDGER_MAX_MB"))
	if raw == "" {
		return DefaultLedgerBytes, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > MaxLedgerMB {
		return DefaultLedgerBytes, false
	}
	return int64(n) << 20, true
}
