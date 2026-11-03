//go:build live

package reaper

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestLiveReaperReadback runs the bridge readback ops against a running
// REAPER with the bridge installed and enabled. It works on a temporary
// track and deletes it at the end.
func TestLiveReaperReadback(t *testing.T) {
	b := Bridge{}
	if !b.Enabled() {
		t.Skip("bridge not enabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tr, err := b.CreateTrack(ctx, "muse-live-test", 0)
	if err != nil {
		t.Skipf("REAPER not reachable: %v", err)
	}
	t.Cleanup(func() { b.DeleteTrack(context.Background(), tr) })

	t.Run("track_state", func(t *testing.T) {
		if _, err := b.SetTrackControl(ctx, tr, "monitor", "1"); err != nil {
			t.Fatal(err)
		}
		s, err := b.TrackState(ctx, tr)
		if err != nil || s.Monitor != 1 || s.Items != 0 {
			t.Fatalf("state %+v %v", s, err)
		}
	})
	t.Run("transport_state", func(t *testing.T) {
		s, err := b.TransportState(ctx)
		if err != nil || s.Tempo <= 0 {
			t.Fatalf("transport %+v %v", s, err)
		}
	})
	t.Run("sends", func(t *testing.T) {
		if err := b.CreateSend(ctx, tr, 1, -12); err != nil {
			t.Fatal(err)
		}
		s, err := b.Sends(ctx, tr)
		if err != nil || len(s) != 1 || s[0].Dest != 1 || math.Abs(20*math.Log10(s[0].Volume)+12) > 0.1 {
			t.Fatalf("sends %+v %v", s, err)
		}
		if err := b.RemoveSend(ctx, tr, 1); err != nil {
			t.Fatal(err)
		}
		if s, _ := b.Sends(ctx, tr); len(s) != 0 {
			t.Fatalf("send not removed: %+v", s)
		}
	})
	t.Run("fx_move_wet_readout", func(t *testing.T) {
		if _, err := b.AddFX(ctx, tr, "VST: ReaEQ (Cockos)", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := b.AddFX(ctx, tr, "VST: ReaComp (Cockos)", 0); err != nil {
			t.Fatal(err)
		}
		if err := b.MoveFX(ctx, tr, 1, 2); err != nil {
			t.Fatalf("move reported failure: %v", err)
		}
		half := 0.5
		if w, err := b.Wet(ctx, tr, 1, &half); err != nil || math.Abs(w-0.5) > 0.01 {
			t.Fatalf("wet %v %v", w, err)
		}
		if r, err := b.ParamReadout(ctx, tr, 2, 0); err != nil || r == "" {
			t.Fatalf("readout %q %v", r, err)
		}
	})
}
