package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// memDAW is an in-memory DAW: one guitar track with an amp (two dead
// slots, gain, bass, bright switch) and an EQ.
type memDAW struct {
	mu   sync.Mutex
	vals map[core.ParamID]float64
	dead map[core.ParamID]bool
}

func newMemDAW() *memDAW {
	return &memDAW{
		vals: map[core.ParamID]float64{"fx1.p1": 0, "fx1.p2": 0, "fx1.p3": 0.5, "fx1.p4": 0.5, "fx1.p5": 0, "fx2.p1": 0.5},
		dead: map[core.ParamID]bool{"fx1.p1": true},
	}
}

func (d *memDAW) Tracks(context.Context) ([]core.Track, error) {
	return []core.Track{{Index: 1, Name: "Guitar"}}, nil
}

func (d *memDAW) FXChain(context.Context, int) ([]core.FX, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := func(id core.ParamID, name, kind string) core.Param {
		return core.Param{ID: id, Name: name, Kind: kind, Value: d.vals[id], Text: "x", Unnamed: strings.HasPrefix(name, "Param")}
	}
	return []core.FX{
		{Index: 1, Name: "Amp", Params: []core.Param{p("fx1.p1", "Param 1", ""), p("fx1.p2", "Param 2", ""), p("fx1.p3", "Gain", ""), p("fx1.p4", "Bass", ""), p("fx1.p5", "Bright", "switch")}},
		{Index: 2, Name: "EQ", Params: []core.Param{p("fx2.p1", "Gain-Band 2", "")}},
	}, nil
}

func (d *memDAW) SetParam(_ context.Context, _ int, id core.ParamID, v float64) (core.Applied, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.vals[id]; !ok {
		return core.Applied{Param: id, Sent: v, Status: core.StatusUnconfirmed}, core.ErrUnconfirmed
	}
	if !d.dead[id] {
		d.vals[id] = v
	}
	return core.Applied{Param: id, Sent: v, Confirmed: d.vals[id], Status: core.StatusConfirmed}, nil
}

func (d *memDAW) SetTrack(context.Context, int, string, float64) (float64, error) { return 0, nil }
func (d *memDAW) Undo(context.Context) error                                      { return nil }
func (d *memDAW) Alive(context.Context) error                                     { return nil }

// script is a fake model: each turn returns the next reply and records
// what it was shown.
type script struct {
	replies []core.Reply
	seen    [][]core.Msg
}

