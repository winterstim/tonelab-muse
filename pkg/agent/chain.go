package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Building a chain: the agent may add stock plugins from the DAW's catalog
// and remove ones it added, then tunes them with the usual plan. Each
// change is one step in the DAW's own undo.

func (a *Agent) addPlugin(ctx context.Context, track int, name string, pos int) (any, error) {
	if a.d.Chain == nil {
		return nil, errors.New("adding plugins is not set up")
	}
	cat, err := a.catalog(ctx)
	if err != nil {
		return nil, err
	}
	known := false
	for _, c := range cat {
		known = known || c.Name == name
	}
	if !known {
		return nil, errors.New("not installed: call list_plugins and use a name exactly")
	}
	tc, canAuto := a.d.Chain.(core.TurboChain)
	isTurbo := canAuto && a.currentMode() != Preview // adding is undoable: only Review asks
	if isTurbo {
		_ = tc.EnableAuto()
	}
	i, err := a.d.Chain.AddFX(ctx, track, name, pos)
	if errors.Is(err, core.ErrNeedsConsent) && isTurbo {
		if errAuto := tc.EnableAuto(); errAuto == nil {
			i, err = a.d.Chain.AddFX(ctx, track, name, pos)
		}
	}
	if errors.Is(err, core.ErrNeedsConsent) {
		// ask once in the UI; the model tells the user and stops
		a.emit("bridge_consent", map[string]any{"plugin": name})
		return map[string]any{"needs_permission": true, "note": "The user sees a one-time card to allow adding plugins. Stop and tell them in one sentence what you want to add and why; they can ask again after allowing."}, nil
	}
	if err != nil {
		return nil, err
	}
	ch, err := a.chainWith(ctx, track, i) // the chain changed: read it again
	if err != nil {
		return nil, err
	}
	out := map[string]any{"added": name, "fx": i, "chain": summary(ch)}
	a.emit("chain", out)
	return out, nil
}

func (a *Agent) removePlugin(ctx context.Context, track, fx int) (any, error) {
	if a.d.Chain == nil {
		return nil, errors.New("removing plugins is not set up")
	}
	var name string
	var err error
	tc, canAuto := a.d.Chain.(core.TurboChain)
	isTurbo := canAuto && a.currentMode() != Preview // adding is undoable: only Review asks
	if a.currentMode() == Turbo && isTurbo {
		_ = tc.EnableAuto()
		if err := a.autoSnapshot(ctx, track); err != nil {
			return nil, err
		}
		name, err = tc.ForceRemoveFX(ctx, track, fx)
	} else {
		name, err = a.d.Chain.RemoveFX(ctx, track, fx)
	}
	if err != nil {
		if a.currentMode() != Turbo && (strings.Contains(strings.ToLower(err.Error()), "not added by the agent") || errors.Is(err, core.ErrNeedsConsent)) {
			note := "This plugin was placed by the user. In Default mode, ask the user before removing their plugin, or tell them they can switch to Turbo mode for full autopilot."
			a.emit("consent", map[string]any{"track": track, "fx": fx, "note": note})
			return map[string]any{"needs_permission": true, "note": note}, nil
		}
		return nil, err
	}
	ch, err := a.chain(ctx, track, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"removed": name, "chain": summary(ch)}
	a.emit("chain", out)
	return out, nil
}

