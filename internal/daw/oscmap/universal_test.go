package oscmap

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

var (
	_ core.Bypasser           = (*DAW)(nil)
	_ core.Bypasser           = (*Chain)(nil)
	_ core.AudioEngineChecker = (*DAW)(nil)
	_ core.AudioEngineChecker = (*Chain)(nil)
	_ core.SessionController  = (*DAW)(nil)
	_ core.SessionController  = (*Chain)(nil)
)

// universalFakeDAW creates a mock OSC server for comprehensive testing.
type universalFakeDAW struct {
	m            Map
	pc           net.PacketConn
	to           *net.UDPAddr
	received     []reaper.Msg
	audioRunning any
	mu           sync.Mutex
	t            *testing.T
}

func newUniversalFakeDAW(t *testing.T) *universalFakeDAW {
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

	f := &universalFakeDAW{
		pc: pc,
		to: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: replyPort},
		t:  t,
	}

	go f.serve()

	f.m = Map{
		Name:        "UniversalFake",
		Port:        pc.LocalAddr().(*net.UDPAddr).Port,
		ReplyPort:   replyPort,
		Base:        0,
		Ping:        cmd("/ping"),
		TrackCount:  cmd("/count"),
		TrackName:   cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"),
		ParamNames:  cmd("/pnames", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/pvalues", "i:{track}", "i:{fx}"),
		GetParam:    cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:    Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		SetVolume:   Command{Send: []string{"/track/volume", "i:{track}", "f:{value}"}},
		SetPan:      Command{Send: []string{"/track/pan", "i:{track}", "f:{value}"}},
		SetMute:     Command{Send: []string{"/track/mute", "i:{track}", "i:{value}"}},
		SetSolo:     Command{Send: []string{"/track/solo", "i:{track}", "i:{value}"}},
		SetBypass:   Command{Send: []string{"/device/set/is_active", "i:{track}", "i:{fx}", "i:{value}"}},
		MoveDevice:  Command{Send: []string{"/device/move", "i:{track}", "i:{fx}", "i:{to}"}},
		DevicePresets: Command{
			Send:  []string{"/device/presets", "i:{track}", "i:{fx}"},
			Reply: "/device/presets",
			Skip:  2,
		},
		SetPreset:     Command{Send: []string{"/device/set_preset", "i:{track}", "i:{fx}", "s:{preset}"}},
		SavePresetAs:  Command{Send: []string{"/device/save_preset_as", "i:{track}", "i:{fx}", "s:{preset}"}},
		DeviceCatalog: cmd("/catalog"),
		InsertDevice: Command{
			Send:  []string{"/device/insert", "i:{track}", "s:{name}", "i:{fx}"},
			Reply: "/device/insert",
			Skip:  0,
		},
		RemoveDevice:   Command{Send: []string{"/device/remove", "i:{track}", "i:{fx}"}},
		ShowDevice:     cmd("/show", "i:{track}", "i:{fx}"),
		CreateTrack:    &Command{Send: []string{"/track/create", "i:{track}", "s:{name}"}, Reply: "/track/create", Skip: 0},
		DuplicateTrack: &Command{Send: []string{"/track/duplicate", "i:{track}"}, Reply: "/track/duplicate", Skip: 0},
		DeleteTrack:    &Command{Send: []string{"/track/delete", "i:{track}"}},
		RenameTrack:    &Command{Send: []string{"/track/rename", "i:{track}", "s:{name}"}},
		InsertMedia:    &Command{Send: []string{"/track/insert_media", "i:{track}", "s:{name}"}, Reply: "/track/insert_media", Skip: 0},
		CreateSend:     &Command{Send: []string{"/track/send", "i:{track}", "i:{to}", "f:{value}"}},
		SetTempo:       &Command{Send: []string{"/song/tempo", "f:{value}"}},
		SetLoop:        &Command{Send: []string{"/song/loop", "f:{value}", "i:{to}"}},
		AudioEngine:    Command{Send: []string{"/song/audio_running"}, Reply: "/song/audio_running"},
		Extra: map[string]Command{
			"custom_echo": {
				Send:  []string{"/custom/echo", "i:{track}", "s:{name}", "f:{value}"},
				Reply: "/custom/echo",
				Skip:  0,
			},
		},
	}
	return f
}

