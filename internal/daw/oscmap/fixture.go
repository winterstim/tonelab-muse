package oscmap

import (
	"context"
	"fmt"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// PrepareConformanceFixture creates a disposable Live track for probes whose
// contract requires a device or media source. The returned cleanup deletes the
// whole track, including the clip and device, even when the probe fails.
func (d *DAW) PrepareConformanceFixture(ctx context.Context, op string, args map[string]any) (func() error, bool, error) {
	needsAudio := op == core.OpReadParams || op == core.OpSetParam ||
		op == core.OpProjectState || op == core.OpVerifyWrite || op == core.OpPresets || op == core.OpShowFXUI
	needsMIDI := op == core.OpStarterRiff
	if !needsAudio && !needsMIDI {
		return nil, false, nil
	}
	tracksBefore, err := d.Tracks(ctx)
	if err != nil {
		return nil, true, fmt.Errorf("list tracks before fixture: %w", err)
	}
	var track int
	if needsMIDI {
		if d.M.CreateMIDITrack == nil || len(d.M.CreateMIDITrack.Send) == 0 {
			return nil, true, fmt.Errorf("missing fixture API /live/song/create_midi_track")
		}
		if _, err = d.Do(ctx, *d.M.CreateMIDITrack, vars{track: -1}); err != nil {
			return nil, true, fmt.Errorf("create temporary MIDI track: %w", err)
		}
		track = len(tracksBefore) + 1
	} else {
		track, err = d.CreateTrack(ctx, "Muse Conformance Audio", 0)
		if err != nil {
			return nil, true, fmt.Errorf("create temporary audio track: %w", err)
		}
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := d.DeleteTrack(cleanupCtx, track); err != nil {
			return fmt.Errorf("delete temporary track %d: %w", track, err)
		}
		remaining, err := d.Tracks(cleanupCtx)
		if err != nil {
			return fmt.Errorf("verify fixture track deletion: %w", err)
		}
		if len(remaining) != len(tracksBefore) {
			return fmt.Errorf("fixture cleanup left %d tracks; original count was %d", len(remaining), len(tracksBefore))
		}
		for i := range tracksBefore {
			if i >= len(remaining) || remaining[i].Name != tracksBefore[i].Name {
				return fmt.Errorf("fixture cleanup changed original track %d", i+1)
			}
		}
		return nil
	}
	setupFailed := func(err error) (func() error, bool, error) {
		if cleanupErr := cleanup(); cleanupErr != nil {
			err = fmt.Errorf("%w; cleanup after setup failure: %v", err, cleanupErr)
		}
		return nil, true, err
	}
	tracksAfter, err := d.Tracks(ctx)
	if err != nil {
		return setupFailed(fmt.Errorf("read tracks after fixture creation: %w", err))
	}
	if len(tracksAfter) != len(tracksBefore)+1 {
		return setupFailed(fmt.Errorf("fixture track count changed by %d, expected one", len(tracksAfter)-len(tracksBefore)))
	}
	if track < 1 || track > len(tracksAfter) {
		return setupFailed(fmt.Errorf("temporary track index %d is invalid", track))
	}
	args["track"] = track
	if needsMIDI {
		return cleanup, true, nil
	}
	if op == core.OpShowFXUI {
		path, err := writeProbeWAV()
		if err != nil {
			return setupFailed(err)
		}
		if err = d.InsertMedia(ctx, track, path, 0); err != nil {
			return setupFailed(fmt.Errorf("put short audio clip on fixture track: %w", err))
		}
	}
	if len(d.M.InsertDevice.Send) == 0 {
		return setupFailed(fmt.Errorf("missing device insertion API"))
	}
	_, err = d.Do(ctx, d.M.InsertDevice, vars{track: track - 1 + d.M.Base, fx: d.M.Base, name: "EQ Eight"})
	if err != nil {
		return setupFailed(fmt.Errorf("load built-in EQ Eight on fixture track: %w", err))
	}
	chain, err := d.FXChain(ctx, track)
	if err != nil {
		return setupFailed(fmt.Errorf("read fixture device chain: %w", err))
	}
	if len(chain) < 1 || chain[0].Name == "" || len(chain[0].Params) == 0 {
		return setupFailed(fmt.Errorf("EQ Eight fixture device has no readable parameters"))
	}
	args["fx"] = 1
	if op == core.OpSetParam || op == core.OpVerifyWrite {
		args["param"] = string(chain[0].Params[0].ID)
	}
	if op == core.OpPresets {
		_, presets, e := d.Presets(ctx, track, 1)
		if e != nil {
			return setupFailed(fmt.Errorf("list EQ Eight presets for probe: %w", e))
		}
		if len(presets) == 0 {
			return setupFailed(fmt.Errorf("EQ Eight has no browser presets available on this Live installation"))
		}
		args["preset"] = presets[0]
	}
	return cleanup, true, nil
}

// prepareRenderFixture creates a fresh source track and short arrangement clip
// for one render operation. The Remote Script creates and routes its receiver.
func (d *DAW) prepareRenderFixture(ctx context.Context) (int, func() error, error) {
	tracksBefore, err := d.Tracks(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("list tracks before render fixture: %w", err)
	}
	track, err := d.CreateTrack(ctx, "Muse Conformance Render Source", 0)
	if err != nil {
		return 0, nil, fmt.Errorf("create render source track: %w", err)
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := d.DeleteTrack(cleanupCtx, track); err != nil {
			return fmt.Errorf("delete render source track %d: %w", track, err)
		}
		remaining, err := d.Tracks(cleanupCtx)
		if err != nil {
			return fmt.Errorf("verify render source deletion: %w", err)
		}
		if len(remaining) != len(tracksBefore) {
			return fmt.Errorf("render fixture cleanup left %d tracks; original count was %d", len(remaining), len(tracksBefore))
		}
		for i := range tracksBefore {
			if remaining[i].Name != tracksBefore[i].Name {
				return fmt.Errorf("render fixture cleanup changed original track %d", i+1)
			}
		}
		return nil
	}
	path, err := writeProbeWAV()
	if err == nil {
		err = d.InsertMedia(ctx, track, path, 0)
	}
	if err == nil {
		var state []any
		state, err = d.RunExtra(ctx, "track_state", track, 0, 0, 0, "")
		if err == nil {
			if len(state) < 9 {
				err = fmt.Errorf("render source track state is incomplete")
			} else if clips, parseErr := asFloat(state[8]); parseErr != nil || clips < 1 {
				err = fmt.Errorf("render source clip was not confirmed: %v", state[8])
			}
		}
	}
	if err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			err = fmt.Errorf("%w; cleanup render source: %v", err, cleanupErr)
		}
		return 0, nil, fmt.Errorf("prepare render source clip: %w", err)
	}
	return track, cleanup, nil
}
