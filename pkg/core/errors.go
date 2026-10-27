package core

import "errors"

var (
	ErrUnconfirmed  = errors.New("daw did not confirm the value")
	ErrNotAllowed   = errors.New("address not on the allowlist")
	ErrDAWGone      = errors.New("daw is not answering")
	ErrBudget       = errors.New("session token budget exceeded")
	ErrUnknownParam = errors.New("unknown parameter")
	ErrSilent       = errors.New("render is silent")
)

// ErrNeedsConsent means an action needs the user's one-time permission;
// the agent asks for it in the UI instead of failing.
var ErrNeedsConsent = errors.New("needs the user's permission")

// ErrReadOnly means the DAW kept none of a write to a plugin's internal
// state: a read-back showed the old value. Such a plugin does not take
// changes from outside, so the same write is not worth repeating.
var ErrReadOnly = errors.New("plugin state is read-only")

// ReadOnlyError says which plugin (FX index) refused a write, and what came back.
type ReadOnlyError struct {
	FX     int
	Detail string
}

func (e ReadOnlyError) Error() string { return ErrReadOnly.Error() + ": " + e.Detail }
func (e ReadOnlyError) Unwrap() error { return ErrReadOnly }