func (a *Agent) movePlugin(ctx context.Context, track, fx, toPos int) (any, error) {
	var mover core.ChainMover
	if m, ok := a.d.Chain.(core.ChainMover); ok {
		mover = m
	} else if m, ok := a.d.DAW.(core.ChainMover); ok {
		mover = m
	}
	if mover == nil {
		return nil, errors.New("moving plugins is not supported")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	if fx == toPos {
		return map[string]any{"moved": fx, "to": toPos, "note": "already in that place"}, nil
	}
	if err := mover.MoveFX(ctx, track, fx, toPos); err != nil {
		return nil, err
	}
	ch, err := a.chain(ctx, track, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"moved": fx, "to": toPos, "chain": summary(ch)}
	a.emit("chain", out)
	return out, nil
}

func (a *Agent) listPresets(ctx context.Context, track, fx int) (any, error) {
	var presetter core.Presetter
	if p, ok := a.d.Chain.(core.Presetter); ok {
		presetter = p
	} else if p, ok := a.d.DAW.(core.Presetter); ok {
		presetter = p
	}
	if presetter == nil {
		return nil, errors.New("presets are not supported")
	}
	cur, list, err := presetter.Presets(ctx, track, fx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"track": track, "fx": fx, "current": cur, "presets": list}
	if namer, ok := a.d.Chain.(core.ProgramNamer); ok {
		if names, err := namer.ProgramNames(ctx, track, fx); err == nil && len(names) > 0 {
			out["programs"] = names
		}
	}
	return out, nil
}

func (a *Agent) loadPreset(ctx context.Context, track, fx int, preset string) (any, error) {
	var presetter core.Presetter
	if p, ok := a.d.Chain.(core.Presetter); ok {
		presetter = p
	} else if p, ok := a.d.DAW.(core.Presetter); ok {
		presetter = p
	}
	if presetter == nil {
		return nil, errors.New("presets are not supported")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	ch, err := a.chain(ctx, track, false)
	if err != nil {
		return nil, err
	}
	if plugin := fxName(ch, fx); a.isReadOnly(plugin) {
		return nil, errors.New(readOnlyMessage(plugin))
	}
	if err := a.autoSnapshot(ctx, track); err != nil {
		return nil, err
	}
	if err := presetter.SetPreset(ctx, track, fx, preset); errors.Is(err, core.ErrNeedsConsent) {
		return needsPermission(reloadNote), nil
	} else if err != nil {
		return nil, a.readOnlyFailure(ch, err)
	}
	ch, err = a.chain(ctx, track, true)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"track": track, "fx": fx, "preset": preset, "chain": summary(ch)}
	a.emit("preset", out)
	return out, nil
}

func (a *Agent) presetSaver() (core.PresetSaver, bool) {
	if p, ok := a.d.Chain.(core.PresetSaver); ok {
		return p, true
	}
	p, ok := a.d.DAW.(core.PresetSaver)
	return p, ok
}

func (a *Agent) savePresetAs(ctx context.Context, track, fx int, name string) (any, error) {
	saver, ok := a.presetSaver()
	if !ok {
		return nil, errors.New("saving presets is not supported")
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\\/\x00") {
		return nil, errors.New("preset name must be non-empty and cannot contain path separators")
	}
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not save presets")
	}
	ch, err := a.chain(ctx, track, false)
	if err != nil {
		return nil, err
	}
	if plugin := fxName(ch, fx); a.isReadOnly(plugin) {
		return nil, errors.New(readOnlyMessage(plugin))
	}
	if a.currentMode() != Turbo && !a.isAllowed(kindPresetSave) {
		a.emit("preset_save_consent", map[string]any{"track": track, "fx": fx, "name": name, "question": presetSaveQ})
		return needsPermission("Preset saving needs one-time permission. Explain what name will be saved, then retry after permission is granted."), nil
	}
	if err := saver.SavePresetAs(ctx, track, fx, name); errors.Is(err, core.ErrNeedsConsent) {
		return needsPermission("The DAW needs permission to save this preset."), nil
	} else if err != nil {
		return nil, err
	}
	current, presets, err := a.listPresetData(ctx, track, fx)
	if err != nil {
		return nil, fmt.Errorf("preset save was sent but readback failed: %w", err)
	}
	found := false
	for _, preset := range presets {
		if preset == name {
			found = true
			break
		}
	}
	if !found && current != name {
		return nil, errors.New("the DAW did not confirm the saved preset in its preset list")
	}
	out := map[string]any{"track": track, "fx": fx, "name": name, "current": current, "presets": presets, "saved": true}
	a.emit("preset", out)
	return out, nil
}

func (a *Agent) listPresetData(ctx context.Context, track, fx int) (string, []string, error) {
	if p, ok := a.d.Chain.(core.Presetter); ok {
		return p.Presets(ctx, track, fx)
	}
	if p, ok := a.d.DAW.(core.Presetter); ok {
		return p.Presets(ctx, track, fx)
	}
	return "", nil, errors.New("preset readback is not supported")
}

// catalog is what the DAW reports as installed, read once per agent.
func (a *Agent) catalog(ctx context.Context) ([]core.CatalogEntry, error) {
	a.mu.Lock()
	c := a.cat
	a.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c, err := a.d.Chain.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cat = c
	a.mu.Unlock()
	return c, nil
}

