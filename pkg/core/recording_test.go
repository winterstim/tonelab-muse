package core

import (
	"context"
	"errors"
	"math"
	"testing"
)

type recordingFake struct {
	starts, finishes  int
	startErr, stopErr error
	cancel            context.CancelFunc
	cleanupCanceled   bool
	clip              RecordedClip
}

func (f *recordingFake) StartInput(ctx context.Context, _ int, _ float64, _ string) error {
	f.starts++
	if f.cancel != nil {
		f.cancel()
	}
	return f.startErr
}
func (f *recordingFake) FinishInput(ctx context.Context, _ int, _ string) (RecordedClip, error) {
	f.finishes++
	f.cleanupCanceled = ctx.Err() != nil
	return f.clip, f.stopErr
}
func TestRecordInputForCleansUpEveryStartedAttempt(t *testing.T) {
	for _, name := range []string{"success", "cancel", "lost start reply", "stop error", "invalid clip"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &recordingFake{clip: RecordedClip{Track: 1, Path: "take.wav", PositionSec: 4, DurationSec: .1}}
			switch name {
			case "cancel":
				f.cancel = cancel
			case "lost start reply":
				f.startErr = errors.New("lost reply")
			case "stop error":
				f.stopErr = errors.New("stop failed")
			case "invalid clip":
				f.clip.Path = ""
			}
			clip, err := RecordInputFor(ctx, f, 1, .1, "token")
			if f.starts != 1 || f.finishes != 1 || f.cleanupCanceled {
				t.Fatalf("cleanup: %+v", f)
			}
			if name == "success" {
				if err != nil || clip.Path != "take.wav" {
					t.Fatalf("%+v %v", clip, err)
				}
			} else if err == nil {
				t.Fatal("failure falsely succeeded")
			}
			if name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
func TestRecordInputForRejectsInvalidBeforeSideEffects(t *testing.T) {
	for _, seconds := range []float64{-1, 0, math.NaN(), math.Inf(1), 121} {
		f := &recordingFake{}
		if _, err := RecordInputFor(context.Background(), f, 1, seconds, "x"); err == nil || f.starts != 0 || f.finishes != 0 {
			t.Fatalf("accepted %v", seconds)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &recordingFake{}
	if _, err := RecordInputFor(ctx, f, 1, 1, "x"); !errors.Is(err, context.Canceled) || f.starts != 0 {
		t.Fatal(err)
	}
}
func TestRecordInputPreservesStartAndCleanupErrors(t *testing.T) {
	start, stop := errors.New("start"), errors.New("stop")
	f := &recordingFake{startErr: start, stopErr: stop}
	_, err := RecordInputFor(context.Background(), f, 1, .1, "x")
	if !errors.Is(err, start) || !errors.Is(err, stop) {
		t.Fatal(err)
	}
}
