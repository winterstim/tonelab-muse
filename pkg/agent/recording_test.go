package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type inputRig struct {
	*memDAW
	starts, stops int
	err           error
}

func (r *inputRig) StartInput(context.Context, int, float64, string) error { r.starts++; return r.err }
func (r *inputRig) FinishInput(context.Context, int, string) (core.RecordedClip, error) {
	r.stops++
	return core.RecordedClip{Track: 1, Path: "new.wav", PositionSec: 12, DurationSec: .1}, nil
}

type rangeRig struct {
	files
	start, seconds float64
	calls          int
}

func (r *rangeRig) RenderRange(ctx context.Context, track int, start, seconds float64) (string, error) {
	r.start, r.seconds = start, seconds
	r.calls++
	return "range.wav", nil
}
func TestRecordInputModesAndNewListenSource(t *testing.T) {
	for _, mode := range []Mode{Preview, Auto, Turbo} {
		t.Run(string(mode), func(t *testing.T) {
			rig := &inputRig{memDAW: newMemDAW()}
			renderer := &rangeRig{files: files{t.TempDir()}}
			a := New(Deps{DAW: rig, Renderer: renderer}, Config{})
			a.mode = mode
			out, err := a.recordInput(context.Background(), 1, .1)
			if mode == Preview {
				if err == nil || rig.starts != 0 {
					t.Fatal("preview recorded")
				}
				return
			}
			if mode == Auto {
				if err != nil || out.(map[string]any)["needs_permission"] != true || rig.starts != 0 {
					t.Fatal("missing consent")
				}
				a.Allow("record")
				out, err = a.recordInput(context.Background(), 1, .1)
			}
			if err != nil || out.(map[string]any)["confirmed"] != true || rig.starts != 1 || rig.stops != 1 {
				t.Fatalf("%v %v %+v", out, err, rig)
			}
			wav, err := a.renderAlone(context.Background(), 1)
			if err != nil || wav != "range.wav" || renderer.start != 12 || renderer.seconds != .1 {
				t.Fatalf("wrong listen source %s %v %+v", wav, err, renderer)
			}
		})
	}
}
func TestRecordInputFailureDoesNotReplaceSource(t *testing.T) {
	rig := &inputRig{memDAW: newMemDAW(), err: errors.New("start failed")}
	a := New(Deps{DAW: rig, Renderer: &rangeRig{}}, Config{})
	a.mode = Turbo
	if _, err := a.recordInput(context.Background(), 1, .1); err == nil || rig.stops != 1 || len(a.recorded) != 0 {
		t.Fatalf("%v %+v", err, rig)
	}
}
