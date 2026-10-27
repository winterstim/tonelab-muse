package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// RecordedClip identifies the new clip and the exact range to audition through
// the track's current effects, rather than an unrelated earlier project item.
type RecordedClip struct {
	Track       int     `json:"track"`
	Path        string  `json:"path"`
	PositionSec float64 `json:"position_sec"`
	DurationSec float64 `json:"duration_sec"`
}

// InputRecorder starts a bounded input take. FinishInput must stop transport,
// disarm the target and return a newly created clip confirmed by the DAW.
// The duration is also a DAW-side watchdog when the client disappears.
// FinishInput is idempotent for its token. A caller must finish even when
// StartInput fails, since a transport may have started before its reply was lost.
type InputRecorder interface {
	StartInput(ctx context.Context, track int, seconds float64, token string) error
	FinishInput(ctx context.Context, track int, token string) (RecordedClip, error)
}

// RangeRenderer renders only the requested project range through the chosen
// track. The implementation must isolate that track and preserve/restore all
// existing solo/mute state, including on failure; a mixed master render is invalid.
type RangeRenderer interface {
	RenderRange(ctx context.Context, track int, start, seconds float64) (string, error)
}

func ValidRecordDuration(seconds float64) bool {
	return !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds >= 0.1 && seconds <= 120
}

// RecordInputFor always attempts cleanup with a fresh bounded context. A lost
// start reply is not evidence that recording never started.
func RecordInputFor(ctx context.Context, r InputRecorder, track int, seconds float64, token string) (clip RecordedClip, err error) {
	if track < 1 || !ValidRecordDuration(seconds) || token == "" {
		return clip, fmt.Errorf("recording requires a track, token and duration from 0.1 to 120 seconds")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		got, e := r.FinishInput(cleanup, track, token)
		if e != nil {
			err = errors.Join(err, fmt.Errorf("recording stop/disarm: %w", e))
			return
		}
		if err != nil {
			return
		}
		if got.Track != track || got.Path == "" || math.IsNaN(got.PositionSec) || math.IsInf(got.PositionSec, 0) || got.PositionSec < 0 || math.IsNaN(got.DurationSec) || math.IsInf(got.DurationSec, 0) || got.DurationSec < 0.1 || got.DurationSec > seconds+2 {
			err = errors.New("DAW did not confirm a new input clip")
			return
		}
		clip = got
	}()
	if err = r.StartInput(ctx, track, seconds, token); err != nil {
		return
	}
	timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
	}
	return
}
