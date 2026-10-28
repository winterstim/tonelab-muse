package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// readOnlyDAW is a memDAW whose first batch write comes back unchanged: the
// plugin kept none of it, as read back from the chunk.
type readOnlyDAW struct {
	*memDAW
	writes int
}

func (d *readOnlyDAW) SetParams(ctx context.Context, track int, changes []core.Change) ([]core.Applied, error) {
	d.writes++
	if d.writes == 1 {
		return nil, core.ReadOnlyError{FX: 1, Detail: "fx1.p3 read back 0.500 after writing 0.850"}
	}
	var out []core.Applied
	for _, c := range changes {
		a, err := d.memDAW.SetParam(ctx, track, c.Param, c.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func TestReadOnlyPluginIsRememberedAndNotWrittenAgain(t *testing.T) {
	ctx := context.Background()
	d := &readOnlyDAW{memDAW: newMemDAW()}
	a := New(Deps{DAW: d}, Config{})

	out, _, err := a.propose(ctx, 1, "metal", []core.Change{{Param: "fx1.p3", Value: 0.85, Why: "gain"}})
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	planID := out.(map[string]any)["plan_id"].(string)

	// the first write fails cleanly: one attempt, the read-only message
	_, err = a.applyPlan(ctx, planID)
	if err == nil || !strings.Contains(err.Error(), "does not accept changes from outside to its internal controls") {
		t.Fatalf("apply error = %v, want the read-only message", err)
	}
	if !strings.Contains(err.Error(), "Amp does not accept") {
		t.Fatalf("message must name the plugin: %v", err)
	}
	if d.writes != 1 {
		t.Fatalf("writes = %d, want 1", d.writes)
	}

	// later plans on that plugin are rejected without another write
	if _, _, err := a.propose(ctx, 1, "metal", []core.Change{{Param: "fx1.p5", Value: 1, Why: "bright"}}); err == nil ||
		!strings.Contains(err.Error(), "does not accept changes") {
		t.Fatalf("propose on read-only plugin: %v", err)
	}
	if _, err := a.applyPlan(ctx, planID); err == nil {
		t.Fatal("re-applying the failed plan must be refused")
	}
	if d.writes != 1 {
		t.Fatalf("writes after refusals = %d, want still 1", d.writes)
	}

	// other plugins are still planned
	if _, _, err := a.propose(ctx, 1, "eq", []core.Change{{Param: "fx2.p1", Value: 0.6, Why: "mud"}}); err != nil {
		t.Fatalf("EQ plan must still work: %v", err)
	}

	// describe_chain keeps showing the controls, marked read_only
	res, _, err := a.call(ctx, core.ToolCall{Name: "describe_chain", Args: json.RawMessage(`{"track":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(res)
	var got struct {
		FX []struct {
			Name     string `json:"name"`
			ReadOnly bool   `json:"read_only"`
			Params   []struct {
				ID       string `json:"id"`
				ReadOnly bool   `json:"read_only"`
			} `json:"params"`
		} `json:"fx"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, f := range got.FX {
		switch f.Name {
		case "Amp":
			if !f.ReadOnly || len(f.Params) == 0 {
				t.Fatalf("Amp must be read_only with its controls: %+v", f)
			}
			for _, p := range f.Params {
				if !p.ReadOnly {
					t.Fatalf("param %s not marked read_only", p.ID)
				}
			}
		case "EQ":
			if f.ReadOnly {
				t.Fatal("EQ must not be read-only")
			}
		}
	}

	// search_params shows the same mark
	res, _, err = a.call(ctx, core.ToolCall{Name: "search_params", Args: json.RawMessage(`{"track":1,"query":"gain"}`)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(res)
	if !strings.Contains(string(raw), `"read_only":true`) {
		t.Fatalf("search_params must mark read-only hits: %s", raw)
	}
}
