package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Token budget for multi-track requests. A model call re-sends everything
// the loop has accumulated, so what a tool returns is paid again on every
// later step. Three things keep that small without losing ability:
//   - a track subagent gets only the tools its track work needs, and a
//     compact chain (fewer controls per plugin; search_params finds the rest);
//   - describe_chain says "unchanged" when the chain is the same as the last
//     time this conversation read it, instead of sending it again;
//   - a subagent's spectrogram is sent only when it asks (look: true); its
//     numbers come back on every listen, and the parent's listens still show it.

// subagentParamsPerPlugin caps the controls a subagent sees per plugin.
const subagentParamsPerPlugin = 20

// subagentToolNames are the tools a track subagent may call.
var subagentToolNames = map[string]bool{
	"describe_chain": true, "search_params": true, "propose_tone": true,
	"apply_plan": true, "undo": true, "listen": true, "set_track": true,
	"list_plugins": true, "add_plugin": true, "remove_plugin": true, "move_plugin": true,
	"list_presets": true, "load_preset": true, "program_change": true,
	"list_plugin_models": true, "set_plugin_model": true,
	"learn_plugin": true, "match_reference": true,
}

// subagentListenSchema is listen for a subagent: the numbers always, the
// picture only when look is true.
const subagentListenSchema = `{"type":"object","properties":{"track":{"type":"integer","description":"track index from list_tracks"},"look":{"type":"boolean","description":"true to be shown the spectrogram; the numbers come back either way"}},"required":["track"]}`

// maxSubagentSummary caps the text a subagent hands back to the parent.
const maxSubagentSummary = 250

type scopeKey struct{}

// withScope tags ctx with the conversation a tool call belongs to: "parent"
// for the main loop, or a unique name per subagent.
func withScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

func scopeOf(ctx context.Context) string {
	if s, ok := ctx.Value(scopeKey{}).(string); ok {
		return s
	}
	return "parent"
}

