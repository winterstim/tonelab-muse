//go:build live

package oscmap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// TestLiveAbletonContractRenderTrack exercises M3 against the already-open
// Live set. The Remote Script owns and removes its temporary render track.
func TestLiveAbletonContractRenderTrack(t *testing.T) {
	m, err := Load("bundled/ableton.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := d.Alive(ctx); err != nil {
		t.Skip("Ableton Live is not answering on the configured OSC bridge")
	}
	tracks, err := d.Tracks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	track := 0
	for _, candidate := range tracks {
		if candidate.Audio {
			track = candidate.Index
			break
		}
	}
	if track == 0 {
		t.Skip("the open Live set has no audio-input track to render")
	}

	result, err := d.Execute(ctx, core.OpRenderTrack, map[string]any{"track": track, "seconds": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	readback, ok := result.Readback.(map[string]any)
	if !ok {
		t.Fatalf("render readback type = %T", result.Readback)
	}
	path, ok := readback["path"].(string)
	if !ok || path == "" {
		t.Fatalf("render path = %#v", readback["path"])
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		t.Fatalf("rendered file %q is not readable: %v", path, err)
	}
	t.Logf("rendered track %d to %s", track, path)
}
