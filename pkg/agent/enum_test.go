package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type enumProbeDAW struct {
	*memDAW
	text             func(float64) string
	failWrites       bool
	valueFinderCalls int
	setParamCalls    int
}

func (d *enumProbeDAW) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	d.setParamCalls++
	if d.failWrites {
		return core.Applied{Param: id, Sent: v}, core.ErrReadOnly
	}
	return d.memDAW.SetParam(ctx, track, id, v)
}

func (d *enumProbeDAW) FindValue(context.Context, int, core.ParamID, string) (float64, error) {
	d.valueFinderCalls++
	return .4, nil
}

func (d *enumProbeDAW) ParamText(_ context.Context, _ int, id core.ParamID) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.text(d.vals[id]), nil
}

func TestDiscreteProbeLearnsDistinctReadoutsAndRestores(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string {
		switch {
		case v < .25:
			return "Clean"
		case v < .2501:
			return "Clean"
		case v < .75:
			return "Crunch"
		default:
			return "Lead"
		}
	}}
	d.vals["fx2.p1"] = .42
	st := &profileStore{profiles: map[string]core.Profile{}}
	a := New(Deps{DAW: d, Store: st}, Config{})
	a.Allow(kindProbe)
	v, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Model", Value: .42}, "Crunch")
	if err != nil {
		t.Fatal(err)
	}
	if v < .25 || v >= .75 {
		t.Fatalf("normalized value %v is outside Crunch plateau", v)
	}
	if d.vals["fx2.p1"] != .42 {
		t.Fatalf("parameter not restored: %v", d.vals["fx2.p1"])
	}
	profile := st.profiles["EQ"]
	choices := profile.Map[1].Choices
	if len(choices) != 3 {
		t.Fatalf("learned choices = %+v, want three distinct readouts", choices)
	}
	if !profile.Map[1].ChoicesPartial {
		t.Fatal("sampled choice profile must report partial coverage")
	}
	firstSweepCalls := d.setParamCalls
	if firstSweepCalls < 257 {
		t.Fatalf("initial learning used only %d writes", firstSweepCalls)
	}
	d.setParamCalls = 0
	if got, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Model", Value: .42}, "Lead"); err != nil || got != choices[2].Value {
		t.Fatalf("cached target = %v, %v", got, err)
	}
	if d.setParamCalls > 3 {
		t.Fatalf("validated cache hit used %d writes; want one probe and one restore", d.setParamCalls)
	}
}

func TestDiscreteProbeRejectsStaleCacheAndUnsupportedTarget(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string {
		if v < .5 {
			return "A"
		}
		return "B"
	}}
	d.vals["fx2.p1"] = .3
	st := &profileStore{profiles: map[string]core.Profile{"EQ": {Plugin: "EQ", Map: map[int]core.ProfileEntry{1: {Choices: []core.ParamChoice{{Text: "B", Value: .4}}}}}}}
	a := New(Deps{DAW: d, Store: st}, Config{})
	a.mode = Auto
	if _, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .3}, "B"); err != core.ErrNeedsConsent {
		t.Fatalf("stale in-range cache should require probe consent: %v", err)
	}
	a.Allow(kindProbe)
	v, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .3}, "B")
	if err != nil || v < .5 {
		t.Fatalf("stale cache was not relearned: %v, %v", v, err)
	}
	if got := st.profiles["EQ"].Map[1].Choices[1].Value; got < .5 {
		t.Fatalf("stale mapping was not refreshed: B=%v", got)
	}
	if _, err = a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .3}, "Absent"); err == nil {
		t.Fatal("unsupported readout unexpectedly matched")
	}
	if d.vals["fx2.p1"] != .3 {
		t.Fatalf("unsupported target left parameter at %v", d.vals["fx2.p1"])
	}
}

func TestReadOnlyDiscreteProbeStopsAfterFailedWriteAndKeepsOriginal(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(float64) string { return "Fixed" }}
	d.failWrites = true
	a := New(Deps{DAW: d}, Config{})
	a.Allow(kindProbe)
	_, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .5}, "Missing")
	if err == nil {
		t.Fatal("read-only parameter should not invent a choice")
	}
	if d.vals["fx2.p1"] != .5 {
		t.Fatalf("unexpected value mutation %v", d.vals["fx2.p1"])
	}
}

func TestDiscreteProbeFindsNarrowReadout(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string {
		if v >= .5 && v < .51 {
			return "Rare"
		}
		return "Common"
	}}
	a := New(Deps{DAW: d}, Config{})
	a.Allow(kindProbe)
	v, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .31}, "Rare")
	if err != nil || v < .5 || v >= .51 {
		t.Fatalf("narrow target = %v, %v", v, err)
	}
}

