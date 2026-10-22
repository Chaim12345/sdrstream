package feeds

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// readIdleTimeout bounds how long an APRS-IS connection may stay silent
// before it is treated as wedged. javAPRSSrvr sends keepalive comment
// lines every ~20 s, so 3 minutes is generous.
const readIdleTimeout = 3 * time.Minute

// APRSIS connects to an APRS-IS server with a read-only login (passcode -1)
// and a range filter, so only traffic near the configured point arrives.
// No key or account is needed for read-only access.
type APRSIS struct {
	Server  string  // default rotate.aprs2.net:14580
	Lat     float64 // filter centre, degrees
	Lon     float64
	Radius  float64 // kilometres
	Version string  // client version string sent at login
	Dial    func(ctx context.Context, addr string) (net.Conn, error)
	Now     func() time.Time
}

func (a *APRSIS) Name() string { return "aprs-is" }
func (a *APRSIS) Kind() string { return "aprs" }

func (a *APRSIS) server() string {
	if a.Server != "" {
		return a.Server
	}
	return "rotate.aprs2.net:14580"
}

func (a *APRSIS) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run logs in read-only and streams parsed packets until the connection
// drops or ctx ends.
func (a *APRSIS) Run(ctx context.Context, emit func(Item)) error {
	return a.RunReady(ctx, emit, func() {})
}
func (a *APRSIS) RunReady(ctx context.Context, emit func(Item), ready func()) error {
	dial := a.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: 20 * time.Second}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	conn, err := dial(ctx, a.server())
	if err != nil {
		return err
	}
	defer conn.Close()
	// Per-attempt context: the closer goroutine must end with this
	// connection, not leak until the supervisor's ctx ends.
	attemptCtx, stopAttempt := context.WithCancel(ctx)
	defer stopAttempt()
	go func() { <-attemptCtx.Done(); conn.Close() }()

	login := fmt.Sprintf("user NOCALL pass -1 vers sdrstream 1.0 filter r/%.4f/%.4f/%.0f\r\n", a.Lat, a.Lon, a.Radius)
	if _, err := conn.Write([]byte(login)); err != nil {
		return err
	}
	// APRS-IS sends a server greeting after login; consuming it also proves the
	// subscription is accepted before reporting the source live.
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(a.now().Add(readIdleTimeout))
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(line), "#") {
		return fmt.Errorf("unexpected APRS-IS greeting")
	}
	ready()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 256<<10)
	for {
		// Refresh the read deadline per line: a server that goes silent
		// mid-stream must not park the feed forever.
		_ = conn.SetReadDeadline(a.now().Add(readIdleTimeout))
		if !scanner.Scan() {
			break
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if it, ok := ParseAPRSLine(line, a.now()); ok {
			emit(it)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("server closed the connection")
}

var aprsHeader = regexp.MustCompile(`^([A-Za-z0-9-]{3,9})>([A-Za-z0-9,* -]+):(.{1,200})`)

// ParseAPRSLine turns one APRS-IS text line into an Item. Position packets
// get coordinates; other packets are kept with a short text summary.
func ParseAPRSLine(line string, when time.Time) (Item, bool) {
	m := aprsHeader.FindStringSubmatch(line)
	if m == nil {
		return Item{}, false
	}
	from, payload := m[1], m[3]
	it := Item{
		ID:    "aprs|" + strings.ToUpper(from),
		Label: strings.ToUpper(from),
		Time:  when,
		Ref:   "https://aprs.fi/?call=" + strings.ToUpper(from),
	}
	if lat, lon, sym, ok := parseAPRSPosition(payload); ok {
		it.Lat, it.Lon, it.HasPos = lat, lon, true
		it.Detail = "symbol " + sym + aprsComment(payload)
	} else {
		it.Detail = summarizeAPRS(payload)
	}
	if it.Detail == "" {
		it.Detail = "packet via APRS-IS"
	}
	return it, true
}

// parseAPRSPosition handles plain uncompressed positions:
// !DDMM.hhN/DDDMM.hhE$ or =, with an optional leading timestamp for / and @.
func parseAPRSPosition(payload string) (lat, lon float64, symbol string, ok bool) {
	if len(payload) < 19 {
		return 0, 0, "", false
	}
	body := payload
	switch body[0] {
	case '!', '=':
		body = body[1:]
	case '/', '@':
		if len(body) < 8 {
			return 0, 0, "", false
		}
		body = body[8:] // 1 type byte + 7 timestamp chars
	default:
		return 0, 0, "", false
	}
	if len(body) < 19 {
		return 0, 0, "", false
	}
	lat, ok = parseAPRSCoord(body[0:8], true)
	if !ok {
		return 0, 0, "", false
	}
	symTable := string(body[8])
	lon, ok = parseAPRSCoord(body[9:18], false)
	if !ok {
		return 0, 0, "", false
	}
	if len(body) > 18 {
		return lat, lon, symTable + string(body[18]), true
	}
	return lat, lon, symTable, true
}

// parseAPRSCoord parses DDMM.hhN (lat) or DDDMM.hhE (lon).
func parseAPRSCoord(field string, isLat bool) (float64, bool) {
	degLen := 2
	if !isLat {
		degLen = 3
	}
	if len(field) != degLen+5+1 {
		return 0, false
	}
	deg, err := strconv.Atoi(field[:degLen])
	if err != nil {
		return 0, false
	}
	min, err := strconv.ParseFloat(field[degLen:degLen+5], 64)
	if err != nil {
		return 0, false
	}
	value := float64(deg) + min/60
	switch field[len(field)-1] {
	case 'S', 'W':
		value = -value
	case 'N', 'E':
	default:
		return 0, false
	}
	return value, true
}

// aprsComment extracts free text after the 19-char position/symbol block.
func aprsComment(payload string) string {
	body := payload
	if len(body) == 0 {
		return ""
	}
	switch body[0] {
	case '/', '@':
		body = body[8:]
	default:
		body = body[1:]
	}
	if len(body) <= 19 {
		return ""
	}
	comment := strings.TrimSpace(body[19:])
	if comment == "" {
		return ""
	}
	return " · " + truncate(comment, 80)
}

func summarizeAPRS(payload string) string {
	if payload == "" {
		return ""
	}
	kind := "data"
	switch payload[0] {
	case '>':
		kind = "status"
		payload = payload[1:]
	case ':':
		kind = "message"
		payload = payload[10:]
	case ';':
		kind = "object"
	case '_':
		kind = "weather"
	case 'T':
		return "" // telemetry, not interesting as a list entry
	}
	return kind + ": " + truncate(strings.TrimSpace(payload), 80)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
