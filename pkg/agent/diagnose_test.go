package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// engineCheckDAW wraps a DAW and implements core.AudioEngineChecker and track mutation recording.
type engineCheckDAW struct {
	core.DAW
	running bool
	err     error
	touched []string
}

func (d *engineCheckDAW) AudioEngineRunning(context.Context) (bool, error) {
	return d.running, d.err
}

func (d *engineCheckDAW) SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error) {
	d.touched = append(d.touched, prop)
	return d.DAW.SetTrack(ctx, track, prop, v)
}

func (d *engineCheckDAW) SetBypass(ctx context.Context, track, fx int, b bool) error {
	if bp, ok := d.DAW.(core.Bypasser); ok {
		return bp.SetBypass(ctx, track, fx, b)
	}
	return nil
}

func TestDiagnoseSilenceAudioEngineOff(t *testing.T) {
	tracks := []core.Track{
		{Index: 1, Name: "Guitar", Audio: true},
		{Index: 2, Name: "Bass", Audio: true},
	}
	baseDAW := tracksDAW{newMemDAW(), tracks}
	d := &engineCheckDAW{DAW: baseDAW, running: false, err: nil}
	a := New(Deps{DAW: d, Renderer: stubRenderer{}, Measurer: silentMeter{}}, Config{})

	// 1. Check via diagnoseSilence
	res, err := a.diagnoseSilence(context.Background(), 1)
	if err != nil {
		t.Fatalf("diagnoseSilence failed: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", res)
	}
	if out["culprit"] != nil {
		t.Fatalf("culprit blamed when audio engine is off: %v", out["culprit"])
	}
	hint, _ := out["hint"].(string)
	if !strings.Contains(hint, "audio engine is switched off or no output device is selected") {
		t.Fatalf("expected audio engine off explanation, got: %q", hint)
	}
	if !strings.Contains(hint, "Enable the audio engine in the DAW preferences") {
		t.Fatalf("expected preference guidance in hint, got: %q", hint)
	}
	if !strings.Contains(hint, "do not change mute or solo on other tracks") {
		t.Fatalf("expected instruction not to touch other tracks, got: %q", hint)
	}
	if len(d.touched) > 0 {
		t.Fatalf("SetTrack was called during audio engine off diagnosis: %v", d.touched)
	}

	// 2. Check silenceCause directly
	cause := a.silenceCause(context.Background(), 1)
	if !strings.Contains(cause, "audio engine is switched off or no output device is selected") {
		t.Fatalf("silenceCause expected audio engine off explanation, got: %q", cause)
	}
	if !strings.Contains(cause, "do not change mute or solo on other tracks") {
		t.Fatalf("silenceCause expected instruction not to touch other tracks, got: %q", cause)
	}
}

func TestDiagnoseSilenceAudioEngineOnPluginSilencing(t *testing.T) {
	rig := &silentRig{memDAW: newMemDAW(), files: files{t.TempDir()}, bypassed: map[int]bool{}}
	d := &engineCheckDAW{DAW: rig, running: true, err: nil}
	a := New(Deps{DAW: d, Renderer: rig.files, Measurer: rig, Visualizer: rig.files}, Config{})

	res, err := a.diagnoseSilence(context.Background(), 1)
	if err != nil {
		t.Fatalf("diagnoseSilence failed: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", res)
	}
	if out["culprit"] != "Amp" {
		t.Fatalf("expected Amp to be identified as culprit, got: %v", out["culprit"])
	}
	hint, _ := out["hint"].(string)
	if !strings.Contains(hint, "Amp is silencing the track") {
		t.Fatalf("expected Amp silencing hint, got: %q", hint)
	}
}

