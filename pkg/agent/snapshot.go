package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// snapshot reads the track as it is now: every named parameter.
func (a *Agent) snapshot(ctx context.Context, track int, name string) (core.Snapshot, error) {
	ch, err := a.chain(ctx, track, true)
	if err != nil {
		return core.Snapshot{}, err
	}
	sn := core.Snapshot{Name: name, Track: track, Values: map[core.ParamID]float64{}, Names: map[core.ParamID]string{}, Saved: time.Now()}
	for _, fx := range ch {
		for _, p := range fx.Params {
			if p.Unnamed {
				continue
			}
			sn.Values[p.ID], sn.Names[p.ID] = p.Value, fx.Name+" / "+p.Name
		}
	}
	return sn, nil
}

func (a *Agent) saveSnapshot(ctx context.Context, track int, name string) (any, error) {
	if a.d.Snapshots == nil {
		return nil, errors.New("snapshots are not set up")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a snapshot needs a name")
	}
	sn, err := a.snapshot(ctx, track, name)
	if err != nil {
		return nil, err
	}
	if err := a.d.Snapshots.SaveSnapshot(sn); err != nil {
		return nil, err
	}
	out := map[string]any{"saved": name, "track": track, "controls": len(sn.Values)}
	a.emit("snapshot", out)
	return out, nil
}

// RecallSnapshot turns a saved tone into a plan of the controls that
// differ now, and applies it: confirmed by the DAW, undoable.
func (a *Agent) RecallSnapshot(ctx context.Context, name string) (any, error) {
	if a.d.Snapshots == nil {
		return nil, errors.New("snapshots are not set up")
	}
	sn, err := a.d.Snapshots.Snapshot(name)
	if err != nil {
		return nil, err
	}
	if sn == nil {
		return nil, fmt.Errorf("no snapshot named %q", name)
	}
	if len(sn.Tracks) > 0 || len(sn.SessionState) > 0 {
		return a.recallRequestSnapshot(ctx, *sn)
	}
	now, err := a.snapshot(ctx, sn.Track, "")
	if err != nil {
		return nil, err
	}
	var changes []core.Change
	for id, v := range sn.Values {
		cur, ok := now.Values[id]
		if !ok {
			continue // that plugin is gone or was swapped
		}
		if math.Abs(cur-v) > 0.0005 {
			changes = append(changes, core.Change{Param: id, Value: v, Why: "recall " + name})
		}
	}
	if len(changes) == 0 {
		return map[string]any{"recalled": name, "note": "already sounds like this snapshot"}, nil
	}
	ch, err := a.chain(ctx, sn.Track, false)
	if err != nil {
		return nil, err
	}
	plan, _, err := Validate(sn.Track, "Recall "+name, changes, ch)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.nextID++
	plan.ID = fmt.Sprintf("p%d", a.nextID)
	a.plans[plan.ID] = plan
	a.mu.Unlock()
	return a.applyPlan(ctx, plan.ID)
}

