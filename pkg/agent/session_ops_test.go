package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type sessionOpsFake struct {
	core.DAW
	fail  map[string]error
	calls []string
}

type programOpsFake struct {
	core.DAW
	err   error
	calls int
}

func (f *programOpsFake) ProgramChange(context.Context, int, int, int) error {
	f.calls++
	return f.err
}

func (f *sessionOpsFake) called(op string) error {
	f.calls = append(f.calls, op)
	return f.fail[op]
}
func (f *sessionOpsFake) CreateTrack(context.Context, string, int) (int, error) {
	return 4, f.called("create_track")
}
func (f *sessionOpsFake) DuplicateTrack(context.Context, int, string) (int, error) {
	return 4, f.called("duplicate_track")
}
func (f *sessionOpsFake) DeleteTrack(context.Context, int) error {
	return f.called("delete_track")
}
func (f *sessionOpsFake) RenameTrack(context.Context, int, string) error {
	return f.called("rename_track")
}
func (f *sessionOpsFake) InsertMedia(context.Context, int, string, float64) error {
	return f.called("insert_media")
}
func (f *sessionOpsFake) CreateSend(context.Context, int, int, float64) error {
	return f.called("route_send")
}
func (f *sessionOpsFake) SetTransport(context.Context, string, float64, float64, float64, float64) error {
	return f.called("set_transport")
}

type sessionOpCase struct {
	tool, op, args string
}

var sessionOpCases = []sessionOpCase{
	{tool: "manage_track", op: "create_track", args: `{"action":"create","track":1,"name":"Bass"}`},
	{tool: "manage_track", op: "duplicate_track", args: `{"action":"duplicate","track":1,"name":"Copy"}`},
	{tool: "manage_track", op: "delete_track", args: `{"action":"delete","track":1}`},
	{tool: "manage_track", op: "rename_track", args: `{"action":"rename","track":1,"name":"Lead"}`},
	{tool: "insert_media", op: "insert_media", args: `{"track":1,"path":"riff.wav","position_sec":1.5}`},
	{tool: "route_send", op: "route_send", args: `{"from_track":1,"to_track":2,"level_db":-6}`},
	{tool: "set_transport", op: "set_transport", args: `{"action":"tempo","bpm":120}`},
}

func invokeSessionOp(a *Agent, c sessionOpCase) (any, []core.Part, error) {
	return a.call(context.Background(), core.ToolCall{Name: c.tool, Args: []byte(c.args)})
}

func TestSessionOpsToolDispatchSuccessErrorAndMissing(t *testing.T) {
	for _, tc := range sessionOpCases {
		t.Run(tc.op+"/success", func(t *testing.T) {
			f := &sessionOpsFake{}
			a := New(Deps{DAW: f}, Config{})
			a.mode = Turbo
			out, _, err := invokeSessionOp(a, tc)
			if err != nil || out == nil {
				t.Fatalf("tool returned out=%v err=%v", out, err)
			}
			if len(f.calls) != 1 || f.calls[0] != tc.op {
				t.Fatalf("DAW calls = %v, want [%s]", f.calls, tc.op)
			}
		})

		t.Run(tc.op+"/daw_error", func(t *testing.T) {
			want := errors.New("device rejected operation")
			f := &sessionOpsFake{fail: map[string]error{tc.op: want}}
			a := New(Deps{DAW: f}, Config{})
			a.mode = Turbo
			_, _, err := invokeSessionOp(a, tc)
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if len(f.calls) != 1 {
				t.Fatalf("DAW calls = %v, want one call", f.calls)
			}
		})

		t.Run(tc.op+"/missing_capability", func(t *testing.T) {
			a := New(Deps{DAW: newMemDAW()}, Config{})
			a.mode = Turbo
			_, _, err := invokeSessionOp(a, tc)
			if err == nil || !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("missing capability error = %v", err)
			}
		})
	}
}

func TestSessionOpsDoNotRepeatFailedCalls(t *testing.T) {
	for _, tc := range sessionOpCases {
		t.Run(tc.op, func(t *testing.T) {
			f := &sessionOpsFake{fail: map[string]error{tc.op: errors.New("operation failed")}}
			a := New(Deps{DAW: f}, Config{})
			a.mode = Turbo
			if _, _, err := invokeSessionOp(a, tc); err == nil {
				t.Fatal("first call should fail")
			}
			_, _, err := invokeSessionOp(a, tc)
			if err == nil || !strings.Contains(err.Error(), "already failed") {
				t.Fatalf("repeat error = %v, want already-failed error", err)
			}
			if len(f.calls) != 1 {
				t.Fatalf("failed command repeated at DAW: %v", f.calls)
			}
		})
	}
}

