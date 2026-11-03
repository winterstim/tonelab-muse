package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// studioDAW is a three-track session. Each track carries a stock-like chain
// of eight plugins, most with sixty controls, every control with its readout.
type studioDAW struct {
	vals map[core.ParamID]float64
}

func newStudioDAW() *studioDAW {
	d := &studioDAW{vals: map[core.ParamID]float64{}}
	for t := 1; t <= 3; t++ {
		for _, fx := range studioChain(t) {
			for _, p := range fx.Params {
				d.vals[core.ParamID(fmt.Sprintf("t%d.%s", t, p.ID))] = p.Value
			}
		}
	}
	return d
}

var studioNames = []string{"Gain", "Bass", "Middle", "Treble", "Presence", "Master", "Bright", "Channel"}

var studioPlugins = []struct {
	name   string
	params int
}{{"Gate", 20}, {"Drive", 60}, {"Amp", 60}, {"Cab", 30}, {"EQ", 60}, {"Chorus", 40}, {"Delay", 50}, {"Reverb", 60}}

func studioChain(track int) []core.FX {
	_ = track
	var out []core.FX
	for i, pl := range studioPlugins {
		fx := core.FX{Index: i + 1, Name: pl.name}
		for n := 1; n <= pl.params; n++ {
			id := core.ParamID(fmt.Sprintf("fx%d.p%d", i+1, n))
			pn := fmt.Sprintf("Param %d", n)
			if n <= len(studioNames) {
				pn = studioNames[n-1]
			}
			fx.Params = append(fx.Params, core.Param{ID: id, Name: pn, Value: 0.5, Text: "-6.0 dB 4.20 kHz", Unnamed: strings.HasPrefix(pn, "Param")})
		}
		out = append(out, fx)
	}
	return out
}

func (d *studioDAW) Tracks(context.Context) ([]core.Track, error) {
	return []core.Track{{Index: 1, Name: "Guitar", Audio: true}, {Index: 2, Name: "Lead", Audio: true}, {Index: 3, Name: "Bus", Audio: true}}, nil
}

func (d *studioDAW) FXChain(_ context.Context, track int) ([]core.FX, error) {
	ch := studioChain(track)
	for i := range ch {
		for j := range ch[i].Params {
			id := ch[i].Params[j].ID
			ch[i].Params[j].Value = d.vals[core.ParamID(fmt.Sprintf("t%d.%s", track, id))]
		}
	}
	return ch, nil
}

func (d *studioDAW) SetParam(_ context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	key := core.ParamID(fmt.Sprintf("t%d.%s", track, id))
	d.vals[key] = v
	return core.Applied{Param: id, Sent: v, Confirmed: v, Status: core.StatusConfirmed}, nil
}

func (d *studioDAW) SetTrack(context.Context, int, string, float64) (float64, error) { return 0, nil }
func (d *studioDAW) Undo(context.Context) error                                      { return nil }
func (d *studioDAW) Alive(context.Context) error                                     { return nil }

// promptCost estimates the tokens one model call reads: about four characters
// per token for text and tool definitions, and a fixed cost per image.
const imageTokens = 1500

func promptCost(msgs []core.Msg, tools []core.Tool) (tokens, images int) {
	chars := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			chars += len(p.Text)
			if p.Type == "image" {
				images++
			}
		}
		for _, tc := range m.ToolCalls {
			chars += len(tc.Args) + len(tc.Name)
		}
	}
	for _, t := range tools {
		b, _ := json.Marshal(t)
		chars += len(b)
	}
	return chars/4 + images*imageTokens, images
}

// budgetLLM plays a three-track request: the parent delegates one subagent
// per track; each subagent looks at its chain, proposes and applies a tone,
// listens once and summarises. It records what each call would send.
type budgetLLM struct {
	steps  []budgetStep
	parent int
}

type budgetStep struct {
	who    string
	tokens int
	images int
	tools  int
}

