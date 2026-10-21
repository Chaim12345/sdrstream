package feeds

// Reverse Beacon Network DX-cluster feed (telnet.reversebeacon.net:7000).
// Skimmers worldwide report who they heard on CW/RTTY/FT8; the stream is
// public and needs only any callsign-style greeting. Spots double as an
// independent cross-check for our digimode decodes.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

type RBN struct {
	Server string // default telnet.reversebeacon.net:7000
	Login  string // greeting callsign, default NOCALL
	Dial   func(ctx context.Context, addr string) (net.Conn, error)
	Now    func() time.Time
}

func (r *RBN) Name() string { return "rbn" }
func (r *RBN) Kind() string { return "beacon" }

func (r *RBN) server() string {
	if r.Server != "" {
		return r.Server
	}
	return "telnet.reversebeacon.net:7000"
}

func (r *RBN) login() string {
	if r.Login != "" {
		return r.Login
	}
	return "NOCALL"
}

func (r *RBN) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RBN) Run(ctx context.Context, emit func(Item)) error {
	dial := r.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: 20 * time.Second}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	conn, err := dial(ctx, r.server())
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	reader := bufio.NewReader(conn)
	if err := rbnExpect(reader, "call:", 20*time.Second, conn); err != nil {
		return err
	}
	if _, err := conn.Write([]byte(r.login() + "\r\n")); err != nil {
		return err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 16<<10), 64<<10)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if it, ok := ParseRBNSpot(line, r.now()); ok {
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

func rbnExpect(reader *bufio.Reader, want string, timeout time.Duration, conn net.Conn) error {
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})
	var b strings.Builder
	for !strings.Contains(b.String(), want) {
		c, err := reader.ReadByte()
		if err != nil {
			return err
		}
		b.WriteByte(c)
		if b.Len() > 4096 {
			return fmt.Errorf("rbn: greeting too long, no %q", want)
		}
	}
	return nil
}

var rbnLine = regexp.MustCompile(`^DX de (\S+?):\s+([0-9.]+)\s+(\S+)\s+(.*?)\s+(\d{2}:?\d{2})?Z?\s*$`)

// ParseRBNSpot parses lines like:
// DX de WZ7I-#:     14074.0  K1ABC       CW    17 dB  25 WPM CQ      2001Z
func ParseRBNSpot(line string, when time.Time) (Item, bool) {
	m := rbnLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Item{}, false
	}
	skimmer, freq, spotted, rest := m[1], m[2], strings.ToUpper(m[3]), strings.TrimSpace(m[4])
	return Item{
		ID:     fmt.Sprintf("rbn|%s|%s|%s|%d", spotted, skimmer, freq, when.Unix()/60),
		Label:  spotted,
		Detail: fmt.Sprintf("%s kHz %s via %s", freq, rest, skimmer),
		Time:   when,
	}, true
}
