package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// ordProfiles keeps one profile per plugin name, as the real store does.
type ordProfiles struct{ m map[string]*core.Profile }

func (s *ordProfiles) SaveProfile(p core.Profile) error { s.m[p.Plugin] = &p; return nil }
func (s *ordProfiles) Profile(name string) (*core.Profile, error) {
	return s.m[name], nil
}
func (s *ordProfiles) Append(string, core.Event) error { return nil }

// ampTrackDAW is track 1 with one read-only amp (fx1). Writes to fx1 are
// refused the way the DAW refuses them; writes counts the attempts.
type ampTrackDAW struct {
	chain  []core.FX
	vals   map[core.ParamID]float64
	writes int
}

func newAmpTrackDAW() *ampTrackDAW {
	return &ampTrackDAW{
		chain: []core.FX{{Index: 1, Name: "Amp", Params: []core.Param{{ID: "fx1.p3", Name: "Gain", Value: 0.5, Text: "5.0"}}}},
		vals:  map[core.ParamID]float64{"fx1.p3": 0.5},
	}
}

func (d *ampTrackDAW) Tracks(context.Context) ([]core.Track, error) {
	return []core.Track{{Index: 1, Name: "Guitar", Audio: true}, {Index: 2, Name: "Lead", Audio: true}}, nil
}
func (d *ampTrackDAW) FXChain(context.Context, int) ([]core.FX, error) {
	out := make([]core.FX, len(d.chain))
	copy(out, d.chain)
	return out, nil
}
func (d *ampTrackDAW) SetParams(_ context.Context, _ int, changes []core.Change) ([]core.Applied, error) {
	for _, c := range changes {
		if strings.HasPrefix(string(c.Param), "fx1.") {
			d.writes++
			return nil, core.ReadOnlyError{FX: 1, Detail: "fx1.p3 read back 0.500 after writing 0.850"}
		}
	}
	var out []core.Applied
	for _, c := range changes {
		d.vals[c.Param] = c.Value
		out = append(out, core.Applied{Param: c.Param, Sent: c.Value, Confirmed: c.Value, Status: core.StatusConfirmed})
	}
	return out, nil
}
func (d *ampTrackDAW) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	out, err := d.SetParams(ctx, track, []core.Change{{Param: id, Value: v}})
	if err != nil {
		return core.Applied{Param: id, Status: core.StatusFailed}, err
	}
	return out[0], nil
}
func (d *ampTrackDAW) SetTrack(context.Context, int, string, float64) (float64, error) { return 0, nil }
func (d *ampTrackDAW) Undo(context.Context) error                                      { return nil }
func (d *ampTrackDAW) Alive(context.Context) error                                     { return nil }

// addTrackDAW adds plugins to the amp track's chain for ChainEditor.
type addTrackDAW struct{ d *ampTrackDAW }

func (a addTrackDAW) Catalog(context.Context) ([]core.CatalogEntry, error) {
	return []core.CatalogEntry{{Name: "EQ", Kind: "EQ"}}, nil
}
func (a addTrackDAW) AddFX(_ context.Context, _ int, name string, _ int) (int, error) {
	i := len(a.d.chain) + 1
	a.d.chain = append(a.d.chain, core.FX{Index: i, Name: name, Params: []core.Param{{ID: core.ParamID(fmt.Sprintf("fx%d.p1", i)), Name: "Low", Value: 0.5, Text: "0 dB"}}})
	return i, nil
}
func (a addTrackDAW) RemoveFX(context.Context, int, int) (string, error) { return "", nil }

// TestParentLastThreeStepsOfferChecksOnly: the parent keeps calling
// delegate_track_task. From its third-last step on, the model is offered only
// the checks, so the final listen or check_mix and the answer always have room.
func TestParentLastThreeStepsOfferChecksOnly(t *testing.T) {
	var parentTools [][]string
	llm := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
		if strings.Contains(msgs[0].Parts[0].Text, "track sound-design engineer") {
			return core.Reply{Text: "Track 1 is set."}, nil
		}
		if len(tools) == 0 {
			return core.Reply{Text: "The final checks are complete."}, nil
		}
		var names []string
		for _, t := range tools {
			names = append(names, t.Name)
		}
		parentTools = append(parentTools, names)
		return call("delegate_track_task", map[string]any{"track": 1, "goal": "tone"}), nil
	}}
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm, Renderer: f, Measurer: f, Visualizer: f, Mix: oneClash{}}, Config{MaxSteps: 6})
	if _, err := a.Run(context.Background(), "two tracks", Turbo); err != nil {
		t.Fatal(err)
	}
	if len(parentTools) < 6 {
		t.Fatalf("parent made %d calls, want 6", len(parentTools))
	}
	has := func(names []string, want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}
	for i, names := range parentTools[:3] {
		if !has(names, "delegate_track_task") {
			t.Fatalf("step %d must still offer delegation", i+1)
		}
	}
	for i, names := range parentTools[3:6] {
		if has(names, "delegate_track_task") || has(names, "apply_plan") || has(names, "propose_tone") {
			t.Fatalf("step %d (reserved) offers changes: %v", i+4, names)
		}
		if !has(names, "listen") || !has(names, "check_mix") {
			t.Fatalf("step %d (reserved) must offer the final checks: %v", i+4, names)
		}
	}
}

