package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// checkMix renders the track alone and the rest of the mix without it,
// then finds the bands where both are strong. Mute and solo are put back
// whatever happens. It assumes the track starts neither muted nor soloed.
func (a *Agent) checkMix(ctx context.Context, track int) (any, []core.Part, error) {
	if a.d.Mix == nil || a.d.Renderer == nil || a.d.Visualizer == nil {
		return nil, nil, errors.New("mix analysis is not set up")
	}
	render := func(prop string) (string, error) {
		a.d.DAW.SetTrack(ctx, track, prop, 1) // the echo is unreliable; the render tells
		defer a.d.DAW.SetTrack(context.WithoutCancel(ctx), track, prop, 0)
		return a.d.Renderer.Render(ctx, track, a.cfg.RenderSecs)
	}
	rest, err := render("mute")
	if err != nil {
		return nil, nil, fmt.Errorf("render without the track: %w", err)
	}
	if _, err := a.d.Measurer.Measure(rest); errors.Is(err, core.ErrSilent) {
		return map[string]any{"clashes": []core.Clash{}, "note": "Nothing else plays in this project, so there is no mix to fit into."}, nil, nil
	}
	alone, err := render("solo")
	if err != nil {
		return nil, nil, fmt.Errorf("render of the track alone: %w", err)
	}
	clashes, err := a.d.Mix.Clashes(alone, rest)
	if err != nil {
		return nil, nil, err
	}
	png, err := a.d.Visualizer.Spectrogram([]string{alone, rest}, []string{"This track", "Rest of the mix"})
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{"clashes": clashes, "spectrogram": png,
		"note": "Each clash is a range where both are strong and close in level. Usually cut this track a little (2-4 dB, moderate width) at the worst clash, but only where it is not this track's main character; never boost to win."}
	a.emit("mix", out)
	img, err := imagePart(png)
	if err != nil {
		return nil, nil, err
	}
	note := core.Part{Type: "text", Text: "Spectrograms from check_mix, top to bottom: this track alone, the rest of the mix without it."}
	return out, []core.Part{note, img}, nil
}

// CheckMix runs mix clash detection for callers outside the tool loop.
func (a *Agent) CheckMix(ctx context.Context, track int) (any, error) {
	out, _, err := a.checkMix(ctx, track)
	return out, err
}

// renderAlone records the track on its own: soloed while the DAW plays,
// then put back. A DAW without solo plays everything, as before.
func (a *Agent) renderAlone(ctx context.Context, track int) (string, error) {
	a.mu.Lock()
	clip, hasClip := a.recorded[track]
	a.mu.Unlock()
	if hasClip {
		if r := a.rangeRenderer(); r != nil {
			seconds := a.cfg.RenderSecs
			if seconds > clip.DurationSec {
				seconds = clip.DurationSec
			}
			return r.RenderRange(ctx, track, clip.PositionSec, seconds)
		}
		return "", fmt.Errorf("recorded clip requires isolated range rendering")
	}
	render := func() (string, error) { return a.d.Renderer.Render(ctx, track, a.cfg.RenderSecs) }
	if a.d.DAW == nil {
		return render()
	}
	if _, err := a.d.DAW.SetTrack(ctx, track, "solo", 1); err == nil {
		defer a.d.DAW.SetTrack(context.WithoutCancel(ctx), track, "solo", 0)
	}
	return render()
}