func (f *universalFakeDAW) serve() {
	buf := make([]byte, 65536)
	devs := []any{"Comp", "EQ"}
	for {
		n, _, err := f.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ms, _ := reaper.Decode(buf[:n])
		f.mu.Lock()
		f.received = append(f.received, ms...)
		f.mu.Unlock()

		for _, m := range ms {
			var out []any
			sendReply := false

			switch m.Addr {
			case "/ping":
				sendReply = true
			case "/count":
				out = []any{int32(1)}
				sendReply = true
			case "/name":
				out = []any{"Audio 1"}
				sendReply = true
			case "/devices":
				out = devs
				sendReply = true
			case "/pnames":
				out = []any{"Thresh", "Ratio"}
				sendReply = true
			case "/pvalues":
				out = []any{float32(0.5), float32(0.25)}
				sendReply = true
			case "/get":
				out = []any{float32(0.5)}
				sendReply = true
			case "/catalog":
				out = []any{"Reverb", "Delay"}
				sendReply = true
			case "/device/insert":
				devs = append(devs, m.Args[1])
				out = []any{int32(len(devs) - 1)}
				sendReply = true
			case "/device/remove":
				idx := int(m.Args[1].(int32))
				if idx >= 0 && idx < len(devs) {
					devs = append(devs[:idx], devs[idx+1:]...)
				}
			case "/device/presets":
				out = []any{m.Args[0], m.Args[1], "Clean", "Punchy", "Heavy"}
				sendReply = true
			case "/device/set_preset":
				out = []any{"ok", "EQ Eight", "Gain", float32(0.5)}
				sendReply = true
			case "/live/muse/preset/load":
				out = []any{"ok", "EQ Eight", "Gain", float32(0.5)}
				sendReply = true
			case "/track/create":
				out = []any{int32(1)}
				sendReply = true
			case "/track/duplicate":
				out = []any{int32(2)}
				sendReply = true
			case "/track/delete":
				// no reply
			case "/track/rename":
				// no reply
			case "/track/insert_media":
				out = []any{int32(0)}
				sendReply = true
			case "/track/send":
				// no reply
			case "/song/tempo":
				// no reply
			case "/song/loop":
				// no reply
			case "/song/audio_running":
				f.mu.Lock()
				ar := f.audioRunning
				f.mu.Unlock()
				if ar != nil {
					out = []any{ar}
				} else {
					out = []any{int32(1)}
				}
				sendReply = true
			case "/custom/echo":
				out = m.Args
				sendReply = true
			}

			if sendReply {
				b, _ := reaper.Encode(reaper.Msg{Addr: m.Addr, Args: out})
				f.pc.WriteTo(b, f.to)
			}
		}
	}
}

func (f *universalFakeDAW) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *universalFakeDAW) waitMsgSince(start int, addr string) *reaper.Msg {
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for i := start; i < len(f.received); i++ {
			if f.received[i].Addr == addr {
				m := f.received[i]
				f.mu.Unlock()
				return &m
			}
		}
		f.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return nil
}

