package oscmap

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestContractRenderTrackWaitsForReadableOutput(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, []byte("wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 65536)
		resultReads := 0
		for {
			n, from, readErr := server.ReadFrom(buf)
			if readErr != nil {
				return
			}
			messages, _ := reaper.Decode(buf[:n])
			for _, msg := range messages {
				var args []any
				switch msg.Addr {
				case "/live/muse/render/start":
					if len(msg.Args) == 2 && msg.Args[0] == int32(2) && msg.Args[1] == float32(1) {
						args = []any{"started"}
					} else {
						args = []any{"error: bad render arguments"}
					}
				case "/live/muse/render/result":
					resultReads++
					if resultReads == 1 {
						args = []any{"pending"}
					} else {
						args = []any{path, float32(0), float32(1)}
					}
				}
				if len(args) > 0 {
					b, _ := reaper.Encode(reaper.Msg{Addr: msg.Addr, Args: args})
					_, _ = server.WriteTo(b, from)
				}
			}
		}
	}()

	d := &DAW{M: Map{Name: "Ableton Live", Base: 0,
		RenderTrackStart:  &Command{Send: []string{"/live/muse/render/start", "i:{track}", "f:{value}"}, Reply: "/live/muse/render/start"},
		RenderTrackResult: &Command{Send: []string{"/live/muse/render/result"}, Reply: "/live/muse/render/result"},
	}, pc: client, to: server.LocalAddr()}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := d.Execute(ctx, core.OpRenderTrack, map[string]any{"track": 3, "seconds": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	readback, ok := result.Readback.(map[string]any)
	if !ok || readback["path"] != path {
		t.Fatalf("render readback = %#v, want path %q", result.Readback, path)
	}
	if result.Applied.(map[string]any)["track"] != 3 {
		t.Fatalf("render applied = %#v", result.Applied)
	}
	capabilities, err := d.Execute(ctx, core.OpCapabilities, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := capabilities.Readback.([]string)
	foundRender := false
	for _, op := range got {
		if op == core.OpRenderTrack {
			foundRender = true
			break
		}
	}
	if !foundRender {
		t.Fatalf("render capability missing from %#v", got)
	}
}

func TestContractRenderRejectsMissingCapability(t *testing.T) {
	d := &DAW{M: Map{Name: "Ableton Live"}}
	if _, err := d.Execute(context.Background(), core.OpRenderTrack, map[string]any{"track": 1, "seconds": 1}); err == nil {
		t.Fatal("render without mapped commands was accepted")
	}
}
