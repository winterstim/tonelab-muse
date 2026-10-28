package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// stepLevels renders a new file each time and measures the levels in turn.
type stepLevels struct {
	files
	levels []float64
	n      *int
}

func (s stepLevels) Render(context.Context, int, float64) (string, error) {
	*s.n++
	p := filepath.Join(s.dir, "r"+strings.Repeat("x", *s.n)+".wav")
	return p, os.WriteFile(p, []byte("wav"), 0o644)
}

func (s stepLevels) Measure(path string) (core.Measure, error) {
	i := len(filepath.Base(path)) - len("r.wav") - 1
	return core.Measure{Level: s.levels[i]}, nil
}

func TestListenKeepsLevelAgainstStart(t *testing.T) {
	n := 0
	f := stepLevels{files{t.TempDir()}, []float64{-20, -11, -15}, &n}
	a := New(Deps{DAW: newMemDAW(), Renderer: f, Measurer: f, Visualizer: f}, Config{})
	a.lastWAV, _ = f.Render(context.Background(), 1, 0) // the sound before the request
	out, _, err := a.listen(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["level_vs_start_db"] != 9.0 || m["level_note"] == nil {
		t.Fatalf("first listen: %v %v", m["level_vs_start_db"], m["level_note"])
	}
	out, _, _ = a.listen(context.Background(), 1) // a correction brought 4 dB back
	m := out.(map[string]any)
	if m["level_change_db"] != -4.0 || m["level_vs_start_db"] != 5.0 {
		t.Fatalf("second listen: change %v, vs start %v", m["level_change_db"], m["level_vs_start_db"])
	}
}
