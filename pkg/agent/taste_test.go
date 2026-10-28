package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type memTaste struct{ t core.Taste }

func (m *memTaste) Taste() (core.Taste, error)   { return m.t, nil }
func (m *memTaste) SaveTaste(t core.Taste) error { m.t = t; return nil }

func plan(id string, changes ...map[string]any) []core.Reply {
	return []core.Reply{
		call("propose_tone", map[string]any{"track": 1, "goal": "g", "changes": changes}),
		call("apply_plan", map[string]string{"plan_id": id}),
		{Text: "done"},
	}
}

func TestTasteLearnsFromUndoAndTweaks(t *testing.T) {
	daw, taste := newMemDAW(), &memTaste{}
	replies := append(plan("p1", map[string]any{"param_id": "fx1.p3", "value": 0.9, "why": "gain"}),
		plan("p2", map[string]any{"param_id": "fx2.p1", "value": 0.8, "why": "mids"})...)
	replies = append(replies, core.Reply{Text: "third"})
	llm := &script{replies: replies}
	a := New(Deps{DAW: daw, LLM: llm, Taste: taste}, Config{})
	ctx := context.Background()

	a.Run(ctx, "more gain", Auto)
	if _, err := a.Undo(ctx); err != nil { // the user did not like more gain
		t.Fatal(err)
	}
	// once is a moment: it must not reach the prompt yet
	if p := a.tastePrompt(); p != "" {
		t.Fatalf("a single undo already shapes the prompt: %q", p)
	}
	for i := range taste.t.Leanings { // pretend it happened twice
		taste.t.Leanings[i].Count = 2
	}
	a.Run(ctx, "more mids", Auto)
	daw.SetParam(ctx, 1, "fx2.p1", 0.6) // the user pulls the mids back a bit
	a.Run(ctx, "anything", Auto)
	for i := range taste.t.Leanings {
		taste.t.Leanings[i].Count = 2
	}
	llm.replies = append(llm.replies, core.Reply{Text: "fourth"})
	a.Run(ctx, "again", Auto)

	got := map[string]int{}
	for _, l := range taste.t.Leanings {
		got[l.Control] = l.Dir
	}
	if got["Amp / Gain"] != -1 || got["EQ / Gain-Band 2"] != -1 {
		t.Fatalf("leanings = %+v", taste.t.Leanings)
	}
	if got := llm.seen[len(llm.seen)-1][0].Parts[0].Text; got != System {
		t.Fatal("dynamic taste changed the cacheable system prompt")
	}
	sys := ""
	for _, msg := range llm.seen[len(llm.seen)-1][1:] {
		for _, part := range msg.Parts {
			sys += part.Text
		}
	}
	if !strings.Contains(sys, "Amp / Gain down") || !strings.Contains(sys, "EQ / Gain-Band 2 down") || !strings.Contains(sys, "sound design comes first") {
		t.Fatalf("the prompt does not carry the taste:\n%s", sys)
	}
}
