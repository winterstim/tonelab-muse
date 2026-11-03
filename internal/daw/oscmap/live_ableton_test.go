//go:build live

package oscmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// TestLiveAbleton runs against a real Ableton Live with the AbletonOSC
// extension running. Run only with: go test -tags live -run TestLiveAbleton
// It writes to the open set and restores what it changes.
func TestLiveAbleton(t *testing.T) {
	raw, err := os.ReadFile("bundled/ableton.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Map
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) // registered first, so it runs after the other cleanups

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := d.Alive(ctx); err != nil {
		t.Skip("Live not running")
	}

	state, err := readLiveTransportState(ctx, d)
	if err != nil {
		t.Fatalf("read loop and time signature before test: %v", err)
	}
	if state.LoopEnd <= state.LoopStart {
		t.Fatalf("cannot safely restore Live loop bounds %.3f..%.3f seconds", state.LoopStart, state.LoopEnd)
	}
	defer func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer restoreCancel()
		if err := d.SetTransport(restoreCtx, "loop", 0, 0, state.LoopStart, state.LoopEnd); err != nil {
			t.Errorf("restore loop bounds: %v", err)
		} else if !state.LoopEnabled {
			if err := d.SetTransport(restoreCtx, "loop", 0, 0, 0, 0); err != nil {
				t.Errorf("restore loop disabled state: %v", err)
			}
		}
		if err := d.SetTimeSignature(restoreCtx, state.Numerator, state.Denominator); err != nil {
			t.Errorf("restore time signature %d/%d: %v", state.Numerator, state.Denominator, err)
		}
		after, err := readLiveTransportState(restoreCtx, d)
		if err != nil {
			t.Errorf("verify restored transport state: %v", err)
			return
		}
		if after.LoopEnabled != state.LoopEnabled || math.Abs(after.LoopStart-state.LoopStart) > 0.01 || math.Abs(after.LoopEnd-state.LoopEnd) > 0.01 {
			t.Errorf("loop state after restore = enabled:%v %.3f..%.3f, want enabled:%v %.3f..%.3f", after.LoopEnabled, after.LoopStart, after.LoopEnd, state.LoopEnabled, state.LoopStart, state.LoopEnd)
		}
		if after.Numerator != state.Numerator || after.Denominator != state.Denominator {
			t.Errorf("time signature after restore = %d/%d, want %d/%d", after.Numerator, after.Denominator, state.Numerator, state.Denominator)
		}
	}()

	before, err := d.Tracks(ctx)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	t.Logf("tracks before test: %d", len(before))

	tr, err := d.CreateTrack(ctx, "muse-live-test", 0)
	if err != nil {
		t.Fatalf("create track: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := d.DeleteTrack(cctx, tr); err != nil {
			t.Errorf("delete temp track %d: %v", tr, err)
		}
	})

	t.Run("track_monitor", func(t *testing.T) {
		got, err := d.SetTrackControl(ctx, tr, "monitor", "0")
		if err != nil {
			t.Fatalf("set monitor 0: %v", err)
		}
		if got != "0" {
			t.Fatalf("monitor 0 readback = %q, want %q", got, "0")
		}
		if _, err := d.SetTrackControl(ctx, tr, "monitor", "1"); err != nil {
			t.Fatalf("set monitor 1: %v", err)
		}
	})

	t.Run("track_input", func(t *testing.T) {
		got, err := d.SetTrackControl(ctx, tr, "input", "No Input")
		if err != nil {
			t.Fatalf("set input: %v", err)
		}
		if got != "No Input" {
			t.Fatalf("input readback = %q, want %q", got, "No Input")
		}
	})

	t.Run("send", func(t *testing.T) {
		if err := d.CreateSend(ctx, tr, 1, -12); err != nil {
			t.Fatalf("create send: %v", err)
		}
		if err := d.RemoveSend(ctx, tr, 1); err != nil {
			t.Fatalf("remove send: %v", err)
		}
	})

	t.Run("time_signature", func(t *testing.T) {
		if err := d.SetTimeSignature(ctx, 7, 8); err != nil {
			t.Fatalf("set 7/8: %v", err)
		}
		if err := d.SetTimeSignature(ctx, state.Numerator, state.Denominator); err != nil {
			t.Fatalf("restore original %d/%d: %v", state.Numerator, state.Denominator, err)
		}
	})

	t.Run("marker", func(t *testing.T) {
		// No Go method deletes markers, so the test marker is left in the set.
		idx, err := d.CreateMarker(ctx, 2.0, 2.0, "live-test", false)
		if err != nil {
			t.Fatalf("create marker: %v", err)
		}
		t.Cleanup(func() { d.DeleteMarker(context.Background(), idx) })
		ms, err := d.Markers(ctx)
		if err != nil {
			t.Fatalf("markers: %v", err)
		}
		found := false
		for _, mk := range ms {
			if mk.Name == "live-test" && math.Abs(mk.PositionSec-2.0) <= 0.01 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("marker live-test at 2.0s not in %+v", ms)
		}
		if err := d.GoToMarker(ctx, idx); err != nil {
			t.Fatalf("go to marker %d: %v", idx, err)
		}
	})

	t.Run("loop", func(t *testing.T) {
		if err := d.SetTransport(ctx, "loop", 0, 0, 1.0, 3.0); err != nil {
			t.Fatalf("set loop: %v", err)
		}
		if err := d.SetTransport(ctx, "loop", 0, 0, 0, 0); err != nil { // end <= start turns the loop off
			t.Fatalf("loop off: %v", err)
		}
	})

	t.Run("move_device", func(t *testing.T) {
		consent := filepath.Join(t.TempDir(), "consent")
		os.WriteFile(consent, nil, 0o600)
		c := &Chain{D: d, Consent: consent}
		if err := c.allowed(); errors.Is(err, core.ErrNeedsConsent) {
			t.Skip("chain edits need one-time consent (no consent file); skipping move")
		}
		if _, err := c.AddFX(ctx, tr, "Utility", 0); err != nil {
			t.Fatalf("add Utility: %v", err)
		}
		if _, err := c.AddFX(ctx, tr, "Saturator", 0); err != nil {
			t.Fatalf("add Saturator: %v", err)
		}
		if err := c.MoveFX(ctx, tr, 1, 2); err != nil {
			t.Fatalf("move fx: %v", err)
		}
		chain, err := d.FXChain(ctx, tr)
		if err != nil {
			t.Fatalf("fx chain: %v", err)
		}
		si, ui := -1, -1
		for i, fx := range chain {
			switch fx.Name {
			case "Saturator":
				si = i
			case "Utility":
				ui = i
			}
		}
		if si < 0 || ui < 0 || si > ui {
			t.Fatalf("want Saturator before Utility, chain = %+v", chain)
		}
	})

	t.Run("browser_preset_load", func(t *testing.T) {
		consent := filepath.Join(t.TempDir(), "consent")
		if err := os.WriteFile(consent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		c := &Chain{D: d, Consent: consent}
		if err := c.allowed(); err != nil {
			t.Skip("chain edits need one-time consent (no consent file): " + err.Error())
		}
		testTrack, err := d.CreateTrack(ctx, "muse-preset-test", 0)
		if err != nil {
			t.Fatalf("create preset test track: %v", err)
		}
		t.Cleanup(func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			if err := d.DeleteTrack(cctx, testTrack); err != nil {
				t.Errorf("delete preset test track %d: %v", testTrack, err)
			}
		})
		fx, err := c.AddFX(ctx, testTrack, "EQ Eight", 0)
		if err != nil {
			t.Fatalf("add temporary EQ Eight: %v", err)
		}
		_, presets, err := d.Presets(ctx, testTrack, fx)
		if err != nil {
			t.Fatalf("list browser presets: %v", err)
		}
		var preset string
		for _, candidate := range presets {
			if strings.HasPrefix(candidate, "audio_effects/") {
				preset = candidate
				break
			}
		}
		if preset == "" {
			t.Skip("Live browser exposes no loadable preset items")
		}
		if err := d.SetPreset(ctx, testTrack, fx, preset); err != nil {
			t.Fatalf("load browser preset %q: %v", preset, err)
		}
		chain, err := d.FXChain(ctx, testTrack)
		if err != nil {
			t.Fatalf("read back preset test chain: %v", err)
		}
		if len(chain) == 0 || chain[fx-1].Name == "" {
			t.Fatalf("preset load did not leave a readable device on the temporary track: %+v", chain)
		}
	})
}

