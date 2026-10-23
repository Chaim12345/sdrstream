package kiwisdr

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// Preflight verdicts.
const (
	VerdictOK      = "ok"
	VerdictBusy    = "busy"    // no free slot (users >= users_max)
	VerdictDead    = "dead"    // HTTP error or malformed /status
	VerdictTimeout = "timeout" // nothing answered in time
	VerdictBackoff = "backoff" // skipped locally, no network traffic
	VerdictBudget  = "budget"  // daily preflight attempt budget used up
)

// Preflight timing policy.
const (
	CacheTTL           = 15 * time.Minute
	BackoffBase        = time.Minute
	BackoffCap         = 24 * time.Hour
	MaxAttemptsPerDay  = 3
	EgressBlockedAfter = 5 // consecutive all-timeout hosts before warning
)

// Status is what a KiwiSDR reports on GET /status plus what the preflight
// learned from the HTTP response and the websocket probe.
type Status struct {
	Host       string
	Users      int
	UsersMax   int
	Preempt    int
	ExtAPI     string
	Server     string // raw Server header
	Protocol   string // "https"/"http" actually answered
	WSPath     string // "ws" path style that upgraded: "/ws/kiwi" or "legacy"
	Passworded bool
	Verdict    string
	Err        string
	CheckedAt  time.Time
	// Latency is how long the successful HTTP /status probe took. It is a
	// wall-clock measurement (the injectable Now clock is logical), zero
	// when the probe never got an answer.
	Latency time.Duration
}

// Free reports whether a slot is available.
func (s Status) Free() bool { return s.Verdict == VerdictOK }

// Preflighter validates Kiwi hosts before a scan spends a session slot on
// them. It is safe for concurrent use.
type Preflighter struct {
	HTTP     *http.Client
	Now      func() time.Time
	Dial     func(ctx context.Context, uri string) error // websocket probe; nil uses gorilla
	Password string
	ProbeWS  bool
	Timeout  time.Duration
	// AllowPrivate permits preflighting loopback/private hosts (local
	// receivers, tests). Leave it false for directory-sourced stations:
	// those are untrusted input and must not reach internal addresses.
	AllowPrivate bool

	mu         sync.Mutex
	cache      map[string]Status
	fails      map[string]int       // consecutive failures per host
	until      map[string]time.Time // backoff deadline
	attempts   map[string][]time.Time
	probeSlots chan struct{} // bounded concurrent network probes
	allTO      int           // consecutive hosts whose attempt timed out with no response at all
	okSeen     bool
}

// NewPreflighter builds one with sane defaults.
func NewPreflighter() *Preflighter {
	return &Preflighter{
		HTTP:       common.PublicHTTPClient(8 * time.Second),
		Now:        time.Now,
		Timeout:    8 * time.Second,
		cache:      map[string]Status{},
		fails:      map[string]int{},
		until:      map[string]time.Time{},
		attempts:   map[string][]time.Time{},
		probeSlots: make(chan struct{}, 16),
	}
}

// BackoffFor returns the delay after n consecutive failures: 1m, 2m, 4m...
// capped at 24h.
func BackoffFor(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := BackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= BackoffCap {
			return BackoffCap
		}
	}
	return d
}

var kvLine = regexp.MustCompile(`^([A-Za-z0-9_]+)=(.*)$`)

// ParseStatus reads the key=value body of /status.
func ParseStatus(body string) (Status, error) {
	var s Status
	seen := false
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		m := kvLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		v := strings.TrimSpace(m[2])
		switch m[1] {
		case "users":
			n, err := strconv.Atoi(v)
			if err != nil {
				return s, fmt.Errorf("bad users=%q", v)
			}
			s.Users, seen = n, true
		case "users_max":
			n, err := strconv.Atoi(v)
			if err != nil {
				return s, fmt.Errorf("bad users_max=%q", v)
			}
			s.UsersMax = n
		case "preempt":
			s.Preempt, _ = strconv.Atoi(v)
		case "ext_api":
			s.ExtAPI = v
		}
	}
	if !seen || s.UsersMax <= 0 {
		return s, errors.New("status lacks users/users_max")
	}
	return s, nil
}

