package agent

import (
	"errors"
	"fmt"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// A plugin is read-only once one of its writes was read back and found
// unchanged: the plugin keeps its own state and rewrites anything sent from
// outside. The agent remembers that for the session, so it does not write
// the same plugin again and again.

func readOnlyMessage(plugin string) string {
	return plugin + " does not accept changes from outside to its internal controls. Stop trying to change this plugin in this request and finish with what worked. " +
		"Plan with its host parameters or other plugins, or ask the user to assign the controls they want " +
		"to the plugin's host parameter slots (Param 1–16): open the plugin, find its MIDI/automation or " +
		"host-parameter settings, assign Gain, Bass, Middle, Treble, Presence, Master; then learn again."
}

// markReadOnly records a read-only plugin for this process and, when the
// store is set, in its profile so the next process knows it at once.
func (a *Agent) markReadOnly(plugin string) {
	if plugin == "" {
		return
	}
	a.mu.Lock()
	if a.readOnly == nil {
		a.readOnly = map[string]bool{}
	}
	a.readOnly[plugin] = true
	a.mu.Unlock()
	if a.d.Store == nil {
		return
	}
	p, err := a.d.Store.Profile(plugin)
	if err != nil {
		return
	}
	if p == nil {
		p = &core.Profile{Plugin: plugin}
	}
	if p.ReadOnly {
		return
	}
	p.ReadOnly = true
	_ = a.d.Store.SaveProfile(*p)
}

// isReadOnly is true when the plugin is read-only in this process or in its
// stored profile (learned in an earlier run).
func (a *Agent) isReadOnly(plugin string) bool {
	if plugin == "" {
		return false
	}
	a.mu.Lock()
	known := a.readOnly[plugin]
	a.mu.Unlock()
	if known {
		return true
	}
	if a.d.Store == nil {
		return false
	}
	p, err := a.d.Store.Profile(plugin)
	if err != nil || p == nil || !p.ReadOnly {
		return false
	}
	a.mu.Lock()
	if a.readOnly == nil {
		a.readOnly = map[string]bool{}
	}
	a.readOnly[plugin] = true
	a.mu.Unlock()
	return true
}

// fxName is the plugin name at FX index fx in ch.
func fxName(ch []core.FX, fx int) string {
	for _, f := range ch {
		if f.Index == fx {
			return f.Name
		}
	}
	return ""
}

// readOnlyFailure turns a write error into the read-only message when the
// DAW says the plugin kept none of the write. It marks the plugin; other
// errors come back unchanged.
func (a *Agent) readOnlyFailure(ch []core.FX, err error) error {
	var ro core.ReadOnlyError
	if !errors.As(err, &ro) {
		return err
	}
	plugin := fxName(ch, ro.FX)
	if plugin == "" {
		return fmt.Errorf("%s: %w", ro.Detail, core.ErrReadOnly)
	}
	a.markReadOnly(plugin)
	a.mu.Lock()
	if a.reopened[plugin] {
		if a.reopenFail == nil {
			a.reopenFail = map[string]bool{}
		}
		a.reopenFail[plugin] = true
	}
	a.mu.Unlock()
	return errors.New(readOnlyMessage(plugin))
}

// annotateReadOnly marks describe_chain's plugins and parameters that are read-only.
func (a *Agent) annotateReadOnly(fxs []map[string]any) {
	for _, fx := range fxs {
		name, _ := fx["name"].(string)
		if !a.isReadOnly(name) {
			continue
		}
		fx["read_only"] = true
		if params, ok := fx["params"].([]map[string]any); ok {
			for _, p := range params {
				p["read_only"] = true
			}
		}
	}
}
