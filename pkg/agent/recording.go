package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func (a *Agent) inputRecorder() core.InputRecorder {
	if r, ok := a.d.Chain.(core.InputRecorder); ok {
		return r
	}
	r, _ := a.d.DAW.(core.InputRecorder)
	return r
}
func (a *Agent) recordInput(ctx context.Context, track int, seconds float64) (any, error) {
	if a.currentMode() == Preview {
		return nil, errors.New("review mode does not record input")
	}
	if track < 1 || !core.ValidRecordDuration(seconds) {
		return nil, errors.New("record duration must be 0.1 to 120 seconds on a discovered track")
	}
	recorder := a.inputRecorder()
	if recorder == nil {
		return nil, errors.New("this DAW does not support input recording")
	}
	if a.rangeRenderer() == nil {
		return nil, errors.New("this renderer cannot audition the new input clip range")
	}
	if a.currentMode() != Turbo && !a.isAllowed("record") {
		a.emit("record_consent", map[string]any{"track": track, "seconds": seconds, "question": "Record your audio input onto this track? Transport will start, stop and disarm the track; a new clip will remain in the project."})
		return needsPermission("Allow the recording card, then ask again. No input was recorded."), nil
	}
	// Snapshot support is optional; recording remains an explicit undoable DAW action.
	a.autoSnapshot(ctx, track)
	token := fmt.Sprintf("take-%d", time.Now().UnixNano())
	clip, err := core.RecordInputFor(ctx, recorder, track, seconds, token)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.recorded == nil {
		a.recorded = map[int]core.RecordedClip{}
	}
	a.recorded[track] = clip
	a.lastWAV = ""
	a.startLevel = nil
	a.mu.Unlock()
	out := map[string]any{"clip": clip, "confirmed": true, "note": "Listen now: this new clip range is the track's audition source."}
	a.emit("record_input", out)
	return out, nil
}

func (a *Agent) rangeRenderer() core.RangeRenderer {
	if r, ok := a.d.Renderer.(core.RangeRenderer); ok {
		return r
	}
	r, _ := a.d.DAW.(core.RangeRenderer)
	return r
}