// MD5Auth returns the hex md5 of a password, the form used when a node wants
// a hashed credential. Empty passwords stay empty.
func MD5Auth(password string) string {
	if password == "" {
		return ""
	}
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// WSPaths lists websocket URI paths to try for ts. Proxied/newer deployments
// serve /ws/kiwi/<ts>/SND; direct servers use the legacy /<ts>/SND. The Server
// header only orders them: a recognisable "KiwiSDR" server tries legacy first,
// anything else (proxy, nginx) tries /ws/kiwi first.
func WSPaths(server string, ts int64) []string {
	legacy := fmt.Sprintf("/%d/SND", ts)
	ws := fmt.Sprintf("/ws/kiwi/%d/SND", ts)
	if strings.Contains(strings.ToLower(server), "kiwi") {
		return []string{legacy, ws}
	}
	return []string{ws, legacy}
}

// hostOnly accepts the forms the directory and the CLI produce: a bare
// "host:port", or a full URL. The probe always builds scheme://host/status,
// so a URL passed as-is used to become "http://http//host/status".
func hostOnly(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// Check validates host ("host:port"). It never hits the network when a fresh
// cached result exists, the host is backing off, or the daily budget is spent.
func (p *Preflighter) Check(ctx context.Context, host string) Status {
	host = hostOnly(host)
	now := p.Now()
	p.mu.Lock()
	if c, ok := p.cache[host]; ok && now.Sub(c.CheckedAt) < CacheTTL {
		p.mu.Unlock()
		return c
	}
	if u, ok := p.until[host]; ok && now.Before(u) {
		p.mu.Unlock()
		return Status{Host: host, Verdict: VerdictBackoff, Err: "backing off until " + u.Format(time.RFC3339), CheckedAt: now}
	}
	cut := now.Add(-24 * time.Hour)
	recent := p.attempts[host][:0:0]
	for _, t := range p.attempts[host] {
		if t.After(cut) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= MaxAttemptsPerDay {
		p.attempts[host] = recent
		p.mu.Unlock()
		return Status{Host: host, Verdict: VerdictBudget, Err: "daily preflight budget used", CheckedAt: now}
	}
	p.attempts[host] = append(recent, now)
	p.mu.Unlock()

	select {
	case p.probeSlots <- struct{}{}:
	case <-ctx.Done():
		return Status{Host: host, Verdict: VerdictTimeout, Err: ctx.Err().Error(), CheckedAt: p.Now()}
	}
	st := p.probe(ctx, host)
	<-p.probeSlots
	st.CheckedAt = p.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	switch st.Verdict {
	case VerdictOK:
		// A successful probe proves the host reachable: clear the failure
		// state and the daily attempt budget, so one good session reopens
		// a host that burned through earlier attempts.
		p.fails[host] = 0
		delete(p.until, host)
		delete(p.attempts, host)
		p.allTO = 0
		p.okSeen = true
	case VerdictBusy:
		// A reachable node is not a failure; busy just means try later.
		p.fails[host] = 0
		delete(p.until, host)
		p.allTO = 0
		p.okSeen = true
	case VerdictTimeout:
		p.allTO++
		p.fail(host, st.CheckedAt)
	default:
		p.allTO = 0
		p.fail(host, st.CheckedAt)
	}
	p.cache[host] = st
	return st
}

func (p *Preflighter) fail(host string, now time.Time) {
	p.fails[host]++
	p.until[host] = now.Add(BackoffFor(p.fails[host]))
}

// EgressDiagnostic returns a qualified warning after repeated all-timeout
// results with no reachable host seen in this process. It is a hint, not a
// diagnosis: the same pattern also appears when many nodes are down.
func (p *Preflighter) EgressDiagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.allTO >= EgressBlockedAfter && !p.okSeen {
		return fmt.Sprintf("possible outbound network block: %d consecutive hosts timed out and none has answered; "+
			"this may be sandbox egress filtering or a local network problem, but could also be many nodes being down", p.allTO)
	}
	return ""
}

func (p *Preflighter) probe(ctx context.Context, host string) Status {
	st := Status{Host: host}
	started := time.Now()
	var lastErr error
	var body []byte
	for _, scheme := range []string{"https", "http"} {
		cctx, cancel := context.WithTimeout(ctx, p.Timeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, scheme+"://"+host+"/status", nil)
		if err != nil {
			cancel()
			st.Verdict, st.Err = VerdictDead, err.Error()
			return st
		}
		resp, err := p.HTTP.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		var buf [8192]byte
		n, _ := resp.Body.Read(buf[:])
		resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			st.Server = resp.Header.Get("Server")
			continue
		}
		body, st.Server, st.Protocol = buf[:n], resp.Header.Get("Server"), scheme
		lastErr = nil
		break
	}
	if lastErr != nil {
		var ne net.Error
		if errors.As(lastErr, &ne) && ne.Timeout() || errors.Is(lastErr, context.DeadlineExceeded) {
			st.Verdict = VerdictTimeout
		} else {
			st.Verdict = VerdictDead
		}
		st.Err = lastErr.Error()
		return st
	}
	parsed, err := ParseStatus(string(body))
	if err != nil {
		st.Verdict, st.Err = VerdictDead, err.Error()
		return st
	}
	st.Users, st.UsersMax, st.Preempt, st.ExtAPI = parsed.Users, parsed.UsersMax, parsed.Preempt, parsed.ExtAPI
	// The host answered: latency counts for busy hosts too, a full receiver
	// is still a reachable one.
	st.Latency = time.Since(started)
	if st.Users >= st.UsersMax && st.Preempt == 0 {
		st.Verdict = VerdictBusy
		return st
	}
	st.Verdict = VerdictOK
	if !p.ProbeWS {
		return st
	}
	ts := p.Now().Unix()
	var werr error
	for _, path := range WSPaths(st.Server, ts) {
		scheme := "ws"
		if st.Protocol == "https" {
			scheme = "wss"
		}
		uri := scheme + "://" + host + path
		dial := p.Dial
		if dial == nil {
			dial = p.defaultDial
		}
		if werr = dial(ctx, uri); werr == nil {
			if strings.HasPrefix(path, "/ws/kiwi/") {
				st.WSPath = "/ws/kiwi"
			} else {
				st.WSPath = "legacy"
			}
			return st
		}
	}
	st.Verdict, st.Err = VerdictDead, "websocket upgrade failed on both paths: "+werr.Error()
	return st
}

func (p *Preflighter) defaultDial(ctx context.Context, uri string) error {
	d := websocket.Dialer{HandshakeTimeout: p.Timeout}
	if !p.AllowPrivate {
		d.NetDialContext = common.PublicDialContext(p.Timeout)
	}
	ws, _, err := d.DialContext(ctx, uri, nil)
	if err != nil {
		return err
	}
	ws.SetReadLimit(maxFrameBytes)
	pw := p.Password
	if pw != "" {
		pw = MD5Auth(pw)
	}
	_ = ws.SetWriteDeadline(time.Now().Add(p.Timeout))
	err = ws.WriteMessage(websocket.TextMessage, []byte("SET auth t=kiwi p="+pw))
	_ = ws.Close()
	return err
}