func TestDiscreteProbeConsentAndReviewCache(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string { return fmt.Sprint(v) }}
	a := New(Deps{DAW: d}, Config{})
	a.mode = Auto
	_, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .5}, "maybe")
	if err != core.ErrNeedsConsent {
		t.Fatalf("Default probe err=%v, want consent", err)
	}
	a.mode = Preview
	_, err = a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .5}, "maybe")
	if err == nil {
		t.Fatal("Review should not probe an unlearned choice")
	}
}

func TestPreviewDoesNotCallValueFinder(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(float64) string { return "0" }}
	a := New(Deps{DAW: d}, Config{})
	a.mode = Preview
	if _, err := a.findValue(context.Background(), 1, "fx2.p1", "3 dB"); err == nil {
		t.Fatal("Review should not search a numeric target")
	}
	if d.valueFinderCalls != 0 {
		t.Fatalf("ValueFinder called %d times in Review", d.valueFinderCalls)
	}
}

func TestPreviewCachedValueIsVerifiedWhenApplied(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string {
		if v >= .7 {
			return "B"
		}
		return "A"
	}}
	d.vals["fx2.p1"] = .5
	st := &profileStore{profiles: map[string]core.Profile{"EQ": {Plugin: "EQ", Map: map[int]core.ProfileEntry{1: {Choices: []core.ParamChoice{{Text: "B", Value: .4}}, ChoicesPartial: true}}}}}
	a := New(Deps{DAW: d, Store: st}, Config{})
	a.mode = Preview
	out, _, err := a.propose(context.Background(), 1, "stale choice", []core.Change{{Param: "fx2.p1", Target: "B", Why: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.vals["fx2.p1"] != .5 {
		t.Fatal("proposal wrote to DAW")
	}
	_, err = a.applyPlan(context.Background(), out.(map[string]any)["plan_id"].(string))
	if err == nil || !strings.Contains(err.Error(), `instead of target "B"`) {
		t.Fatalf("stale target apply err=%v", err)
	}
	if d.vals["fx2.p1"] != .5 {
		t.Fatalf("failed target was not rolled back: %v", d.vals["fx2.p1"])
	}
}

func TestBooleanWordMapsToDiscoveredNonstandardReadout(t *testing.T) {
	d := &enumProbeDAW{memDAW: newMemDAW(), text: func(v float64) string {
		if v >= .2 && v < .3 {
			return "Active"
		}
		return "Inactive"
	}}
	a := New(Deps{DAW: d}, Config{})
	a.mode = Turbo
	v, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Enabled", Value: .5}, "on")
	if err != nil || v < .2 || v >= .3 {
		t.Fatalf("on mapping=%v err=%v, want discovered Active plateau", v, err)
	}
}

type badStatusDAW struct {
	*enumProbeDAW
	calls int
}

func (d *badStatusDAW) SetParam(_ context.Context, _ int, id core.ParamID, v float64) (core.Applied, error) {
	d.calls++
	return core.Applied{Param: id, Sent: v, Confirmed: d.vals[id], Status: core.StatusFailed}, nil
}

func TestUnconfirmedProbeIsNotCached(t *testing.T) {
	d := &badStatusDAW{enumProbeDAW: &enumProbeDAW{memDAW: newMemDAW(), text: func(float64) string { return "Only" }}}
	st := &profileStore{profiles: map[string]core.Profile{}}
	a := New(Deps{DAW: d, Store: st}, Config{})
	a.mode = Turbo
	if _, err := a.findChoice(context.Background(), 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .5}, "Only"); err == nil {
		t.Fatal("failed status must fail the probe")
	}
	if p := st.profiles["EQ"]; len(p.Map) != 0 {
		t.Fatalf("failed probe cached: %+v", p.Map)
	}
	if d.calls != 2 {
		t.Fatalf("failed probe made %d writes, want one failed probe plus one restore", d.calls)
	}
}

type cancelRestoreDAW struct {
	*enumProbeDAW
	cancel context.CancelFunc
	calls  int
}

func (d *cancelRestoreDAW) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	d.calls++
	if d.calls > 1 && ctx.Err() != nil {
		return core.Applied{}, ctx.Err()
	}
	if v == .5 && d.calls > 1 {
		return core.Applied{}, fmt.Errorf("restore failed")
	}
	r, err := d.enumProbeDAW.SetParam(ctx, track, id, v)
	if d.calls == 1 {
		d.cancel()
	}
	return r, err
}

func TestCanceledProbeReportsRestoreFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &cancelRestoreDAW{enumProbeDAW: &enumProbeDAW{memDAW: newMemDAW(), text: func(float64) string { return "Choice" }}, cancel: cancel}
	a := New(Deps{DAW: d}, Config{})
	a.mode = Turbo
	_, err := a.findChoice(ctx, 1, core.Param{ID: "fx2.p1", Name: "Mode", Value: .5}, "Choice")
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "restore failed") {
		t.Fatalf("combined cancellation/restore failure err=%v", err)
	}
}