// findPlugins answers list_plugins: entries whose name or kind has every
// word of the query, at most limit of them, and how many there are.
func (a *Agent) findPlugins(ctx context.Context, query string, limit int) (any, error) {
	if a.d.Chain == nil {
		return nil, errors.New("adding plugins is not set up")
	}
	cat, err := a.catalog(ctx)
	if err != nil {
		return nil, err
	}
	words := strings.Fields(strings.ToLower(query))
	var hits []core.CatalogEntry
	for _, c := range cat {
		s := strings.ToLower(c.Name + " " + c.Kind)
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(s, w)
		}
		if ok {
			hits = append(hits, c)
		}
	}
	out := map[string]any{"installed": len(cat), "matches": len(hits)}
	if len(hits) > limit {
		hits = hits[:limit]
		out["note"] = "more match: narrow the query"
	}
	out["plugins"] = hits
	return out, nil
}

// summary is the chain as plugin names in order, with index.
func summary(ch []core.FX) []map[string]any {
	var out []map[string]any
	for _, f := range ch {
		out = append(out, map[string]any{"fx": f.Index, "name": f.Name})
	}
	return out
}

// Vocabulary is the plugin and control names the agent has read this
// session, so speech recognition knows them.
func (a *Agent) Vocabulary() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, ch := range a.chains {
		for _, f := range ch {
			add(f.Name)
		}
	}
	for _, ch := range a.chains {
		for _, f := range ch {
			for _, p := range f.Params {
				add(p.Name)
			}
		}
	}
	return out
}

// Chain reads a track's plugins fresh from the DAW, for the page.
func (a *Agent) Chain(ctx context.Context, track int) ([]core.FX, error) {
	return a.chain(ctx, track, true)
}

func (a *Agent) modelSwapper() core.ModelSwapper {
	if ms, ok := a.d.DAW.(core.ModelSwapper); ok {
		return ms
	}
	if ms, ok := a.d.Chain.(core.ModelSwapper); ok {
		return ms
	}
	return nil
}

func (a *Agent) listPluginModels(ctx context.Context, track, fx int, slot string) (any, error) {
	ms := a.modelSwapper()
	if ms == nil {
		return nil, errors.New("model swapping is not supported by this DAW")
	}
	slots, err := ms.PluginModels(ctx, track, fx, slot)
	if err != nil {
		return nil, err
	}
	return map[string]any{"track": track, "fx": fx, "slots": slots}, nil
}