func TestProgramChangeErrorMissingAndRepeat(t *testing.T) {
	tc := core.ToolCall{Name: "program_change", Args: []byte(`{"track":1,"fx":1,"program":4}`)}
	t.Run("DAW error is not retried", func(t *testing.T) {
		want := errors.New("program refused")
		f := &programOpsFake{err: want}
		a := New(Deps{DAW: f}, Config{})
		a.mode = Turbo
		_, _, err := a.call(context.Background(), tc)
		if !errors.Is(err, want) {
			t.Fatalf("first error = %v", err)
		}
		_, _, err = a.call(context.Background(), tc)
		if err == nil || !strings.Contains(err.Error(), "already failed") || f.calls != 1 {
			t.Fatalf("repeat error=%v calls=%d", err, f.calls)
		}
	})

	t.Run("missing command is explicit", func(t *testing.T) {
		a := New(Deps{DAW: newMemDAW()}, Config{})
		a.mode = Turbo
		_, _, err := a.call(context.Background(), tc)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("missing ProgramChanger error = %v", err)
		}
	})

	t.Run("consent follows mode", func(t *testing.T) {
		defaultFake := &programOpsFake{}
		defaultAgent := New(Deps{DAW: defaultFake, Sink: &sink{}}, Config{})
		defaultAgent.mode = Auto
		out, err := defaultAgent.programChange(context.Background(), 1, 1, 4, 1)
		if err != nil || out.(map[string]any)["needs_permission"] != true || defaultFake.calls != 0 {
			t.Fatalf("Default out=%v err=%v calls=%d", out, err, defaultFake.calls)
		}
		if _, exposed := toolIndex(defaultAgent.tools(Preview))["program_change"]; exposed {
			t.Fatal("Preview exposed program_change")
		}
		turboFake := &programOpsFake{}
		turboAgent := New(Deps{DAW: turboFake}, Config{})
		turboAgent.mode = Turbo
		if _, err := turboAgent.programChange(context.Background(), 1, 1, 4, 1); err != nil || turboFake.calls != 1 {
			t.Fatalf("Turbo err=%v calls=%d", err, turboFake.calls)
		}
	})
}

func toolIndex(tools []core.Tool) map[string]bool {
	index := make(map[string]bool, len(tools))
	for _, tool := range tools {
		index[tool.Name] = true
	}
	return index
}

func TestSessionOpsConsentAndPreviewMode(t *testing.T) {
	t.Run("delete requires permission in default and executes in turbo", func(t *testing.T) {
		f := &sessionOpsFake{}
		a := New(Deps{DAW: f, Sink: &sink{}}, Config{})
		a.mode = Auto
		out, err := a.manageTrack(context.Background(), "delete", 1, "")
		if err != nil || out.(map[string]any)["needs_permission"] != true || len(f.calls) != 0 {
			t.Fatalf("Default delete out=%v err=%v calls=%v", out, err, f.calls)
		}
		a.mode = Turbo
		if _, err := a.manageTrack(context.Background(), "delete", 1, ""); err != nil || len(f.calls) != 1 {
			t.Fatalf("Turbo delete err=%v calls=%v", err, f.calls)
		}
	})

	t.Run("preview tools are hidden and direct calls are refused", func(t *testing.T) {
		f := &sessionOpsFake{}
		a := New(Deps{DAW: f}, Config{})
		a.mode = Preview
		for _, name := range []string{"manage_track", "insert_media", "route_send", "set_transport"} {
			for _, tool := range a.tools(Preview) {
				if tool.Name == name {
					t.Fatalf("Preview exposed %s", name)
				}
			}
		}
		for _, tc := range sessionOpCases {
			_, _, err := invokeSessionOp(a, tc)
			if err == nil || !strings.Contains(err.Error(), "review mode") {
				t.Fatalf("Preview %s error = %v", tc.op, err)
			}
		}
		if len(f.calls) != 0 {
			t.Fatalf("Preview sent session operations: %v", f.calls)
		}
	})
}

