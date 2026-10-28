package agent

import (
	"context"
	"errors"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Consent by mode. Turbo (-turbo) is full autopilot and never asks. Default
// (Auto) asks once per session, with a card, for each kind of action that
// has side effects, then proceeds. Review (Preview) only plans: it never
// reopens the project and never sends program changes.

const (
	kindReload     = "reload"  // reopening the project with a plugin's state
	kindProgram    = "program" // sending a MIDI program change
	kindProbe      = "probe"   // bounded parameter readout probe
	kindPresetSave = "preset_save"

	reloadNote  = "Applying this needs the user's one-time yes: the card reopens the project. Stop and tell them in one sentence what you want to change and why; they can ask again after allowing."
	reloadQ     = "Apply by reopening the project? The DAW saves it and reopens it in about two seconds; its undo history starts fresh, the Before snapshot stays."
	programQ    = "Send a program change to switch the plugin's preset? The plugin loads that program; the Before snapshot stays."
	presetSaveQ = "Save this plugin's current settings as a named preset in the DAW?"
	probeQ      = "Probe this parameter across its normalized range to learn the DAW's available readouts? The original value will be restored."
)

// Allow records the user's one-time yes for kind ("reload", "program", or "preset_save")
// for the rest of this session.
func (a *Agent) Allow(kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.allowed == nil {
		a.allowed = map[string]bool{}
	}
	a.allowed[kind] = true
}

func (a *Agent) probePolicy() error {
	switch a.currentMode() {
	case Turbo:
		return nil
	case Preview:
		return errors.New("review mode does not probe parameters")
	}
	if a.isAllowed(kindProbe) {
		return nil
	}
	a.emit("probe_consent", map[string]any{"question": probeQ})
	return core.ErrNeedsConsent
}

func (a *Agent) isAllowed(kind string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowed[kind]
}

// needsPermission is the result the model sees when the user has to say yes.
func needsPermission(note string) map[string]any {
	return map[string]any{"needs_permission": true, "note": note}
}

// reloadPolicy answers the DAW when a plugin kept none of a write and the
// project could be reopened with the state in its file. A plugin whose
// reopen already failed in this process, or that is already read-only, is
// refused at once: no card, no reopen, the read-only message follows.
func (a *Agent) reloadPolicy(ctx context.Context, track, fx int, state string) error {
	plugin := a.pluginAt(track, fx)
	if a.reopenGiveUp(plugin) {
		return errors.New("the project was already reopened for " + plugin + " and it still kept none of the change")
	}
	switch a.currentMode() {
	case Turbo:
		a.markReopened(plugin)
		return nil
	case Preview:
		return errors.New("review mode does not reopen the project")
	}
	if a.isAllowed(kindReload) {
		a.markReopened(plugin)
		return nil
	}
	a.emit("reload_consent", map[string]any{"fx": fx, "question": reloadQ})
	return core.ErrNeedsConsent
}

// pluginAt is the name of the plugin at fx on track, from the chain the agent
// already read. It is empty when that chain is not known yet.
func (a *Agent) pluginAt(track, fx int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.chains[track] {
		if f.Index == fx {
			return f.Name
		}
	}
	return ""
}

func (a *Agent) markReopened(plugin string) {
	if plugin == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reopened == nil {
		a.reopened = map[string]bool{}
	}
	a.reopened[plugin] = true
}

// reopenGiveUp is true once a reopen was tried for plugin and did not make
// its write stick, or once the plugin is read-only.
func (a *Agent) reopenGiveUp(plugin string) bool {
	if plugin == "" {
		return false
	}
	a.mu.Lock()
	failed := a.reopenFail[plugin]
	a.mu.Unlock()
	return failed || a.isReadOnly(plugin)
}

// programChanger is the DAW object that can send program changes: the chain
// editor first, then the DAW itself.
func (a *Agent) programChanger() core.ProgramChanger {
	if pc, ok := a.d.Chain.(core.ProgramChanger); ok {
		return pc
	}
	if pc, ok := a.d.DAW.(core.ProgramChanger); ok {
		return pc
	}
	return nil
}

func (a *Agent) programChange(ctx context.Context, track, fx, program, channel int) (any, error) {
	pc := a.programChanger()
	if pc == nil {
		return nil, errors.New("program changes are not supported by this DAW")
	}
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not send program changes")
	}
	if channel == 0 {
		channel = 1
	}
	if channel < 1 || channel > 16 {
		return nil, errors.New("channel must be 1 to 16")
	}
	if program < 0 || program > 127 {
		return nil, errors.New("program must be 0 to 127")
	}
	if a.currentMode() != Turbo && !a.isAllowed(kindProgram) {
		a.emit("program_consent", map[string]any{"track": track, "fx": fx, "program": program, "question": programQ})
		return needsPermission("The user sees a one-time card to allow program changes. Stop and tell them in one sentence which program you want and why; they can ask again after allowing."), nil
	}
	if err := pc.ProgramChange(ctx, track, channel, program); err != nil {
		return nil, err
	}
	out := map[string]any{"track": track, "fx": fx, "program": program, "channel": channel, "sent": true,
		"note": "The plugin switched to that program. Listen now before tuning anything else."}
	if namer, ok := a.d.Chain.(core.ProgramNamer); ok {
		if names, err := namer.ProgramNames(ctx, track, fx); err == nil && names[program] != "" {
			out["preset"] = names[program]
		}
	}
	a.emit("program_change", out)
	return out, nil
}
