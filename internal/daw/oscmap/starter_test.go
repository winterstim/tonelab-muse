package oscmap

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

// setFake answers a small Live-like session: audio flags per track, a number
// of scenes, and the clips filled as "track/slot". Every message is recorded.
type setFake struct {
	audio  []bool
	scenes int
	clips  map[string]bool
	mu     sync.Mutex
	msgs   []reaper.Msg
}

func (f *setFake) serve(pc net.PacketConn, to *net.UDPAddr) {
	buf := make([]byte, 65536)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ms, _ := reaper.Decode(buf[:n])
		for _, m := range ms {
			f.mu.Lock()
			f.msgs = append(f.msgs, m)
			f.mu.Unlock()
			var out []any
			switch m.Addr {
			case "/ping":
			case "/count":
				out = []any{int32(len(f.audio))}
			case "/name":
				out = []any{"Track"}
			case "/scenes":
				out = []any{int32(f.scenes)}
			case "/audio":
				t := int(m.Args[0].(int32))
				out = []any{int32(t), boolInt(f.audio[t])}
			case "/clip":
				t, s := int(m.Args[0].(int32)), int(m.Args[1].(int32))
				out = []any{int32(t), int32(s), boolInt(f.clips[slot(t, s)])}
			case "/load":
				out = []any{m.Args[0], int32(0)}
			default:
				continue
			}
			if out == nil && m.Addr != "/load" {
				continue
			}
			b, _ := reaper.Encode(reaper.Msg{Addr: m.Addr, Args: out})
			pc.WriteTo(b, to)
		}
	}
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func slot(t, s int) string { return fmt.Sprintf("%d/%d", t, s) }

// setDAW opens a Map against a setFake; base 0, like AbletonOSC.
func setDAW(t *testing.T, f *setFake) *DAW {
	t.Helper()
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
	go f.serve(pc, to)
	m := Map{
		Name: "SetFake", Port: pc.LocalAddr().(*net.UDPAddr).Port, ReplyPort: replyPort,
		Ping:        cmd("/ping"),
		TrackCount:  cmd("/count"),
		TrackName:   cmd("/name", "i:{track}"),
		DeviceNames: cmd("/devices", "i:{track}"),
		ParamNames:  cmd("/pnames", "i:{track}", "i:{fx}"),
		ParamValues: cmd("/pvalues", "i:{track}", "i:{fx}"),
		GetParam:    cmd("/get", "i:{track}", "i:{fx}", "i:{param}"),
		SetParam:    Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
		TrackAudio:  Command{Send: []string{"/audio", "i:{track}"}, Reply: "/audio", Skip: 1},
		SceneCount:  Command{Send: []string{"/scenes"}, Reply: "/scenes"},
		HasClip:     Command{Send: []string{"/clip", "i:{track}", "i:{slot}"}, Reply: "/clip", Skip: 2},
		InsertMedia: &Command{Send: []string{"/load", "i:{track}", "s:{name}"}, Reply: "/load", Skip: 1},
		RenameTrack: &Command{Send: []string{"/rename", "i:{track}", "s:{name}"}},
	}
	d, err := Open(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func (f *setFake) sent(addr string) []reaper.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []reaper.Msg
	for _, m := range f.msgs {
		if m.Addr == addr {
			out = append(out, m)
		}
	}
	return out
}

// twoTracks is a set with a MIDI track 1 and an audio track 2.
func twoTracks() *setFake {
	return &setFake{audio: []bool{false, true}, scenes: 3, clips: map[string]bool{}}
}

func TestTracksReportAudio(t *testing.T) {
	d := setDAW(t, twoTracks())
	tracks, err := d.Tracks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].Audio || !tracks[1].Audio {
		t.Fatalf("tracks %+v, want MIDI then audio", tracks)
	}
}

func TestStarterLoadsRiffOnFirstAudioTrackOfEmptySet(t *testing.T) {
	f := twoTracks()
	f.msgs = nil
	d := setDAW(t, f)
	idx, why, err := d.Starter(t.Context(), "/data/riff.wav")
	if err != nil || why != "" {
		t.Fatalf("err %v why %q", err, why)
	}
	if err != nil {
		t.Fatal(err)
	}
	if idx != 2 {
		t.Fatalf("riff went on track %d, want the audio track 2", idx)
	}
	loads := f.sent("/load")
	if len(loads) != 1 || loads[0].Args[0] != int32(1) || loads[0].Args[1] != "/data/riff.wav" {
		t.Fatalf("load messages %+v", loads)
	}
	var renames []reaper.Msg
	for i := 0; i < 100 && len(renames) == 0; i++ { // a rename gets no reply: wait for the fake to log it
		time.Sleep(10 * time.Millisecond)
		renames = f.sent("/rename")
	}
	if len(renames) != 1 || renames[0].Args[1] != StarterName {
		t.Fatalf("rename messages %+v", renames)
	}
}

func TestStarterLeavesASetWithAudioAlone(t *testing.T) {
	f := twoTracks()
	f.clips[slot(1, 2)] = true // a clip in the MIDI track's third slot
	d := setDAW(t, f)
	idx, why, err := d.Starter(t.Context(), "/data/riff.wav")
	if err != nil || idx != 0 || !strings.Contains(why, "already has a clip") {
		t.Fatalf("idx %d why %q err %v, want nothing loaded and the clip named", idx, why, err)
	}
	if n := len(f.sent("/load")); n != 0 {
		t.Fatalf("%d loads into a set that has audio", n)
	}
}

func TestStarterSkipsWithoutAudioTrack(t *testing.T) {
	f := &setFake{audio: []bool{false}, scenes: 2, clips: map[string]bool{}}
	d := setDAW(t, f)
	idx, why, _ := d.Starter(t.Context(), "/data/riff.wav")
	if idx != 0 || !strings.Contains(why, "no track reports audio") {
		t.Fatalf("loaded on track %d, or no reason given: %q", idx, why)
	}
	if n := len(f.sent("/load")); n != 0 {
		t.Fatal("loaded without an audio track")
	}
}

func TestMergeBundledAddsMissingCommandsOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ableton.json")
	saved := `{"name":"mine","port":11000,"reply_port":11001,"set_param":{"send":["/mine"]}}`
	if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	bundled := fstest.MapFS{"ableton.json": {Data: []byte(`{"name":"bundled","set_param":{"send":["/bundled"]},"set_bypass":{"send":["/live/device/set/is_active"]}}`)}}
	added, err := MergeBundled(path, bundled)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "set_bypass" {
		t.Fatalf("added %v, want only set_bypass", added)
	}
	var m Map
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "mine" || m.SetParam.Send[0] != "/mine" {
		t.Fatalf("saved commands were overwritten: %+v", m)
	}
	if len(m.SetBypass.Send) == 0 {
		t.Fatal("set_bypass was not merged in")
	}
	if again, _ := MergeBundled(path, bundled); len(again) != 0 {
		t.Fatalf("second merge added %v", again)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
