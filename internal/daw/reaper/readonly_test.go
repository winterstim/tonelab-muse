package reaper

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// fakeLua answers the bridge's command file the way REAPER would for chunk
// ops. A plugin that keeps its own state (readOnly) ignores set_chunk: the
// chunk read back is the old one.
type fakeLua struct {
	mu       sync.Mutex
	chunk    string
	readOnly bool
	sets     int
	undos    int
	reinst   []string // state file contents of each reinstantiate
	offline  []string // "op:state" for each offline reload
	lastSet  string   // the chunk last written with set_chunk
	reloads  int      // project reloads asked for
	// reloadKeeps makes a project reload load the last written chunk;
	// reloadFail is the reply the reload gets instead of a success
	reloadKeeps bool
	reloadFail  string
	programs    []string // "channel:program" of each program change
}

func (f *fakeLua) run(dir string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Millisecond):
		}
		raw, err := os.ReadFile(filepath.Join(dir, "cmd.txt"))
		if err != nil || len(raw) == 0 {
			continue
		}
		os.Remove(filepath.Join(dir, "cmd.txt"))
		f.handle(dir, strings.TrimSpace(string(raw)))
	}
}

func (f *fakeLua) handle(dir, line string) {
	fields := strings.Split(line, "\t")
	id, op := fields[0], fields[1]
	reply := "ok\tdone\t"
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "get_chunk":
		os.WriteFile(filepath.Join(dir, "chunk_out.txt"), []byte(f.chunk), 0o600)
	case "set_chunk":
		f.sets++
		in, _ := os.ReadFile(filepath.Join(dir, "chunk_in.txt"))
		f.lastSet = string(in)
		if !f.readOnly {
			f.chunk = string(in)
		}
	case "reinstantiate":
		state, _ := os.ReadFile(fields[3])
		f.reinst = append(f.reinst, string(state))
		reply = "ok\tAU: Test\t"
	case "undo":
		f.undos++
	case "reload_project_with_state":
		f.reloads++
		if f.reloadFail != "" {
			reply = "err\t" + f.reloadFail + "\t"
		} else if f.reloadKeeps {
			f.chunk = f.lastSet
		}
	case "program_change":
		f.programs = append(f.programs, fields[3])
	case "reload_offline", "reload_offline_b":
		state, _ := os.ReadFile(fields[3])
		f.offline = append(f.offline, op+":"+string(state))
		reply = "ok\tAU: Test\t"
	case "name":
		reply = "ok\tAmp\t"
	}
	os.WriteFile(filepath.Join(dir, "reply.tmp"), []byte(id+"\t"+reply+"\n"), 0o600)
	os.Rename(filepath.Join(dir, "reply.tmp"), filepath.Join(dir, "reply.txt"))
}

const readOnlyFixture = `<AU "AU: Test" "Test" "" 123
` + "PLACEHOLDER" + `
>`

func fixtureChunk() string {
	xmlPayload := `<Program Version="1" PresetName="ReadOnly">
  <AmpA Bypass="0" Model="JCM800">
    <Amp PreAmp_JCM800="5.00" Bass_JCM800="4.00" Treble_JCM800="6.00" />
  </AmpA>
  <CabA Bypass="0" CabModel="Brit" RoomType="Hall" />
</Program>`
	return strings.Replace(readOnlyFixture, "PLACEHOLDER", base64.StdEncoding.EncodeToString([]byte(xmlPayload)), 1)
}

func bassID(t *testing.T, chunk string) core.ParamID {
	params, err := ParseChunk(1, chunk)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range params {
		if p.Name == "AmpA Bass" {
			return p.ID
		}
	}
	t.Fatal("no AmpA Bass in fixture")
	return ""
}

func startFakeLua(t *testing.T, f *fakeLua) Bridge {
	t.Helper()
	dir := t.TempDir()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go f.run(dir, stop)
	return Bridge{Dir: dir}
}

func TestSetParamReadBackDetectsReadOnlyPlugin(t *testing.T) {
	chunk := fixtureChunk()
	f := &fakeLua{chunk: chunk, readOnly: true}
	r := &REAPER{Bridge: ptrBridge(startFakeLua(t, f))}
	id := bassID(t, chunk)

	_, err := r.SetParam(context.Background(), 1, id, 0.2)
	if !errors.Is(err, core.ErrReadOnly) {
		t.Fatalf("SetParam error = %v, want core.ErrReadOnly", err)
	}
	var ro core.ReadOnlyError
	if !errors.As(err, &ro) || ro.FX != 1 {
		t.Fatalf("error must name FX 1: %v", err)
	}
	if f.sets != 1 {
		t.Fatalf("sets = %d, want 1", f.sets)
	}
}

func TestSetParamKeptWhenPluginTakesTheWrite(t *testing.T) {
	chunk := fixtureChunk()
	f := &fakeLua{chunk: chunk}
	r := &REAPER{Bridge: ptrBridge(startFakeLua(t, f))}
	id := bassID(t, chunk)

	got, err := r.SetParam(context.Background(), 1, id, 0.2)
	if err != nil {
		t.Fatalf("SetParam: %v", err)
	}
	if got.Status != core.StatusConfirmed {
		t.Fatalf("status = %v, want confirmed", got.Status)
	}
}

func TestCheckKeptFlagsOnlyChangedParams(t *testing.T) {
	chunk := fixtureChunk()
	_, p, _ := bassID(t, chunk).Parse()
	written, _, _, err := PatchChunk(chunk, p-1000, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKept(1, chunk, written, written); err != nil {
		t.Fatalf("kept write must pass: %v", err)
	}
	err = checkKept(1, chunk, written, chunk)
	if !errors.Is(err, core.ErrReadOnly) {
		t.Fatalf("reverted write must be read-only, got %v", err)
	}
	if err := checkKept(1, chunk, chunk, chunk); err != nil {
		t.Fatalf("a write that changes nothing has nothing to keep: %v", err)
	}
}

func ptrBridge(b Bridge) *Bridge { return &b }
