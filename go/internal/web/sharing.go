package web

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/sdrshare"
)

// Shared-receiver gate for the single global monitor. It only DECIDES who
// may start, join, tune or stop; the receiver itself is still opened and
// closed by MonitorHooks. Nothing here grants takeover by default: a
// takeover happens only when the caller sends takeover=1 on that request.

// principal returns the authenticated session ID of the request. With a UI
// password set it re-validates the cookie against the live session table
// (present and unexpired), not just its presence. Without a password there
// is no identity: every caller is the single implicit "open" session, and
// because ServeContext refuses a non-loopback bind without -no-auth, open
// mode is loopback-only unless the operator passed -no-auth. Open mode can
// therefore never produce two distinct sessions, so it can never produce a
// join or a conflict.
func (s *Server) principal(r *http.Request) (string, bool) {
	if os.Getenv("SDRSTREAM_UI_PASSWORD") == "" {
		return sdrshare.LoopbackSession, true
	}
	c, err := r.Cookie("sdrstream_session")
	if err != nil {
		return "", false
	}
	s.auth.mu.Lock()
	expires, ok := s.auth.sessions[c.Value]
	s.auth.mu.Unlock()
	if !ok || time.Now().After(expires) {
		return "", false
	}
	return sdrshare.SessionID(c.Value), true
}

// sharedHub lazily creates the hub; its worker-stop callback closes the
// receiver through the existing Stop hook, so teardown has one path.
func (s *Server) sharedHub() *sdrshare.Hub {
	s.hubOnce.Do(func() {
		s.hub = sdrshare.NewHub()
		s.hub.OnWorkerStop = func(string) {
			s.mu.Lock()
			hooks := s.monitorHooks
			s.mu.Unlock()
			if hooks.Stop != nil {
				_ = hooks.Stop()
			}
		}
	})
	return s.hub
}

// errConflict is written as 409 with the other sessions listed, so the UI
// can offer Join or an explicit Take over. Session IDs are hashes.
func writeShareConflict(w http.ResponseWriter, ce *sdrshare.ConflictError) {
	type other struct {
		ID       string `json:"id"`
		Internal bool   `json:"internal"`
	}
	var others []other
	for _, o := range ce.Others {
		others = append(others, other{o.ID, o.Internal})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeJSON(w, map[string]any{
		"error":             "receiver in use",
		"source":            ce.SourceID,
		"other_sessions":    others,
		"requires_takeover": true,
		"includes_internal": ce.IncludesInternal,
		"hint":              "join it, or repeat the request with takeover=1 to take over",
	})
}

// claimStart reserves the receiver for the caller before the Start hook
// dials anything. It returns false after writing the response.
func (s *Server) claimStart(w http.ResponseWriter, r *http.Request, spec MonitorStartSpec) bool {
	sess, ok := s.principal(r)
	if !ok {
		http.Error(w, "access denied", http.StatusUnauthorized)
		return false
	}
	hub := s.sharedHub()
	takeover := r.FormValue("takeover") == "1"
	key := sdrshare.SourceKey(spec.Kind, spec.Server, spec.Profile)
	// One global monitor: starting anything replaces it, so any other
	// session on any source is a conflict, not only the same receiver.
	for src, members := range hub.Active() {
		var others []sdrshare.SessionInfo
		for _, m := range members {
			if m != sess {
				others = append(others, sdrshare.SessionInfo{ID: m, Internal: sdrshare.IsInternal(m)})
			}
		}
		if len(others) > 0 && !takeover {
			writeShareConflict(w, &sdrshare.ConflictError{SourceID: src, Others: others, RequiresTakeover: true})
			return false
		}
	}
	// Release the caller's own previous seat, then claim.
	hub.Release(sess)
	if err := hub.Start(key, sess, map[string]any{"server": spec.Server, "kind": spec.Kind, "profile": spec.Profile}, takeover); err != nil {
		var ce *sdrshare.ConflictError
		if errors.As(err, &ce) {
			writeShareConflict(w, ce)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return false
	}
	return true
}

// mayControl gates settings changes (tune, thresholds). While the hub is
// empty (a monitor started from the command line) behaviour is unchanged.
// Otherwise only the controller may change the shared receiver.
func (s *Server) mayControl(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := s.principal(r)
	if !ok {
		http.Error(w, "access denied", http.StatusUnauthorized)
		return false
	}
	hub := s.sharedHub()
	if len(hub.Active()) == 0 {
		return true
	}
	if _, role := hub.Role(sess); role != "controller" {
		http.Error(w, "you joined this receiver; start your own or take over to change its settings", http.StatusForbidden)
		return false
	}
	return true
}

// handleMonitorJoin attaches the caller to the running shared receiver. It
// sends no configuration and dials nothing.
func (s *Server) handleMonitorJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOriginPOST(r) {
		http.Error(w, "same-origin POST required", http.StatusForbidden)
		return
	}
	if s.previewRemoteControlDenied(w) {
		return
	}
	sess, ok := s.principal(r)
	if !ok {
		http.Error(w, "access denied", http.StatusUnauthorized)
		return
	}
	hub := s.sharedHub()
	active := hub.Active()
	if len(active) != 1 {
		http.Error(w, "no shared receiver is running", http.StatusNotFound)
		return
	}
	var src string
	for k := range active {
		src = k
	}
	live, err := hub.Join(src, sess)
	switch {
	case errors.Is(err, sdrshare.ErrControllerBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		writeJSON(w, map[string]any{"joined": true, "source": src, "config": live})
	}
}

// handleMonitorLeave detaches the caller; the last one out closes the
// receiver through the Stop hook.
func (s *Server) handleMonitorLeave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOriginPOST(r) {
		http.Error(w, "same-origin POST required", http.StatusForbidden)
		return
	}
	sess, ok := s.principal(r)
	if !ok {
		http.Error(w, "access denied", http.StatusUnauthorized)
		return
	}
	hub := s.sharedHub()
	src, role := hub.Role(sess)
	if role == "" {
		writeJSON(w, map[string]any{"left": false})
		return
	}
	if role == "listener" {
		_ = hub.Leave(src, sess)
	} else {
		hub.Stop(sess)
	}
	writeJSON(w, map[string]any{"left": true})
}
