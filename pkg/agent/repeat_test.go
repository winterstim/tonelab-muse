package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// countDAW counts the DAW reads so a test can see whether a call ran.
type countDAW struct {
	*studioDAW
	tracks, chains int
	hideFX         int // FXChain leaves this fx index out this many times
}

func (c *countDAW) Tracks(ctx context.Context) ([]core.Track, error) {
	c.tracks++
	return c.studioDAW.Tracks(ctx)
}

func (c *countDAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	c.chains++
	ch, err := c.studioDAW.FXChain(ctx, track)
	if c.hideFX > 0 {
		c.hideFX--
		var out []core.FX
		for _, f := range ch {
			if f.Index != 1 {
				out = append(out, f)
			}
		}
		return out, err
	}
	return ch, err
}

func TestIdenticalReadOnlyCallIsNotRunAgain(t *testing.T) {
	d := &countDAW{studioDAW: newStudioDAW()}
	a := New(Deps{DAW: d}, Config{})
	ctx := context.Background()
	tc := core.ToolCall{Name: "list_tracks", Args: []byte(`{}`)}
	if _, _, err := a.call(ctx, tc); err != nil {
		t.Fatal(err)
	}
	out, _, err := a.call(ctx, tc)
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["already_done"] != true {
		t.Fatalf("repeat must say it is already above, got %v", out)
	}
	if d.tracks != 1 {
		t.Fatalf("list_tracks ran %d times, want 1", d.tracks)
	}
}

func TestIdenticalFailedCallIsNotRunAgain(t *testing.T) {
	d := &countDAW{studioDAW: newStudioDAW()}
	a := New(Deps{DAW: d}, Config{})
	ctx := context.Background()
	tc := core.ToolCall{Name: "learn_plugin", Args: []byte(`{"track":1,"fx":99}`)}
	_, _, err1 := a.call(ctx, tc)
	if err1 == nil {
		t.Fatal("learn of a missing plugin must fail")
	}
	chainsAfterFirst := d.chains
	_, _, err2 := a.call(ctx, tc)
	if err2 == nil || !strings.Contains(err2.Error(), "already failed") {
		t.Fatalf("repeat must say it already failed, got %v", err2)
	}
	if d.chains != chainsAfterFirst {
		t.Fatalf("a failed call was executed again (%d -> %d chain reads)", chainsAfterFirst, d.chains)
	}
}

func TestLearnMissingPluginNamesTheChainIndexes(t *testing.T) {
	a := New(Deps{DAW: newStudioDAW()}, Config{})
	_, _, err := a.learn(context.Background(), 1, 99, nil)
	if err == nil || !strings.Contains(err.Error(), "has fx [1 2 3 4 5 6 7 8]") {
		t.Fatalf("learn error must list the chain's fx numbers, got %v", err)
	}
}

// A plugin that shows up in the chain a moment after it was added is found
// by the re-read, not reported as missing.
func TestAddedPluginFoundAfterLateChainRead(t *testing.T) {
	d := &countDAW{studioDAW: newStudioDAW(), hideFX: 1}
	ch := &fakeChainAdder{}
	a := New(Deps{DAW: d, Chain: ch}, Config{})
	ch.index = 9 // the add reports index 9; the studio chain has 8 plugins
	out, err := a.addPlugin(context.Background(), 1, "VST: ReaEQ (Cockos)", 0)
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["fx"] != 9 {
		t.Fatalf("add result = %v", m)
	}
	if d.chains < 2 {
		t.Fatalf("the chain was read %d times, want a re-read", d.chains)
	}
}

// fakeChainAdder is a ChainEditor that adds at a fixed index.
type fakeChainAdder struct {
	core.ChainEditor
	index int
}

func (f *fakeChainAdder) Catalog(context.Context) ([]core.CatalogEntry, error) {
	return []core.CatalogEntry{{Name: "VST: ReaEQ (Cockos)"}}, nil
}
func (f *fakeChainAdder) AddFX(context.Context, int, string, int) (int, error) { return f.index, nil }

// moveRec is a ChainMover that records moves.
type moveRec struct {
	core.ChainEditor
	moves int
}

func (m *moveRec) MoveFX(context.Context, int, int, int) error {
	m.moves++
	return nil
}

func TestMoveToSamePlaceIsNoOp(t *testing.T) {
	m := &moveRec{}
	a := New(Deps{DAW: newStudioDAW(), Chain: m}, Config{})
	if _, err := a.movePlugin(context.Background(), 1, 3, 3); err != nil {
		t.Fatalf("a move to the same place must succeed: %v", err)
	}
	if m.moves != 0 {
		t.Fatalf("the DAW was asked to move in place %d times", m.moves)
	}
	if _, err := a.movePlugin(context.Background(), 1, 3, 1); err != nil {
		t.Fatal(err)
	}
	if m.moves != 1 {
		t.Fatalf("a real move must reach the DAW, moves = %d", m.moves)
	}
}

// A track subagent that keeps asking for things stops at the step cap.
func TestSubagentStopsAtStepCap(t *testing.T) {
	subCalls := 0
	llm := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
		if strings.Contains(msgs[0].Parts[0].Text, "track sound-design engineer") {
			subCalls++
			return call("search_params", map[string]any{"track": 1, "query": "gain" + strings.Repeat("x", subCalls)}), nil
		}
		return call("delegate_track_task", map[string]any{"track": 1, "goal": "dial it"}), nil
	}}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm}, Config{MaxSteps: 2})
	a.mode = Turbo
	if _, err := a.runSubagent(context.Background(), 1, "dial it"); err != nil {
		t.Fatal(err)
	}
	if subCalls != subagentStepCap {
		t.Fatalf("subagent made %d model calls, want the cap %d", subCalls, subagentStepCap)
	}
}

// A subagent that proposes but never applies is flagged, so the parent sees it.
func TestUnappliedSubagentIsFlagged(t *testing.T) {
	llm := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
		if strings.Contains(msgs[0].Parts[0].Text, "track sound-design engineer") {
			return core.Reply{Text: "Track 1: tone proposed but not applied."}, nil
		}
		return core.Reply{}, errors.New("unused")
	}}
	a := New(Deps{DAW: newStudioDAW(), LLM: llm}, Config{})
	out, err := a.runSubagent(context.Background(), 1, "dial it")
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["unapplied"] != true {
		t.Fatalf("an unapplied tone must be flagged, got %v", m)
	}
}

// A transient DAW failure is retried, not refused as already failed.
func TestTransientFailureIsRetried(t *testing.T) {
	a := New(Deps{DAW: newStudioDAW()}, Config{})
	ctx := context.Background()
	key := repeatKey(ctx, core.ToolCall{Name: "learn_plugin", Args: []byte(`{}`)})
	a.rememberCall(key, "")
	a.mu.Lock()
	a.calls = map[string]callRec{key: {err: "daw is not answering"}}
	a.mu.Unlock()
	if !transientError("daw is not answering") {
		t.Fatal("'not answering' must count as transient")
	}
	tc := core.ToolCall{Name: "list_tracks", Args: []byte(`{}`)}
	if _, _, err := a.call(ctx, tc); err != nil {
		t.Fatalf("a first transient failure must not block a retry: %v", err)
	}
	if _, _, err := a.call(ctx, tc); err != nil {
		t.Fatalf("second call after a retry must run: %v", err)
	}
}
