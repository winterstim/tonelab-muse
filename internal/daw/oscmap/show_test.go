package oscmap

import (
	"context"
	"net"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

func cmd(addr string, args ...string) Command {
	return Command{Send: append([]string{addr}, args...), Reply: addr, Skip: 0}
}

// fakeDAW answers a one-track, one-device API on its own port. When nudge
// is set, showing the device moves its first parameter.
func fakeDAW(t *testing.T, nudge bool) (Map, *[][]any) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	replyPort := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()
	t.Cleanup(func() { pc.Close() })
	to := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: replyPort}
	vals := []any{float32(0.2), float32(0.5)}
	shown := &[][]any{}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			ms, _ := reaper.Decode(buf[:n])
			for _, m := range ms {
				var out []any
				switch m.Addr {
				case "/ping":
				case "/count":
					out = []any{int32(1)}
				case "/name":
					out = []any{"Gtr"}
				case "/devices":
					out = []any{"EQ"}
				case "/pnames":
					out = []any{"On", "Gain"}
				case "/pvalues":
					out = vals
				case "/get":
					out = []any{vals[m.Args[2].(int32)]}
				case "/set":
					vals[m.Args[2].(int32)] = m.Args[3]
					continue
				case "/show":
					*shown = append(*shown, m.Args)
					if nudge {
						vals[0] = float32(1)
					}
				default:
					continue
				}
				b, _ := reaper.Encode(reaper.Msg{Addr: m.Addr, Args: out})
				pc.WriteTo(b, to)
			}
		}
	}()
	m := Map{
		Name: "fake", Port: pc.LocalAddr().(*net.UDPAddr).Port, ReplyPort: replyPort,
		Ping: cmd("/ping"), TrackCount: cmd("/count"), TrackName: cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"), ParamNames: cmd("/pnames", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/pvalues", "i:{track}", "i:{fx}"), GetParam: cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:   Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		ShowDevice: cmd("/show", "i:{track}", "i:{fx}"),
	}
	return m, shown
}

func role(cs []Check, r string) (Check, bool) {
	for _, c := range cs {
		if c.Role == r {
			return c, true
		}
	}
	return Check{}, false
}

func TestOpenUIShowsDeviceInDAWBase(t *testing.T) {
	m, shown := fakeDAW(t, false)
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.OpenUI(context.Background(), 1, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := d.OpenUI(context.Background(), 1, 1, false); err != nil { // hiding sends nothing
		t.Fatal(err)
	}
	if len(*shown) != 1 || (*shown)[0][0] != int32(0) || (*shown)[0][1] != int32(0) {
		t.Fatalf("show_device got %v, want one call for track 0 device 0", *shown)
	}
}

func TestOpenUIWithoutShowDevice(t *testing.T) {
	m, _ := fakeDAW(t, false)
	m.ShowDevice = Command{}
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.OpenUI(context.Background(), 1, 1, true) == nil {
		t.Fatal("want an error when the map cannot show devices")
	}
}

func TestVerifyShowDevice(t *testing.T) {
	for _, nudge := range []bool{false, true} {
		m, _ := fakeDAW(t, nudge)
		d, err := Open(m)
		if err != nil {
			t.Fatal(err)
		}
		cs := Verify(context.Background(), d)
		d.Close()
		c, ok := role(cs, "show_device")
		if !ok || c.OK == nudge {
			t.Fatalf("nudge %v: show_device check %+v (found %v); all %+v", nudge, c, ok, cs)
		}
	}
}

func TestCheckRefusesForbiddenShowDevice(t *testing.T) {
	m, _ := fakeDAW(t, false)
	m.ShowDevice = cmd("/device/delete", "i:{track}", "i:{fx}")
	if m.Check() == nil {
		t.Fatal("a show_device that deletes must be refused")
	}
}
