// Package sdrshare models shared access to one IQ source: one controller
// owns the settings, any number of joiners listen to the same stream, and
// every session has its own small bank of VFOs. It is a port of the session
// semantics in sgoudelis/ground-station (backend/session, backend/vfos and
// handlers/entities/sdr.py at 351631d). It holds state only: it does not
// open receivers, touch IQ, or grant anyone access. Callers decide who may
// call it (existing web auth), so joining never creates a new permission.
package sdrshare

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const (
	// UserVFOs and InternalVFOs match upstream USER_VFO_NUMBER and
	// INTERNAL_VFO_NUMBER.
	UserVFOs     = 4
	InternalVFOs = 10
	// InternalPrefix marks scheduler-owned sessions (upstream "internal:").
	InternalPrefix = "internal:"
)

var (
	ErrNotStreaming   = errors.New("source is not streaming")
	ErrControllerBusy = errors.New("stop your stream before joining it")
	ErrJoinerSettings = errors.New("you joined this source; choose take over to change its settings")
	ErrNoJoin         = errors.New("no active join for this source")
	ErrBadVFO         = errors.New("vfo number out of range")
	ErrUnknownSession = errors.New("unknown session")
)

// ConflictError is returned when a controller change needs explicit takeover.
type ConflictError struct {
	SourceID         string
	Others           []SessionInfo
	IncludesInternal bool
	RequiresTakeover bool
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("source %q is in use by %d other session(s); join it or take over", e.SourceID, len(e.Others))
}

// SessionInfo describes another session on a source.
type SessionInfo struct {
	ID       string
	Internal bool
}

// VFO is one demodulator slot of a session.
type VFO struct {
	Number    int
	CenterHz  int64
	Bandwidth int
	Mode      string
	Active    bool
	Selected  bool
	Squelch   int
}

// VFOUpdate holds optional changes; nil keeps the current value.
type VFOUpdate struct {
	CenterHz  *int64
	Bandwidth *int
	Mode      *string
	Active    *bool
	Selected  *bool
	Squelch   *int
}

// IsInternal reports whether the session ID is scheduler-owned.
func IsInternal(id string) bool { return strings.HasPrefix(id, InternalPrefix) }

func vfoLimit(id string) int {
	if IsInternal(id) {
		return InternalVFOs
	}
	return UserVFOs
}

type source struct {
	controller string
	joiners    map[string]bool
	config     map[string]any
}

// Hub tracks sources and sessions. Safe for concurrent use.
type Hub struct {
	mu      sync.Mutex
	sources map[string]*source
	vfos    map[string]map[int]*VFO
	// OnWorkerStop is called (without the lock) when the last session leaves.
	OnWorkerStop func(sourceID string)
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{sources: map[string]*source{}, vfos: map[string]map[int]*VFO{}}
}

func (s *source) members() []string {
	out := make([]string, 0, len(s.joiners)+1)
	if s.controller != "" {
		out = append(out, s.controller)
	}
	for j := range s.joiners {
		out = append(out, j)
	}
	sort.Strings(out)
	return out
}

func (h *Hub) others(s *source, id string) []SessionInfo {
	var o []SessionInfo
	for _, m := range s.members() {
		if m != id {
			o = append(o, SessionInfo{ID: m, Internal: IsInternal(m)})
		}
	}
	return o
}

func conflict(id string, o []SessionInfo) *ConflictError {
	c := &ConflictError{SourceID: id, Others: o, RequiresTakeover: true}
	for _, s := range o {
		c.IncludesInternal = c.IncludesInternal || s.Internal
	}
	return c
}

// Start makes session the controller of source with config. If anyone else
// is on the source (or the caller is a joiner) it fails with *ConflictError
// unless takeover is true. A takeover keeps joiners attached, replaces the
// controller and publishes the new config (joiners never send settings).
func (h *Hub) Start(sourceID, session string, config map[string]any, takeover bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sources[sourceID]
	if s == nil {
		h.sources[sourceID] = &source{controller: session, joiners: map[string]bool{}, config: clone(config)}
		h.ensureVFOs(session)
		return nil
	}
	o := h.others(s, session)
	if (len(o) > 0 || s.joiners[session]) && !takeover {
		return conflict(sourceID, o)
	}
	// A displaced controller stays attached as a listener: takeover changes
	// who owns settings, it does not disconnect anyone. (Upstream's exact
	// handling of the displaced controller is not verified; this is the
	// conservative choice.)
	if s.controller != "" && s.controller != session {
		s.joiners[s.controller] = true
	}
	delete(s.joiners, session)
	s.controller = session
	s.config = clone(config)
	h.ensureVFOs(session)
	return nil
}

// Join attaches session as a listener of a running source and returns the
// live config. A controller of the same source cannot join it.
func (h *Hub) Join(sourceID, session string) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sources[sourceID]
	if s == nil {
		return nil, ErrNotStreaming
	}
	if s.controller == session {
		return nil, ErrControllerBusy
	}
	for id, other := range h.sources {
		if id != sourceID && other.joiners[session] {
			delete(other.joiners, session) // one joined source at a time
			h.dropIfEmptyLocked(id)
		}
	}
	s.joiners[session] = true
	h.ensureVFOs(session)
	return clone(s.config), nil
}

