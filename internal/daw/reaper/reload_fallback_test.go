package reaper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// withPolicy runs f under a reload policy that answers with err.
func withPolicy(err error, calls *int) context.Context {
	return core.WithReloadPolicy(context.Background(), func(ctx context.Context, track, fx int, state string) error {
		*calls++
		return err
	})
}

func TestReloadFallbackKeepsTheWriteWhenAllowed(t *testing.T) {
	chunk := fixtureChunk()
	f := &fakeLua{chunk: chunk, readOnly: true, reloadKeeps: true}
	r := &REAPER{Bridge: ptrBridge(startFakeLua(t, f))}
	id := bassID(t, chunk)
	calls := 0

	if _, err := r.SetParam(withPolicy(nil, &calls), 1, id, 0.2); err != nil {
		t.Fatalf("SetParam after reload: %v", err)
	}
	if calls != 1 || f.reloads != 1 {
		t.Fatalf("policy calls %d, reloads %d; want 1 and 1", calls, f.reloads)
	}
}

func TestReloadFallbackWaitsForConsent(t *testing.T) {
	chunk := fixtureChunk()
	f := &fakeLua{chunk: chunk, readOnly: true, reloadKeeps: true}
	r := &REAPER{Bridge: ptrBridge(startFakeLua(t, f))}
	id := bassID(t, chunk)
	calls := 0

	_, err := r.SetParam(withPolicy(core.ErrNeedsConsent, &calls), 1, id, 0.2)
	if !errors.Is(err, core.ErrNeedsConsent) {
		t.Fatalf("error = %v, want ErrNeedsConsent", err)
	}
	if f.reloads != 0 {
		t.Fatalf("reloads = %d without consent", f.reloads)
	}
}

func TestReadOnlyOnlyAfterTheReloadFails(t *testing.T) {
	chunk := fixtureChunk()
	cases := []struct {
		name   string
		setup  func(f *fakeLua)
		policy error
		noCtx  bool // no reload policy in ctx at all
		reload bool // whether the reload is expected to be attempted
	}{
		{"reload refused by the plugin's state", func(f *fakeLua) {}, nil, false, true},
		{"no project file", func(f *fakeLua) { f.reloadFail = "save the project once first" }, nil, false, true},
		{"reload refused by the user's policy", func(f *fakeLua) {}, errors.New("review mode"), false, false},
		{"no policy at all", func(f *fakeLua) {}, nil, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeLua{chunk: chunk, readOnly: true}
			c.setup(f)
			r := &REAPER{Bridge: ptrBridge(startFakeLua(t, f))}
			id := bassID(t, chunk)
			ctx := context.Background()
			if !c.noCtx {
				ctx = withPolicy(c.policy, new(int))
			}
			_, err := r.SetParam(ctx, 1, id, 0.2)
			if !errors.Is(err, core.ErrReadOnly) {
				t.Fatalf("error = %v, want read-only", err)
			}
			f.mu.Lock()
			reloads := f.reloads
			f.mu.Unlock()
			if c.reload && reloads != 1 {
				t.Fatalf("reloads = %d, want the reload tried once", reloads)
			}
			if !c.reload && reloads != 0 {
				t.Fatalf("reloads = %d, want none", reloads)
			}
		})
	}
}

func TestProgramChangeSendsChannelAndProgram(t *testing.T) {
	f := &fakeLua{chunk: fixtureChunk()}
	b := startFakeLua(t, f)
	if err := b.ProgramChange(context.Background(), 1, 3, 42); err != nil {
		t.Fatalf("ProgramChange: %v", err)
	}
	if len(f.programs) != 1 || f.programs[0] != "3:42" {
		t.Fatalf("programs = %q, want 3:42", f.programs)
	}
}

func TestProgramNamesReadsAmpliTubeList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, "Documents", "IK Multimedia", "AmpliTube 5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	list := `<?xml version="1.0" ?>
<ProgramChangeMap PC0="AmpliTube 5 CS/Appetite.at5p" PC1="AmpliTube 5 CS/From the 80&apos;s.at5p" PC2="Custom Shop/Bad &amp; Rock.at5p"/>`
	if err := os.WriteFile(filepath.Join(dir, "ProgramChangePresetList.ik"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	chunk := strings.Replace(fixtureChunk(), `"AU: Test" "Test"`, `"AU: AmpliTube 5 (IK Multimedia)" "x"`, 1)
	f := &fakeLua{chunk: chunk}
	b := startFakeLua(t, f)

	names, err := b.ProgramNames(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("ProgramNames: %v", err)
	}
	want := map[int]string{0: "Appetite", 1: "From the 80's", 2: "Bad & Rock"}
	for k, v := range want {
		if names[k] != v {
			t.Fatalf("program %d = %q, want %q (all %v)", k, names[k], v, names)
		}
	}
}

func TestProgramNamesNilForUnknownPlugin(t *testing.T) {
	f := &fakeLua{chunk: fixtureChunk()}
	b := startFakeLua(t, f)
	names, err := b.ProgramNames(context.Background(), 1, 1)
	if err != nil || names != nil {
		t.Fatalf("names = %v, %v; want nil", names, err)
	}
}
