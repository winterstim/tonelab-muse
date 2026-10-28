package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// trackDAW remembers mute and solo so the test can see they come back.
type trackDAW struct {
	*memDAW
	state map[string]float64
}

func (d trackDAW) SetTrack(_ context.Context, _ int, prop string, v float64) (float64, error) {
	d.state[prop] = v
	return v, nil
}

type oneClash struct{}

func (oneClash) Clashes(string, string) ([]core.Clash, error) {
	return []core.Clash{{Lo: 200, Hi: 250, Center: 224, Score: 7}}, nil
}

type failingRender struct{ files }

func (failingRender) Render(context.Context, int, float64) (string, error) {
	return "", errors.New("render stuck")
}

func TestCheckMixRestoresMuteAndSolo(t *testing.T) {
	f := files{t.TempDir()}
	daw := trackDAW{newMemDAW(), map[string]float64{}}
	a := New(Deps{DAW: daw, Renderer: f, Measurer: f, Visualizer: f, Mix: oneClash{}}, Config{})
	out, _, err := a.checkMix(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if cs := out.(map[string]any)["clashes"].([]core.Clash); len(cs) != 1 || cs[0].Center != 224 {
		t.Fatalf("clashes = %v", cs)
	}
	if daw.state["mute"] != 0 || daw.state["solo"] != 0 {
		t.Fatalf("left the track as %v", daw.state)
	}
	// a failed render must not leave the track muted
	a = New(Deps{DAW: daw, Renderer: failingRender{f}, Measurer: f, Visualizer: f, Mix: oneClash{}}, Config{})
	if _, _, err := a.checkMix(context.Background(), 1); err == nil || daw.state["mute"] != 0 {
		t.Fatalf("err = %v, state = %v", err, daw.state)
	}
}
