package reaper_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/fake"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

func studio(t *testing.T) (*reaper.REAPER, *fake.Server) {
	t.Helper()
	// reserve a port for our side, then let the fake send there
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ours := l.LocalAddr().String()
	l.Close()
	srv, err := fake.Listen(ours, fake.Studio())
	if err != nil {
		t.Fatal(err)
	}
	c, err := reaper.Dial(ours, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); srv.Close() })
	return reaper.New(c), srv
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestTracks(t *testing.T) {
	r, _ := studio(t)
	got, err := r.Tracks(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Guitar" || got[1].Name != "Park" {
		t.Fatalf("tracks = %+v", got)
	}
}

func TestFXChain(t *testing.T) {
	r, _ := studio(t)
	chain, err := r.FXChain(ctx(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].Name != "Amp Sim (Test)" || chain[1].Name != "ReaEQ (Cockos)" {
		t.Fatalf("chain = %+v", chain)
	}
	amp := chain[0].Params
	if len(amp) != 11 || !amp[0].Unnamed || amp[4].Name != "Gain" || amp[4].Unnamed || amp[4].Text != "5" {
		t.Fatalf("amp params = %+v", amp)
	}
	if bright := amp[10]; bright.Kind != "switch" || bright.ID != "fx1.p11" {
		t.Fatalf("bright = %+v", bright)
	}
	// reading a second time must still report everything (park trick)
	again, err := r.FXChain(ctx(t), 1)
	if err != nil || len(again) != 2 || len(again[1].Params) != 6 {
		t.Fatalf("second read = %+v, %v", again, err)
	}
}

func TestSetParamConfirms(t *testing.T) {
	r, srv := studio(t)
	a, err := r.SetParam(ctx(t), 1, "fx2.p2", 0.75)
	if err != nil || a.Status != core.StatusConfirmed || a.Confirmed != 0.75 {
		t.Fatalf("set = %+v, %v", a, err)
	}
	if v := srv.Value(1, 2, 2); v != 0.75 {
		t.Fatalf("fake holds %v", v)
	}
	// a dead slot is confirmed at the value REAPER kept, not the one sent
	a, err = r.SetParam(ctx(t), 1, "fx1.p1", 0.9)
	if err != nil || a.Confirmed != 0 || a.Sent != 0.9 {
		t.Fatalf("dead slot = %+v, %v", a, err)
	}
	// no such parameter: no echo
	if _, err := r.SetParam(ctx(t), 1, "fx2.p40", 0.5); !errors.Is(err, core.ErrUnconfirmed) {
		t.Fatalf("missing param: want ErrUnconfirmed, got %v", err)
	}
}

func TestKind(t *testing.T) {
	for in, want := range map[string]string{
		"On": "switch", "normal": "switch", "7.8": "", "-3.0dB": "", "+2.5 dB": "", "100.0": "",
		"-inf": "", "Brit 8000": "discrete", "": "", "50%": "",
	} {
		if got := reaper.Kind(in); got != want {
			t.Errorf("Kind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParamTextFromEcho(t *testing.T) {
	r, srv := studio(t)
	// nothing set yet: one read of the track
	got, err := r.ParamText(ctx(t), 1, "fx2.p2")
	if err != nil || got != "0.0" {
		t.Fatalf("cold read = %q, %v", got, err)
	}
	if _, err := r.SetParam(ctx(t), 1, "fx2.p2", 0.75); err != nil {
		t.Fatal(err)
	}
	before := len(srv.Sent)
	got, err = r.ParamText(ctx(t), 1, "fx2.p2")
	if err != nil || got != "12.0" {
		t.Fatalf("echoed text = %q, %v", got, err)
	}
	if len(srv.Sent) != before {
		t.Fatalf("ParamText after a set sent %v", srv.Sent[before:])
	}
}

// The agent's bisection is SetParam then ParamText per step; on REAPER
// that must not re-select (re-read) the track.
func TestBisectionNoChainReads(t *testing.T) {
	r, srv := studio(t)
	before := len(srv.Sent)
	lo, hi := 0.0, 1.0
	for i := 0; i < 12; i++ {
		mid := (lo + hi) / 2
		if _, err := r.SetParam(ctx(t), 1, "fx2.p2", mid); err != nil {
			t.Fatal(err)
		}
		text, err := r.ParamText(ctx(t), 1, "fx2.p2")
		if err != nil || text == "" {
			t.Fatalf("step %d: %q, %v", i, text, err)
		}
		if (mid-0.5)*48 < -6 {
			lo = mid
		} else {
			hi = mid
		}
	}
	for _, s := range srv.Sent[before:] {
		if s != "/track/1/fx/2/fxparam/2/value" {
			t.Fatalf("bisection sent %q", s)
		}
	}
	if n := len(srv.Sent) - before; n != 12 {
		t.Fatalf("%d messages for 12 steps", n)
	}
}