func (b *budgetLLM) Chat(_ context.Context, _ string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
	tok, imgs := promptCost(msgs, tools)
	sub := len(msgs) > 0 && strings.Contains(msgs[0].Parts[0].Text, "track sound-design engineer")
	who := "parent"
	if sub {
		who = "subagent"
	}
	b.steps = append(b.steps, budgetStep{who: who, tokens: tok, images: imgs, tools: len(tools)})
	if !sub {
		b.parent++
		if b.parent <= 3 {
			return call("delegate_track_task", map[string]any{"track": b.parent, "goal": fmt.Sprintf("Dial a tight tone on track %d.", b.parent)}), nil
		}
		return core.Reply{Text: "Tracks 1 and 2 are dialled, the bus is in."}, nil
	}
	// a subagent: the number of its own tool replies so far decides the step
	n := 0
	for _, m := range msgs {
		if m.Role == "tool" {
			n++
		}
	}
	track := 1
	if strings.Contains(fmt.Sprint(msgs), "Track 2") {
		track = 2
	} else if strings.Contains(fmt.Sprint(msgs), "Track 3") {
		track = 3
	}
	switch n {
	case 0:
		return call("describe_chain", map[string]int{"track": track}), nil
	case 1:
		return call("propose_tone", map[string]any{"track": track, "goal": "tone", "changes": []map[string]any{
			{"param_id": "fx1.p1", "value": 0.7, "why": "gain"},
			{"param_id": "fx1.p4", "value": 0.4, "why": "bass"},
		}}), nil
	case 2:
		return call("apply_plan", map[string]string{"plan_id": lastPlanID(msgs)}), nil
	case 3:
		return call("listen", map[string]int{"track": track}), nil
	case 4:
		return call("describe_chain", map[string]int{"track": track}), nil
	}
	return core.Reply{Text: fmt.Sprintf("Track %d: amp gain up, bass down, listened once.", track)}, nil
}

func lastPlanID(msgs []core.Msg) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "tool" || len(msgs[i].Parts) == 0 {
			continue
		}
		var out struct {
			PlanID string `json:"plan_id"`
		}
		if json.Unmarshal([]byte(msgs[i].Parts[0].Text), &out) == nil && out.PlanID != "" {
			return out.PlanID
		}
	}
	return ""
}

// TestMultiTrackPromptStaysBounded runs a three-track request through the
// fake studio and bounds what every model call reads. It logs the per-step
// estimate so the cost of a request is visible in test output.
func TestMultiTrackPromptStaysBounded(t *testing.T) {
	llm := &budgetLLM{}
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm, Renderer: f, Measurer: f, Visualizer: f}, Config{})
	if _, err := a.Run(context.Background(), "Make a tight rhythm tone on the Guitar track, a brighter lead on Lead, and a reverb bus", Turbo); err != nil {
		t.Fatal(err)
	}
	assertCallSavings(t, llm.steps, []int{2420, 1378, 2619, 2722, 2745, 2882, 2905, 2478, 1378, 2619, 2722, 2745, 2894, 2916, 2536, 1378, 2619, 2722, 2745, 2905, 2928, 2594})
	total, max := 0, 0
	for i, s := range llm.steps {
		total += s.tokens
		if s.tokens > max {
			max = s.tokens
		}
		t.Logf("step %2d %-8s prompt ~%6d tokens, %d images, %d tools", i+1, s.who, s.tokens, s.images, s.tools)
	}
	t.Logf("total prompt tokens ~%d over %d calls; largest single call ~%d", total, len(llm.steps), max)
}

// The bound: no single call reads more than maxCallTokens, and a three-track
// request reads far less in total than sending every step's full history
// would. Subagent calls carry no images unless they ask for one.
const maxCallTokens = 6000

func TestMultiTrackPromptBounded(t *testing.T) {
	llm := &budgetLLM{}
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm, Renderer: f, Measurer: f, Visualizer: f}, Config{})
	if _, err := a.Run(context.Background(), "Make a tight rhythm tone on the Guitar track, a brighter lead on Lead, and a reverb bus", Turbo); err != nil {
		t.Fatal(err)
	}
	if len(llm.steps) == 0 {
		t.Fatal("no model calls recorded")
	}
	for i, s := range llm.steps {
		if s.tokens > maxCallTokens {
			t.Fatalf("step %d (%s) reads ~%d tokens, bound is %d", i+1, s.who, s.tokens, maxCallTokens)
		}
		if s.who == "subagent" && s.images > 0 {
			t.Fatalf("step %d: a subagent call carried %d images without asking for one", i+1, s.images)
		}
	}
}

