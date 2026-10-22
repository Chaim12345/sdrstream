package kiwisdr

import (
	"errors"
	"fmt"
)

// Refusals a KiwiSDR announces on the /SND stream as "MSG name=value".
// Callers use errors.Is / errors.As to back off instead of retrying hard.
var (
	// ErrBusy matches a BusyError: every receiver slot is taken.
	ErrBusy = errors.New("kiwisdr: no free receiver slot")
	// ErrBadPassword means the server wants a password we did not supply.
	ErrBadPassword = errors.New("kiwisdr: password refused")
	// ErrDown means the server says it is going down or is disabled.
	ErrDown = errors.New("kiwisdr: server is down")
)

// BusyError is the "MSG too_busy=N" refusal. Slots is the server's channel
// count when it said so. The scanner should skip the host and retry later;
// opening more connections only wastes a shared public receiver.
type BusyError struct {
	Host  string
	Slots int
}

func (e *BusyError) Error() string {
	if e.Slots > 0 {
		return fmt.Sprintf("kiwisdr: %s: all %d slots busy", e.Host, e.Slots)
	}
	return fmt.Sprintf("kiwisdr: %s: all slots busy", e.Host)
}

// Is makes errors.Is(err, ErrBusy) true for any BusyError.
func (e *BusyError) Is(target error) bool { return target == ErrBusy }

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}
