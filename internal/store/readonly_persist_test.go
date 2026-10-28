package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/store"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

// ampDAW is one track with an amp (fx1) that keeps none of a write from
// outside, and an EQ (fx2) that takes writes. writes counts attempts on the amp.
type ampDAW struct {
	vals   map[core.ParamID]float64
	writes int
}

func newAmpDAW() *ampDAW {
	return &ampDAW{vals: map[core.ParamID]float64{"fx1.p3": 0.5, "fx2.p1": 0.5}}
}

func (d *ampDAW) chain() []core.FX {
	return []core.FX{
		{Index: 1, Name: "Amp", Params: []core.Param{{ID: "fx1.p3", Name: "Gain", Value: d.vals["fx1.p3"], Text: "5.0"}}},
		{Index: 2, Name: "EQ", Params: []core.Param{{ID: "fx2.p1", Name: "Low", Value: d.vals["fx2.p1"], Text: "0 dB"}}},
	}
}

func (d *ampDAW) Tracks(context.Context) ([]core.Track, error) {
	return []core.Track{{Index: 1, Name: "Guitar", Audio: true}}, nil
}
func (d *ampDAW) FXChain(context.Context, int) ([]core.FX, error) { return d.chain(), nil }

func (d *ampDAW) SetParams(_ context.Context, _ int, changes []core.Change) ([]core.Applied, error) {
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

func (d *ampDAW) SetParam(ctx context.Context, _ int, id core.ParamID, v float64) (core.Applied, error) {
	out, err := d.SetParams(ctx, 1, []core.Change{{Param: id, Value: v}})
	if err != nil {
		return core.Applied{Param: id, Status: core.StatusFailed}, err
	}
	return out[0], nil
}
func (d *ampDAW) SetTrack(context.Context, int, string, float64) (float64, error) { return 0, nil }
func (d *ampDAW) Undo(context.Context) error                                      { return nil }
func (d *ampDAW) Alive(context.Context) error                                     { return nil }

type fixedRenderer struct{ dir string }

func (f fixedRenderer) Render(context.Context, int, float64) (string, error) {
	p := filepath.Join(f.dir, "r.wav")
	return p, os.WriteFile(p, []byte("wav"), 0o644)
}

// lastTool returns the newest tool result in msgs, decoded.
func lastTool(msgs []core.Msg) map[string]any {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" && len(msgs[i].Parts) > 0 {
			var out map[string]any
			_ = json.Unmarshal([]byte(msgs[i].Parts[0].Text), &out)
			return out
		}
	}
	return nil
}

// scripted plays one model reply per call, in order. Each step may look at
// the newest tool result before it answers.
type scripted struct {
	steps []func(msgs []core.Msg) core.Reply
	n     int
}

func (s *scripted) Chat(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
	if s.n >= len(s.steps) {
		return core.Reply{Text: "done"}, nil
	}
	f := s.steps[s.n]
	s.n++
	return f(msgs), nil
}

func toolCall(name string, args any) core.Reply {
	b, _ := json.Marshal(args)
	return core.Reply{ToolCalls: []core.ToolCall{{ID: "c" + name, Name: name, Args: b}}}
}

// TestReadOnlyPluginIsKnownFromTheFirstReadInALaterRun: run 1 writes the amp,
// the DAW keeps none of it, and the profile in the store records the plugin.
// Run 2 is a new agent on the same store: describe_chain says read_only from
// the start and a plan on the amp is refused without any write.
func TestReadOnlyPluginIsKnownFromTheFirstReadInALaterRun(t *testing.T) {
	dir := t.TempDir()
	st := &store.Store{Dir: dir}
	ctx := context.Background()

	// run 1
	d1 := newAmpDAW()
	run1 := &scripted{steps: []func([]core.Msg) core.Reply{
		func([]core.Msg) core.Reply {
			return toolCall("propose_tone", map[string]any{"track": 1, "goal": "gain", "changes": []map[string]any{{"param_id": "fx1.p3", "value": 0.85, "why": "gain"}}})
		},
		func(msgs []core.Msg) core.Reply {
			return toolCall("apply_plan", map[string]any{"plan_id": lastTool(msgs)["plan_id"]})
		},
	}}
	a1 := agent.New(agent.Deps{DAW: d1, LLM: run1, Store: st, Renderer: fixedRenderer{dir}}, agent.Config{})
	if _, err := a1.Run(ctx, "gain up", agent.Turbo); err != nil {
		t.Fatal(err)
	}
	if d1.writes != 1 {
		t.Fatalf("run 1 writes = %d, want 1", d1.writes)
	}
	p, err := st.Profile("Amp")
	if err != nil || p == nil || !p.ReadOnly {
		t.Fatalf("profile for Amp must be stored read-only: %+v, %v", p, err)
	}

	// run 2: new process state, same store
	d2 := newAmpDAW()
	var sawReadOnly bool
	var refused bool
	run2 := &scripted{steps: []func([]core.Msg) core.Reply{
		func([]core.Msg) core.Reply { return toolCall("describe_chain", map[string]any{"track": 1}) },
		func(msgs []core.Msg) core.Reply {
			raw, _ := json.Marshal(lastTool(msgs))
			sawReadOnly = strings.Contains(string(raw), `"read_only":true`)
			return toolCall("propose_tone", map[string]any{"track": 1, "goal": "gain", "changes": []map[string]any{{"param_id": "fx1.p3", "value": 0.85, "why": "gain"}}})
		},
		func(msgs []core.Msg) core.Reply {
			refused = lastTool(msgs)["error"] != nil
			return core.Reply{Text: "The amp is read-only; EQ is unchanged."}
		},
	}}
	a2 := agent.New(agent.Deps{DAW: d2, LLM: run2, Store: st, Renderer: fixedRenderer{dir}}, agent.Config{})
	if _, err := a2.Run(ctx, "gain up", agent.Turbo); err != nil {
		t.Fatal(err)
	}
	if !sawReadOnly {
		t.Fatal("run 2: describe_chain must mark Amp read_only from the first read")
	}
	if !refused {
		t.Fatal("run 2: a plan on the read-only amp must be refused")
	}
	if d2.writes != 0 {
		t.Fatalf("run 2: %d write(s) attempted on the read-only amp, want none", d2.writes)
	}
}

// A profile that was never learned is not read-only.
func TestUnknownPluginIsNotReadOnly(t *testing.T) {
	st := &store.Store{Dir: t.TempDir()}
	p, err := st.Profile("EQ")
	if err != nil || p != nil {
		t.Fatalf("unknown plugin: profile %+v, err %v", p, err)
	}
}
