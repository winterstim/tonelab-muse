package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type twoTrackDAW struct {
	*memDAW
	count  int
	track2 map[core.ParamID]float64
}

func (d *twoTrackDAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	chain, err := d.memDAW.FXChain(ctx, track)
	if err != nil || track == 1 {
		return chain, err
	}
	if d.track2 == nil {
		d.track2 = map[core.ParamID]float64{}
		for id, v := range d.vals {
			d.track2[id] = v
		}
	}
	for i := range chain {
		for j := range chain[i].Params {
			chain[i].Params[j].Value = d.track2[chain[i].Params[j].ID]
		}
	}
	return chain, nil
}

func (d *twoTrackDAW) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	if track == 1 {
		return d.memDAW.SetParam(ctx, track, id, v)
	}
	if d.track2 == nil {
		d.track2 = map[core.ParamID]float64{}
		for p, x := range d.vals {
			d.track2[p] = x
		}
	}
	if _, ok := d.track2[id]; !ok {
		return core.Applied{Param: id, Sent: v, Status: core.StatusUnconfirmed}, core.ErrUnconfirmed
	}
	d.track2[id] = v
	return core.Applied{Param: id, Sent: v, Confirmed: v, Status: core.StatusConfirmed}, nil
}

func (d *twoTrackDAW) Tracks(context.Context) ([]core.Track, error) {
	out := make([]core.Track, d.count)
	for i := range out {
		out[i] = core.Track{Index: i + 1, Name: "Guitar"}
	}
	return out, nil
}

type fakeUndoGroups struct {
	begins, ends     int
	beginErr, endErr error
	names            []string
	commits          []bool
}

type canceledSetDAW struct{ *memDAW }

func (d *canceledSetDAW) SetParam(ctx context.Context, track int, id core.ParamID, value float64) (core.Applied, error) {
	if err := ctx.Err(); err != nil {
		return core.Applied{Param: id, Sent: value, Status: core.StatusFailed}, err
	}
	return d.memDAW.SetParam(ctx, track, id, value)
}

type changedChainDAW struct {
	*twoTrackDAW
	variant string
	writes  int
}

func (d *changedChainDAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	chain, err := d.twoTrackDAW.FXChain(ctx, track)
	if err != nil || track != 1 {
		return chain, err
	}
	switch d.variant {
	case "missing":
		return chain[1:], nil
	case "reordered":
		chain[0].Name, chain[1].Name = chain[1].Name, chain[0].Name
	}
	return chain, nil
}

func (d *changedChainDAW) SetParam(ctx context.Context, track int, id core.ParamID, value float64) (core.Applied, error) {
	d.writes++
	return d.twoTrackDAW.SetParam(ctx, track, id, value)
}

type exactSnapshotDAW struct {
	*twoTrackDAW
	state string
}

func (*exactSnapshotDAW) SupportsRequestSnapshot() bool { return true }
func (d *exactSnapshotDAW) CaptureRequestSnapshot(context.Context) ([]byte, error) {
	return []byte(d.state), nil
}
func (d *exactSnapshotDAW) RestoreRequestSnapshot(_ context.Context, state []byte) error {
	d.state = string(state)
	return nil
}

func (f *fakeUndoGroups) SupportsUndoActionGrouping() bool { return true }
func (f *fakeUndoGroups) BeginUndoAction(_ context.Context, name string) error {
	f.begins++
	f.names = append(f.names, name)
	return f.beginErr
}
func (f *fakeUndoGroups) EndUndoAction(_ context.Context, name string, commit bool) error {
	f.ends++
	f.names = append(f.names, name)
	f.commits = append(f.commits, commit)
	return f.endErr
}
func (*fakeUndoGroups) UndoActionIsCurrent(context.Context, string) (bool, error) { return false, nil }

