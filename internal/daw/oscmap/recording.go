package oscmap

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Recording map commands are optional and explicit: the endpoint must reject
// unsafe active transport/other armed tracks, enforce its own duration watchdog,
// and finish idempotently by token, returning a newly confirmed clip.
func (d *DAW) StartInput(ctx context.Context, track int, seconds float64, token string) error {
	if track < 1 || !core.ValidRecordDuration(seconds) || token == "" {
		return fmt.Errorf("invalid recording request")
	}
	if d.M.RecordInputStart == nil || d.M.RecordInputFinish == nil || d.M.RenderRange == nil {
		return fmt.Errorf("input recording requires start, finish and range-render map commands")
	}
	r, err := d.Do(ctx, *d.M.RecordInputStart, vars{track: track - 1 + d.M.Base, value: seconds, name: token})
	if err != nil {
		return err
	}
	if len(r) != 1 || str(r[0]) != token {
		return fmt.Errorf("recording start token was not confirmed")
	}
	return nil
}
func (d *DAW) FinishInput(ctx context.Context, track int, token string) (core.RecordedClip, error) {
	clip := core.RecordedClip{Track: track}
	if d.M.RecordInputFinish == nil {
		return clip, fmt.Errorf("recording finish is unavailable")
	}
	r, err := d.Do(ctx, *d.M.RecordInputFinish, vars{track: track - 1 + d.M.Base, name: token})
	if err != nil {
		return clip, err
	}
	if len(r) != 3 {
		return clip, fmt.Errorf("recording finish must return file, position, duration after stop/disarm")
	}
	position, pok := num(r[1])
	duration, dok := num(r[2])
	clip.Path = str(r[0])
	clip.PositionSec = position
	clip.DurationSec = duration
	if clip.Path == "" || !pok || !dok || math.IsNaN(position) || math.IsInf(position, 0) || position < 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0.1 || duration > 122 {
		return clip, fmt.Errorf("invalid new recording clip readback")
	}
	return clip, nil
}
func (d *DAW) RenderRange(ctx context.Context, track int, start, seconds float64) (string, error) {
	if d.M.RenderRange == nil {
		return "", fmt.Errorf("recording range render is unavailable")
	}
	if start < 0 || math.IsNaN(start) || math.IsInf(start, 0) || !core.ValidRecordDuration(seconds) {
		return "", fmt.Errorf("invalid recording render range")
	}
	r, err := d.Do(ctx, *d.M.RenderRange, vars{track: track - 1 + d.M.Base, recordStart: start, value: seconds})
	if err != nil {
		return "", err
	}
	if len(r) != 1 || str(r[0]) == "" {
		return "", fmt.Errorf("range render file was not confirmed")
	}
	return str(r[0]), nil
}

// Render performs the in-host track render implemented by the Remote Script.
func (d *DAW) Render(ctx context.Context, track int, seconds float64) (string, error) {
	if d.M.RenderTrackStart == nil || d.M.RenderTrackResult == nil {
		return "", fmt.Errorf("missing API calls /live/muse/render/start and /live/muse/render/result")
	}
	if track < 1 || !core.ValidRecordDuration(seconds) {
		return "", fmt.Errorf("invalid track render request")
	}
	ack, err := d.Do(ctx, *d.M.RenderTrackStart, vars{track: track - 1 + d.M.Base, value: seconds})
	if err != nil {
		return "", fmt.Errorf("start track render: %w", err)
	}
	if len(ack) != 1 || str(ack[0]) != "started" {
		return "", fmt.Errorf("track render start was not confirmed: %#v", ack)
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
			result, e := d.Do(ctx, *d.M.RenderTrackResult, vars{})
			if e != nil {
				return "", fmt.Errorf("read track render result: %w", e)
			}
			if len(result) == 0 {
				return "", fmt.Errorf("track render returned empty result")
			}
			status := str(result[0])
			switch {
			case status == "pending":
				continue
			case strings.HasPrefix(status, "error:"):
				return "", fmt.Errorf("track render: %s", strings.TrimSpace(strings.TrimPrefix(status, "error:")))
			case status == "":
				return "", fmt.Errorf("track render returned empty file path")
			default:
				if _, e := os.Stat(status); e != nil {
					return "", fmt.Errorf("track render path is not readable: %w", e)
				}
				return status, nil
			}
		}
	}
}
func (c *Chain) StartInput(ctx context.Context, track int, seconds float64, token string) error {
	return c.D.StartInput(ctx, track, seconds, token)
}
func (c *Chain) FinishInput(ctx context.Context, track int, token string) (core.RecordedClip, error) {
	return c.D.FinishInput(ctx, track, token)
}
