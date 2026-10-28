package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

const maxLearn = 16

// learn maps unnamed parameters to on-screen controls: move one parameter
// 0 → 1, capture the window each time, and ask Muse which control moved.
// A mapping counts only when the DAW confirmed the move AND Muse saw a
// control change, so a dead slot never gets a label.
func (a *Agent) learn(ctx context.Context, track, fx int, ids []string) (any, []core.Part, error) {
	ch, err := a.chain(ctx, track, true)
	if err != nil {
		return nil, nil, err
	}
	var plugin *core.FX
	for i := range ch {
		if ch[i].Index == fx {
			plugin = &ch[i]
		}
	}
	if plugin == nil {
		var have []int
		for _, f := range ch {
			have = append(have, f.Index)
		}
		return nil, nil, fmt.Errorf("no plugin %d on track %d; the chain has fx %v now (use describe_chain's fx numbers)", fx, track, have)
	}
	var todo []core.Param
	for _, p := range plugin.Params {
		if len(ids) == 0 && p.Unnamed || contains(ids, string(p.ID)) {
			todo = append(todo, p)
		}
	}
	if len(todo) > maxLearn {
		todo = todo[:maxLearn]
	}
	if len(todo) == 0 {
		return nil, nil, errors.New("no unnamed parameters to learn on this plugin")
	}
	prof := core.Profile{Plugin: plugin.Name, Map: map[int]core.ProfileEntry{}, Learned: time.Now()}
	if a.d.Store != nil {
		if old, _ := a.d.Store.Profile(plugin.Name); old != nil {
			prof.Map = old.Map
			prof.ReadOnly = old.ReadOnly // learning labels does not make a plugin writable
		}
	}
	var results []map[string]any
	for _, p := range todo {
		res := map[string]any{"param_id": p.ID}
		results = append(results, res)
		lo, err := a.d.DAW.SetParam(ctx, track, p.ID, 0)
		if err != nil {
			res["result"] = "no response from the DAW"
			continue
		}
		// a slot the DAW will not move is unassigned: no need to look at it
		hi, _ := a.d.DAW.SetParam(ctx, track, p.ID, 1)
		if math.Abs(hi.Confirmed-lo.Confirmed) < 0.01 {
			a.d.DAW.SetParam(ctx, track, p.ID, p.Value)
			res["result"] = "dead: the DAW kept the value, nothing assigned to this slot"
			a.emit("learn", res)
			continue
		}
		a.d.DAW.SetParam(ctx, track, p.ID, 0)
		before, err := a.d.Capturer.CaptureFX(ctx, track, fx)
		if err != nil {
			return nil, nil, fmt.Errorf("capture: %w", err)
		}
		a.d.DAW.SetParam(ctx, track, p.ID, 1)
		after, err := a.d.Capturer.CaptureFX(ctx, track, fx)
		a.d.DAW.SetParam(ctx, track, p.ID, p.Value) // restore
		if err != nil {
			return nil, nil, fmt.Errorf("capture: %w", err)
		}
		seen, err := a.look(ctx, before, after)
		if err != nil {
			res["result"] = "vision error: " + err.Error()
			continue
		}
		if !seen.Moved || seen.Label == "" {
			res["result"] = "the DAW moved it but no control visibly changed"
			a.emit("learn", res)
			continue
		}
		_, n, _ := p.ID.Parse()
		prof.Map[n] = core.ProfileEntry{Label: seen.Label, Kind: seen.Kind, Section: seen.Section, Confidence: seen.Confidence}
		res["result"], res["label"], res["section"] = "learned", seen.Label, seen.Section
		a.emit("learn", res)
	}
	learned := 0
	for _, r := range results {
		if r["result"] == "learned" {
			learned++
		}
	}
	if learned > 0 && a.d.Store != nil {
		if err := a.d.Store.SaveProfile(prof); err != nil {
			return nil, nil, err
		}
	}
	a.mu.Lock()
	delete(a.chains, track) // labels changed: next describe_chain must re-merge
	a.mu.Unlock()
	out := map[string]any{"plugin": plugin.Name, "learned": learned, "tried": len(todo), "results": results}
	if learned == 0 {
		out["setup_needed"] = fmt.Sprintf("None of %s's unnamed slots move anything: the plugin has not exposed its controls to the DAW. Tell the user, in at most three short steps, how to connect them: open the plugin, find its automation or host-parameter settings, assign the controls they care about to slots 1, 2, 3...; then you learn again.", plugin.Name)
	}
	return out, nil, nil
}

type sight struct {
	Moved      bool    `json:"moved"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	Section    string  `json:"section"`
	Confidence float64 `json:"confidence"`
}

// look asks Muse which control changed between two window captures.
func (a *Agent) look(ctx context.Context, before, after string) (sight, error) {
	b, err := imagePart(before)
	if err != nil {
		return sight{}, err
	}
	af, err := imagePart(after)
	if err != nil {
		return sight{}, err
	}
	msgs := []core.Msg{core.Text("system", Learner), {Role: "user", Parts: []core.Part{
		{Type: "text", Text: "Before (parameter at 0):"}, b,
		{Type: "text", Text: "After (parameter at 1):"}, af,
	}}}
	llm, model := a.eyes()
	r, err := llm.Chat(ctx, model, msgs, nil)
	if err != nil {
		return sight{}, err
	}
	a.emit("usage", r.Usage)
	var s sight
	if err := json.Unmarshal([]byte(jsonObject(r.Text)), &s); err != nil {
		return sight{}, fmt.Errorf("unreadable answer: %q", r.Text)
	}
	return s, nil
}

// jsonObject cuts the first {...} out of a reply that may wrap it in prose
// or a code fence.
func jsonObject(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
