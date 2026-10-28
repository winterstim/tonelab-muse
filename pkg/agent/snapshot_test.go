package agent

import (
	"context"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestBeforeSnapshotBringsTheToneBack(t *testing.T) {
	daw, f := newMemDAW(), files{t.TempDir()}
	st := memSnaps{}
	llm := &script{replies: []core.Reply{
		call("propose_tone", map[string]any{"track": 1, "goal": "metal", "changes": []map[string]any{
			{"param_id": "fx1.p3", "value": 0.9, "why": "gain"},
			{"param_id": "fx2.p1", "value": 0.2, "why": "mud"},
		}}),
		call("apply_plan", map[string]string{"plan_id": "p1"}),
		{Text: "done"},
	}}
	a := New(Deps{DAW: daw, LLM: llm, Renderer: f, Measurer: f, Visualizer: f, Snapshots: st}, Config{})
	if _, err := a.Run(context.Background(), "metal", Auto); err != nil {
		t.Fatal(err)
	}
	if daw.vals["fx1.p3"] != 0.9 {
		t.Fatalf("plan not applied: %v", daw.vals)
	}
	if _, err := a.RecallSnapshot(context.Background(), "Before: metal"); err != nil {
		t.Fatal(err)
	}
	if daw.vals["fx1.p3"] != 0.5 || daw.vals["fx2.p1"] != 0.5 {
		t.Fatalf("recall left %v", daw.vals)
	}
	// recall is a plan like any other: undo returns to the metal tone
	if _, err := a.Undo(context.Background()); err != nil || daw.vals["fx1.p3"] != 0.9 {
		t.Fatalf("undo after recall: %v, %v", err, daw.vals)
	}
}

type memSnaps map[string]core.Snapshot

func (m memSnaps) SaveSnapshot(s core.Snapshot) error { m[s.Name] = s; return nil }
func (m memSnaps) Snapshot(n string) (*core.Snapshot, error) {
	if s, ok := m[n]; ok {
		return &s, nil
	}
	return nil, nil
}
func (m memSnaps) Snapshots() ([]core.Snapshot, error) {
	var out []core.Snapshot
	for _, s := range m {
		out = append(out, s)
	}
	return out, nil
}