// chainFingerprint is a short hash of the chain as sent to the model.
func chainFingerprint(fxs any) string {
	b, _ := json.Marshal(fxs)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

// seenChain records that scope read track with fingerprint fp and reports
// whether the same fingerprint was already read in that scope.
func (a *Agent) seenChain(scope string, track int, fp string) bool {
	key := scope + "/" + strconv.Itoa(track)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.chainSeen == nil {
		a.chainSeen = map[string]string{}
	}
	prev := a.chainSeen[key]
	a.chainSeen[key] = fp
	return prev == fp
}

func truncateSummary(s string) string {
	r := []rune(s)
	if len(r) <= maxSubagentSummary {
		return s
	}
	return string(r[:maxSubagentSummary]) + "…"
}

// readOnlyTools answer the same way when asked again, so a repeat gets
// "already above" instead of the answer twice.
var readOnlyTools = map[string]bool{
	"list_tracks": true, "list_plugins": true, "search_params": true,
	"list_presets": true, "list_plugin_models": true, "list_snapshots": true,
}

// callRec is what an earlier identical call came to: "" for success.
type callRec struct{ err string }

// repeatKey identifies a call: its conversation, its tool and its arguments.
func repeatKey(ctx context.Context, tc core.ToolCall) string {
	return scopeOf(ctx) + "\x00" + tc.Name + "\x00" + strings.TrimSpace(string(tc.Args))
}

func (a *Agent) repeatOf(key string) (callRec, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.calls[key]
	return r, ok
}

func (a *Agent) rememberCall(key, errText string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.calls == nil {
		a.calls = map[string]callRec{}
	}
	a.calls[key] = callRec{err: errText}
}

// chainWith reads track fresh until the chain lists fx. A plugin just added
// can take a moment to show in the DAW's chain; three short re-reads cover it.
func (a *Agent) chainWith(ctx context.Context, track, fx int) ([]core.FX, error) {
	ch, err := a.chain(ctx, track, true)
	for try := 0; err == nil && try < 3 && !hasFX(ch, fx); try++ {
		select {
		case <-ctx.Done():
			return ch, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		ch, err = a.chain(ctx, track, true)
	}
	return ch, err
}

func hasFX(ch []core.FX, fx int) bool {
	for _, f := range ch {
		if f.Index == fx {
			return true
		}
	}
	return false
}

// subagentStepCap is how many steps a track subagent gets.
const subagentStepCap = 10

// transientError is true for a failure that a retry can fix: the DAW did not
// answer or a read ran out of time. Such failures are not remembered.
func transientError(e string) bool {
	e = strings.ToLower(e)
	for _, w := range []string{"not answering", "timed out", "timeout", "deadline", "did not come back", "connection", "busy"} {
		if strings.Contains(e, w) {
			return true
		}
	}
	return false
}

// reserveSteps are the parent's last steps: the final check and the answer
// always have room, whatever the delegated work used.
const reserveSteps = 3

// checkTools is what the parent may call in its reserved steps: only the
// reads and the measurements. No tone, no plugin change, no delegation.
func checkTools(all []core.Tool) []core.Tool {
	keep := map[string]bool{"listen": true, "check_mix": true, "describe_chain": true, "search_params": true, "list_tracks": true}
	var out []core.Tool
	for _, t := range all {
		if keep[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// trimHistory replaces the content of tool results that are no longer
// needed with a one-line stub, so every later call does not re-send them:
//   - a describe_chain, search_params, listen or propose_tone result is
//     superseded by a newer one for the same tool and track;
//   - a propose_tone result whose plan was applied is replaced once applied;
//   - apply_plan keeps only the newest result.
//
// The newest result of each kind stays whole. Spectrogram images are kept
// only in the newest message that carries one.
func trimHistory(msgs []core.Msg) {
	type call struct {
		name, plan string
		track      int
	}
	type result struct {
		idx, step int
		c         call
		plan      string // plan_id in the result, for propose_tone
	}
	byID := map[string]call{}
	applied := map[string]int{}    // plan id -> step its apply_plan ran at
	appliedTrack := map[int]int{}  // track -> step an apply_plan succeeded
	planTrack := map[string]int{}  // plan id -> track
	delegated := map[int]int{}     // track -> step delegate_track_task ran at
	addedPlugin := 0
	step := 0
	var results []result
	for i, m := range msgs {
		if len(m.ToolCalls) > 0 {
			step++
			for _, tc := range m.ToolCalls {
				var in struct {
					Track  int    `json:"track"`
					PlanID string `json:"plan_id"`
				}
				_ = json.Unmarshal(tc.Args, &in)
				byID[tc.ID] = call{name: tc.Name, track: in.Track, plan: in.PlanID}
				if tc.Name == "delegate_track_task" && in.Track > 0 {
					delegated[in.Track] = step
				}
				if tc.Name == "add_plugin" {
					addedPlugin = step
				}
			}
		}
		if m.Role != "tool" {
			continue
		}
		c, ok := byID[m.ToolCallID]
		if !ok || !supersedable[c.name] {
			continue
		}
		var out struct {
			PlanID string `json:"plan_id"`
			Error  string `json:"error"`
		}
		if len(m.Parts) > 0 {
			_ = json.Unmarshal([]byte(m.Parts[0].Text), &out)
		}
		if c.name == "propose_tone" && out.PlanID != "" && c.track > 0 {
			planTrack[out.PlanID] = c.track
		}
		if c.name == "apply_plan" && out.Error == "" {
			applied[c.plan] = step
			if t, ok := planTrack[c.plan]; ok {
				appliedTrack[t] = step
			}
		}
		results = append(results, result{idx: i, step: step, c: c, plan: out.PlanID})
	}
	keyOf := func(c call) string {
		switch c.name {
		case "apply_plan", "check_mix", "list_tracks", "list_plugins", "list_presets", "more_tools":
			return c.name
		default:
			return c.name + "/" + strconv.Itoa(c.track)
		}
	}
	newest := map[string]result{}
	for _, r := range results {
		newest[keyOf(r.c)] = r // results are in message order
	}
	for _, r := range results {
		if len(msgs[r.idx].Parts) == 0 {
			continue
		}
		var stub string
		switch {
		case r.c.name == "propose_tone" && r.plan != "" && applied[r.plan] > 0:
			stub = fmt.Sprintf("propose_tone for track %d: plan %s applied at step %d; trimmed.", r.c.track, r.plan, applied[r.plan])
		case r.c.name == "describe_chain" && r.c.track > 0 && delegated[r.c.track] > 0 && r.step < delegated[r.c.track]:
			stub = fmt.Sprintf("describe_chain for track %d: delegated at step %d; trimmed.", r.c.track, delegated[r.c.track])
		case r.c.name == "search_params" && r.c.track > 0 && appliedTrack[r.c.track] > 0 && r.step < appliedTrack[r.c.track]:
			stub = fmt.Sprintf("search_params for track %d: plan applied at step %d; trimmed.", r.c.track, appliedTrack[r.c.track])
		case r.c.name == "list_plugins" && addedPlugin > 0 && r.step < addedPlugin:
			stub = fmt.Sprintf("list_plugins result: plugin added at step %d; trimmed.", addedPlugin)
		case r.c.track > 0 && newest[keyOf(r.c)].idx != r.idx:
			stub = fmt.Sprintf("%s result for track %d: superseded by step %d; trimmed.", r.c.name, r.c.track, newest[keyOf(r.c)].step)
		case newest[keyOf(r.c)].idx != r.idx:
			stub = fmt.Sprintf("%s result: superseded by step %d; trimmed.", r.c.name, newest[keyOf(r.c)].step)
		default:
			continue
		}
		msgs[r.idx].Parts = []core.Part{{Type: "text", Text: stub}}
	}
	// spectrograms: keep the newest image only
	lastImg := -1
	for i, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "image" {
				lastImg = i
			}
		}
	}
	for i := range msgs {
		if i == lastImg {
			continue
		}
		for j, p := range msgs[i].Parts {
			if p.Type == "image" {
				msgs[i].Parts[j] = core.Part{Type: "text", Text: "An earlier spectrogram, superseded by a newer one; trimmed."}
			}
		}
	}
}

var supersedable = map[string]bool{
	"describe_chain": true, "search_params": true, "listen": true,
	"propose_tone": true, "apply_plan": true, "check_mix": true,
	"list_tracks": true, "list_plugins": true, "list_presets": true,
	"more_tools": true,
}
