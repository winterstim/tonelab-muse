package reaper

import (
	"context"
	"strings"
	"testing"
)

func TestReinstantiateHandsOverTheStateFile(t *testing.T) {
	f := &fakeLua{chunk: fixtureChunk()}
	b := startFakeLua(t, f)

	name, err := b.Reinstantiate(context.Background(), 1, 1, "QUJDREVGR0g=")
	if err != nil {
		t.Fatalf("Reinstantiate: %v", err)
	}
	if name != "AU: Test" {
		t.Fatalf("name = %q, want the FX name REAPER replied with", name)
	}
	if len(f.reinst) != 1 || f.reinst[0] != "QUJDREVGR0g=" {
		t.Fatalf("bridge saw state %q, want the state passed in", f.reinst)
	}
}

func TestUndoSendsOneUndo(t *testing.T) {
	f := &fakeLua{}
	b := startFakeLua(t, f)

	if err := b.Undo(context.Background()); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if f.undos != 1 {
		t.Fatalf("undos = %d, want 1", f.undos)
	}
}

func TestFxStateKeepsOnlyTheBase64State(t *testing.T) {
	chunk := fixtureChunk()
	state := fxState(chunk)
	if state == "" {
		t.Fatal("no state found in fixture")
	}
	for _, l := range strings.Split(state, "\n") {
		if !base64Line.MatchString(l) {
			t.Fatalf("state line %q is not base64", l)
		}
	}
	if strings.Contains(state, "AU:") || strings.Contains(state, "<") {
		t.Fatalf("state must not carry the chunk header: %q", state)
	}
	if !strings.Contains(chunk, state) {
		t.Fatalf("state %q is not part of the chunk", state)
	}
}

func TestReloadOfflineSendsTheVariantAndState(t *testing.T) {
	f := &fakeLua{chunk: fixtureChunk()}
	b := startFakeLua(t, f)
	ctx := context.Background()

	name, err := b.ReloadOffline(ctx, 1, 1, "QUJDREVGR0g=")
	if err != nil || name != "AU: Test" {
		t.Fatalf("ReloadOffline = %q, %v", name, err)
	}
	if _, err := b.ReloadOfflineNamed(ctx, 1, 1, "SElKS0xN"); err != nil {
		t.Fatalf("ReloadOfflineNamed: %v", err)
	}
	want := []string{"reload_offline:QUJDREVGR0g=", "reload_offline_b:SElKS0xN"}
	if len(f.offline) != 2 || f.offline[0] != want[0] || f.offline[1] != want[1] {
		t.Fatalf("offline reloads = %q, want %q", f.offline, want)
	}
}
