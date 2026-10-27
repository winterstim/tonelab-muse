package core

import "context"

// ReloadPolicy is the agent's answer when a plugin kept none of a chunk
// write: nil lets the DAW reopen the project with the state written into
// it; ErrNeedsConsent means the user must allow it first; any other error
// refuses it, and the plugin is then read-only.
type ReloadPolicy func(ctx context.Context, track, fx int, state string) error

type reloadPolicyKey struct{}

// WithReloadPolicy carries the policy to the DAW's write path.
func WithReloadPolicy(ctx context.Context, p ReloadPolicy) context.Context {
	return context.WithValue(ctx, reloadPolicyKey{}, p)
}

// ReloadPolicyFrom returns the policy in ctx, or nil when there is none.
func ReloadPolicyFrom(ctx context.Context) ReloadPolicy {
	p, _ := ctx.Value(reloadPolicyKey{}).(ReloadPolicy)
	return p
}

// ProgramChanger sends a MIDI program change to a track's plugins (the
// plugin switches to its program). Optional.
type ProgramChanger interface {
	ProgramChange(ctx context.Context, track, channel, program int) error
}

// ProgramNamer names the programs a plugin answers to, by program number.
// It returns nil when the plugin's own list is not readable. Optional.
type ProgramNamer interface {
	ProgramNames(ctx context.Context, track, fx int) (map[int]string, error)
}