func TestSetTrackUniversal(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// 1. Volume
	c := fake.count()
	if v, err := d.SetTrack(ctx, 1, "volume", 0.75); err != nil || v != 0.75 {
		t.Fatalf("SetTrack volume: got (%v, %v), want (0.75, nil)", v, err)
	}
	msg := fake.waitMsgSince(c, "/track/volume")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != float32(0.75) {
		t.Fatalf("unexpected volume msg: %+v", msg)
	}

	// 2. Pan
	c = fake.count()
	if v, err := d.SetTrack(ctx, 1, "pan", -0.25); err != nil || v != -0.25 {
		t.Fatalf("SetTrack pan: got (%v, %v), want (-0.25, nil)", v, err)
	}
	msg = fake.waitMsgSince(c, "/track/pan")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != float32(-0.25) {
		t.Fatalf("unexpected pan msg: %+v", msg)
	}

	// 3. Mute (integer parsing for i:{value})
	c = fake.count()
	if v, err := d.SetTrack(ctx, 1, "mute", 1.0); err != nil || v != 1.0 {
		t.Fatalf("SetTrack mute 1: got (%v, %v)", v, err)
	}
	msg = fake.waitMsgSince(c, "/track/mute")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != int32(1) {
		t.Fatalf("unexpected mute msg for 1: %+v", msg)
	}

	c = fake.count()
	if v, err := d.SetTrack(ctx, 1, "mute", 0.0); err != nil || v != 0.0 {
		t.Fatalf("SetTrack mute 0: got (%v, %v)", v, err)
	}
	msg = fake.waitMsgSince(c, "/track/mute")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != int32(0) {
		t.Fatalf("unexpected mute msg for 0: %+v", msg)
	}

	// 4. Solo (integer parsing for i:{value})
	c = fake.count()
	if v, err := d.SetTrack(ctx, 1, "solo", 1.0); err != nil || v != 1.0 {
		t.Fatalf("SetTrack solo 1: got (%v, %v)", v, err)
	}
	msg = fake.waitMsgSince(c, "/track/solo")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != int32(1) {
		t.Fatalf("unexpected solo msg for 1: %+v", msg)
	}

	// 5. Unsupported prop
	if _, err := d.SetTrack(ctx, 1, "reverb_send", 0.5); err == nil {
		t.Fatal("expected error for unsupported prop")
	}

	// 6. Empty command Send
	d.Close()
	emptyM := fake.m
	emptyM.SetSolo = Command{}
	dEmpty, err := Open(emptyM)
	if err != nil {
		t.Fatal(err)
	}
	defer dEmpty.Close()
	if _, err := dEmpty.SetTrack(ctx, 1, "solo", 1.0); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected 'not supported' error, got %v", err)
	}
}

func TestSetPresetReadsBackBrowserReply(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	fake.m.SetPreset = Command{
		Send:  []string{"/live/muse/preset/load", "i:{track}", "i:{fx}", "s:{preset}"},
		Reply: "/live/muse/preset/load",
	}
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	start := fake.count()
	if err := d.SetPreset(context.Background(), 1, 2, "sounds/Pack/Lead"); err != nil {
		t.Fatal(err)
	}
	msg := fake.waitMsgSince(start, "/live/muse/preset/load")
	if msg == nil || len(msg.Args) != 3 || msg.Args[0] != int32(0) || msg.Args[1] != int32(1) || msg.Args[2] != "sounds/Pack/Lead" {
		t.Fatalf("unexpected browser preset request: %+v", msg)
	}
}