// describe_chain says unchanged when nothing changed since the same
// conversation last read the track, and sends the chain again after a change.
func TestDescribeChainUnchangedUntilItChanges(t *testing.T) {
	ctx := context.Background()
	daw := newStudioDAW()
	a := New(Deps{DAW: daw}, Config{})
	args := []byte(`{"track":1}`)
	first, _, err := a.call(ctx, core.ToolCall{Name: "describe_chain", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if m := first.(map[string]any); m["unchanged"] != nil {
		t.Fatalf("first read must be sent in full")
	}
	second, _, _ := a.call(ctx, core.ToolCall{Name: "describe_chain", Args: args})
	if m := second.(map[string]any); m["unchanged"] != true {
		t.Fatalf("second read with no change must say unchanged, got %v", m)
	}
	// another conversation (a subagent) has not read it yet: full chain
	sub, _, _ := a.call(withScope(ctx, "sub1-track1"), core.ToolCall{Name: "describe_chain", Args: args})
	if m := sub.(map[string]any); m["unchanged"] != nil {
		t.Fatalf("a subagent must get the chain in full the first time")
	}
	daw.vals["t1.fx1.p1"] = 0.9
	a.mu.Lock()
	delete(a.chains, 1)
	a.mu.Unlock()
	third, _, _ := a.call(ctx, core.ToolCall{Name: "describe_chain", Args: args})
	if m := third.(map[string]any); m["unchanged"] != nil {
		t.Fatalf("after a change the chain must be sent again")
	}
}

// A subagent's listen returns its numbers; the picture only with look true.
func TestSubagentListenShowsPictureOnlyWhenAsked(t *testing.T) {
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), Renderer: f, Measurer: f, Visualizer: f}, Config{})
	ctx := withScope(context.Background(), "sub1-track1")
	_, parts, err := a.call(ctx, core.ToolCall{Name: "listen", Args: []byte(`{"track":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 0 {
		t.Fatalf("subagent listen without look must carry no picture, got %d parts", len(parts))
	}
	_, parts, err = a.call(ctx, core.ToolCall{Name: "listen", Args: []byte(`{"track":1,"look":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) == 0 {
		t.Fatal("subagent listen with look must show the picture")
	}
}

// Across 20 steps of describe and listen, the history a call sends stops
// growing once superseded results are stubbed: the call at step 20 is not
// much bigger than the call at step 4, and only one spectrogram is kept.
func TestHistoryStaysBoundedAcrossTwentySteps(t *testing.T) {
	var costs []int
	var images []int
	n := 0
	llm := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
		tok, imgs := promptCost(msgs, tools)
		costs = append(costs, tok)
		images = append(images, imgs)
		n++
		if n > 20 {
			return core.Reply{Text: "done"}, nil
		}
		if n%2 == 1 {
			return call("describe_chain", map[string]int{"track": 1}), nil
		}
		return call("listen", map[string]int{"track": 1}), nil
	}}
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm, Renderer: f, Measurer: f, Visualizer: f}, Config{MaxSteps: 24})
	if _, err := a.Run(context.Background(), "tighten the tone", Auto); err != nil {
		t.Fatal(err)
	}
	if len(costs) < 20 {
		t.Fatalf("only %d calls", len(costs))
	}
	last := costs[len(costs)-1]
	if last > costs[3]+2*imageTokens+2000 {
		t.Fatalf("history grows: call 4 ~%d tokens, last call ~%d", costs[3], last)
	}
	for i, k := range images {
		if k > 1 {
			t.Fatalf("call %d sends %d spectrograms, want at most the newest one", i+1, k)
		}
	}
}

// singleBudgetLLM records the same single-track workflow for before/after cost checks.
type singleBudgetLLM struct {
	steps []budgetStep
	n     int
}

func (b *singleBudgetLLM) Chat(_ context.Context, _ string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
	tok, imgs := promptCost(msgs, tools)
	b.steps = append(b.steps, budgetStep{who: "single", tokens: tok, images: imgs, tools: len(tools)})
	b.n++
	switch b.n {
	case 1:
		return call("describe_chain", map[string]int{"track": 1}), nil
	case 2:
		return call("propose_tone", map[string]any{"track": 1, "goal": "tone", "changes": []map[string]any{{"param_id": "fx1.p1", "value": .7, "why": "gain"}}}), nil
	case 3:
		return call("apply_plan", map[string]string{"plan_id": lastPlanID(msgs)}), nil
	case 4:
		return call("listen", map[string]int{"track": 1}), nil
	default:
		return core.Reply{Text: "Adjusted the track and checked it."}, nil
	}
}
func TestSingleTrackPromptCost(t *testing.T) {
	llm := &singleBudgetLLM{}
	f := files{t.TempDir()}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm, Renderer: f, Measurer: f, Visualizer: f}, Config{})
	if _, err := a.Run(context.Background(), "Tighten this track", Turbo); err != nil {
		t.Fatal(err)
	}
	assertCallSavings(t, llm.steps, []int{2403, 3644, 3710, 3731, 5416})
	for i, s := range llm.steps {
		t.Logf("call %d tokens ~%d images %d tools %d", i+1, s.tokens, s.images, s.tools)
	}
}

// Baselines captured from main 79d03d9 with the same deterministic fake studio.
// These are chars/4 plus a fixed image estimate, not provider billing tokens.
func assertCallSavings(t *testing.T, steps []budgetStep, baseline []int) {
	t.Helper()
	if len(steps) != len(baseline) {
		t.Fatalf("call count changed: %d vs baseline %d", len(steps), len(baseline))
	}
	for i, step := range steps {
		if step.tokens*100 > baseline[i]*60 {
			t.Errorf("call %d: %d exceeds 60%% of baseline %d", i+1, step.tokens, baseline[i])
		}
	}
}
