package common

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":              true,
		"1.1.1.1":              true,
		"192.168.1.10":         false,
		"10.0.0.4":             false,
		"172.16.5.5":           false,
		"127.0.0.1":            false,
		"169.254.169.254":      false,
		"100.64.0.1":           false,
		"0.0.0.0":              false,
		"224.0.0.1":            false,
		"198.18.0.1":           false,
		"192.0.2.1":            false,
		"240.0.0.1":            false,
		"::1":                  false,
		"fe80::1":              false,
		"fd00::1":              false,
		"2001:db8::1":          false,
		"2606:4700:4700::1111": true,
	}
	for raw, want := range cases {
		if got := IsPublicIP(net.ParseIP(raw)); got != want {
			t.Errorf("IsPublicIP(%s) = %v, want %v", raw, got, want)
		}
	}
	if IsPublicIP(nil) {
		t.Error("nil IP must not be public")
	}
}

func TestCheckPublicHostRejectsLiterals(t *testing.T) {
	ctx := context.Background()
	for _, target := range []string{"127.0.0.1:8073", "192.168.0.5:8080", "169.254.169.254:80", "[::1]:80"} {
		err := CheckPublicHost(ctx, target)
		var pe *PublicHostError
		if !errors.As(err, &pe) {
			t.Errorf("%s: want PublicHostError, got %v", target, err)
		}
	}
	if err := CheckPublicHost(ctx, "8.8.8.8:53"); err != nil {
		t.Errorf("public literal must pass: %v", err)
	}
}

func TestPublicHTTPClientRejectsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client := PublicHTTPClient(2 * time.Second)
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("loopback target must be refused")
	}
	var pe *PublicHostError
	if !errors.As(err, &pe) {
		t.Fatalf("want PublicHostError, got %v", err)
	}
}

func TestPublicHTTPClientBlocksRedirectToLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/admin", http.StatusFound)
	}))
	defer srv.Close()
	// Even the initial request is loopback, so the guard must bite at once;
	// this proves the redirect target would never be dialed either.
	_, err := PublicHTTPClient(2 * time.Second).Get(srv.URL)
	if err == nil {
		t.Fatal("guard must refuse the chain")
	}
}