// TestSubagentOnReadOnlyTrackAddsOnePluginThenApplies: the amp is known to be
// read-only from an earlier run. The subagent does not touch it, adds one
// stock EQ, proposes on it, applies and listens.
func TestSubagentOnReadOnlyTrackAddsOnePluginThenApplies(t *testing.T) {
	ps := &ordProfiles{m: map[string]*core.Profile{"Amp": {Plugin: "Amp", ReadOnly: true}}}
	d := newAmpTrackDAW()
	f := files{t.TempDir()}
	var sawAmpWrite bool
	steps := []func(msgs []core.Msg) core.Reply{
		func([]core.Msg) core.Reply { return call("describe_chain", map[string]int{"track": 1}) },
		func([]core.Msg) core.Reply { return call("list_plugins", map[string]string{"query": "eq"}) },
		func([]core.Msg) core.Reply { return call("add_plugin", map[string]any{"track": 1, "plugin": "EQ"}) },
		func([]core.Msg) core.Reply {
			return call("propose_tone", map[string]any{"track": 1, "goal": "brighter", "changes": []map[string]any{
				{"param_id": "fx2.p1", "value": 0.8, "why": "air"},
			}})
		},
		func(msgs []core.Msg) core.Reply {
			return call("apply_plan", map[string]string{"plan_id": lastPlanID(msgs)})
		},
		func([]core.Msg) core.Reply { return call("listen", map[string]int{"track": 1}) },
	}
	n := 0
	llm := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
		if !strings.Contains(msgs[0].Parts[0].Text, "track sound-design engineer") {
			return core.Reply{Text: "ok"}, nil
		}
		if n >= len(steps) {
			return core.Reply{Text: "EQ added on track 1 and brightened; applied."}, nil
		}
		r := steps[n](msgs)
		n++
		return r, nil
	}}
	a := New(Deps{DAW: d, LLM: llm, Store: ps, Chain: addTrackDAW{d}, Renderer: f, Measurer: f, Visualizer: f}, Config{})
	res, _, err := a.call(context.Background(), core.ToolCall{Name: "delegate_track_task", Args: json.RawMessage(`{"track":1,"goal":"brighter lead"}`)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var got struct {
		Applied   int  `json:"applied"`
		Steps     int  `json:"steps"`
		Unapplied bool `json:"unapplied"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if d.writes != 0 {
		sawAmpWrite = true
	}
	if sawAmpWrite {
		t.Fatalf("the read-only amp was written %d time(s)", d.writes)
	}
	if got.Applied != 1 || got.Unapplied {
		t.Fatalf("subagent must apply its tone: %s", raw)
	}
	if got.Steps > subagentStepCap {
		t.Fatalf("subagent used %d steps, cap %d", got.Steps, subagentStepCap)
	}
	if d.vals["fx2.p1"] != 0.8 {
		t.Fatalf("EQ value = %v, want the applied 0.8", d.vals["fx2.p1"])
	}
}

// TestHistoryStaysBoundedOverTwentySteps: a fake loop of 20 steps, each step
// adding a describe_chain, propose_tone, apply_plan and listen result (about
// 2000 characters each) and an image. After every step the history is trimmed
// the way the loop does it; the total must stay near the newest of each kind.
func TestHistoryStaysBoundedOverTwentySteps(t *testing.T) {
	pad := strings.Repeat("x", 2000)
	var msgs []core.Msg
	msgs = append(msgs, core.Text("system", "sys"), core.Text("user", "req"))
	size := func() int {
		n := 0
		images := 0
		for _, m := range msgs {
			for _, p := range m.Parts {
				n += len(p.Text)
				if p.Type == "image" {
					images++
				}
			}
		}
		if images > 1 {
			t.Fatalf("%d images kept, want at most the newest one", images)
		}
		return n
	}
	var atEight int
	stubs := 0
	for step := 1; step <= 20; step++ {
		k := (step - 1) / 4
		plan := fmt.Sprintf("p%d", k+1)
		var name string
		var args any
		var result string
		switch (step - 1) % 4 {
		case 0:
			name, args, result = "describe_chain", map[string]int{"track": 1}, `{"fx":[],"pad":"`+pad+`"}`
		case 1:
			name, args, result = "propose_tone", map[string]any{"track": 1, "goal": "g", "changes": []any{}}, `{"plan_id":"`+plan+`","pad":"`+pad+`"}`
		case 2:
			name, args, result = "apply_plan", map[string]string{"plan_id": plan}, `{"applied":true,"pad":"`+pad+`"}`
		default:
			name, args, result = "listen", map[string]int{"track": 1}, `{"bands_db":{},"pad":"`+pad+`"}`
		}
		id := fmt.Sprintf("c%d", step)
		b, _ := json.Marshal(args)
		msgs = append(msgs, core.Msg{Role: "assistant", ToolCalls: []core.ToolCall{{ID: id, Name: name, Args: b}}})
		msgs = append(msgs, core.Msg{Role: "tool", ToolCallID: id, Parts: []core.Part{{Type: "text", Text: result}}})
		msgs = append(msgs, core.Msg{Role: "user", Parts: []core.Part{{Type: "image", Text: "png"}}})
		trimHistory(msgs)
		if step == 8 {
			atEight = size()
		}
	}
	total := size()
	// Newest of each kind stays whole: describe_chain, listen, apply_plan.
	// Everything else is a one-line stub.
	if max := 3*2100 + 20*150; total > max {
		t.Fatalf("history after 20 steps is %d chars, bound %d", total, max)
	}
	if total > atEight+2500 {
		t.Fatalf("history grew from %d (step 8) to %d (step 20): not bounded", atEight, total)
	}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, "superseded by step") || strings.Contains(p.Text, "applied at step") {
				stubs++
			}
		}
	}
	if stubs < 14 {
		t.Fatalf("only %d stubs, want the superseded results stubbed", stubs)
	}
}
