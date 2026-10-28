package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// pcDAW is a DAW that can send program changes; the rest of core.DAW is
// never called by these tests.
type pcDAW struct {
	core.DAW
	programs *[]int
}

func (p pcDAW) ProgramChange(_ context.Context, track, channel, program int) error {
	*p.programs = append(*p.programs, program)
	return nil
}

func agentIn(mode Mode) (*Agent, *[]int) {
	var sent []int
	a := New(Deps{DAW: pcDAW{programs: &sent}}, Config{})
	a.mode = mode
	a.chains[1] = []core.FX{{Index: 1, Name: "Amp"}}
	return a, &sent
}

// A plugin whose reopen was tried and did not stick is not reopened again,
// and no card is shown for it: the read-only route is the answer.
func TestReloadPolicyGivesUpAfterFailedReopen(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []Mode{Auto, Turbo} {
		a, _ := agentIn(mode)
		a.Allow(kindReload)
		if err := a.reloadPolicy(ctx, 1, 1, "s"); err != nil {
			t.Fatalf("%v: first reopen must proceed: %v", mode, err)
		}
		// the write still did not stick: the agent marks it read-only
		a.readOnlyFailure(a.chains[1], core.ReadOnlyError{FX: 1, Detail: "p3"})
		if err := a.reloadPolicy(ctx, 1, 1, "s"); err == nil || errors.Is(err, core.ErrNeedsConsent) {
			t.Fatalf("%v: second reopen must be refused without a card, got %v", mode, err)
		}
	}
}

func TestReloadPolicyDefaultAsksOnceNotAgainAfterFailure(t *testing.T) {
	ctx := context.Background()
	a, _ := agentIn(Auto)
	if err := a.reloadPolicy(ctx, 1, 1, "s"); !errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("first request must ask, got %v", err)
	}
	a.Allow(kindReload)
	if err := a.reloadPolicy(ctx, 1, 1, "s"); err != nil {
		t.Fatalf("after the yes the reopen proceeds: %v", err)
	}
	a.readOnlyFailure(a.chains[1], core.ReadOnlyError{FX: 1, Detail: "p3"})
	if err := a.reloadPolicy(ctx, 1, 1, "s"); err == nil || errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("a failed reopen must not ask again, got %v", err)
	}
}

// Only the plugin whose reopen failed is refused; others are still asked.
func TestReloadPolicyGiveUpIsPerPlugin(t *testing.T) {
	ctx := context.Background()
	a, _ := agentIn(Auto)
	a.chains[1] = append(a.chains[1], core.FX{Index: 2, Name: "EQ"})
	a.Allow(kindReload)
	a.reloadPolicy(ctx, 1, 1, "s")
	a.readOnlyFailure(a.chains[1], core.ReadOnlyError{FX: 1, Detail: "p3"})
	if err := a.reloadPolicy(ctx, 1, 2, "s"); err != nil {
		t.Fatalf("another plugin must still be reopened: %v", err)
	}
}

// Read-only marks outlive the run: a second request in the same process
// does not repeat the failed reopen.
func TestReopenGiveUpSurvivesAcrossRuns(t *testing.T) {
	ctx := context.Background()
	a, _ := agentIn(Auto)
	a.Allow(kindReload)
	a.reloadPolicy(ctx, 1, 1, "s")
	a.readOnlyFailure(a.chains[1], core.ReadOnlyError{FX: 1, Detail: "p3"})
	a.mu.Lock()
	a.mode, a.request = Auto, "second request"
	a.mu.Unlock()
	if err := a.reloadPolicy(ctx, 1, 1, "s"); err == nil || errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("second request must not reopen again, got %v", err)
	}
}

func TestReloadPolicyByMode(t *testing.T) {
	ctx := context.Background()

	turbo, _ := agentIn(Turbo)
	if err := turbo.reloadPolicy(ctx, 1, 1, "s"); err != nil {
		t.Fatalf("Turbo must never ask: %v", err)
	}

	review, _ := agentIn(Preview)
	if err := review.reloadPolicy(ctx, 1, 1, "s"); err == nil || errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("Review must refuse without asking, got %v", err)
	}

	auto, _ := agentIn(Auto)
	if err := auto.reloadPolicy(ctx, 1, 1, "s"); !errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("Default must ask first, got %v", err)
	}
	auto.Allow(kindReload)
	if err := auto.reloadPolicy(ctx, 1, 1, "s"); err != nil {
		t.Fatalf("after one yes Default proceeds for the rest of the session: %v", err)
	}
}

func TestProgramChangeConsentOnceInDefault(t *testing.T) {
	ctx := context.Background()
	a, sent := agentIn(Auto)

	out, err := a.programChange(ctx, 1, 1, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); m["needs_permission"] != true {
		t.Fatalf("Default must return needs_permission first, got %v", out)
	}
	if len(*sent) != 0 {
		t.Fatalf("sent %v before consent", *sent)
	}

	a.Allow(kindProgram)
	if _, err := a.programChange(ctx, 1, 1, 42, 1); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 || (*sent)[0] != 42 {
		t.Fatalf("sent = %v, want [42]", *sent)
	}
}

func TestProgramChangeTurboNeverAsksAndReviewNever(t *testing.T) {
	ctx := context.Background()
	turbo, sent := agentIn(Turbo)
	if _, err := turbo.programChange(ctx, 1, 1, 7, 0); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("Turbo sent %v, want one change", *sent)
	}

	review, sentR := agentIn(Preview)
	if _, err := review.programChange(ctx, 1, 1, 7, 1); err == nil {
		t.Fatal("Review must not send program changes")
	}
	if len(*sentR) != 0 {
		t.Fatalf("Review sent %v", *sentR)
	}
}

func TestProgramChangeValidatesRanges(t *testing.T) {
	ctx := context.Background()
	a, _ := agentIn(Turbo)
	if _, err := a.programChange(ctx, 1, 1, 128, 1); err == nil {
		t.Fatal("program 128 must be refused")
	}
	if _, err := a.programChange(ctx, 1, 1, 5, 17); err == nil {
		t.Fatal("channel 17 must be refused")
	}
}

// At the step cap the run still ends with a short answer: one last call
// without tools.
func TestRunEndsWithAnswerAtStepCap(t *testing.T) {
	var lastTools []core.Tool
	calls := 0
	llm := &customScript{chat: func(_ context.Context, _ string, _ []core.Msg, tools []core.Tool) (core.Reply, error) {
		calls++
		lastTools = tools
		if len(tools) == 0 {
			return core.Reply{Text: "The amp did not change; the EQ is in."}, nil
		}
		return core.Reply{ToolCalls: []core.ToolCall{{ID: "t", Name: "nothing", Args: []byte(`{}`)}}}, nil
	}}
	a := New(Deps{LLM: llm}, Config{MaxSteps: 3})
	ans, err := a.Run(context.Background(), "switch the amp", Auto)
	if err != nil {
		t.Fatalf("Run at the step cap must answer, got %v", err)
	}
	if ans != "The amp did not change; the EQ is in." {
		t.Fatalf("answer = %q", ans)
	}
	if calls != 4 || len(lastTools) != 0 {
		t.Fatalf("calls = %d, last tools = %d; want 4 calls and a final call without tools", calls, len(lastTools))
	}
}
