package oscmap

import (
	"context"
	"net"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

func TestRecordingMapRoundTripAndBadReadbacks(t *testing.T) {
	for _, bad := range []string{"", "token", "range"} {
		t.Run("bad="+bad, func(t *testing.T) {
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
			go func() {
				buf := make([]byte, 65536)
				for {
					n, from, e := server.ReadFrom(buf)
					if e != nil {
						return
					}
					ms, _ := reaper.Decode(buf[:n])
					for _, m := range ms {
						var out []any
						switch m.Addr {
						case "/input/record/start":
							out = []any{m.Args[2]}
							if bad == "token" {
								out = []any{"wrong"}
							}
						case "/input/record/finish":
							out = []any{"new.wav", float32(12), float32(.1)}
							if bad == "range" {
								out = []any{"new.wav", "invalid", float32(.1)}
							}
						case "/render/range":
							out = []any{"range.wav"}
						}
						b, _ := reaper.Encode(reaper.Msg{Addr: m.Addr, Args: out})
						_, _ = server.WriteTo(b, from)
					}
				}
			}()
			m := Map{Base: 0, RecordInputStart: &Command{Send: []string{"/input/record/start", "i:{track}", "f:{value}", "s:{name}"}, Reply: "/input/record/start"}, RecordInputFinish: &Command{Send: []string{"/input/record/finish", "i:{track}", "s:{name}"}, Reply: "/input/record/finish"}, RenderRange: &Command{Send: []string{"/render/range", "i:{track}", "f:{record_start}", "f:{value}"}, Reply: "/render/range"}}
			d := &DAW{M: m, pc: client, to: server.LocalAddr()}
			err = d.StartInput(context.Background(), 1, .1, "token")
			if bad == "token" {
				if err == nil {
					t.Fatal("bad token accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clip, err := d.FinishInput(context.Background(), 1, "token")
			if bad == "range" {
				if err == nil {
					t.Fatal("bad range accepted")
				}
				return
			}
			if err != nil || clip.Path != "new.wav" || clip.PositionSec != 12 {
				t.Fatalf("%+v %v", clip, err)
			}
			if path, err := d.RenderRange(context.Background(), 1, 12, .1); err != nil || path != "range.wav" {
				t.Fatalf("%s %v", path, err)
			}
		})
	}
}
func TestRecordingMissingMapDoesNotSend(t *testing.T) {
	d := &DAW{}
	if err := d.StartInput(context.Background(), 1, 1, "x"); err == nil {
		t.Fatal("unsupported start accepted")
	}
	if _, err := d.FinishInput(context.Background(), 1, "x"); err == nil {
		t.Fatal("unsupported finish accepted")
	}
}
