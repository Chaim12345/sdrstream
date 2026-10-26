package ubersdr

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"time"
)

// DirectoryURL is the public instance list.
const DirectoryURL = "https://instances.ubersdr.org/api/instances?online_only=true"

// Instance is a receiver the operator might choose.
type Instance struct {
	Name     string
	Callsign string
	Location string
	Host     string
	Port     int
	TLS      bool
	Version  string

	// PublicIQModes is the wide IQ modes anyone may use, in rate order. A
	// password may unlock more; this is only what is open without one.
	PublicIQModes []string

	// Available and MaxClients are free and total listener slots; -1 unknown.
	Available  int
	MaxClients int

	// Local is true for a receiver found on the LAN rather than the directory.
	Local bool
}

// URL is the base URL to connect to.
func (i Instance) URL() string {
	scheme := "http"
	if i.TLS {
		scheme = "https"
	}
	host := i.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if i.Port == 0 || (i.TLS && i.Port == 443) || (!i.TLS && i.Port == 80) {
		return scheme + "://" + host
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, i.Port)
}

// Label is the one-line name for a list: the callsign first, since that is
// what an operator searches by.
func (i Instance) Label() string {
	name := i.Name
	if name == "" {
		name = i.Host
	}
	if i.Callsign != "" && !strings.Contains(strings.ToUpper(name), strings.ToUpper(i.Callsign)) {
		return i.Callsign + " · " + name
	}
	return name
}

// Matches is a case-insensitive filter over the fields an operator would type.
func (i Instance) Matches(filter string) bool {
	f := strings.ToLower(strings.TrimSpace(filter))
	if f == "" {
		return true
	}
	for _, field := range []string{i.Name, i.Callsign, i.Location, i.Host} {
		if strings.Contains(strings.ToLower(field), f) {
			return true
		}
	}
	return false
}

// FetchDirectory reads the public instance list. Only receivers that publish
// at least one rate the bridge can carry are kept, sorted by callsign (or name)
// case-insensitively, as the C bridge's --discover listed them.
func FetchDirectory(ctx context.Context, directoryURL string) ([]Instance, error) {
	if directoryURL == "" {
		directoryURL = DirectoryURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directoryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("directory returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return parseDirectory(raw)
}

func parseDirectory(raw []byte) ([]Instance, error) {
	var payload struct {
		Instances []struct {
			Name          string   `json:"name"`
			Callsign      string   `json:"callsign"`
			Location      string   `json:"location"`
			Host          string   `json:"host"`
			Port          int      `json:"port"`
			TLS           bool     `json:"tls"`
			Version       string   `json:"version"`
			MaxClients    int      `json:"max_clients"`
			Available     *int     `json:"available_clients"`
			PublicIQModes []string `json:"public_iq_modes"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("bad directory response: %w", err)
	}
	out := []Instance{}
	for _, in := range payload.Instances {
		modes := filterWideModes(in.PublicIQModes)
		if in.Host == "" || in.Port == 0 || len(modes) == 0 {
			continue
		}
		inst := Instance{
			Name: in.Name, Callsign: in.Callsign, Location: in.Location,
			Host: in.Host, Port: in.Port, TLS: in.TLS, Version: in.Version,
			PublicIQModes: modes, Available: -1, MaxClients: in.MaxClients,
		}
		if in.Available != nil {
			inst.Available = *in.Available
		}
		out = append(out, inst)
	}
	sortInstances(out)
	return out, nil
}

func sortInstances(list []Instance) {
	key := func(i Instance) string {
		if i.Callsign != "" {
			return strings.ToLower(i.Callsign)
		}
		return strings.ToLower(i.Name)
	}
	sort.SliceStable(list, func(a, b int) bool { return key(list[a]) < key(list[b]) })
}

// FindCallsign returns the instance with a callsign, case-insensitively.
func FindCallsign(list []Instance, callsign string) (Instance, bool) {
	for _, i := range list {
		if strings.EqualFold(i.Callsign, strings.TrimSpace(callsign)) {
			return i, true
		}
	}
	return Instance{}, false
}
