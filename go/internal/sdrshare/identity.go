package sdrshare

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// LoopbackSession is the single implicit session for an open (no-password)
// UI. Every visitor of an open UI shares it, so open mode has one controller
// and no joiner distinction. That matches today's behaviour: no auth means
// no identity to separate.
const LoopbackSession = "open"

// SessionID derives a Hub session ID from the sdrstream_session cookie. The
// raw token is a bearer credential, so only a truncated SHA-256 of it is
// used as an ID: it can be logged and sent to the browser without leaking
// the cookie. Empty cookie (unauthenticated) returns "" and callers must
// refuse to join/take over. IDs never start with InternalPrefix, so a
// browser cannot claim a scheduler session.
func SessionID(cookieValue string) string {
	if cookieValue == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cookieValue))
	return "u:" + hex.EncodeToString(sum[:8])
}

// RequestSession returns the session ID for a request. authEnabled is
// whether SDRSTREAM_UI_PASSWORD is set. With auth on and no cookie it
// returns "" (deny). With auth off it returns LoopbackSession.
func RequestSession(r *http.Request, authEnabled bool) string {
	if !authEnabled {
		return LoopbackSession
	}
	c, err := r.Cookie("sdrstream_session")
	if err != nil {
		return ""
	}
	return SessionID(c.Value)
}

// SourceKey normalises a receiver spec into the Hub source ID so two users
// pointing at the same receiver share one IQ source regardless of case or
// a trailing slash.
func SourceKey(kind, server, profile string) string {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	return norm(kind) + "|" + norm(server) + "|" + strings.TrimSpace(profile)
}