// Leave detaches a joiner. The worker stops only when nobody is left.
func (h *Hub) Leave(sourceID, session string) error {
	h.mu.Lock()
	s := h.sources[sourceID]
	if s == nil || !s.joiners[session] {
		h.mu.Unlock()
		return ErrNoJoin
	}
	delete(s.joiners, session)
	delete(h.vfos, session)
	stopped := h.dropIfEmptyLocked(sourceID)
	cb := h.OnWorkerStop
	h.mu.Unlock()
	if stopped && cb != nil {
		cb(sourceID)
	}
	return nil
}

// Stop removes a session wherever it is. If the controller leaves while
// joiners remain, the oldest-sorted joiner is not promoted: the source keeps
// its last config and has no controller until someone takes over, so a
// departure never hands settings to a listener.
func (h *Hub) Stop(session string) { h.stop(session, true) }

// Release is Stop without the OnWorkerStop callback, for a caller that is
// about to replace the worker itself (restart) and must not close it twice.
func (h *Hub) Release(session string) { h.stop(session, false) }

func (h *Hub) stop(session string, notify bool) {
	h.mu.Lock()
	var stopped []string
	for id, s := range h.sources {
		if s.controller == session {
			s.controller = ""
		}
		delete(s.joiners, session)
		if h.dropIfEmptyLocked(id) {
			stopped = append(stopped, id)
		}
	}
	delete(h.vfos, session)
	cb := h.OnWorkerStop
	if !notify {
		cb = nil
	}
	h.mu.Unlock()
	for _, id := range stopped {
		if cb != nil {
			cb(id)
		}
	}
}

func (h *Hub) dropIfEmptyLocked(id string) bool {
	s := h.sources[id]
	if s != nil && s.controller == "" && len(s.joiners) == 0 {
		delete(h.sources, id)
		return true
	}
	return false
}

// SetConfig changes source settings. Only the controller may; a joiner gets
// ErrJoinerSettings unless takeover is set (then it becomes controller).
func (h *Hub) SetConfig(sourceID, session string, patch map[string]any, takeover bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sources[sourceID]
	if s == nil {
		return ErrNotStreaming
	}
	switch {
	case s.controller == session:
	case s.joiners[session] && !takeover:
		return ErrJoinerSettings
	case !s.joiners[session] && s.controller != "" && !takeover:
		return conflict(sourceID, h.others(s, session))
	default:
		if s.controller != "" {
			s.joiners[s.controller] = true
		}
		delete(s.joiners, session)
		s.controller = session
	}
	for k, v := range patch {
		s.config[k] = v
	}
	return nil
}

// Config returns a copy of the live config.
func (h *Hub) Config(sourceID string) (map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sources[sourceID]
	if s == nil {
		return nil, false
	}
	return clone(s.config), true
}

// Inspect reports the others on a source without changing anything.
func (h *Hub) Inspect(sourceID, session string) (active bool, others []SessionInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sources[sourceID]
	if s == nil {
		return false, nil
	}
	return true, h.others(s, session)
}

// Members lists every session on a source (worker lifetime refcount).
func (h *Hub) Members(sourceID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sources[sourceID]; s != nil {
		return s.members()
	}
	return nil
}

func (h *Hub) ensureVFOs(session string) {
	if h.vfos[session] != nil {
		return
	}
	m := map[int]*VFO{}
	for i := 1; i <= vfoLimit(session); i++ {
		m[i] = &VFO{Number: i, Bandwidth: 10000, Mode: "FM", Squelch: -150}
	}
	h.vfos[session] = m
}

// UpdateVFO changes one VFO in the caller's own bank. VFO 0 with Selected
// set deselects all, as upstream does. Other sessions' VFOs are never
// reachable, so joiners tune independently of the controller.
func (h *Hub) UpdateVFO(session string, n int, u VFOUpdate) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	bank := h.vfos[session]
	if bank == nil {
		return ErrUnknownSession
	}
	if n == 0 && u.Selected != nil {
		for _, v := range bank {
			v.Selected = false
		}
		return nil
	}
	v := bank[n]
	if v == nil {
		return ErrBadVFO
	}
	if u.CenterHz != nil {
		v.CenterHz = *u.CenterHz
	}
	if u.Bandwidth != nil && *u.Bandwidth > 0 {
		v.Bandwidth = *u.Bandwidth
	}
	if u.Mode != nil && *u.Mode != "" {
		v.Mode = strings.ToUpper(*u.Mode)
	}
	if u.Active != nil {
		v.Active = *u.Active
	}
	if u.Squelch != nil {
		v.Squelch = *u.Squelch
	}
	if u.Selected != nil {
		if *u.Selected {
			for _, o := range bank {
				o.Selected = false
			}
		}
		v.Selected = *u.Selected
	}
	return nil
}

// VFOs returns a sorted copy of a session's bank.
func (h *Hub) VFOs(session string) []VFO {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []VFO
	for _, v := range h.vfos[session] {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func clone(m map[string]any) map[string]any {
	o := make(map[string]any, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

// Role reports where a session sits: the source it is on and whether it is
// the "controller" or a "listener". Empty source means it is on none.
func (h *Hub) Role(session string) (sourceID, role string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.sources {
		if s.controller == session {
			return id, "controller"
		}
		if s.joiners[session] {
			return id, "listener"
		}
	}
	return "", ""
}

// Active returns every source with its members, for callers that run a
// single global receiver and must not replace it under other sessions.
func (h *Hub) Active() map[string][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string][]string{}
	for id, s := range h.sources {
		out[id] = s.members()
	}
	return out
}
