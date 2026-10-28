package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// diagnoseSilence finds which plugin silences a track: switch each one off
// in turn, render, and see whether sound comes back. Every plugin is
// switched back on before returning, whatever happens.
func (a *Agent) diagnoseSilence(ctx context.Context, track int) (any, error) {
	out := map[string]any{"silent": true}
	if checker, ok := a.d.DAW.(core.AudioEngineChecker); ok {
		if running, err := checker.AudioEngineRunning(ctx); err == nil && !running {
			out["hint"] = a.silenceCause(ctx, track)
			a.emit("diagnose", out)
			return out, nil
		}
	}
	ch, err := a.chain(ctx, track, true)
	if err != nil {
		return nil, err
	}
	a.emit("diagnose", map[string]any{"track": track, "plugins": len(ch)})
	for _, f := range ch {
		off, on, ok := a.bypass(f, track)
		if !ok {
			continue
		}
		if err := off(ctx); err != nil {
			return nil, err
		}
		// soloed like the track alone: a render of the whole mix would carry
		// other tracks' audio and blame a plugin for it
		wav, rerr := a.renderAlone(ctx, track)
		var merr error
		if rerr == nil {
			_, merr = a.d.Measurer.Measure(wav)
		}
		if err := on(ctx); err != nil {
			return nil, fmt.Errorf("could not switch %s back on: %w", f.Name, err)
		}
		if rerr != nil {
			return nil, rerr
		}
		if merr == nil {
			out["culprit"], out["fx"] = f.Name, f.Index
			out["hint"] = fmt.Sprintf("%s is silencing the track: with it switched off the sound comes back. Plugins usually do this when a locked or trial preset/model is loaded, a settings window is open, or their licence check failed. Close its windows, pick a model or preset that is not locked, or re-authorise the plugin.", f.Name)
			a.emit("diagnose", out)
			return out, nil
		} else if !errors.Is(merr, core.ErrSilent) {
			return nil, merr
		}
	}
	out["hint"] = a.silenceCause(ctx, track)
	a.emit("diagnose", out)
	return out, nil
}

// silenceCause says why a track stays silent with every plugin off. When the
// DAW reports which tracks hold audio, a MIDI track is named as such and the
// audio track is pointed to; an audio track with nothing feeding it is said
// so. Either way the other tracks are not to be touched.
func (a *Agent) silenceCause(ctx context.Context, track int) string {
	const stop = " Nothing else is changed: do not change mute or solo on other tracks. Tell the person what the track lacks and stop."
	if checker, ok := a.d.DAW.(core.AudioEngineChecker); ok {
		if running, err := checker.AudioEngineRunning(ctx); err == nil && !running {
			return "The DAW's audio engine is switched off or no output device is selected. Enable the audio engine in the DAW preferences." + stop
		}
	}
	tracks, err := a.d.DAW.Tracks(ctx)
	if err != nil {
		return "The track stays silent with every plugin off, so the audio source or routing is the problem, not a plugin." + stop
	}
	audioTrack := 0
	for _, t := range tracks {
		if t.Audio && audioTrack == 0 {
			audioTrack = t.Index
		}
	}
	if audioTrack == 0 { // the DAW does not say which tracks hold audio
		return "The track stays silent with every plugin off, so the audio source or routing is the problem, not a plugin." + stop
	}
	for _, t := range tracks {
		if t.Index == track && !t.Audio {
			return fmt.Sprintf("Track %d is not an audio track (the DAW reports it holds no audio). The guitar audio is on track %d: build the chain and listen there.", track, audioTrack) + stop
		}
	}
	return fmt.Sprintf("Track %d is an audio track but no audio reaches it with every plugin off: nothing is recorded on it.", track) + stop
}

// bypass returns functions that switch a plugin off and back on: through
// the host's "Bypass" parameter when the DAW reports one (confirmed like any
// other value), else through the DAW's Bypasser.
func (a *Agent) bypass(f core.FX, track int) (off, on func(context.Context) error, ok bool) {
	for _, p := range f.Params {
		if strings.EqualFold(p.Name, "bypass") {
			set := func(v float64) func(context.Context) error {
				return func(ctx context.Context) error {
					_, err := a.d.DAW.SetParam(ctx, track, p.ID, v)
					return err
				}
			}
			return set(1), set(p.Value), true
		}
	}
	if b, ok := a.d.DAW.(core.Bypasser); ok {
		return func(ctx context.Context) error { return b.SetBypass(ctx, track, f.Index, true) },
			func(ctx context.Context) error { return b.SetBypass(ctx, track, f.Index, false) }, true
	}
	return nil, nil, false
}
