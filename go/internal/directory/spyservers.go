package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// SpyServerDirectoryURL is Airspy's machine-readable public SpyServer
// directory (the same document SDR# and SDRangel browse).
const SpyServerDirectoryURL = "https://airspy.com/directory/status.json"

// SpyServer is one public SpyServer from the directory, projected to the
// fields a client needs to choose a receiver. Owner names and emails from
// the directory payload are deliberately not carried.
type SpyServer struct {
	Host         string  `json:"host"`
	Port         int     `json:"port"`
	DeviceType   string  `json:"device_type"`
	MinFrequency int64   `json:"min_frequency"`
	MaxFrequency int64   `json:"max_frequency"`
	Clients      int     `json:"clients"`
	MaxClients   int     `json:"max_clients"`
	FullControl  bool    `json:"full_control"`
	Latitude     float64 `json:"latitude"`
	Longitude    float64 `json:"longitude"`
	Online       bool    `json:"online"`
}

// spyServerEntry mirrors one element of the directory's "servers" array.
type spyServerEntry struct {
	StreamingHost      string `json:"streamingHost"`
	StreamingPort      int    `json:"streamingPort"`
	DeviceType         string `json:"deviceType"`
	MinimumFrequency   int64  `json:"minimumFrequency"`
	MaximumFrequency   int64  `json:"maximumFrequency"`
	CurrentClientCount int    `json:"currentClientCount"`
	MaxClients         int    `json:"maxClients"`
	FullControlAllowed bool   `json:"fullControlAllowed"`
	AntennaLocation    struct {
		Lat  float64 `json:"lat"`
		Long float64 `json:"long"`
	} `json:"antennaLocation"`
	Online bool `json:"online"`
}

// ParseSpyServers decodes the directory payload and projects the safe
// fields. Offline entries are dropped: the list exists so the user can pick
// a receiver to try, not to archive the directory.
func ParseSpyServers(body string) ([]SpyServer, error) {
	var payload struct {
		Servers []spyServerEntry `json:"servers"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, fmt.Errorf("directory: spyserver payload: %w", err)
	}
	servers := make([]SpyServer, 0, len(payload.Servers))
	for _, e := range payload.Servers {
		if !e.Online || e.StreamingHost == "" || e.StreamingPort == 0 {
			continue
		}
		servers = append(servers, SpyServer{
			Host:         e.StreamingHost,
			Port:         e.StreamingPort,
			DeviceType:   e.DeviceType,
			MinFrequency: e.MinimumFrequency,
			MaxFrequency: e.MaximumFrequency,
			Clients:      e.CurrentClientCount,
			MaxClients:   e.MaxClients,
			FullControl:  e.FullControlAllowed,
			Latitude:     e.AntennaLocation.Lat,
			Longitude:    e.AntennaLocation.Long,
			Online:       e.Online,
		})
	}
	sort.SliceStable(servers, func(i, j int) bool {
		return servers[i].Host < servers[j].Host
	})
	return servers, nil
}

// FilterPublicSpyServers drops entries that do not resolve to at least one
// publicly routable address: a directory entry is untrusted input, and a
// public receiver list must never point the engine at loopback, LAN or
// link-local targets. Entries are directory listings as published; this
// filter says nothing about reachability, which is only known on connect.
func FilterPublicSpyServers(ctx context.Context, servers []SpyServer) []SpyServer {
	out := make([]SpyServer, 0, len(servers))
	for _, s := range servers {
		if ip := net.ParseIP(s.Host); ip != nil {
			if common.IsPublicIP(ip) {
				out = append(out, s)
			}
			continue
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", s.Host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if common.IsPublicIP(ip) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// FetchSpyServers downloads and parses the SpyServer directory, keeping
// only public-address entries, without probing any listed server.
func FetchSpyServers(ctx context.Context, timeout time.Duration) ([]SpyServer, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := fetch(ctx, SpyServerDirectoryURL, timeout)
	if err != nil {
		return nil, err
	}
	servers, err := ParseSpyServers(body)
	if err != nil {
		return nil, err
	}
	return FilterPublicSpyServers(ctx, servers), nil
}