func TestDiagnoseSilenceAudioEngineOnEmptyTrack(t *testing.T) {
	tracks := []core.Track{
		{Index: 1, Name: "Guitar", Audio: true},
		{Index: 2, Name: "Bass", Audio: true},
	}
	baseDAW := tracksDAW{newMemDAW(), tracks}
	d := &engineCheckDAW{DAW: baseDAW, running: true, err: nil}
	a := New(Deps{DAW: d, Renderer: stubRenderer{}, Measurer: silentMeter{}}, Config{})

	res, err := a.diagnoseSilence(context.Background(), 1)
	if err != nil {
		t.Fatalf("diagnoseSilence failed: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", res)
	}
	if out["culprit"] != nil {
		t.Fatalf("expected no culprit for empty track, got: %v", out["culprit"])
	}
	hint, _ := out["hint"].(string)
	if !strings.Contains(hint, "nothing is recorded on it") {
		t.Fatalf("expected empty track hint, got: %q", hint)
	}
	if !strings.Contains(hint, "do not change mute or solo on other tracks") {
		t.Fatalf("expected stop warning, got: %q", hint)
	}
}

func TestDiagnoseSilenceAudioEngineUnknownContinuesDiagnosis(t *testing.T) {
	// DAW without core.AudioEngineChecker:
	rig := &silentRig{memDAW: newMemDAW(), files: files{t.TempDir()}, bypassed: map[int]bool{}}
	a := New(Deps{DAW: rig, Renderer: rig.files, Measurer: rig, Visualizer: rig.files}, Config{})

	res, err := a.diagnoseSilence(context.Background(), 1)
	if err != nil {
		t.Fatalf("diagnoseSilence failed: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", res)
	}
	if out["culprit"] != "Amp" {
		t.Fatalf("expected Amp as culprit when engine checker is absent, got: %v", out["culprit"])
	}
}

func TestDiagnoseSilenceAudioEngineErrorContinuesDiagnosis(t *testing.T) {
	// DAW with core.AudioEngineChecker returning error:
	rig := &silentRig{memDAW: newMemDAW(), files: files{t.TempDir()}, bypassed: map[int]bool{}}
	d := &engineCheckDAW{DAW: rig, running: false, err: errors.New("DAW communication timeout")}
	a := New(Deps{DAW: d, Renderer: rig.files, Measurer: rig, Visualizer: rig.files}, Config{})

	res, err := a.diagnoseSilence(context.Background(), 1)
	if err != nil {
		t.Fatalf("diagnoseSilence failed: %v", err)
	}
	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", res)
	}
	if out["culprit"] != "Amp" {
		t.Fatalf("expected Amp as culprit when engine check returns error, got: %v", out["culprit"])
	}
}

func TestDiagnoseSilenceNeverTouchesOtherTracks(t *testing.T) {
	type trackCall struct {
		track int
		prop  string
	}
	var calls []trackCall

	tracks := []core.Track{
		{Index: 1, Name: "Lead", Audio: true},
		{Index: 2, Name: "Rhythm", Audio: true},
		{Index: 3, Name: "Bass", Audio: true},
	}
	baseDAW := tracksDAW{newMemDAW(), tracks}
	d := &trackingDAWWithChecker{
		DAW: baseDAW,
		onSetTrack: func(track int, prop string, v float64) {
			calls = append(calls, trackCall{track: track, prop: prop})
		},
	}

	// 1. Off state: diagnose track 2
	d.running = false
	a := New(Deps{DAW: d, Renderer: stubRenderer{}, Measurer: silentMeter{}}, Config{})
	if _, err := a.diagnoseSilence(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls made when engine off: %v", calls)
	}

	// 2. On state: empty track 2
	d.running = true
	calls = nil
	if _, err := a.diagnoseSilence(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.track != 2 {
			t.Fatalf("touched other track %d during diagnosis: %v", c.track, c)
		}
	}
}

type trackingDAWWithChecker struct {
	core.DAW
	running    bool
	err        error
	onSetTrack func(track int, prop string, v float64)
}

func (d *trackingDAWWithChecker) AudioEngineRunning(context.Context) (bool, error) {
	return d.running, d.err
}

func (d *trackingDAWWithChecker) SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error) {
	if d.onSetTrack != nil {
		d.onSetTrack(track, prop, v)
	}
	return d.DAW.SetTrack(ctx, track, prop, v)
}
