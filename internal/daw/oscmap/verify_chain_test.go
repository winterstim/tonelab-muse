package oscmap

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

type chainCheckFake struct {
	pc                 net.PacketConn
	mu                 sync.Mutex
	devices            []any
	insertedName       string
	noInsert           bool
	silenceInsertReply bool
	removeCalls        int
}

func newChainCheckFake(t *testing.T, initial []any, insertedName string, noInsert bool) (Map, *chainCheckFake) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	replyPort := free.LocalAddr().(*net.UDPAddr).Port
	_ = free.Close()
	f := &chainCheckFake{pc: pc, devices: append([]any(nil), initial...), insertedName: insertedName, noInsert: noInsert}
	t.Cleanup(func() { _ = pc.Close() })
	go f.serve()
	call := func(addr string, args ...string) Command {
		return Command{Send: append([]string{addr}, args...), Reply: addr}
	}
	m := Map{Name: "fake", Port: pc.LocalAddr().(*net.UDPAddr).Port, ReplyPort: replyPort, Base: 1,
		Ping: call("/ping"), TrackCount: call("/count"), TrackName: call("/name", "i:{track}"),
		DeviceNames: call("/devices", "i:{track}"), ParamNames: call("/pnames", "i:{track}", "i:{fx}"),
		ParamValues: call("/pvalues", "i:{track}", "i:{fx}"), GetParam: call("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:      Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		DeviceCatalog: call("/catalog", "i:{track}"), InsertDevice: call("/insert", "i:{track}", "s:{name}", "i:{fx}"),
		RemoveDevice: Command{Send: []string{"/remove", "i:{track}", "i:{fx}"}}}
	return m, f
}

func (f *chainCheckFake) serve() {
	buf := make([]byte, 65536)
	for {
		n, addr, err := f.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		msgs, _ := reaper.Decode(buf[:n])
		for _, msg := range msgs {
			var out []any
			f.mu.Lock()
			switch msg.Addr {
			case "/devices":
				out = append([]any(nil), f.devices...)
			case "/catalog":
				out = []any{"selected"}
			case "/insert":
				at := int(msg.Args[2].(int32)) - 1
				name := f.insertedName
				if name == "" {
					name = fmt.Sprint(msg.Args[1])
				}
				if !f.noInsert && at >= 0 && at <= len(f.devices) {
					f.devices = append(f.devices, nil)
					copy(f.devices[at+1:], f.devices[at:])
					f.devices[at] = name
				}
				out = []any{int32(at + 1)}
			case "/remove":
				f.removeCalls++
				at := int(msg.Args[1].(int32)) - 1
				if at >= 0 && at < len(f.devices) {
					f.devices = append(f.devices[:at], f.devices[at+1:]...)
				}
			}
			silenceInsertReply := f.silenceInsertReply
			f.mu.Unlock()
			if msg.Addr == "/remove" || (msg.Addr == "/insert" && silenceInsertReply) {
				continue
			}
			var encoded []byte
			encoded, _ = reaper.Encode(reaper.Msg{Addr: msg.Addr, Args: out})
			_, _ = f.pc.WriteTo(encoded, addr)
		}
	}
}

func chainCheckResult(cs []Check, role string) (Check, bool) {
	for _, c := range cs {
		if c.Role == role {
			return c, true
		}
	}
	return Check{}, false
}

func runChainCheck(t *testing.T, m Map) []Check {
	t.Helper()
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var checks []Check
	verifyChain(context.Background(), d, 1, func(role string, ok bool, detail string, args ...any) bool {
		checks = append(checks, Check{Role: role, OK: ok, Detail: fmt.Sprintf(detail, args...)})
		return ok
	})
	return checks
}

func TestVerifyChainUsesRereadAndRemovesDespiteFailedInsertCheck(t *testing.T) {
	m, f := newChainCheckFake(t, []any{"existing"}, "unexpected", false)
	cs := runChainCheck(t, m)
	if c, ok := chainCheckResult(cs, "insert_device"); !ok || c.OK {
		t.Fatalf("insert check should fail on reread name: %+v", c)
	}
	if c, ok := chainCheckResult(cs, "remove_device"); !ok || !c.OK {
		t.Fatalf("inserted device was not removed after failed check: %+v", c)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sameDevices(f.devices, []any{"existing"}) || f.removeCalls != 1 {
		t.Fatalf("chain=%v remove calls=%d", f.devices, f.removeCalls)
	}
}

func TestVerifyChainDoesNotTrustInsertReplyWithoutRereadEvidence(t *testing.T) {
	m, f := newChainCheckFake(t, []any{"existing"}, "", true)
	cs := runChainCheck(t, m)
	if c, ok := chainCheckResult(cs, "insert_device"); !ok || c.OK {
		t.Fatalf("reply-only insertion should fail: %+v", c)
	}
	if _, ok := chainCheckResult(cs, "remove_device"); ok {
		t.Fatal("must not remove an existing device when reread shows no insertion")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeCalls != 0 || !sameDevices(f.devices, []any{"existing"}) {
		t.Fatalf("false-positive removal: chain=%v calls=%d", f.devices, f.removeCalls)
	}
}

func TestVerifyChainAcceptsNoInsertReplyWhenRereadConfirms(t *testing.T) {
	m, f := newChainCheckFake(t, []any{"existing"}, "", false)
	m.InsertDevice.Reply = ""
	cs := runChainCheck(t, m)
	if c, ok := chainCheckResult(cs, "insert_device"); !ok || !c.OK {
		t.Fatalf("reread-confirmed insert failed: %+v", c)
	}
	if c, ok := chainCheckResult(cs, "remove_device"); !ok || !c.OK {
		t.Fatalf("device not removed: %+v", c)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sameDevices(f.devices, []any{"existing"}) || f.removeCalls != 1 {
		t.Fatalf("chain=%v remove calls=%d", f.devices, f.removeCalls)
	}
}

func TestVerifyChainCleansUpAfterInsertReplyFailure(t *testing.T) {
	m, f := newChainCheckFake(t, []any{"existing"}, "", false)
	f.mu.Lock()
	f.silenceInsertReply = true
	f.mu.Unlock()
	cs := runChainCheck(t, m)
	if c, ok := chainCheckResult(cs, "insert_device"); !ok || c.OK {
		t.Fatalf("missing insert reply should fail the check: %+v", c)
	}
	if c, ok := chainCheckResult(cs, "remove_device"); !ok || !c.OK {
		t.Fatalf("chain-proven insertion was not cleaned up after reply failure: %+v", c)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !sameDevices(f.devices, []any{"existing"}) || f.removeCalls != 1 {
		t.Fatalf("chain=%v remove calls=%d", f.devices, f.removeCalls)
	}
}
