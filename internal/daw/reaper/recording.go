package reaper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

var _ core.InputRecorder = Bridge{}

func (b Bridge) StartInput(ctx context.Context, track int, seconds float64, token string) error {
	if track < 1 || !core.ValidRecordDuration(seconds) || token == "" || strings.ContainsAny(token, "\t\n\r|") {
		return fmt.Errorf("invalid input recording request")
	}
	_, _, err := b.call(ctx, "record_input_start", track, token+"|"+strconv.FormatFloat(seconds, 'g', -1, 64), 0)
	return err
}
func (b Bridge) FinishInput(ctx context.Context, track int, token string) (core.RecordedClip, error) {
	clip := core.RecordedClip{Track: track}
	if track < 1 || token == "" || strings.ContainsAny(token, "\t\n\r|") {
		return clip, fmt.Errorf("invalid recording finish request")
	}
	path, rangeText, err := b.call(ctx, "record_input_finish", track, token, 0)
	if err != nil {
		return clip, err
	}
	parts := strings.Split(rangeText, ":")
	if len(parts) != 2 {
		return clip, fmt.Errorf("missing new recording range readback")
	}
	clip.Path = path
	clip.PositionSec, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return clip, err
	}
	clip.DurationSec, err = strconv.ParseFloat(parts[1], 64)
	return clip, err
}
