package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// enumDAW is memDAW with a stepped enum on fx1.p3 (7 choices, 6 steps) whose
// readout names the choice the value lands on.
type enumDAW struct{ *memDAW }

var enumNames = []string{"Clean", "Crunch", "Lead", "Drive", "Hi Gain", "Heavy", "Metal"}

func (d enumDAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	ch, err := d.memDAW.FXChain(ctx, track)
	if err != nil {
		return nil, err
	}
	for i := range ch[0].Params {
		p := &ch[0].Params[i]
		if p.ID != "fx1.p3" {
			continue
		}
		p.Steps = 6
		p.Text = enumNames[int(p.Value*6+0.5)]
	}
	return ch, nil
}

type capture struct{ evs []core.Event }

func (c *capture) Emit(e core.Event) { c.evs = append(c.evs, e) }

func TestValidateSnapsSteppedValues(t *testing.T) {
	ch := []core.FX{{Index: 1, Name: "Amp", Params: []core.Param{
		{ID: "fx1.p1", Name: "Amp Type", Steps: 6, Value: 0},
		{ID: "fx1.p2", Name: "Gain", Value: 0},
	}}}
	plan, warns, err := Validate(1, "g", []core.Change{
		{Param: "fx1.p1", Value: 0.844, Why: "heavy"},
		{Param: "fx1.p2", Value: 0.844, Why: "gain"},
	}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Changes[0].Value; got != 5.0/6 {
		t.Fatalf("stepped value sent %v, want 5/6", got)
	}
	if got := plan.Changes[1].Value; got != 0.844 {
		t.Fatalf("continuous value changed to %v", got)
	}
	if len(warns) != 1 {
		t.Fatalf("warnings %v, want one snap warning", warns)
	}
}

func TestApplyPlanReportsTheDAWReadout(t *testing.T) {
	d := enumDAW{newMemDAW()}
	cap := &capture{}
	a := New(Deps{DAW: d, Sink: cap}, Config{})
	out, _, err := a.propose(context.Background(), 1, "heavy", []core.Change{{Param: "fx1.p3", Value: 0.844, Why: "heavy"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["plan_id"].(string)
	if _, err := a.applyPlan(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	for _, e := range cap.evs {
		if m, ok := e.Data.(map[string]any); ok && e.Kind == "change" {
			got = m
		}
	}
	if got == nil {
		t.Fatal("no change event")
	}
	if got["text"] != "Heavy" {
		t.Fatalf("event text %v, want the DAW's readout Heavy", got["text"])
	}
	if c := got["confirmed"].(float64); fmt.Sprintf("%.3f", c) != "0.833" {
		t.Fatalf("confirmed %v, want 0.833", c)
	}
}

// Names are the DAW's to choose: snapping must not depend on any plugin or
// parameter name.
func TestValidateSnapsWithAnyNames(t *testing.T) {
	ch := []core.FX{{Index: 1, Name: "Zq plugin", Params: []core.Param{
		{ID: "fx1.p1", Name: "Qwerty", Steps: 6, Value: 0},
	}}}
	plan, _, err := Validate(1, "g", []core.Change{{Param: "fx1.p1", Value: 0.844, Why: "w"}}, ch)
	if err != nil || plan.Changes[0].Value != 5.0/6 {
		t.Fatalf("plan %+v err %v, want snap to 5/6 with any names", plan, err)
	}
}