func TestSessionOpErrorsStillGetAClosingAnswer(t *testing.T) {
	f := &sessionOpsFake{fail: map[string]error{"route_send": errors.New("send rejected")}}
	llm := &customScript{chat: func(_ context.Context, _ string, _ []core.Msg, tools []core.Tool) (core.Reply, error) {
		if len(tools) == 0 {
			return core.Reply{Text: "The send failed; no route was created."}, nil
		}
		return call("route_send", map[string]any{"from_track": 1, "to_track": 2, "level_db": -6}), nil
	}}
	a := New(Deps{DAW: f, LLM: llm}, Config{MaxSteps: 3})
	answer, err := a.Run(context.Background(), "send track one to track two", Auto)
	if err != nil || answer != "The send failed; no route was created." {
		t.Fatalf("Run answer=%q err=%v", answer, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("send calls = %v", f.calls)
	}
}

func TestRunAlwaysReturnsFinalAnswerOnLLMErrorCancelAndEmptyFinal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{name: "model_error", err: errors.New("model failed")},
		{name: "canceled", err: context.Canceled, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := &customScript{chat: func(ctx context.Context, _ string, _ []core.Msg, _ []core.Tool) (core.Reply, error) {
				if tc.cancel {
					return core.Reply{}, ctx.Err()
				}
				return core.Reply{}, tc.err
			}}
			a := New(Deps{LLM: llm}, Config{})
			ev := &sink{}
			a.d.Sink = ev
			ctx := context.Background()
			if tc.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			answer, err := a.Run(ctx, "make a change", Auto)
			if err == nil || strings.TrimSpace(answer) == "" {
				t.Fatalf("Run answer=%q err=%v", answer, err)
			}
			if len(ev.kinds) == 0 || ev.kinds[len(ev.kinds)-1] != "text" {
				t.Fatalf("last event = %v, want final text", ev.kinds)
			}
		})
	}

	t.Run("empty tool-only final at step limit", func(t *testing.T) {
		calls := 0
		llm := &customScript{chat: func(_ context.Context, _ string, _ []core.Msg, tools []core.Tool) (core.Reply, error) {
			calls++
			if len(tools) == 0 {
				return call("something", map[string]any{}), nil
			}
			return call("something", map[string]any{}), nil
		}}
		a := New(Deps{LLM: llm}, Config{MaxSteps: 1})
		answer, err := a.Run(context.Background(), "continue", Auto)
		if err != nil || strings.TrimSpace(answer) == "" {
			t.Fatalf("Run answer=%q err=%v", answer, err)
		}
		if calls != 2 {
			t.Fatalf("LLM calls = %d, want one tool step and one final call", calls)
		}
	})
}

func TestDelegateFailuresAreReportedOnceAndParentAnswers(t *testing.T) {
	t.Run("missing model is explicit", func(t *testing.T) {
		a := New(Deps{DAW: newMemDAW()}, Config{})
		_, _, err := a.call(context.Background(), core.ToolCall{Name: "delegate_track_task", Args: []byte(`{"track":1,"goal":"brighten"}`)})
		if err == nil || !strings.Contains(err.Error(), "no LLM configured") {
			t.Fatalf("missing model error = %v", err)
		}
	})

	t.Run("failed subagent call is not repeated and parent closes", func(t *testing.T) {
		calls := 0
		failedLLM := &customScript{chat: func(_ context.Context, _ string, _ []core.Msg, _ []core.Tool) (core.Reply, error) {
			calls++
			return core.Reply{}, errors.New("subagent model failed")
		}}
		direct := New(Deps{DAW: newMemDAW(), LLM: failedLLM}, Config{})
		delegate := core.ToolCall{Name: "delegate_track_task", Args: []byte(`{"track":1,"goal":"brighten"}`)}
		first, _, err := direct.call(context.Background(), delegate)
		if err == nil {
			t.Fatalf("first delegate result %v, want error", first)
		}
		_, _, err = direct.call(context.Background(), delegate)
		if err == nil || !strings.Contains(err.Error(), "already failed") || calls != 1 {
			t.Fatalf("repeat error=%v calls=%d", err, calls)
		}

		parent := &customScript{chat: func(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
			if strings.Contains(msgs[0].Parts[0].Text, "specialized track sound-design engineer") {
				return core.Reply{}, errors.New("subagent model failed")
			}
			for _, m := range msgs {
				if m.Role == "tool" {
					return core.Reply{Text: "The track task failed and was not retried."}, nil
				}
			}
			return call("delegate_track_task", map[string]any{"track": 1, "goal": "brighten"}), nil
		}}
		runner := New(Deps{DAW: newMemDAW(), LLM: parent}, Config{MaxSteps: 3})
		answer, runErr := runner.Run(context.Background(), "brighten the track", Auto)
		if runErr != nil || answer == "" {
			t.Fatalf("parent answer=%q err=%v", answer, runErr)
		}
	})
}
