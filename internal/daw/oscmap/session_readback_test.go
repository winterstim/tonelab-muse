package oscmap

import (
	"context"
	"net"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

func sessionReadbackDAW(t *testing.T, replies map[string][]any) *DAW {
	t.Helper()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	replyPort := free.LocalAddr().(*net.UDPAddr).Port
	_ = free.Close()
	t.Cleanup(func() { _ = server.Close() })
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: replyPort}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, readErr := server.ReadFrom(buf)
			if readErr != nil {
				return
			}
			messages, _ := reaper.Decode(buf[:n])
			for _, msg := range messages {
				args, ok := replies[msg.Addr]
				if !ok {
					continue
				}
				b, _ := reaper.Encode(reaper.Msg{Addr: msg.Addr, Args: args})
				_, _ = server.WriteTo(b, to)
			}
		}
	}()
	m := Map{
		Name: "fixture", Port: server.LocalAddr().(*net.UDPAddr).Port, ReplyPort: replyPort, Base: 1,
		Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/params", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/values", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:         Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		SetTimeSignature: &Command{Send: []string{"/meter"}, Reply: "/meter", Success: "ok"},
		RemoveSend:       &Command{Send: []string{"/remove"}, Reply: "/remove", Success: "ok"},
		SaveProject:      &Command{Send: []string{"/save"}, Reply: "/save", Success: "ok"},
		GoToMarker:       &Command{Send: []string{"/goto"}, Reply: "/goto", Success: "ok"},
		ListMarkers:      &Command{Send: []string{"/markers"}, Reply: "/markers"},
		CreateMarker:     &Command{Send: []string{"/create"}, Reply: "/create"},
	}
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSessionMutationsRejectNegativeReadbacks(t *testing.T) {
	d := sessionReadbackDAW(t, map[string][]any{
		"/meter": {"false"}, "/remove": {int32(0)}, "/save": {false}, "/goto": {int32(0)},
	})
	ctx := context.Background()
	checks := []struct {
		name string
		run  func() error
	}{
		{"time signature", func() error { return d.SetTimeSignature(ctx, 4, 4) }},
		{"remove send", func() error { return d.RemoveSend(ctx, 1, 1) }},
		{"save project", func() error { return d.SaveProject(ctx) }},
		{"go to marker", func() error { return d.GoToMarker(ctx, 1) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err == nil {
				t.Fatal("negative status was accepted")
			}
		})
	}
}

func TestSessionMutationsAcceptOnlyExactConfiguredStatus(t *testing.T) {
	d := sessionReadbackDAW(t, map[string][]any{
		"/meter": {"ok"}, "/remove": {"ok"}, "/save": {"ok"}, "/goto": {"ok"},
	})
	ctx := context.Background()
	if err := d.SetTimeSignature(ctx, 4, 4); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveSend(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveProject(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.GoToMarker(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

func TestMarkerListRejectsMalformedTypedEntries(t *testing.T) {
	cases := []struct {
		name string
		args []any
	}{
		{"unparsed start", []any{int32(1), "M", "bad", float32(0), int32(0)}},
		{"invalid region flag", []any{int32(1), "M", float32(0), float32(0), int32(2)}},
		{"fractional index", []any{float32(1.5), "M", float32(0), float32(0), int32(0)}},
		{"non-string name", []any{int32(1), int32(4), float32(0), float32(0), int32(0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := sessionReadbackDAW(t, map[string][]any{"/markers": tc.args})
			if _, err := d.Markers(context.Background()); err == nil {
				t.Fatal("malformed marker entry was accepted")
			}
		})
	}
}

func TestCreateMarkerRejectsNonIntegralIndex(t *testing.T) {
	d := sessionReadbackDAW(t, map[string][]any{"/create": {float32(1.5)}})
	if _, err := d.CreateMarker(context.Background(), 0, 0, "M", false); err == nil {
		t.Fatal("fractional marker index was accepted")
	}
}
