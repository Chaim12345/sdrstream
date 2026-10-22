package common

// SSRF guards for outbound connections to receivers found in public
// directories. A directory entry is untrusted input: it can point at
// loopback, link-local (cloud metadata), or private-network addresses, and
// an HTTP endpoint can redirect there too. These helpers refuse such
// targets and pin the dial to the address that was validated, so a DNS
// answer cannot be swapped between the check and the connect.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// IsPublicIP reports whether ip is a globally routable unicast address.
// Loopback, private (RFC 1918 / ULA), link-local, multicast, CGNAT,
// benchmarking, documentation and reserved ranges are all rejected.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // "this host" 0.0.0.0/8
			return false
		case ip4[0] == 100 && ip4[1]&0xC0 == 64: // CGNAT 100.64.0.0/10
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // IETF protocol assignments
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 2: // TEST-NET-1
			return false
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19): // benchmarking
			return false
		case ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100: // TEST-NET-2
			return false
		case ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113: // TEST-NET-3
			return false
		case ip4[0] >= 240: // reserved / broadcast
			return false
		}
		return true
	}
	if len(ip) == net.IPv6len && ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
		return false // documentation 2001:db8::/32
	}
	return true
}

// PublicHostError reports a dial target that failed the SSRF guard.
type PublicHostError struct{ Host string }

func (e *PublicHostError) Error() string {
	return "common: refusing non-public dial target " + e.Host
}

// CheckPublicHost resolves host (with or without a port) and fails unless
// every resolved address is publicly routable.
func CheckPublicHost(ctx context.Context, hostport string) error {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "" {
		return &PublicHostError{Host: hostport}
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("common: %q resolves to no addresses", host)
	}
	for _, ip := range ips {
		if !IsPublicIP(ip) {
			return &PublicHostError{Host: host}
		}
	}
	return nil
}

// DialPublic validates addr's host, then dials the first publicly routable
// resolved address directly, so the connection goes to the address that was
// checked rather than to whatever a second resolution might return.
func DialPublic(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, ip := range ips {
		if !IsPublicIP(ip) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
	}
	if dialErr != nil {
		return nil, dialErr
	}
	return nil, &PublicHostError{Host: host}
}

// PublicDialContext returns a DialContext-compatible function guarded by
// DialPublic, suitable for http.Transport and websocket.Dialer.
func PublicDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return DialPublic(ctx, dialer, network, addr)
	}
}

// PublicHTTPClient builds an HTTP client that only dials publicly routable
// addresses. Redirects are re-validated automatically because each redirect
// target is dialed through the guarded transport; CheckRedirect additionally
// restricts the scheme to http/https and caps the chain.
func PublicHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:       http.ProxyFromEnvironment,
			DialContext: PublicDialContext(timeout),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("common: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("common: refusing redirect to scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
}
