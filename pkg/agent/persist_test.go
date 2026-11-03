package agent

import (
	"context"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// profileStore is an in-memory core.Store holding profiles by plugin name, as
// the file store does across processes.
type profileStore struct{ profiles map[string]core.Profile }

func (s *profileStore) SaveProfile(p core.Profile) error {
	s.profiles[p.Plugin] = p
	return nil
}
func (s *profileStore) Profile(plugin string) (*core.Profile, error) {
	p, ok := s.profiles[plugin]
	if !ok {
		return nil, nil
	}
	return &p, nil
}
func (s *profileStore) Append(string, core.Event) error { return nil }

// A plugin found read-only in one process is read-only from the first read of
// the next process: the flag comes from the store, not from memory.
func TestReadOnlyPersistsAcrossProcesses(t *testing.T) {
	store := &profileStore{profiles: map[string]core.Profile{}}
	first := New(Deps{Store: store}, Config{})
	first.markReadOnly("Amp")
	if p := store.profiles["Amp"]; !p.ReadOnly {
		t.Fatalf("profile = %+v, want ReadOnly", p)
	}

	store.profiles["Amp"] = core.Profile{Plugin: "Amp", ReadOnly: true}
	next := New(Deps{Store: store}, Config{}) // a new process: empty memory
	if !next.isReadOnly("Amp") {
		t.Fatal("the next process must know the plugin is read-only at once")
	}
	if next.isReadOnly("EQ") {
		t.Fatal("an unknown plugin must not be read-only")
	}

	// describe_chain shows the flag on the very first read
	ch := New(Deps{DAW: newStudioDAW(), Store: store}, Config{})
	out, _, err := ch.call(context.Background(), core.ToolCall{Name: "describe_chain", Args: []byte(`{"track":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fx := range out.(map[string]any)["fx"].([]map[string]any) {
		if fx["name"] == "Amp" {
			found = fx["read_only"] == true
		}
	}
	if !found {
		t.Fatal("describe_chain must mark the read-only Amp on the first read")
	}
}

// Learning labels keeps the read-only flag.
func TestLearnKeepsReadOnlyFlag(t *testing.T) {
	store := &profileStore{profiles: map[string]core.Profile{"Amp": {Plugin: "Amp", ReadOnly: true}}}
	a := New(Deps{Store: store}, Config{})
	a.markReadOnly("Amp")
	if p := store.profiles["Amp"]; !p.ReadOnly {
		t.Fatal("the flag must survive")
	}
}
