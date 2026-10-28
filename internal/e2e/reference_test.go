// Package e2e wires the real pieces together (agent + ears) on recorded
// clips, where unit tests use fakes.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/fake"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/agent"
	"github.com/winterstim/tonelab-muse/pkg/core"
	"github.com/winterstim/tonelab-muse/pkg/ears"
)

// clip "renders" a fixed recording, so the gap is measured on real audio.
type clip struct{ path, dir string }

func (c clip) Render(context.Context, int, float64) (string, error) { return c.path, nil }
func (c clip) Spectrogram([]string, []string) (string, error) {
	p := filepath.Join(c.dir, "s.png")
	w, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer w.Close()
	return p, png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 2)))
}

type noLLM struct{}

func (noLLM) Chat(context.Context, string, []core.Msg, []core.Tool) (core.Reply, error) {
	return core.Reply{Text: "ok"}, nil
}

type gapSink struct{ gap map[string]float64 }

func (g *gapSink) Emit(e core.Event) {
	if e.Kind == "reference_gap" {
		g.gap = e.Data.(map[string]any)["gap_db"].(map[string]float64)
	}
}

func TestReferenceGapOnRealClips(t *testing.T) {
	c := clip{"../../pkg/ears/testdata/mudd.wav", t.TempDir()}
	ev := &gapSink{}
	a := agent.New(agent.Deps{LLM: noLLM{}, Renderer: c, Measurer: ears.Meter{}, Visualizer: c, Sink: ev}, agent.Config{})
	if _, err := a.SetReference("../../pkg/ears/testdata/distortion.wav"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.MatchReference(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// a distorted reference has far more top end than a muddy clean take
	if ev.gap["presence"] <= 0 || ev.gap["mud"] >= 0 {
		t.Fatalf("gap = %v; want presence > 0 and mud < 0", ev.gap)
	}
}

type scriptedLLM struct {
	replies []core.Reply
	seen    [][]core.Msg
}

func (s *scriptedLLM) Chat(_ context.Context, _ string, msgs []core.Msg, _ []core.Tool) (core.Reply, error) {
	s.seen = append(s.seen, msgs)
	if len(s.replies) == 0 {
		return core.Reply{Text: "done"}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func toolCall(name string, args any) core.Reply {
	b, _ := json.Marshal(args)
	return core.Reply{ToolCalls: []core.ToolCall{{ID: name, Name: name, Args: b}}}
}

func TestSeamlessTurboAndChunkEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	// Set up Fake REAPER with an opaque plugin (Param 1..4 all dead) + ReaEQ
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ours := l.LocalAddr().String()
	l.Close()

	tracks := []*fake.Track{
		{
			Name: "Guitar",
			FX: []*fake.FX{
				{
					Name: "AU: AmpliTube 5",
					Params: []*fake.Param{
						{Name: "Param 1", Dead: true},
						{Name: "Param 2", Dead: true},
					},
				},
				{
					Name:   "VST: Baduzz",
					Params: []*fake.Param{{Name: "Drive", Value: 0.5}},
				},
			},
		},
		{Name: "Park"},
	}
	srv, err := fake.Listen(ours, tracks)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := reaper.Dial(ours, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	bridgeDir := filepath.Join(dir, "bridge")
	br := &reaper.Bridge{Dir: bridgeDir}
	rp := reaper.New(conn)
	rp.Bridge = br

	// Simulated XML state chunk for AmpliTube 5 (with active AmpA and CabA)
	var mu sync.Mutex
	currentChunk := `<?xml version="1.0" encoding="UTF-8" ?><Program Version="2" Format="at5p" GUID="94988e45-b0ca-44f5-9b4a-34f9e1869095" PresetName="Default"><AmpA Bypass="0" Mute="0" Model="8fe96936-5178-4950-9b80-d89c32534bad"><Amp PreAmp_JCM800AT4="3.0" Bass_JCM800AT4="6.0" Middle_JCM800AT4="5.0" Treble_JCM800AT4="5.0" Presence_JCM800AT4="2.5" Master_JCM800AT4="6.0" /></AmpA><CabA Bypass="0" Mute="0"><Cab HighLevel="0.77" RoomType="Hall" /></CabA></Program>`
	var forceRemoved []string

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			cmdPath := filepath.Join(bridgeDir, "cmd.txt")
			data, err := os.ReadFile(cmdPath)
			if err != nil {
				continue
			}
			os.Remove(cmdPath)
			parts := strings.Split(strings.TrimRight(string(data), "\r\n"), "\t")
			if len(parts) < 5 {
				continue
			}
			id, op, _, arg, _ := parts[0], parts[1], parts[2], parts[3], parts[4]
			a, bVal := "ok", ""
			switch op {
			case "get_chunk":
				if arg == "1" {
					mu.Lock()
					_ = os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(currentChunk), 0o600)
					mu.Unlock()
					a = "written"
				} else {
					_ = os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(""), 0o600)
				}
			case "set_chunk":
				in, _ := os.ReadFile(filepath.Join(bridgeDir, "chunk_in.txt"))
				mu.Lock()
				currentChunk = string(in)
				mu.Unlock()
				a = "updated"
			case "force_remove":
				forceRemoved = append(forceRemoved, arg)
				a, bVal = arg, "VST: Baduzz"
			}
			tmp := filepath.Join(bridgeDir, "reply.tmp")
			_ = os.WriteFile(tmp, []byte(fmt.Sprintf("%s\tok\t%s\t%s\n", id, a, bVal)), 0o600)
			_ = os.Rename(tmp, filepath.Join(bridgeDir, "reply.txt"))
		}
	}()

	llm := &scriptedLLM{replies: []core.Reply{
		toolCall("describe_chain", map[string]any{"track": 1}),
		// Remove user-placed plugin #2 in Turbo mode without needing prior consent
		toolCall("remove_plugin", map[string]any{"track": 1, "fx": 2}),
		// Propose tuning projected chunk parameters fx1.p1003 (Amp PreAmp) and fx1.p1004 (Amp Bass)
		toolCall("propose_tone", map[string]any{
			"track": 1,
			"goal":  "tighter high gain",
			"changes": []map[string]any{
				{"param_id": "fx1.p1003", "value": 0.85, "why": "push preamp gain to 8.5"},
				{"param_id": "fx1.p1004", "value": 0.35, "why": "tighten bass to 3.5"},
			},
		}),
		toolCall("apply_plan", map[string]any{"plan_id": "p1"}),
		toolCall("listen", map[string]any{"track": 1}),
		{Text: "Removed Baduzz and dialed in AmpliTube 5 AmpA PreAmp to 8.5 and Bass to 3.5."},
	}}

	c := clip{"../../pkg/ears/testdata/distortion.wav", dir}
	ag := agent.New(agent.Deps{
		DAW:        rp,
		LLM:        llm,
		Chain:      br,
		Renderer:   c,
		Measurer:   ears.Meter{},
		Visualizer: c,
	}, agent.Config{})

	ans, err := ag.Run(context.Background(), "remove baduzz and tighten the amp", agent.Turbo)
	if err != nil {
		t.Fatalf("Turbo Run failed: %v", err)
	}
	if !strings.Contains(ans, "AmpliTube 5") {
		t.Fatalf("unexpected answer: %q", ans)
	}

	// Verify Turbo auto-enabled the bridge and force-removed FX 2 without consent block
	if !br.Enabled() {
		t.Fatal("Turbo mode did not auto-enable the bridge")
	}
	if len(forceRemoved) != 1 || forceRemoved[0] != "2" {
		t.Fatalf("expected FX 2 to be force-removed in Turbo mode, got %v", forceRemoved)
	}

	// Verify the XML chunk was patched in-place with PreAmp=8.5 and Bass=3.5
	mu.Lock()
	patched := currentChunk
	mu.Unlock()
	if !strings.Contains(patched, `PreAmp_JCM800AT4="8.50"`) || !strings.Contains(patched, `Bass_JCM800AT4="3.50"`) {
		t.Fatalf("chunk was not patched as expected:\n%s", patched)
	}
}
