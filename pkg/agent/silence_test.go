package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// tracksDAW reports fixed tracks; the FX chain comes from memDAW.
type tracksDAW struct {
	*memDAW
	tracks []core.Track
}

func (d tracksDAW) Tracks(context.Context) ([]core.Track, error) { return d.tracks, nil }

// silentMeter measures every render as silent.
type silentMeter struct{}

func (silentMeter) Measure(string) (core.Measure, error) { return core.Measure{}, core.ErrSilent }

type stubRenderer struct{}

func (stubRenderer) Render(context.Context, int, float64) (string, error) { return "x.wav", nil }

func silentHint(t *testing.T, tracks []core.Track, track int) string {
	t.Helper()
	a := New(Deps{DAW: tracksDAW{newMemDAW(), tracks}, Renderer: stubRenderer{}, Measurer: silentMeter{}}, Config{})
	out, err := a.diagnoseSilence(context.Background(), track)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["hint"].(string)
}

func TestSilentMIDITrackIsPointedToTheAudioTrack(t *testing.T) {
	hint := silentHint(t, []core.Track{{Index: 1, Name: "x"}, {Index: 2, Name: "y", Audio: true}}, 1)
	if !strings.Contains(hint, "not an audio track") || !strings.Contains(hint, "track 2") {
		t.Fatalf("hint %q should name the audio track", hint)
	}
	if strings.Contains(hint, "mute") && !strings.Contains(hint, "do not change mute") {
		t.Fatalf("hint %q invites mass mute or solo changes", hint)
	}
}

func TestSilentAudioTrackSaysSoAndTouchesNothingElse(t *testing.T) {
	hint := silentHint(t, []core.Track{{Index: 1, Name: "x", Audio: true}, {Index: 2, Name: "y", Audio: true}}, 1)
	if !strings.Contains(hint, "nothing is recorded on it") || !strings.Contains(hint, "do not change mute or solo on other tracks") {
		t.Fatalf("hint %q", hint)
	}
}

// soloRig renders the soloed track as silent (it has no input) and the
// whole mix, with the other tracks, as loud. A diagnosis that renders the
// mix instead of the track alone would blame a plugin for the other tracks.
type soloRig struct {
	*memDAW
	tracks []core.Track
	soloed bool
}

func (r *soloRig) Tracks(context.Context) ([]core.Track, error) { return r.tracks, nil }

func (r *soloRig) SetTrack(_ context.Context, _ int, prop string, v float64) (float64, error) {
	if prop == "solo" {
		r.soloed = v == 1
	}
	return v, nil
}

func (r *soloRig) SetBypass(context.Context, int, int, bool) error { return nil }

func (r *soloRig) Render(context.Context, int, float64) (string, error) {
	if r.soloed {
		return "solo.wav", nil
	}
	return "mix.wav", nil
}

func (r *soloRig) Measure(path string) (core.Measure, error) {
	if path == "mix.wav" {
		return core.Measure{}, nil
	}
	return core.Measure{}, core.ErrSilent
}

func TestSilentTrackIsNotBlamedOnAPluginForOtherTracksAudio(t *testing.T) {
	r := &soloRig{memDAW: newMemDAW(), tracks: []core.Track{{Index: 1, Name: "Guitar", Audio: true}, {Index: 2, Name: "Lead", Audio: true}}}
	a := New(Deps{DAW: r, Renderer: r, Measurer: r}, Config{})
	out, err := a.diagnoseSilence(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if c, ok := m["culprit"]; ok {
		t.Fatalf("blamed %v for the other track's audio", c)
	}
	if !strings.Contains(m["hint"].(string), "nothing is recorded on it") {
		t.Fatalf("hint %q", m["hint"])
	}
	if r.soloed {
		t.Fatal("left the track soloed")
	}
}