func (s *script) Chat(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
	s.seen = append(s.seen, msgs)
	if len(s.replies) == 0 {
		return core.Reply{}, errors.New("script ran out")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func call(name string, args any) core.Reply {
	b, _ := json.Marshal(args)
	return core.Reply{ToolCalls: []core.ToolCall{{ID: name, Name: name, Args: b}}}
}

type files struct{ dir string }

func (f files) Render(context.Context, int, float64) (string, error) {
	p := filepath.Join(f.dir, "r.wav")
	return p, os.WriteFile(p, []byte("wav"), 0o644)
}
func (f files) Measure(string) (core.Measure, error) {
	return core.Measure{Bands: core.Bands{Mud: -2}}, nil
}
func (f files) Spectrogram(wavs, _ []string) (string, error) {
	p := filepath.Join(f.dir, "s.png")
	w, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer w.Close()
	return p, png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
}
func (f files) CaptureFX(context.Context, int, int) (string, error) { return f.Spectrogram(nil, nil) }

type sink struct{ kinds []string }

func (s *sink) Emit(e core.Event) { s.kinds = append(s.kinds, e.Kind) }

func TestToneRequestEndToEnd(t *testing.T) {
	daw, f, ev := newMemDAW(), files{t.TempDir()}, &sink{}
	llm := &script{replies: []core.Reply{
		call("describe_chain", map[string]int{"track": 1}),
		call("propose_tone", map[string]any{"track": 1, "goal": "metal", "changes": []map[string]any{
			{"param_id": "fx1.p3", "value": 0.85, "why": "more gain"},
			{"param_id": "fx1.p5", "value": 0.7, "why": "bite"},
			{"param_id": "fx2.p1", "value": 1.4, "why": "cut mud"},
		}}),
		call("apply_plan", map[string]string{"plan_id": "p1"}),
		call("listen", map[string]any{"track": 1, "look": true}),
		{Text: "Gain up, bright on, mid band set."},
	}}
	a := New(Deps{DAW: daw, LLM: llm, Renderer: f, Measurer: f, Visualizer: f, Sink: ev}, Config{})
	out, err := a.Run(context.Background(), "proper metal tone", Auto)
	if err != nil || out != "Gain up, bright on, mid band set." {
		t.Fatalf("run = %q, %v", out, err)
	}
	if daw.vals["fx1.p3"] != 0.85 || daw.vals["fx1.p5"] != 1 || daw.vals["fx2.p1"] != 1 {
		t.Fatalf("daw = %v (switch must snap to 1, value must clamp to 1)", daw.vals)
	}
	// the listen turn must show the model an image
	last := llm.seen[len(llm.seen)-1]
	var img bool
	for _, p := range last[len(last)-1].Parts {
		img = img || p.Type == "image"
	}
	if !img {
		t.Fatal("spectrogram was not attached after listen")
	}
	if k := strings.Join(ev.kinds, ","); !strings.Contains(k, ",plan,") || !strings.Contains(k, ",change,") || !strings.Contains(k, ",listen,") {
		t.Fatalf("events = %v", ev.kinds)
	}
	if _, err := a.Undo(context.Background()); err != nil || daw.vals["fx1.p3"] != 0.5 || daw.vals["fx2.p1"] != 0.5 {
		t.Fatalf("undo: %v, daw = %v", err, daw.vals)
	}
}

func TestUnknownIDComesBackAsToolError(t *testing.T) {
	llm := &script{replies: []core.Reply{
		call("propose_tone", map[string]any{"track": 1, "goal": "x", "changes": []map[string]any{{"param_id": "fx9.p9", "value": 0.5, "why": "?"}}}),
		{Text: "sorry"},
	}}
	a := New(Deps{DAW: newMemDAW(), LLM: llm}, Config{})
	if _, err := a.Run(context.Background(), "x", Auto); err != nil {
		t.Fatal(err)
	}
	tool := llm.seen[1][len(llm.seen[1])-1]
	if tool.Role != "tool" || !strings.Contains(tool.Parts[0].Text, "unknown parameter") {
		t.Fatalf("tool message = %+v", tool)
	}
}

func TestPreviewStopsBeforeApplying(t *testing.T) {
	daw := newMemDAW()
	llm := &script{replies: []core.Reply{
		call("propose_tone", map[string]any{"track": 1, "goal": "x", "changes": []map[string]any{{"param_id": "fx1.p3", "value": 0.9, "why": "g"}}}),
	}}
	a := New(Deps{DAW: daw, LLM: llm}, Config{})
	if _, err := a.Run(context.Background(), "x", Preview); err != nil {
		t.Fatal(err)
	}
	if daw.vals["fx1.p3"] != 0.5 {
		t.Fatal("preview changed the DAW")
	}
	if _, err := a.ApplyPlan(context.Background(), "p1"); err != nil || daw.vals["fx1.p3"] != 0.9 {
		t.Fatalf("apply after preview: %v, %v", err, daw.vals)
	}
}

type memStore struct{ p *core.Profile }

func (m *memStore) SaveProfile(p core.Profile) error      { m.p = &p; return nil }
func (m *memStore) Profile(string) (*core.Profile, error) { return m.p, nil }
func (m *memStore) Append(string, core.Event) error       { return nil }

func TestLearnSkipsDeadSlotsAndLabelsLive(t *testing.T) {
	daw, f, st := newMemDAW(), files{t.TempDir()}, &memStore{}
	daw.vals["fx1.p2"] = 0.3 // live unnamed slot
	llm := &script{replies: []core.Reply{
		call("learn_plugin", map[string]any{"track": 1, "fx": 1}),
		{Text: "```json\n{\"moved\": true, \"label\": \"PRESENCE\", \"kind\": \"knob\", \"section\": \"Amp\", \"confidence\": 0.9}\n```"},
		{Text: "learned"},
	}}
	a := New(Deps{DAW: daw, LLM: llm, Capturer: f, Store: st}, Config{})
	if _, err := a.Run(context.Background(), "learn the amp", Auto); err != nil {
		t.Fatal(err)
	}
	if st.p == nil || st.p.Map[2].Label != "PRESENCE" || len(st.p.Map) != 1 {
		t.Fatalf("profile = %+v", st.p)
	}
	if daw.vals["fx1.p2"] != 0.3 {
		t.Fatalf("learn did not restore the value: %v", daw.vals["fx1.p2"])
	}
	ch, _ := a.chain(context.Background(), 1, true)
	if p, _ := find(ch, "fx1.p2"); p.Name != "PRESENCE" || p.Unnamed {
		t.Fatalf("label not merged: %+v", p)
	}
}

func TestValidate(t *testing.T) {
	ch, _ := newMemDAW().FXChain(context.Background(), 1)
	plan, warns, err := Validate(1, "g", []core.Change{{Param: "fx1.p3", Value: 0.2}, {Param: "fx1.p3", Value: 0.4}, {Param: "fx1.p5", Value: 0.4}}, ch)
	if err != nil || len(plan.Changes) != 2 || plan.Changes[0].Value != 0.4 || plan.Changes[1].Value != 0 || len(warns) != 2 {
		t.Fatalf("plan %+v warns %v err %v", plan, warns, err)
	}
}

// silentRig: the track is silent while fx 1 is active.
type silentRig struct {
	*memDAW
	files
	bypassed map[int]bool
}

func (s *silentRig) SetBypass(_ context.Context, _ int, fx int, b bool) error {
	s.bypassed[fx] = b
	return nil
}
func (s *silentRig) Measure(string) (core.Measure, error) {
	if !s.bypassed[1] {
		return core.Measure{}, core.ErrSilent
	}
	return core.Measure{}, nil
}

func TestListenFindsTheSilencingPlugin(t *testing.T) {
	rig := &silentRig{memDAW: newMemDAW(), files: files{t.TempDir()}, bypassed: map[int]bool{}}
	llm := &script{replies: []core.Reply{call("listen", map[string]int{"track": 1}), {Text: "Amp is muting."}}}
	a := New(Deps{DAW: rig, LLM: llm, Renderer: rig.files, Measurer: rig, Visualizer: rig.files}, Config{})
	if _, err := a.Run(context.Background(), "how does it sound", Auto); err != nil {
		t.Fatal(err)
	}
	res := llm.seen[1][len(llm.seen[1])-1].Parts[0].Text
	if !strings.Contains(res, `"culprit":"Amp"`) {
		t.Fatalf("tool result = %s", res)
	}
	if rig.bypassed[1] || rig.bypassed[2] {
		t.Fatalf("plugins left bypassed: %v", rig.bypassed)
	}
}

func TestBlindPlannerGetsNumbersOnly(t *testing.T) {
	daw, f := newMemDAW(), files{t.TempDir()}
	llm := &script{replies: []core.Reply{call("listen", map[string]int{"track": 1}), {Text: "ok"}}}
	a := New(Deps{DAW: daw, LLM: llm, Renderer: f, Measurer: f, Visualizer: f}, Config{NoImages: true})
	if _, err := a.Run(context.Background(), "how does it sound", Auto); err != nil {
		t.Fatal(err)
	}
	for _, m := range llm.seen[len(llm.seen)-1] {
		for _, p := range m.Parts {
			if p.Type == "image" {
				t.Fatal("an image reached a planner that cannot see")
			}
		}
	}
}

func TestToolsByMode(t *testing.T) {
	fc := &fakeChain{}
	a := New(Deps{DAW: newMemDAW(), Chain: fc}, Config{})

	hasTool := func(ts []core.Tool, name string) bool {
		for _, tool := range ts {
			if tool.Name == name {
				return true
			}
		}
		return false
	}

	prevTools := a.tools(Preview)
	if hasTool(prevTools, "apply_plan") || hasTool(prevTools, "undo") || hasTool(prevTools, "move_plugin") || hasTool(prevTools, "load_preset") {
		t.Fatalf("Preview mode must not have mutating tools: %+v", prevTools)
	}
	if !hasTool(prevTools, "propose_tone") || !hasTool(prevTools, "list_presets") {
		t.Fatalf("Preview mode missing read/plan tools: %+v", prevTools)
	}

	autoTools := a.tools(Auto)
	if !hasTool(autoTools, "apply_plan") || !hasTool(autoTools, "move_plugin") || !hasTool(autoTools, "load_preset") || !hasTool(autoTools, "list_presets") {
		t.Fatalf("Auto mode missing expected tools: %+v", autoTools)
	}

	turboTools := a.tools(Turbo)
	if !hasTool(turboTools, "apply_plan") || !hasTool(turboTools, "move_plugin") || !hasTool(turboTools, "load_preset") || !hasTool(turboTools, "list_presets") {
		t.Fatalf("Turbo mode missing expected tools: %+v", turboTools)
	}
}

type trackingDAW struct {
	*memDAW
	mu         sync.Mutex
	muteEvents []float64
}

func (d *trackingDAW) SetTrack(_ context.Context, _ int, prop string, v float64) (float64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prop == "mute" {
		d.muteEvents = append(d.muteEvents, v)
	}
	return v, nil
}

func (d *trackingDAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	ch, err := d.memDAW.FXChain(ctx, track)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range ch {
		for j := range ch[i].Params {
			if ch[i].Params[j].ID == "fx1.p3" {
				val := d.vals["fx1.p3"]
				ch[i].Params[j].Text = fmt.Sprintf("%.1f dB", (val-0.5)*10)
			}
		}
	}
	return ch, nil
}

type valueFinderDAW struct {
	*memDAW
	calledTarget string
}

func (v *valueFinderDAW) FindValue(_ context.Context, _ int, _ core.ParamID, target string) (float64, error) {
	v.calledTarget = target
	if target == "5.0 dB" {
		return 0.85, nil
	}
	return 0, errors.New("not found")
}

func TestValueFinderFastPath(t *testing.T) {
	vf := &valueFinderDAW{memDAW: newMemDAW()}
	a := New(Deps{DAW: vf}, Config{})
	a.Allow(kindProbe)

	val, err := a.findValue(context.Background(), 1, "fx1.p3", "5.0 dB")
	if err != nil {
		t.Fatalf("findValue: %v", err)
	}
	if val != 0.85 {
		t.Fatalf("got val = %v, want 0.85", val)
	}
	if vf.calledTarget != "5.0 dB" {
		t.Fatalf("expected FindValue to be called with 5.0 dB, got %q", vf.calledTarget)
	}
}

func TestFindValueMuteGuard(t *testing.T) {
	td := &trackingDAW{memDAW: newMemDAW()}
	a := New(Deps{DAW: td}, Config{})
	a.Allow(kindProbe)

	val, err := a.findValue(context.Background(), 1, "fx1.p3", "2.0 dB")
	if err != nil {
		t.Fatalf("findValue: %v", err)
	}
	// "2.0 dB" corresponds to (v - 0.5)*10 = 2 => v = 0.7
	if math.Abs(val-0.7) > 0.05 {
		t.Fatalf("got val = %v, expected ~0.7", val)
	}

	td.mu.Lock()
	mutes := td.muteEvents
	finalVal := td.vals["fx1.p3"]
	td.mu.Unlock()

	if len(mutes) < 2 {
		t.Fatalf("expected at least 2 mute events, got: %v", mutes)
	}
	if mutes[0] != 1 {
		t.Fatalf("expected track to be muted first (mute=1), got: %v", mutes[0])
	}
	if mutes[len(mutes)-1] != 0 {
		t.Fatalf("expected track to be unmuted at the end (mute=0), got: %v", mutes[len(mutes)-1])
	}
	if finalVal != 0.5 {
		t.Fatalf("expected original value 0.5 to be restored, got %v", finalVal)
	}
}

type fakePresetMoverChain struct {
	fakeChain
	current string
	list    []string
	loaded  string
	from    int
	to      int
}

func (f *fakePresetMoverChain) Presets(context.Context, int, int) (string, []string, error) {
	return f.current, f.list, nil
}
func (f *fakePresetMoverChain) SetPreset(_ context.Context, _, _ int, preset string) error {
	f.loaded = preset
	return nil
}
func (f *fakePresetMoverChain) MoveFX(_ context.Context, _, from, to int) error {
	f.from = from
	f.to = to
	return nil
}

func TestToolsMoveAndPresetsInLoop(t *testing.T) {
	chain := &fakePresetMoverChain{
		current: "Crunch",
		list:    []string{"Clean", "Crunch", "Lead"},
	}
	daw := newMemDAW()
	llm := &script{replies: []core.Reply{
		call("list_presets", map[string]int{"track": 1, "fx": 1}),
		call("load_preset", map[string]any{"track": 1, "fx": 1, "preset": "Lead"}),
		call("move_plugin", map[string]any{"track": 1, "fx": 1, "to_position": 2}),
		{Text: "presets listed, loaded, and moved"},
	}}
	a := New(Deps{DAW: daw, LLM: llm, Chain: chain}, Config{})
	out, err := a.Run(context.Background(), "tweak chain", Auto)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "presets listed, loaded, and moved" {
		t.Fatalf("unexpected out: %q", out)
	}
	if chain.loaded != "Lead" {
		t.Fatalf("expected loaded Lead, got %q", chain.loaded)
	}
	if chain.from != 1 || chain.to != 2 {
		t.Fatalf("expected moved 1 -> 2, got %d -> %d", chain.from, chain.to)
	}
}

func TestDelegateTrackSubagent(t *testing.T) {
	daw := newMemDAW()
	sk := &sink{}

	type callRec struct {
		msgs  []core.Msg
		tools []core.Tool
	}
	var (
		mu      sync.Mutex
		calls   []callRec
		replies []core.Reply
	)

	replies = []core.Reply{
		// 1. Parent receives user request: delegates to track 1
		call("delegate_track_task", map[string]any{"track": 1, "goal": "Dial tight heavy rhythm"}),

		// 2. Track 1 Subagent Turn 1: describes chain on track 1
		call("describe_chain", map[string]any{"track": 1}),

		// 3. Track 1 Subagent Turn 2: proposes tone for track 1
		call("propose_tone", map[string]any{
			"track": 1,
			"goal":  "Tight rhythm",
			"changes": []map[string]any{
				{"param_id": "fx1.p3", "value": 0.85, "why": "more gain"},
				{"param_id": "fx1.p4", "value": 0.4, "why": "tight bass"},
			},
		}),

		// 4. Track 1 Subagent Turn 3: applies plan
		call("apply_plan", map[string]any{"plan_id": "p1"}),

		// 5. Track 1 Subagent Turn 4: finishes and provides concise explanation
		{Text: "Track 1 rhythm chain tuned with aggressive gain and tight low end."},

		// 6. Parent receives Track 1 subagent summary: delegates to track 2
		call("delegate_track_task", map[string]any{"track": 2, "goal": "Dial singing lead"}),

		// 7. Track 2 Subagent Turn 1: finishes directly with summary
		{Text: "Track 2 lead tone tuned with smooth sustain."},

		// 8. Parent receives Track 2 subagent summary: final session answer
		{Text: "Engineered both Track 1 rhythm and Track 2 lead successfully."},
	}

	chatFn := func(_ context.Context, _ string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, callRec{msgs: msgs, tools: tools})
		if len(replies) == 0 {
			return core.Reply{}, errors.New("replies exhausted")
		}
		r := replies[0]
		replies = replies[1:]
		return r, nil
	}

	llm := &customScript{chat: chatFn}
	a := New(Deps{DAW: daw, LLM: llm, Sink: sk}, Config{MaxSteps: 8})

	ans, err := a.Run(context.Background(), "Engineer rhythm on track 1 and lead on track 2", Auto)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(ans, "Engineered both Track 1 rhythm and Track 2 lead") {
		t.Fatalf("unexpected parent answer: %q", ans)
	}

	mu.Lock()
	totalCalls := len(calls)
	mu.Unlock()

	if totalCalls != 8 {
		t.Fatalf("expected 8 chat turns, got %d", totalCalls)
	}

	// Verify Context Isolation:
	// Call 1 is parent: msgs[0]=system prompt, msgs[1]=user goal
	if !strings.Contains(calls[0].msgs[1].Parts[0].Text, "Engineer rhythm on track 1") {
		t.Fatalf("call 0 expected parent prompt, got %v", calls[0].msgs)
	}

	// Call 1 parent tools MUST include delegate_track_task
	hasDelegateParent := false
	for _, tool := range calls[0].tools {
		if tool.Name == "delegate_track_task" {
			hasDelegateParent = true
			break
		}
	}
	if !hasDelegateParent {
		t.Fatalf("expected parent tools to contain delegate_track_task")
	}

	// Call 2 is Subagent 1: system prompt starts with Track 1 specialization, user message is the goal
	if !strings.Contains(calls[1].msgs[2].Parts[0].Text, "Track 1") {
		t.Fatalf("call 1 expected isolated user context for track 1, got %s", calls[1].msgs[0].Parts[0].Text)
	}
	if calls[1].msgs[1].Parts[0].Text != "Dial tight heavy rhythm" {
		t.Fatalf("call 1 expected subagent isolated goal, got %s", calls[1].msgs[1].Parts[0].Text)
	}

	// Subagent tools MUST NOT include delegate_track_task
	for _, tool := range calls[1].tools {
		if tool.Name == "delegate_track_task" {
			t.Fatalf("subagent must NOT have delegate_track_task in its tools")
		}
	}

	// Verify events were emitted: subagent_start and subagent_done
	var sawStart, sawDone int
	for _, k := range sk.kinds {
		if k == "subagent_start" {
			sawStart++
		}
		if k == "subagent_done" {
			sawDone++
		}
	}
	if sawStart != 2 || sawDone != 2 {
		t.Fatalf("expected 2 subagent_start and 2 subagent_done events, got start=%d done=%d", sawStart, sawDone)
	}

	// Verify plan applied by subagent is stored in agent history and undoable
	if len(a.history) == 0 {
		t.Fatalf("expected applied plan to be in agent history")
	}
	if _, err := a.Undo(context.Background()); err != nil {
		t.Fatalf("undo failed: %v", err)
	}
}

type customScript struct {
	chat func(ctx context.Context, model string, msgs []core.Msg, tools []core.Tool) (core.Reply, error)
}

func (c *customScript) Chat(ctx context.Context, model string, msgs []core.Msg, tools []core.Tool) (core.Reply, error) {
	return c.chat(ctx, model, msgs, tools)
}
