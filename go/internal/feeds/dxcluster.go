package feeds

// DX cluster telnet feed (default dxc.ve7cc.net:23, CC Cluster). DX
// clusters are the original amateur-radio spotting network: operators and
// skimmers post who is on which frequency right now. The feed is public;
// CC Cluster nodes accept read-only guest logins (the software prints a
// reminder to use your own callsign and then carries on); we never send
// spot, announcement or SET commands, we only read the spot stream. Nodes
// whose banner requires a licensed callsign (for example AR-Cluster) are
// deliberately not the default.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

type DXCluster struct {
	Server string // default dxc.ve7cc.net:23
	Login  string // greeting callsign, default NOCALL
	Dial   func(ctx context.Context, addr string) (net.Conn, error)
	Now    func() time.Time
}

func (d *DXCluster) Name() string { return "dxcluster" }
func (d *DXCluster) Kind() string { return "dx" }

func (d *DXCluster) server() string {
	if d.Server != "" {
		return d.Server
	}
	return "dxc.ve7cc.net:23"
}

func (d *DXCluster) login() string {
	if d.Login != "" {
		return d.Login
	}
	return "N0CALL"
}

func (d *DXCluster) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// DX de W3LPL:     14074.0  K1ABC       FT8 -11 dB                0658Z
var dxLine = regexp.MustCompile(`^DX de ([A-Za-z0-9/._-]+):\s+([0-9]+(?:\.[0-9]+)?)\s+([A-Za-z0-9/._-]+)\s*(.*?)\s*([0-2][0-9][0-5][0-9])Z\s*$`)

// ParseDXLine parses one cluster spot line. ok is false for banners,
// prompts and talk messages.
func ParseDXLine(line string) (spotter, dxcall string, freqKHz float64, comment string, ok bool) {
	m := dxLine.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
	if m == nil {
		return "", "", 0, "", false
	}
	var f float64
	if _, err := fmt.Sscanf(m[2], "%g", &f); err != nil || f <= 0 {
		return "", "", 0, "", false
	}
	return strings.ToUpper(m[1]), strings.ToUpper(m[3]), f, strings.TrimSpace(m[4]), true
}

// Run connects, logs in read-only and streams parsed spots until the
// connection drops or ctx ends.
func (d *DXCluster) Run(ctx context.Context, emit func(Item)) error {
	return d.RunReady(ctx, emit, func() {})
}

func (d *DXCluster) RunReady(ctx context.Context, emit func(Item), ready func()) error {
	dial := d.Dial
	if dial == nil {
		nd := &net.Dialer{Timeout: 20 * time.Second}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return nd.DialContext(ctx, "tcp", addr) }
	}
	conn, err := dial(ctx, d.server())
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()

	if err := conn.SetDeadline(d.now().Add(90 * time.Second)); err != nil {
		return err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	// The node asks for a callsign before anything else; answer once and
	// then only read. No set/ or spot commands are ever sent.
	sentLogin := false
	for sc.Scan() {
		line := sc.Text()
		if !sentLogin && loginPrompt(line) {
			if _, err := fmt.Fprintf(conn, "%s\n", d.login()); err != nil {
				return err
			}
			sentLogin = true
			ready()
			continue
		}
		spotter, dxcall, freq, comment, ok := ParseDXLine(line)
		if !ok {
			continue
		}
		emit(Item{
			ID:     fmt.Sprintf("dxcluster:%s:%s:%.1f", spotter, dxcall, freq),
			Source: "dxcluster",
			Kind:   "dx",
			Time:   d.now(),
			Label:  fmt.Sprintf("%s %.3f MHz", dxcall, freq/1000),
			Detail: fmt.Sprintf("spotted by %s %s", spotter, comment),
		})
		if err := conn.SetDeadline(d.now().Add(90 * time.Second)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("dxcluster: connection closed")
}

func loginPrompt(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "call") && (strings.Contains(l, "enter") || strings.Contains(l, "login") || strings.HasSuffix(strings.TrimSpace(l), ":"))
}
