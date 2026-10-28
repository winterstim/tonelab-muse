package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// SubagentSystem produces the specialized system prompt for a single-track subagent.
func SubagentSystem(_ int) string {
	return `You are a specialized track sound-design engineer. The user context identifies your one track and goal. Never operate on other tracks or delegate.
Do not overcomplicate:
1. Inspect the chain with describe_chain.
2. If empty, pick ONE stock EQ or tone plugin by searching list_plugins for 'EQ', add it with add_plugin, and describe_chain to inspect its controls.
3. Propose your tone adjustments in ONE coordinated propose_tone.
4. Immediately apply it with apply_plan.
5. Finish with a short confirmed summary.
Never call list_plugins more than once. Never guess plugin names. Never stop before apply_plan.`
}

// subagentTools returns the tools a track subagent needs. delegate_track_task
// is never among them, so subagents cannot spawn subagents; the rest of the
// session tools (tracks, sends, snapshots, transport) stay with the parent.
// listen takes the look flag: the picture is sent only when asked for.
func (a *Agent) subagentTools() []core.Tool {
	all := a.tools(a.currentMode())
	sub := make([]core.Tool, 0, len(subagentToolNames))
	for _, t := range all {
		if !subagentToolNames[t.Name] {
			continue
		}
		if t.Name == "listen" {
			t.Schema = json.RawMessage(subagentListenSchema)
		}
		sub = append(sub, t)
	}
	return sub
}

// runSubagent runs an isolated subagent tool loop for a specific track.
func (a *Agent) runSubagent(ctx context.Context, track int, goal string) (any, error) {
	if a.d.LLM == nil {
		return nil, errors.New("no LLM configured")
	}

	a.emit("subagent_start", map[string]any{"track": track, "goal": goal})
	a.mu.Lock()
	a.subCount++
	scope := "sub" + strconv.Itoa(a.subCount) + "-track" + strconv.Itoa(track)
	a.mu.Unlock()
	ctx = withScope(ctx, scope)

	msgs := []core.Msg{
		core.Text("system", SubagentSystem(track)),
		core.Text("user", goal),
	}
	msgs = append(msgs, core.Text("user", fmt.Sprintf("Track %d. Maximum %d steps.", track, subagentStepCap)))
	if taste := a.tastePrompt(); taste != "" {
		msgs = append(msgs, core.Text("user", taste))
	}
	catalog := newToolCatalog(a.subagentTools(), a.currentMode())

	maxSteps := subagentStepCap
	var summaryText string
	var stepsTaken int
	applied := 0

	for step := 0; step < maxSteps; step++ {
		stepsTaken = step + 1
		trimHistory(msgs)
		reply, err := a.d.LLM.Chat(ctx, a.cfg.Planner, msgs, catalog.offered(msgs, false))
		if err != nil {
			a.emit("error", err.Error())
			return nil, err
		}
		a.emit("usage", reply.Usage)

		if len(reply.ToolCalls) == 0 {
			summaryText = reply.Text
			break
		}

		if reply.Text != "" {
			a.emit("thought", reply.Text)
		}

		msgs = append(msgs, core.Msg{Role: "assistant", Parts: textParts(reply.Text), ToolCalls: reply.ToolCalls})
		var attach []core.Part
		for _, tc := range reply.ToolCalls {
			a.emit("tool_call", map[string]any{"name": tc.Name, "args": tc.Args, "subagent_track": track})
			out, handled, err := catalog.local(tc)
			var parts []core.Part
			if !handled {
				out, parts, err = a.call(ctx, tc)
			}
			if err != nil {
				out = map[string]any{"error": err.Error()}
				a.emit("tool_error", map[string]any{"name": tc.Name, "error": err.Error(), "subagent_track": track})
			} else {
				a.emit("tool_result", map[string]any{"name": tc.Name, "result": out, "subagent_track": track})
				if tc.Name == "apply_plan" {
					applied++
				}
			}
			b := catalog.modelResult(out)
			msgs = append(msgs, core.Msg{Role: "tool", ToolCallID: tc.ID, Parts: []core.Part{{Type: "text", Text: string(b)}}})
			attach = append(attach, parts...)
		}

		if a.cfg.NoImages {
			attach = textOnly(attach)
		}
		if len(attach) > 0 {
			msgs = append(msgs, core.Msg{Role: "user", Parts: attach})
		}
	}

	if summaryText == "" {
		summaryText = fmt.Sprintf("Completed %d steps on track %d.", stepsTaken, track)
	}

	a.mu.Lock()
	var plugins []string
	for _, fx := range a.chains[track] {
		plugins = append(plugins, fx.Name)
	}
	a.mu.Unlock()
	if len(plugins) == 0 {
		if ch, err := a.chain(ctx, track, false); err == nil {
			for _, fx := range ch {
				plugins = append(plugins, fx.Name)
			}
		}
	}

	res := map[string]any{
		"track":   track,
		"summary": truncateSummary(summaryText),
		"steps":   stepsTaken,
		"plugins": plugins,
		"applied": applied,
	}
	if applied == 0 {
		res["unapplied"] = true
		res["note"] = "This track's tone is not in place: apply it yourself or tell the user it was not applied."
	}
	a.emit("subagent_done", res)
	return res, nil
}