func (a *Agent) recallRequestSnapshot(ctx context.Context, sn core.Snapshot) (any, error) {
	a.undoMu.Lock()
	defer a.undoMu.Unlock()
	if len(sn.SessionState) > 0 {
		r, ok := a.d.DAW.(core.RequestSnapshotter)
		if !ok || !r.SupportsRequestSnapshot() {
			return nil, errors.New("this DAW cannot restore the full session snapshot")
		}
		before, err := r.CaptureRequestSnapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("capture current session before restore: %w", err)
		}
		if err := r.RestoreRequestSnapshot(ctx, sn.SessionState); err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.history = append(a.history, applied{Plan: &core.Plan{ID: "session-snapshot", Goal: "Recall " + sn.Name}, SessionBefore: before})
		a.mu.Unlock()
		return map[string]any{"recalled": sn.Name, "scope": "session", "confirmed": true}, nil
	}
	current, err := a.d.DAW.Tracks(ctx)
	if err != nil {
		return nil, err
	}
	if len(current) != len(sn.Tracks) {
		return nil, errors.New("this DAW cannot restore session structure exactly; no changes were made")
	}
	for i, t := range current {
		if i >= len(sn.Tracks) || t.Index != sn.Tracks[i].Track || t.Name != sn.Tracks[i].Name {
			return nil, errors.New("this DAW cannot restore session structure exactly; no changes were made")
		}
	}
	chains := make(map[int][]core.FX, len(sn.Tracks))
	for _, saved := range sn.Tracks {
		chain, e := a.chain(ctx, saved.Track, true)
		if e != nil {
			return nil, e
		}
		currentValues := map[core.ParamID]core.Param{}
		currentNames := map[core.ParamID]string{}
		for _, fx := range chain {
			for _, p := range fx.Params {
				if p.Unnamed {
					continue
				}
				currentValues[p.ID] = p
				currentNames[p.ID] = fx.Name + " / " + p.Name
			}
		}
		if len(currentValues) != len(saved.Values) || len(saved.Names) != len(saved.Values) {
			return nil, fmt.Errorf("track %d controls do not match snapshot; no changes were made", saved.Track)
		}
		for id := range saved.Values {
			if _, ok := currentValues[id]; !ok || currentNames[id] != saved.Names[id] {
				return nil, fmt.Errorf("track %d control %s does not match snapshot; no changes were made", saved.Track, id)
			}
		}
		chains[saved.Track] = chain
	}
	previous := map[int]map[core.ParamID]float64{}
	names := map[int]map[core.ParamID]string{}
	restored := 0
	group, grouped := a.d.DAW.(core.UndoActionGrouper)
	if grouped && !group.SupportsUndoActionGrouping() {
		grouped = false
	}
	label := "Agent: recall snapshot " + sn.Name
	if grouped {
		if err := group.BeginUndoAction(ctx, label); err != nil {
			return nil, err
		}
	}
	closed := false
	defer func() {
		if grouped && !closed {
			_ = group.EndUndoAction(context.WithoutCancel(ctx), label, false)
		}
	}()
	for _, saved := range sn.Tracks {
		chain := chains[saved.Track]
		previous[saved.Track] = map[core.ParamID]float64{}
		names[saved.Track] = map[core.ParamID]string{}
		for id, want := range saved.Values {
			p, _ := find(chain, id) // preflight above guarantees this control and name.
			previous[saved.Track][id] = p.Value
			names[saved.Track][id] = pluginOf(chain, id) + " / " + p.Name
			r, e := a.d.DAW.SetParam(ctx, saved.Track, id, want)
			if e != nil {
				return nil, e
			}
			if r.Status != core.StatusConfirmed {
				return nil, fmt.Errorf("could not confirm restore of %s on track %d", id, saved.Track)
			}
			restored++
		}
	}
	if grouped {
		if err := group.EndUndoAction(ctx, label, true); err != nil {
			return nil, err
		}
		closed = true
	}
	if restored > 0 {
		a.mu.Lock()
		a.history = append(a.history, applied{Plan: &core.Plan{ID: "request-snapshot", Goal: "Recall " + sn.Name}, PreviousByTrack: previous, NamesByTrack: names, Grouped: grouped, UndoLabel: label})
		a.mu.Unlock()
	}
	return map[string]any{"recalled": sn.Name, "scope": "all tracks' named plugin controls", "tracks": len(sn.Tracks), "controls": restored, "session_state": "unsupported: structure, routing and transport are not restored by this DAW"}, nil
}

// autoSnapshot keeps the tone as it was before a request's first change,
// so "go back to how it was" always has somewhere to go.
func (a *Agent) autoSnapshot(ctx context.Context, track int) error {
	a.mu.Lock()
	req, done := a.request, a.snapped
	a.snapped = true
	a.mu.Unlock()
	if done || a.d.Snapshots == nil || req == "" {
		return nil
	}
	if len(req) > 48 {
		req = req[:48] + "…"
	}
	tracks, err := a.d.DAW.Tracks(ctx)
	if err != nil {
		a.resetSnapshotFlag()
		a.emit("snapshot_error", err.Error())
		return fmt.Errorf("capture Before snapshot: %w", err)
	}
	aggregate := core.Snapshot{Name: "Before: " + req, Track: track, Saved: time.Now(), Values: map[core.ParamID]float64{}, Names: map[core.ParamID]string{}}
	for _, t := range tracks {
		one, e := a.snapshot(ctx, t.Index, aggregate.Name)
		if e != nil {
			a.resetSnapshotFlag()
			a.emit("snapshot_error", e.Error())
			return fmt.Errorf("capture Before snapshot for track %d: %w", t.Index, e)
		}
		aggregate.Tracks = append(aggregate.Tracks, core.SnapshotTrack{Track: t.Index, Name: t.Name, Values: one.Values, Names: one.Names})
		if t.Index == track {
			aggregate.Values, aggregate.Names = one.Values, one.Names
		}
	}
	if r, ok := a.d.DAW.(core.RequestSnapshotter); ok && r.SupportsRequestSnapshot() {
		state, e := r.CaptureRequestSnapshot(ctx)
		if e != nil {
			a.resetSnapshotFlag()
			a.emit("snapshot_error", e.Error())
			return fmt.Errorf("capture exact Before session state: %w", e)
		}
		aggregate.SessionState = state
	}
	if err := a.d.Snapshots.SaveSnapshot(aggregate); err != nil {
		a.resetSnapshotFlag()
		a.emit("snapshot_error", err.Error())
		return fmt.Errorf("save Before snapshot: %w", err)
	}
	return nil
}

func (a *Agent) resetSnapshotFlag() { a.mu.Lock(); a.snapped = false; a.mu.Unlock() }