func TestSetBypassUniversal(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// SetBypass on address containing "is_active":
	// Bypassed = true -> is_active = 0
	c := fake.count()
	if err := d.SetBypass(ctx, 1, 1, true); err != nil {
		t.Fatal(err)
	}
	msg := fake.waitMsgSince(c, "/device/set/is_active")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != int32(0) {
		t.Fatalf("expected is_active=0 for bypassed=true, got %+v", msg)
	}

	// Bypassed = false -> is_active = 1
	c = fake.count()
	if err := d.SetBypass(ctx, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	msg = fake.waitMsgSince(c, "/device/set/is_active")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != int32(1) {
		t.Fatalf("expected is_active=1 for bypassed=false, got %+v", msg)
	}

	// Non-active bypass command
	d.Close()
	bypassM := fake.m
	bypassM.SetBypass = Command{Send: []string{"/device/bypass", "i:{track}", "i:{fx}", "i:{value}"}}
	dBypass, err := Open(bypassM)
	if err != nil {
		t.Fatal(err)
	}
	defer dBypass.Close()

	c = fake.count()
	if err := dBypass.SetBypass(ctx, 1, 1, true); err != nil {
		t.Fatal(err)
	}
	msg = fake.waitMsgSince(c, "/device/bypass")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != int32(1) {
		t.Fatalf("expected bypass=1 for bypassed=true, got %+v", msg)
	}

	c = fake.count()
	if err := dBypass.SetBypass(ctx, 1, 1, false); err != nil {
		t.Fatal(err)
	}
	msg = fake.waitMsgSince(c, "/device/bypass")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != int32(0) {
		t.Fatalf("expected bypass=0 for bypassed=false, got %+v", msg)
	}
}

func TestPresetsUniversal(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// Presets
	cur, presets, err := d.Presets(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cur != "" {
		t.Fatalf("expected empty current preset, got %q", cur)
	}
	if len(presets) != 3 || presets[0] != "Clean" || presets[1] != "Punchy" || presets[2] != "Heavy" {
		t.Fatalf("unexpected presets: %v", presets)
	}

	// SetPreset
	c := fake.count()
	if err := d.SetPreset(ctx, 1, 1, "Punchy"); err != nil {
		t.Fatal(err)
	}
	msg := fake.waitMsgSince(c, "/device/set_preset")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != "Punchy" {
		t.Fatalf("unexpected set_preset msg: %+v", msg)
	}
	c = fake.count()
	if err := d.SavePresetAs(ctx, 1, 1, "Verse"); err != nil {
		t.Fatal(err)
	}
	msg = fake.waitMsgSince(c, "/device/save_preset_as")
	if msg == nil || len(msg.Args) != 3 || msg.Args[2] != "Verse" {
		t.Fatalf("unexpected save_preset_as msg: %+v", msg)
	}

	// Test forwarding on Chain
	consentFile := filepath.Join(t.TempDir(), "consent")
	chain := &Chain{D: d, Consent: consentFile}
	_, cPresets, err := chain.Presets(ctx, 1, 1)
	if err != nil || len(cPresets) != 3 {
		t.Fatalf("chain.Presets failed: %v, %v", cPresets, err)
	}
	if err := chain.SetPreset(ctx, 1, 1, "Heavy"); err != nil {
		t.Fatalf("chain.SetPreset failed: %v", err)
	}
}

func TestChainTurboAndMove(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	consentPath := filepath.Join(t.TempDir(), "sub", "dir", "consent")
	chain := &Chain{D: d, Consent: consentPath}

	// 1. MoveFX before consent fails
	if err := chain.MoveFX(ctx, 1, 1, 2); err == nil {
		t.Fatal("expected error moving FX without consent")
	}

	// 2. EnableAuto creates consent file
	if err := chain.EnableAuto(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(consentPath); err != nil {
		t.Fatalf("consent file was not created: %v", err)
	}

	// 3. MoveFX now succeeds
	c := fake.count()
	if err := chain.MoveFX(ctx, 1, 1, 2); err != nil {
		t.Fatal(err)
	}
	msg := fake.waitMsgSince(c, "/device/move")
	if msg == nil || len(msg.Args) != 3 || msg.Args[0] != int32(0) || msg.Args[1] != int32(0) || msg.Args[2] != int32(1) {
		t.Fatalf("unexpected move msg: %+v", msg)
	}

	// 4. ForceRemoveFX removes a pre-existing device (not added by agent)
	c = fake.count()
	removed, err := chain.ForceRemoveFX(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != "Comp" {
		t.Fatalf("expected removed 'Comp', got %q", removed)
	}
	msg = fake.waitMsgSince(c, "/device/remove")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != int32(0) {
		t.Fatalf("unexpected remove msg: %+v", msg)
	}

	// 5. ForceRemoveFX auto-enables consent if consent file was missing
	os.Remove(consentPath)
	removed2, err := chain.ForceRemoveFX(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed2 != "EQ" {
		t.Fatalf("expected removed 'EQ', got %q", removed2)
	}
	if _, err := os.Stat(consentPath); err != nil {
		t.Fatalf("ForceRemoveFX did not auto-enable consent: %v", err)
	}
}

func TestRunExtra(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	res, err := d.RunExtra(ctx, "custom_echo", 1, 0, 0, 42.5, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0] != int32(0) || res[1] != "hello" || res[2] != float32(42.5) {
		t.Fatalf("unexpected RunExtra reply: %v", res)
	}

	// Unknown extra command
	if _, err := d.RunExtra(ctx, "non_existent", 1, 0, 0, 0, ""); err == nil {
		t.Fatal("expected error for unknown extra command")
	}
}

func TestMapCheckRules(t *testing.T) {
	// 1. Ableton JSON passes Check()
	m, err := Load("bundled/ableton.json")
	if err != nil {
		t.Fatalf("Load ableton.json failed: %v", err)
	}
	if err := m.Check(); err != nil {
		t.Fatalf("ableton.json Check() failed: %v", err)
	}

	// 2. set_preset with "load_preset" is permitted
	mLoadPreset := m
	mLoadPreset.SetPreset = Command{Send: []string{"/live/device/load_preset", "i:{track}", "i:{fx}", "s:{preset}"}}
	if err := mLoadPreset.Check(); err != nil {
		t.Fatalf("load_preset in set_preset should be allowed, got: %v", err)
	}

	// 3. insert_device with "create_device" is permitted
	mCreate := m
	mCreate.InsertDevice = Command{Send: []string{"/live/track/create_device", "i:{track}", "s:{name}", "i:{fx}"}}
	if err := mCreate.Check(); err != nil {
		t.Fatalf("create_device in insert_device should be allowed, got: %v", err)
	}

	// 4. remove_device with "delete_device" is permitted
	mRemove := m
	mRemove.RemoveDevice = Command{Send: []string{"/live/track/delete_device", "i:{track}", "i:{fx}"}}
	if err := mRemove.Check(); err != nil {
		t.Fatalf("delete_device in remove_device should be allowed, got: %v", err)
	}

	// 5. Destructive commands are blocked everywhere
	for _, forbiddenWord := range []string{"quit", "save", "export", "clear", "record", "arm"} {
		badMap := m
		badMap.SetPreset = Command{Send: []string{"/live/device/" + forbiddenWord, "i:{track}", "i:{fx}"}}
		if badMap.Check() == nil {
			t.Fatalf("forbidden word %q in set_preset was not blocked", forbiddenWord)
		}

		badMap2 := m
		badMap2.SetParam = Command{Send: []string{"/live/param/" + forbiddenWord, "f:{value}"}}
		if badMap2.Check() == nil {
			t.Fatalf("forbidden word %q in set_param was not blocked", forbiddenWord)
		}

		badMap3 := m
		badMap3.Extra = map[string]Command{"bad": {Send: []string{"/live/" + forbiddenWord}}}
		if badMap3.Check() == nil {
			t.Fatalf("forbidden word %q in Extra was not blocked", forbiddenWord)
		}
	}

	// 6. "load" in general command like set_param is blocked
	badMapLoad := m
	badMapLoad.SetParam = Command{Send: []string{"/live/device/load_something", "f:{value}"}}
	if badMapLoad.Check() == nil {
		t.Fatal("expected 'load' to be blocked on set_param")
	}

	// 7. "delete" in set_preset is blocked
	badMapDelPreset := m
	badMapDelPreset.SetPreset = Command{Send: []string{"/live/device/delete_preset", "s:{preset}"}}
	if badMapDelPreset.Check() == nil {
		t.Fatal("expected 'delete' to be blocked on set_preset")
	}
}

func TestBuildIntArgFallback(t *testing.T) {
	cmd := Command{Send: []string{"/toggle", "i:{value}"}}

	// Float 1.0 -> int32(1)
	_, args, err := cmd.build(vars{value: 1.0})
	if err != nil || len(args) != 1 || args[0] != int32(1) {
		t.Fatalf("expected int32(1), got args=%v, err=%v", args, err)
	}

	// Float 0.0 -> int32(0)
	_, args, err = cmd.build(vars{value: 0.0})
	if err != nil || len(args) != 1 || args[0] != int32(0) {
		t.Fatalf("expected int32(0), got args=%v, err=%v", args, err)
	}

	// Placeholders {to} and {preset}
	cmdMove := Command{Send: []string{"/move", "i:{fx}", "i:{to}", "s:{preset}"}}
	_, args, err = cmdMove.build(vars{fx: 2, to: 5, preset: "MyPreset"})
	if err != nil || len(args) != 3 || args[0] != int32(2) || args[1] != int32(5) || args[2] != "MyPreset" {
		t.Fatalf("expected [2, 5, MyPreset], got args=%v, err=%v", args, err)
	}
}

func TestSessionControllerUniversal(t *testing.T) {
	fake := newUniversalFakeDAW(t)
	fake.m.Play = Command{Send: []string{"/play"}}
	fake.m.Stop = Command{Send: []string{"/stop"}}
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	// 1. CreateTrack
	c := fake.count()
	trIdx, err := d.CreateTrack(ctx, "Gtr Solo", 0)
	if err != nil || trIdx != 2 { // fake replies int32(1) in base 0 -> 2 in base 1
		t.Fatalf("CreateTrack failed: got (%v, %v), want (2, nil)", trIdx, err)
	}
	msg := fake.waitMsgSince(c, "/track/create")
	if msg == nil || len(msg.Args) != 2 || msg.Args[1] != "Gtr Solo" {
		t.Fatalf("unexpected create_track msg: %+v", msg)
	}

	// 2. DuplicateTrack
	c = fake.count()
	dupIdx, err := d.DuplicateTrack(ctx, 1, "Gtr Solo Dbl")
	if err != nil || dupIdx != 3 { // fake replies int32(2) in base 0 -> 3 in base 1
		t.Fatalf("DuplicateTrack failed: got (%v, %v), want (3, nil)", dupIdx, err)
	}
	msg = fake.waitMsgSince(c, "/track/duplicate")
	if msg == nil || len(msg.Args) != 1 || msg.Args[0] != int32(0) {
		t.Fatalf("unexpected duplicate_track msg: %+v", msg)
	}

	// 3. DeleteTrack
	c = fake.count()
	if err := d.DeleteTrack(ctx, 2); err != nil {
		t.Fatalf("DeleteTrack failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/track/delete")
	if msg == nil || len(msg.Args) != 1 || msg.Args[0] != int32(1) {
		t.Fatalf("unexpected delete_track msg: %+v", msg)
	}

	// 4. RenameTrack
	c = fake.count()
	if err := d.RenameTrack(ctx, 1, "Main Rhythm"); err != nil {
		t.Fatalf("RenameTrack failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/track/rename")
	if msg == nil || len(msg.Args) != 2 || msg.Args[0] != int32(0) || msg.Args[1] != "Main Rhythm" {
		t.Fatalf("unexpected rename_track msg: %+v", msg)
	}

	// 5. InsertMedia
	c = fake.count()
	if err := d.InsertMedia(ctx, 1, "/audio/take1.wav", 4.5); err != nil {
		t.Fatalf("InsertMedia failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/track/insert_media")
	if msg == nil || len(msg.Args) != 2 || msg.Args[0] != int32(0) || msg.Args[1] != "/audio/take1.wav" {
		t.Fatalf("unexpected insert_media msg: %+v", msg)
	}

	// 6. CreateSend
	c = fake.count()
	if err := d.CreateSend(ctx, 1, 2, -6.0); err != nil {
		t.Fatalf("CreateSend failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/track/send")
	if msg == nil || len(msg.Args) != 3 || msg.Args[0] != int32(0) || msg.Args[1] != int32(1) || msg.Args[2] != float32(-6.0) {
		t.Fatalf("unexpected create_send msg: %+v", msg)
	}

	// 7. SetTransport
	// play
	c = fake.count()
	if err := d.SetTransport(ctx, "play", 0, 0, 0, 0); err != nil {
		t.Fatalf("SetTransport play failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/play")
	if msg == nil {
		t.Fatal("expected /play msg")
	}

	// stop
	c = fake.count()
	if err := d.SetTransport(ctx, "stop", 0, 0, 0, 0); err != nil {
		t.Fatalf("SetTransport stop failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/stop")
	if msg == nil {
		t.Fatal("expected /stop msg")
	}

	// tempo
	c = fake.count()
	if err := d.SetTransport(ctx, "tempo", 132.5, 0, 0, 0); err != nil {
		t.Fatalf("SetTransport tempo failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/song/tempo")
	if msg == nil || len(msg.Args) != 1 || msg.Args[0] != float32(132.5) {
		t.Fatalf("unexpected tempo msg: %+v", msg)
	}

	// loop
	c = fake.count()
	if err := d.SetTransport(ctx, "loop", 0, 0, 8.0, 16.0); err != nil {
		t.Fatalf("SetTransport loop failed: %v", err)
	}
	msg = fake.waitMsgSince(c, "/song/loop")
	if msg == nil || len(msg.Args) != 2 || msg.Args[0] != float32(8.0) || msg.Args[1] != int32(16) {
		t.Fatalf("unexpected loop msg: %+v", msg)
	}

	// 8. Forwarding on Chain
	chain := &Chain{D: d}
	if tr, err := chain.CreateTrack(ctx, "Acoustic", 0); err != nil || tr != 2 {
		t.Fatalf("chain.CreateTrack failed: %v", err)
	}
	if err := chain.SetTransport(ctx, "play", 0, 0, 0, 0); err != nil {
		t.Fatalf("chain.SetTransport failed: %v", err)
	}
}

func TestAudioEngineUniversal(t *testing.T) {
	ctx := context.Background()
	fake := newUniversalFakeDAW(t)
	d, err := Open(fake.m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// 1. Default: running (int32(1))
	running, err := d.AudioEngineRunning(ctx)
	if err != nil || !running {
		t.Fatalf("expected running true, got %v, err: %v", running, err)
	}

	// 2. Off: int32(0)
	fake.mu.Lock()
	fake.audioRunning = int32(0)
	fake.mu.Unlock()
	running, err = d.AudioEngineRunning(ctx)
	if err != nil || running {
		t.Fatalf("expected running false, got %v, err: %v", running, err)
	}

	// 3. Float32: float32(1.0)
	fake.mu.Lock()
	fake.audioRunning = float32(1.0)
	fake.mu.Unlock()
	running, err = d.AudioEngineRunning(ctx)
	if err != nil || !running {
		t.Fatalf("expected running true from float32, got %v, err: %v", running, err)
	}

	// 4. String: "off"
	fake.mu.Lock()
	fake.audioRunning = "off"
	fake.mu.Unlock()
	running, err = d.AudioEngineRunning(ctx)
	if err != nil || running {
		t.Fatalf("expected running false from string off, got %v, err: %v", running, err)
	}

	// 5. Chain forwarding
	chain := &Chain{D: d}
	running, err = chain.AudioEngineRunning(ctx)
	if err != nil || running {
		t.Fatalf("chain expected running false, got %v, err: %v", running, err)
	}

	// 6. When audio_engine is not configured in map: returns error
	d.Close()
	dNoCmd, err := Open(Map{
		Name: fake.m.Name, Port: fake.m.Port, ReplyPort: fake.m.ReplyPort, Base: 0,
		Ping: fake.m.Ping, TrackCount: fake.m.TrackCount, TrackName: fake.m.TrackName,
		DeviceNames: fake.m.DeviceNames, ParamNames: fake.m.ParamNames, ParamValues: fake.m.ParamValues,
		GetParam: fake.m.GetParam, SetParam: fake.m.SetParam,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dNoCmd.Close()
	_, err = dNoCmd.AudioEngineRunning(ctx)
	if err == nil {
		t.Fatal("expected error when audio_engine is not configured in map")
	}
}