func (a *Agent) setPluginModel(ctx context.Context, track, fx int, slot, model string) (any, error) {
	ms := a.modelSwapper()
	if ms == nil {
		return nil, errors.New("model swapping is not supported by this DAW")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	ch, _ := a.chain(ctx, track, false)
	plugin := ""
	for _, f := range ch {
		if f.Index == fx {
			plugin = f.Name
		}
	}
	a.mu.Lock()
	refused := a.readOnly[plugin]
	a.mu.Unlock()
	if refused {
		return nil, errors.New(readOnlyMessage(plugin))
	}
	if err := a.autoSnapshot(ctx, track); err != nil {
		return nil, err
	}
	params, err := ms.SetPluginModel(ctx, track, fx, slot, model)
	if errors.Is(err, core.ErrNeedsConsent) {
		return needsPermission(reloadNote), nil
	}
	if err != nil {
		return nil, a.readOnlyFailure(ch, err)
	}
	a.mu.Lock()
	delete(a.chains, track)
	a.mu.Unlock()
	// only the swapped slot's controls go back to the model: the whole
	// plugin is hundreds of parameters it already has
	prefix, _, _ := strings.Cut(slot, ".")
	var mine []core.Param
	for _, p := range params {
		if strings.HasPrefix(p.Name, prefix+" ") && !p.Unnamed {
			mine = append(mine, p)
		}
	}
	a.emit("model_swap", map[string]any{"track": track, "fx": fx, "slot": slot, "model": model, "params": mine})
	slim := compactChain([]core.FX{{Index: fx, Params: mine}})
	return map[string]any{"track": track, "fx": fx, "slot": slot, "model": model, "kept": true, "controls": slim[0]["params"],
		"note": "The plugin kept this model. Do not swap it again; plan its controls next."}, nil
}

func (a *Agent) sessionController() core.SessionController {
	if sc, ok := a.d.DAW.(core.SessionController); ok {
		return sc
	}
	if sc, ok := a.d.Chain.(core.SessionController); ok {
		return sc
	}
	return nil
}

func (a *Agent) sessionInterface(feature string) any {
	for _, candidate := range []any{a.d.DAW, a.d.Chain} {
		switch feature {
		case "track_controls":
			if v, ok := candidate.(core.SessionTrackControls); ok {
				return v
			}
		case "routing":
			if v, ok := candidate.(core.SessionRouting); ok {
				return v
			}
		case "markers":
			if v, ok := candidate.(core.SessionMarkers); ok {
				return v
			}
		case "meter":
			if v, ok := candidate.(core.SessionMeter); ok {
				return v
			}
		case "project":
			if v, ok := candidate.(core.SessionProject); ok {
				return v
			}
		}
	}
	return nil
}

func (a *Agent) hasSessionFeature(feature string) bool { return a.sessionInterface(feature) != nil }

func (a *Agent) manageTrack(ctx context.Context, action string, track int, name string) (any, error) {
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not change session tracks")
	}
	sc := a.sessionController()
	if sc == nil {
		return nil, errors.New("session control is not supported by this DAW")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	out := map[string]any{"action": action}
	switch action {
	case "create":
		idx, err := sc.CreateTrack(ctx, name, track)
		if err != nil {
			return nil, err
		}
		out["track"] = idx
		out["name"] = name
	case "duplicate":
		idx, err := sc.DuplicateTrack(ctx, track, name)
		if err != nil {
			return nil, err
		}
		out["source_track"] = track
		out["track"] = idx
		out["name"] = name
	case "delete":
		if a.currentMode() != Turbo {
			note := "Deleting tracks requires user confirmation in Default mode. Ask the user before deleting a track, or tell them they can switch to Turbo mode for full autopilot."
			a.emit("consent", map[string]any{"track": track, "action": "delete", "note": note})
			return map[string]any{"needs_permission": true, "note": note}, nil
		}
		if err := sc.DeleteTrack(ctx, track); err != nil {
			return nil, err
		}
		out["track"] = track
	case "rename":
		if err := sc.RenameTrack(ctx, track, name); err != nil {
			return nil, err
		}
		out["track"] = track
		out["name"] = name
	default:
		return nil, fmt.Errorf("unknown manage_track action %q (use create, duplicate, delete, rename)", action)
	}
	a.mu.Lock()
	a.chains = map[int][]core.FX{}
	a.mu.Unlock()
	a.emit("track_op", out)
	return out, nil
}

func (a *Agent) insertMedia(ctx context.Context, track int, path string, posSec float64) (any, error) {
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not insert media")
	}
	sc := a.sessionController()
	if sc == nil {
		return nil, errors.New("session control is not supported by this DAW")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	if err := sc.InsertMedia(ctx, track, path, posSec); err != nil {
		return nil, err
	}
	out := map[string]any{"track": track, "path": path, "position_sec": posSec}
	a.emit("insert_media", out)
	return out, nil
}

func (a *Agent) routeSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) (any, error) {
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not change routing")
	}
	sc := a.sessionController()
	if sc == nil {
		return nil, errors.New("session control is not supported by this DAW")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	if err := sc.CreateSend(ctx, fromTrack, toTrack, levelDB); err != nil {
		return nil, err
	}
	out := map[string]any{"from_track": fromTrack, "to_track": toTrack, "level_db": levelDB}
	a.emit("route_send", out)
	return out, nil
}

func (a *Agent) setTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) (any, error) {
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not change transport")
	}
	sc := a.sessionController()
	if sc == nil {
		return nil, errors.New("session control is not supported by this DAW")
	}
	if tc, ok := a.d.Chain.(core.TurboChain); ok {
		if a.currentMode() == Turbo {
			_ = tc.EnableAuto()
		}
	}
	if err := sc.SetTransport(ctx, action, bpm, posSec, loopStart, loopEnd); err != nil {
		return nil, err
	}
	out := map[string]any{
		"action":       action,
		"bpm":          bpm,
		"position_sec": posSec,
		"loop_start":   loopStart,
		"loop_end":     loopEnd,
	}
	a.emit("transport", out)
	return out, nil
}