type liveTransportState struct {
	LoopEnabled bool
	LoopStart   float64
	LoopEnd     float64
	Numerator   int
	Denominator int
}

func readLiveTransportState(ctx context.Context, d *DAW) (liveTransportState, error) {
	command, ok := d.M.Extra["transport_state"]
	if !ok {
		return liveTransportState{}, errors.New("transport_state command missing from map")
	}
	reply, err := d.Do(ctx, command, vars{})
	if err != nil {
		return liveTransportState{}, err
	}
	if len(reply) != 8 {
		return liveTransportState{}, fmt.Errorf("transport_state returned %d values, want 8", len(reply))
	}
	enabled, ok := asBool(reply[3])
	if !ok {
		return liveTransportState{}, fmt.Errorf("loop enabled value %T is invalid", reply[3])
	}
	start, startOK := num(reply[4])
	end, endOK := num(reply[5])
	numerator, numeratorOK := num(reply[6])
	denominator, denominatorOK := num(reply[7])
	if !startOK || !endOK || !numeratorOK || !denominatorOK || numerator < 1 || denominator < 1 || math.Trunc(numerator) != numerator || math.Trunc(denominator) != denominator {
		return liveTransportState{}, fmt.Errorf("invalid loop or time-signature readback: %#v", reply)
	}
	return liveTransportState{
		LoopEnabled: enabled,
		LoopStart:   start,
		LoopEnd:     end,
		Numerator:   int(numerator),
		Denominator: int(denominator),
	}, nil
}
