package reaper

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// peer is a stand-in REAPER: it records what we send and can talk back.
func peer(t *testing.T) (net.PacketConn, *Conn) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Dial("127.0.0.1:0", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); pc.Close() })
	return pc, c
}

func TestSendAllowedOnly(t *testing.T) {
	pc, c := peer(t)
	if err := c.Send("/record"); !errors.Is(err, core.ErrNotAllowed) {
		t.Fatalf("/record: want ErrNotAllowed, got %v", err)
	}
	if err := c.Send("/track/1/volume", 0.5); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	pc.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := Decode(buf[:n])
	if v, _ := ms[0].Float(0); ms[0].Addr != "/track/1/volume" || v != 0.5 {
		t.Fatalf("got %+v", ms)
	}
}

func TestSubscribeAndQuiet(t *testing.T) {
	pc, c := peer(t)
	ch, cancel := c.Subscribe("/track/1/")
	defer cancel()
	to := c.pc.LocalAddr()
	for _, m := range []Msg{{Addr: "/track/2/name", Args: []any{"Bass"}}, {Addr: "/track/1/name", Args: []any{"Guitar"}}} {
		b, _ := Encode(m)
		pc.WriteTo(b, to)
	}
	select {
	case m := <-ch:
		if s, _ := m.String(0); m.Addr != "/track/1/name" || s != "Guitar" {
			t.Fatalf("got %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
	start := time.Now()
	if err := c.WaitQuiet(context.Background(), 100*time.Millisecond, time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 500*time.Millisecond {
		t.Fatalf("WaitQuiet took %v", d)
	}
}