func TestBeforeSnapshotCapturesAndRecallsEveryTrack(t *testing.T) {
	d := &twoTrackDAW{memDAW: newMemDAW(), count: 2}
	st := memSnaps{}
	a := New(Deps{DAW: d, Snapshots: st}, Config{})
	a.mu.Lock()
	a.request = "two track request"
	a.mu.Unlock()
	if err := a.autoSnapshot(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	sn := st["Before: two track request"]
	if len(sn.Tracks) != 2 || sn.Tracks[0].Track != 1 || sn.Tracks[1].Track != 2 {
		t.Fatalf("Before tracks = %#v", sn.Tracks)
	}
	if _, err := d.SetParam(context.Background(), 2, "fx1.p3", 0.9); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecallSnapshot(context.Background(), sn.Name); err != nil {
		t.Fatal(err)
	}
	if got := d.vals["fx1.p3"]; got != 0.5 {
		t.Fatalf("restored value = %v", got)
	}
	if len(a.history) != 1 || len(a.history[0].PreviousByTrack) != 2 {
		t.Fatalf("multi-track recall history = %#v", a.history)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := d.track2["fx1.p3"]; got != 0.9 {
		t.Fatalf("undo recall value = %v", got)
	}
}

func TestBeforeRecallRefusesUnrestorableStructure(t *testing.T) {
	d := &twoTrackDAW{memDAW: newMemDAW(), count: 2}
	st := memSnaps{}
	a := New(Deps{DAW: d, Snapshots: st}, Config{})
	a.mu.Lock()
	a.request = "structural edit"
	a.mu.Unlock()
	if err := a.autoSnapshot(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	d.count = 3
	d.vals["fx1.p3"] = 0.9
	if _, err := a.RecallSnapshot(context.Background(), "Before: structural edit"); err == nil {
		t.Fatal("recall claimed it could restore changed structure")
	}
	if d.vals["fx1.p3"] != 0.9 {
		t.Fatal("partial parameter restore ran despite unsupported structure")
	}
}

func TestRequestRecallPreflightsEveryTrackBeforeWriting(t *testing.T) {
	for _, variant := range []string{"missing", "reordered"} {
		t.Run(variant, func(t *testing.T) {
			d := &changedChainDAW{twoTrackDAW: &twoTrackDAW{memDAW: newMemDAW(), count: 2}}
			st := memSnaps{}
			a := New(Deps{DAW: d, Snapshots: st}, Config{})
			a.mu.Lock()
			a.request = "preflight"
			a.mu.Unlock()
			if err := a.autoSnapshot(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			d.variant = variant
			if _, err := a.RecallSnapshot(context.Background(), "Before: preflight"); err == nil {
				t.Fatal("mismatched plugin chain was restored")
			}
			if d.writes != 0 {
				t.Fatalf("preflight wrote %d controls before rejecting chain", d.writes)
			}
		})
	}
}

func TestExactSessionSnapshotRestoresAndUndoesWholeState(t *testing.T) {
	d := &exactSnapshotDAW{twoTrackDAW: &twoTrackDAW{memDAW: newMemDAW(), count: 2}, state: "before"}
	st := memSnaps{}
	a := New(Deps{DAW: d, Snapshots: st}, Config{})
	a.mu.Lock()
	a.request = "routing and tracks"
	a.mu.Unlock()
	if err := a.autoSnapshot(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if got := string(st["Before: routing and tracks"].SessionState); got != "before" {
		t.Fatalf("snapshot token = %q", got)
	}
	d.state = "after"
	if _, err := a.RecallSnapshot(context.Background(), "Before: routing and tracks"); err != nil {
		t.Fatal(err)
	}
	if d.state != "before" {
		t.Fatalf("restored state = %q", d.state)
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.state != "after" {
		t.Fatalf("undo restored state = %q", d.state)
	}
}

func TestApplyPlanGroupsOneUndoActionAndClosesOnCancel(t *testing.T) {
	d := newMemDAW()
	groups := &fakeUndoGroups{}
	a := New(Deps{DAW: d}, Config{})
	a.d.DAW = struct {
		core.DAW
		core.UndoActionGrouper
	}{d, groups}
	p := &core.Plan{ID: "p1", Track: 1, Goal: "grouped", Changes: []core.Change{{Param: "fx1.p3", Value: 0.8, Why: "gain"}, {Param: "fx2.p1", Value: 0.2, Why: "cut"}}}
	a.plans[p.ID] = p
	if _, err := a.applyPlan(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if groups.begins != 1 || groups.ends != 1 || groups.names[0] != groups.names[1] {
		t.Fatalf("group calls = %#v", groups)
	}
	groups.beginErr = context.Canceled
	p.ID = "p2"
	a.plans[p.ID] = p
	if _, err := a.applyPlan(context.Background(), p.ID); err == nil {
		t.Fatal("failed undo-group begin was ignored")
	}
	if groups.begins != 2 || groups.ends != 1 {
		t.Fatalf("unbalanced failed begin: %#v", groups)
	}
}

func TestApplyPlanClosesCanceledUndoGroupWithoutCommit(t *testing.T) {
	d := &canceledSetDAW{memDAW: newMemDAW()}
	groups := &fakeUndoGroups{}
	a := New(Deps{DAW: d}, Config{})
	a.d.DAW = struct {
		core.DAW
		core.UndoActionGrouper
	}{d, groups}
	p := &core.Plan{ID: "cancel", Track: 1, Goal: "grouped", Changes: []core.Change{{Param: "fx1.p3", Value: 0.8, Why: "gain"}}}
	a.plans[p.ID] = p
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.applyPlan(ctx, p.ID); err == nil {
		t.Fatal("canceled parameter change unexpectedly succeeded")
	}
	if groups.begins != 1 || groups.ends != 1 || len(groups.commits) != 1 || groups.commits[0] {
		t.Fatalf("canceled undo group was not balanced and canceled: %#v", groups)
	}
}

func TestUndoGroupCleanupErrorIsReturnedWithActionError(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	groups := &fakeUndoGroups{endErr: cleanupErr}
	a := New(Deps{}, Config{})
	a.d.DAW = struct {
		core.DAW
		core.UndoActionGrouper
	}{newMemDAW(), groups}
	actionErr := context.Canceled
	err := a.withUndoAction(context.Background(), "failing restore", func() error { return actionErr })
	if !errors.Is(err, actionErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("joined action and cleanup error = %v", err)
	}
}
