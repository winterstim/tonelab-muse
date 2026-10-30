package reaper

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestBridgeInstallCoexistence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	b := Bridge{Dir: filepath.Join(dir, "bridge")}

	// Case 1: Fresh install
	restart, err := b.Install()
	if err != nil {
		t.Fatalf("fresh install failed: %v", err)
	}
	if !restart {
		t.Errorf("fresh install should require restart")
	}
	if !b.installed() {
		t.Errorf("expected installed() to be true")
	}

	// Verify both files exist
	bridgeScript, err := os.ReadFile(bridgeScriptPath())
	if err != nil || len(bridgeScript) == 0 {
		t.Fatalf("tonelab_agent_bridge.lua not written: %v", err)
	}
	startupScript, err := os.ReadFile(startupPath())
	if err != nil || len(startupScript) == 0 {
		t.Fatalf("__startup.lua not written: %v", err)
	}

	// Case 2: Subsequent install should not require restart
	restart, err = b.Install()
	if err != nil || restart {
		t.Errorf("subsequent install: restart=%v, err=%v", restart, err)
	}

	// Case 3: Coexisting with user's __startup.lua
	userScriptContent := `reaper.ShowConsoleMsg("Hello from user's custom script!\n")`
	if err := os.WriteFile(startupPath(), []byte(userScriptContent), 0o644); err != nil {
		t.Fatalf("writing user startup script: %v", err)
	}

	restart, err = b.Install()
	if err != nil {
		t.Fatalf("install with user script failed: %v", err)
	}
	if !restart {
		t.Errorf("expected restart=true when appending to user script")
	}
	if !b.installed() {
		t.Errorf("expected installed()=true after appending to user script")
	}

	updatedStartup, err := os.ReadFile(startupPath())
	if err != nil {
		t.Fatalf("reading updated user startup script: %v", err)
	}
	str := string(updatedStartup)
	if !strings.Contains(str, "Hello from user's custom script!") {
		t.Errorf("user's original script was lost! Got:\n%s", str)
	}
	if !strings.Contains(str, "tonelab_agent_bridge.lua") {
		t.Errorf("dofile for agent bridge not appended! Got:\n%s", str)
	}

	// Verify another install does not duplicate the dofile line
	restart, err = b.Install()
	if err != nil || restart {
		t.Errorf("expected restart=false on repeat install, got restart=%v, err=%v", restart, err)
	}
	contentAgain, _ := os.ReadFile(startupPath())
	if strings.Count(string(contentAgain), "tonelab_agent_bridge.lua") != 1 {
		t.Errorf("dofile was duplicated: count=%d", strings.Count(string(contentAgain), "tonelab_agent_bridge.lua"))
	}
}

func TestBridgeEnableAuto(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	b := Bridge{Dir: filepath.Join(dir, "bridge")}
	if err := b.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto failed: %v", err)
	}
	if !b.Enabled() {
		t.Errorf("expected Enabled()=true after EnableAuto()")
	}
}

// fakeBridgeServer mocks REAPER's Lua bridge loop in a background goroutine for testing Go bridge calls.
func fakeBridgeServer(t *testing.T, dir string) func() {
	stop := make(chan struct{})
	var currentChunk []byte
	currentUndo := ""
	if init, err := os.ReadFile(filepath.Join(dir, "chunk_out.txt")); err == nil {
		currentChunk = init
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			cmdPath := filepath.Join(dir, "cmd.txt")
			data, err := os.ReadFile(cmdPath)
			if err != nil {
				continue
			}
			os.Remove(cmdPath)
			line := strings.TrimRight(string(data), "\r\n")
			parts := strings.Split(line, "\t")
			if len(parts) < 5 {
				continue
			}
			id, op, trackStr, arg, posStr := parts[0], parts[1], parts[2], parts[3], parts[4]
			track, _ := strconv.Atoi(trackStr)
			pos, _ := strconv.Atoi(posStr)

			var a, bVal string
			ok := true

			fOps, _ := os.OpenFile(filepath.Join(dir, "ops_log.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if fOps != nil {
				_, _ = fOps.WriteString(op + "\n")
				_ = fOps.Close()
			}

			switch op {
			case "begin_undo_action":
				a, bVal = "started", arg
			case "end_undo_action":
				if strings.HasPrefix(arg, "0:") {
					a = "cancelled"
				} else {
					a = "committed"
					_, currentUndo, _ = strings.Cut(arg, ":")
				}
			case "undo_action_current":
				a = currentUndo
			case "transport_echo":
				a = arg
			case "record_input_start":
				a = strings.Split(arg, "|")[0]
			case "record_input_finish":
				a = "new-input.wav"
				bVal = "12:0.1"
			case "reload":
				a = "reloaded"
			case "remove", "force_remove":
				a = arg
				bVal = "ReaEQ"
			case "move":
				a = strconv.Itoa(pos)
			case "presets":
				a = "Default"
				bVal = "Default|Lead|Crunch|Clean"
			case "set_preset":
				parts := strings.SplitN(arg, ":", 2)
				if len(parts) > 1 {
					a = parts[1]
				} else {
					a = arg
				}
			case "find_value":
				a = "0.7500"
			case "get_chunk":
				chunk := currentChunk
				if len(chunk) == 0 {
					chunk = []byte("<FXCHUNK ...>")
				}
				_ = os.WriteFile(filepath.Join(dir, "chunk_out.txt"), chunk, 0o600)
				a = "chunk written"
			case "set_chunk":
				in, err := os.ReadFile(filepath.Join(dir, "chunk_in.txt"))
				if err == nil && len(in) > 0 {
					currentChunk = in
					_ = os.WriteFile(filepath.Join(dir, "chunk_out.txt"), in, 0o600)
					a = "ok"
				} else {
					ok = false
					a = "empty chunk"
				}
			case "create_track":
				if pos > 0 {
					a = strconv.Itoa(pos)
				} else {
					a = "2"
				}
			case "duplicate_track":
				a = strconv.Itoa(track + 1)
			case "delete_track", "rename_track", "insert_media", "create_send", "set_transport", "remove_send", "track_control", "set_timesig", "save_project", "goto_marker":
				a = "ok"
			case "list_markers":
				a = "0"
			case "create_marker":
				a = "1"
			case "track_state":
				a = "1:0:0:0:-1:0.500000:0.000000:2"
			case "transport_state":
				a = "120.000000:1:4.000000:0.000000:8.000000:4:4"
			case "send_list":
				a = "2:0.501187|3:1.000000"
			case "fx_wet":
				a = "0.250000"
				if arg != "get" {
					a = arg
				}
			case "param_readout":
				a = "-6.0 dB"
			default:
				a = "mock ok"
			}
			_ = track

			tmp := filepath.Join(dir, "reply.tmp")
			replyLine := fmt.Sprintf("%s\t%s\t%s\t%s\n", id, map[bool]string{true: "ok", false: "err"}[ok], a, bVal)
			_ = os.WriteFile(tmp, []byte(replyLine), 0o600)
			_ = os.Rename(tmp, filepath.Join(dir, "reply.txt"))
		}
	}()
	return func() { close(stop) }
}

func TestBridgeUndoActionGrouping(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}
	if err := b.EnableAuto(); err != nil {
		t.Fatal(err)
	}
	stop := fakeBridgeServer(t, bridgeDir)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !b.SupportsUndoActionGrouping() {
		t.Fatal("bridge must support grouping")
	}
	if err := b.BeginUndoAction(ctx, "Agent: apply plan p1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.call(ctx, "set_transport", 1, "tempo:120:0:0:0", 0); err != nil {
		t.Fatal(err)
	}
	if err := b.EndUndoAction(ctx, "Agent: apply plan p1", true); err != nil {
		t.Fatal(err)
	}
	if current, err := b.UndoActionIsCurrent(ctx, "Agent: apply plan p1"); err != nil || !current {
		t.Fatalf("current undo action: %v %v", current, err)
	}
	if err := b.BeginUndoAction(ctx, "Agent: partial plan"); err != nil {
		t.Fatal(err)
	}
	if err := b.EndUndoAction(ctx, "Agent: partial plan", false); err != nil {
		t.Fatal(err)
	}
	if current, err := b.UndoActionIsCurrent(ctx, "Agent: partial plan"); err != nil || current {
		t.Fatalf("cancelled action still current: %v %v", current, err)
	}
}

func TestBridgeSessionOps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}
	if err := b.EnableAuto(); err != nil {
		t.Fatal(err)
	}
	stop := fakeBridgeServer(t, bridgeDir)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got, err := b.SetTrackControl(ctx, 1, "arm", "1"); err != nil || got != "ok" {
		t.Fatalf("track control: %q %v", got, err)
	}
	if err := b.RemoveSend(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.SetTimeSignature(ctx, 7, 8); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveProject(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateMarker(ctx, 2, 4, "region", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Markers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.GoToMarker(ctx, 1); err != nil {
		t.Fatal(err)
	}
	ops, err := os.ReadFile(filepath.Join(bridgeDir, "ops_log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"track_control", "remove_send", "set_timesig", "save_project", "create_marker", "list_markers", "goto_marker"} {
		if !strings.Contains(string(ops), op+"\n") {
			t.Errorf("bridge op %q missing from log: %s", op, ops)
		}
	}
}

func TestBridgeOperations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}
	if err := b.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto failed: %v", err)
	}

	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. ForceRemoveFX
	name, err := b.ForceRemoveFX(ctx, 1, 1)
	if err != nil {
		t.Fatalf("ForceRemoveFX failed: %v", err)
	}
	if name != "ReaEQ" {
		t.Errorf("expected removed plugin ReaEQ, got %q", name)
	}

	// 2. MoveFX
	if err := b.MoveFX(ctx, 1, 1, 2); err != nil {
		t.Fatalf("MoveFX failed: %v", err)
	}

	// 3. Presets
	cur, list, err := b.Presets(ctx, 1, 1)
	if err != nil {
		t.Fatalf("Presets failed: %v", err)
	}
	if cur != "Default" {
		t.Errorf("expected cur preset Default, got %q", cur)
	}
	if len(list) != 4 || list[1] != "Lead" {
		t.Errorf("unexpected presets list: %v", list)
	}

	// 4. SetPreset
	if err := b.SetPreset(ctx, 1, 1, "Lead"); err != nil {
		t.Fatalf("SetPreset failed: %v", err)
	}

	// 5. FindValue
	val, err := b.FindValue(ctx, 1, core.MakeParamID(1, 1), "0 dB")
	if err != nil {
		t.Fatalf("FindValue failed: %v", err)
	}
	if val < 0.74 || val > 0.76 {
		t.Errorf("expected FindValue ~0.75, got %f", val)
	}

	// 6. GetChunk & SetChunk
	chunk, err := b.GetChunk(ctx, 1, 1)
	if err != nil {
		t.Fatalf("GetChunk failed: %v", err)
	}
	if chunk != "<FXCHUNK ...>" {
		t.Errorf("unexpected chunk: %q", chunk)
	}

	if err := b.SetChunk(ctx, 1, 1, "<UPDATED CHUNK>"); err != nil {
		t.Fatalf("SetChunk failed: %v", err)
	}
}

func TestSessionController(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}
	if err := b.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto failed: %v", err)
	}

	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Verify interface assertions
	var _ core.SessionController = b
	var _ core.SessionController = (*REAPER)(nil)

	// 1. CreateTrack
	idx, err := b.CreateTrack(ctx, "Vocals", 3)
	if err != nil {
		t.Fatalf("CreateTrack failed: %v", err)
	}
	if idx != 3 {
		t.Errorf("expected track index 3, got %d", idx)
	}

	// 2. DuplicateTrack
	dupIdx, err := b.DuplicateTrack(ctx, 3, "Vocals Double")
	if err != nil {
		t.Fatalf("DuplicateTrack failed: %v", err)
	}
	if dupIdx != 4 {
		t.Errorf("expected duplicate track index 4, got %d", dupIdx)
	}

	// 3. RenameTrack
	if err := b.RenameTrack(ctx, 3, "Lead Vocals"); err != nil {
		t.Fatalf("RenameTrack failed: %v", err)
	}

	// 4. InsertMedia
	if err := b.InsertMedia(ctx, 3, "/path/to/lead.wav", 5.5); err != nil {
		t.Fatalf("InsertMedia failed: %v", err)
	}

	// 5. CreateSend
	if err := b.CreateSend(ctx, 3, 1, -4.5); err != nil {
		t.Fatalf("CreateSend failed: %v", err)
	}

	// 6. SetTransport
	if err := b.SetTransport(ctx, "play", 120.0, 0.0, 4.0, 16.0); err != nil {
		t.Fatalf("SetTransport failed: %v", err)
	}

	// 7. DeleteTrack
	if err := b.DeleteTrack(ctx, 4); err != nil {
		t.Fatalf("DeleteTrack failed: %v", err)
	}

	// 8. Forwarding through REAPER struct
	r := &REAPER{Bridge: &b}
	rIdx, err := r.CreateTrack(ctx, "Bass", 0)
	if err != nil {
		t.Fatalf("r.CreateTrack failed: %v", err)
	}
	if rIdx != 2 {
		t.Errorf("expected track index 2, got %d", rIdx)
	}
	if _, err := r.DuplicateTrack(ctx, 2, "Bass Parallel"); err != nil {
		t.Fatalf("r.DuplicateTrack failed: %v", err)
	}
	if err := r.RenameTrack(ctx, rIdx, "Bass DI"); err != nil {
		t.Fatalf("r.RenameTrack failed: %v", err)
	}
	if err := r.InsertMedia(ctx, rIdx, "/path/to/bass.wav", 0.0); err != nil {
		t.Fatalf("r.InsertMedia failed: %v", err)
	}
	if err := r.CreateSend(ctx, rIdx, 1, 0.0); err != nil {
		t.Fatalf("r.CreateSend failed: %v", err)
	}
	if err := r.SetTransport(ctx, "stop", 0, 0, 0, 0); err != nil {
		t.Fatalf("r.SetTransport failed: %v", err)
	}
	if err := r.DeleteTrack(ctx, rIdx); err != nil {
		t.Fatalf("r.DeleteTrack failed: %v", err)
	}
}

func TestModelSwapper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	// Create fake preset directory
	presetsDir := filepath.Join(dir, "CustomPresets")
	if err := os.MkdirAll(presetsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	rockPreset := `<?xml version="1.0" encoding="UTF-8"?>
<Program Version="1" PresetName="Rock">
  <AmpA Bypass="0" Model="uuid-jcm800">
    <Amp PreAmp_JCM800="6.5" Bass_JCM800="5.0" />
  </AmpA>
  <CabA Bypass="0" CabModel="cab-412">
    <Cab RoomType="Hall" />
  </CabA>
  <StompA1 Bypass="0" Stomp0="stomp-uuid-drive">
    <Slot0 Bypass="0" Drive="7.5" Tone="5.0" />
  </StompA1>
</Program>`

	jazzPreset := `<?xml version="1.0" encoding="UTF-8"?>
<Program Version="1" PresetName="Jazz">
  <AmpA Bypass="0" Model="uuid-tweed">
    <Amp Gain_Tweed="4.0" Treble_Tweed="6.0" />
  </AmpA>
  <CabA Bypass="0" CabModel="cab-112">
    <Cab RoomType="Studio" />
  </CabA>
  <StompA1 Bypass="0" Stomp0="stomp-uuid-comp">
    <Slot0 Bypass="0" Comp="5.0" />
  </StompA1>
</Program>`

	if err := os.WriteFile(filepath.Join(presetsDir, "Rock.at5p"), []byte(rockPreset), 0o644); err != nil {
		t.Fatalf("writing Rock.at5p: %v", err)
	}
	if err := os.WriteFile(filepath.Join(presetsDir, "Jazz.at5p"), []byte(jazzPreset), 0o644); err != nil {
		t.Fatalf("writing Jazz.at5p: %v", err)
	}

	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir, PresetDirs: []string{presetsDir}}
	if err := b.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto failed: %v", err)
	}

	// Seed chunk_out.txt with Rock preset
	initialAUChunk := `<AU "AU: AmpliTube 5" "AmpliTube 5" "" 1234
` + base64.StdEncoding.EncodeToString([]byte(rockPreset)) + `
>`
	if err := os.MkdirAll(bridgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll bridgeDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(initialAUChunk), 0o600); err != nil {
		t.Fatalf("writing initial chunk_out.txt: %v", err)
	}

	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Verify interface assertions
	var _ core.ModelSwapper = b
	var _ core.ModelSwapper = (*REAPER)(nil)

	// 1. Discover all slots
	slots, err := b.PluginModels(ctx, 1, 1, "")
	if err != nil {
		t.Fatalf("PluginModels failed: %v", err)
	}
	if len(slots) < 3 {
		t.Fatalf("expected at least 3 slots (AmpA, CabA, StompA1.Slot0), got %d: %+v", len(slots), slots)
	}

	slotMap := map[string]core.PluginSlot{}
	for _, s := range slots {
		slotMap[s.Slot] = s
	}

	ampA, ok := slotMap["AmpA"]
	if !ok {
		t.Fatalf("AmpA slot not discovered")
	}
	if ampA.Current != "JCM800" {
		t.Errorf("expected AmpA current model JCM800, got %q", ampA.Current)
	}
	hasTweed := false
	for _, m := range ampA.Available {
		if m == "Tweed" {
			hasTweed = true
		}
	}
	if !hasTweed {
		t.Errorf("expected Tweed in AmpA available models: %v", ampA.Available)
	}

	// 2. Query filter
	cabSlots, err := b.PluginModels(ctx, 1, 1, "Cab")
	if err != nil {
		t.Fatalf("PluginModels query filter failed: %v", err)
	}
	if len(cabSlots) != 1 || cabSlots[0].Slot != "CabA" {
		t.Errorf("expected only CabA slot for 'Cab' query, got %+v", cabSlots)
	}

	// 3. Swap AmpA model to Tweed
	updatedParams, err := b.SetPluginModel(ctx, 1, 1, "AmpA", "Tweed")
	if err != nil {
		t.Fatalf("SetPluginModel failed: %v", err)
	}
	paramNames := getParamNames(updatedParams)
	hasGain := false
	for _, n := range paramNames {
		if strings.Contains(n, "Gain") {
			hasGain = true
		}
		if strings.Contains(n, "PreAmp") {
			t.Errorf("old model parameter PreAmp still present after swapping to Tweed: %s", n)
		}
	}
	if !hasGain {
		t.Errorf("expected AmpA Gain parameter after swapping to Tweed; got: %v", paramNames)
	}

	// 4. Test disk preset fallback via Presets and SetPreset
	curPreset, presetsList, err := b.Presets(ctx, 1, 1)
	if err != nil {
		t.Fatalf("b.Presets failed: %v", err)
	}
	t.Logf("Presets returned cur=%q, list=%v", curPreset, presetsList)
	hasJazzPreset := false
	for _, p := range presetsList {
		if p == "Jazz" {
			hasJazzPreset = true
		}
	}
	if !hasJazzPreset {
		t.Errorf("expected disk preset 'Jazz' in presets list: %v", presetsList)
	}

	if err := b.SetPreset(ctx, 1, 1, "Jazz"); err != nil {
		t.Fatalf("b.SetPreset('Jazz') failed: %v", err)
	}

	// 5. Test forwarding via *REAPER
	r := &REAPER{Bridge: &b, PresetDirs: []string{presetsDir}}
	rSlots, err := r.PluginModels(ctx, 1, 1, "Stomp")
	if err != nil {
		t.Fatalf("r.PluginModels failed: %v", err)
	}
	if len(rSlots) == 0 {
		t.Fatalf("r.PluginModels returned 0 stomp slots")
	}

	rParams, err := r.SetPluginModel(ctx, 1, 1, "StompA1.Slot0", "Empty")
	if err != nil {
		t.Fatalf("r.SetPluginModel failed: %v", err)
	}
	rNames := getParamNames(rParams)
	for _, n := range rNames {
		if strings.Contains(n, "Slot0") {
			t.Errorf("expected Slot0 parameters to disappear after setting Empty, found %s", n)
		}
	}
}

func TestSetParamsBatching(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	xmlPayload := `<Program Version="1" PresetName="BatchParamsTest">
  <AmpA Bypass="0" Model="JCM800">
    <Amp PreAmp_JCM800="5.00" Bass_JCM800="4.00" Middle_JCM800="5.00" Treble_JCM800="6.00" />
  </AmpA>
</Program>`

	rawChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(xmlPayload)) + `
>`

	bridgeDir := filepath.Join(dir, "bridge")
	if err := os.MkdirAll(bridgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll bridgeDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(rawChunk), 0o600); err != nil {
		t.Fatalf("writing initial chunk_out.txt: %v", err)
	}

	b := Bridge{Dir: bridgeDir}
	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	r := &REAPER{Bridge: &b}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 4 chunk parameter changes on track 1, fx 1
	changes := []core.Change{
		{Param: core.MakeParamID(1, 1001), Value: 0.85, Why: "more gain"},
		{Param: core.MakeParamID(1, 1002), Value: 0.30, Why: "cut bass"},
		{Param: core.MakeParamID(1, 1003), Target: "7.0", Why: "boost mids"},
		{Param: core.MakeParamID(1, 1004), Value: 0.60, Why: "treble"},
	}

	applied, err := r.SetParams(ctx, 1, changes)
	if err != nil {
		t.Fatalf("SetParams failed: %v", err)
	}

	if len(applied) != 4 {
		t.Fatalf("expected 4 applied results, got %d", len(applied))
	}

	for i, a := range applied {
		if a.Status != core.StatusConfirmed {
			t.Errorf("change %d not confirmed: %+v", i, a)
		}
	}

	// Verify one batch: 1 set_chunk, with the get_chunk read and read-back.
	opsData, err := os.ReadFile(filepath.Join(bridgeDir, "ops_log.txt"))
	if err != nil {
		t.Fatalf("reading ops_log.txt: %v", err)
	}
	ops := strings.Split(strings.TrimSpace(string(opsData)), "\n")
	getCount, setCount := 0, 0
	for _, op := range ops {
		switch op {
		case "get_chunk":
			getCount++
		case "set_chunk":
			setCount++
		}
	}

	// one read, then one read-back that proves the plugin kept the write
	if getCount != 2 {
		t.Errorf("expected 1 get_chunk to read and 1 to read back for 4 batched params, got %d", getCount)
	}
	if setCount != 1 {
		t.Errorf("expected exactly 1 set_chunk call for 4 batched params, got %d", setCount)
	}
}

func TestPluginModelsCompaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	presetsDir := filepath.Join(dir, "BigPresets")
	if err := os.MkdirAll(presetsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	// Create many fake amp models in preset to verify >40 capping
	var ampTags []string
	for i := 0; i < 50; i++ {
		modelName := fmt.Sprintf("AmpModel%02d", i)
		ampTags = append(ampTags, fmt.Sprintf(`<AmpA Bypass="0" Model="uuid-%s"><Amp PreAmp_%s="5.0" /></AmpA>`, modelName, modelName))
	}
	presetContent := `<Program Version="1" PresetName="ManyAmps">` + strings.Join(ampTags, "\n") + `</Program>`
	if err := os.WriteFile(filepath.Join(presetsDir, "ManyAmps.at5p"), []byte(presetContent), 0o644); err != nil {
		t.Fatalf("writing ManyAmps.at5p: %v", err)
	}

	// Session chunk with 6 stomp banks
	sessionXML := `<Program Version="1" PresetName="Session">
  <AmpA Bypass="0" Model="uuid-AmpModel00"><Amp PreAmp_AmpModel00="5.0" /></AmpA>
  <AmpB Bypass="1" Model="" />
  <AmpC Bypass="1" Model="" />
  <CabA Bypass="0" CabModel="412" RoomType="Hall" />
  <CabB Bypass="1" CabModel="" />
  <StompA1 Bypass="0" Stomp0="uuid-stomp0">
    <Slot0 Bypass="0" Threshold="0.01" Release="500" />
    <Slot1 />
    <Slot2 />
    <Slot3 />
    <Slot4 />
    <Slot5 />
  </StompA1>
  <StompB1 Bypass="1" />
</Program>`

	rawChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(sessionXML)) + `
>`

	bridgeDir := filepath.Join(dir, "bridge")
	if err := os.MkdirAll(bridgeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll bridgeDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(rawChunk), 0o600); err != nil {
		t.Fatalf("writing chunk_out.txt: %v", err)
	}

	b := Bridge{Dir: bridgeDir, PresetDirs: []string{presetsDir}}
	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. With slotQuery == "" (unfiltered), verify compaction:
	// Empty slots (AmpB, AmpC, CabB, Slot2..5) must not be returned!
	slots, err := b.PluginModels(ctx, 1, 1, "")
	if err != nil {
		t.Fatalf("PluginModels failed: %v", err)
	}

	for _, s := range slots {
		if strings.Contains(s.Slot, "AmpB") || strings.Contains(s.Slot, "AmpC") || strings.Contains(s.Slot, "CabB") {
			t.Errorf("empty slot returned in unfiltered query: %s", s.Slot)
		}
		if strings.Contains(s.Slot, "Slot2") || strings.Contains(s.Slot, "Slot3") || strings.Contains(s.Slot, "Slot4") || strings.Contains(s.Slot, "Slot5") {
			t.Errorf("empty stomp slot returned in unfiltered query: %s", s.Slot)
		}
		// Verify Available list is capped to <= 40
		if len(s.Available) > 40 {
			t.Errorf("slot %s available models not capped (len=%d > 40)", s.Slot, len(s.Available))
		}
	}

	// 2. With slotQuery != "" (explicit query), verify full matching:
	slot2Matches, err := b.PluginModels(ctx, 1, 1, "Slot2")
	if err != nil {
		t.Fatalf("PluginModels query 'Slot2' failed: %v", err)
	}
	if len(slot2Matches) == 0 {
		t.Errorf("expected matching slots for 'Slot2' query")
	}
}

func TestBridgeAutoInstallNoConsentGate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)

	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}

	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Notice: b.Enable() is NOT called!
	// Operations should succeed seamlessly without ErrNeedsConsent gate
	idx, err := b.CreateTrack(ctx, "Auto Track", 1)
	if err != nil {
		t.Fatalf("CreateTrack should not be gated: %v", err)
	}
	if idx != 1 {
		t.Errorf("expected track index 1, got %d", idx)
	}

	// Verify reload op
	reply, _, err := b.call(ctx, "reload", 0, "", 0)
	if err != nil {
		t.Fatalf("reload call failed: %v", err)
	}
	if reply != "reloaded" {
		t.Errorf("expected 'reloaded', got %q", reply)
	}
}

func TestCleanMachinePortability(t *testing.T) {
	cleanDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(cleanDir, "empty_home"))
	t.Setenv("USERPROFILE", filepath.Join(cleanDir, "empty_home"))
	t.Setenv("APPDATA", filepath.Join(cleanDir, "empty_appdata"))

	bridgeDir := filepath.Join(cleanDir, "bridge")
	b := Bridge{Dir: bridgeDir}

	xmlPayload := `<Program Version="1" PresetName="CleanMachineTest">
  <AmpA Bypass="0" Model="uuid-amp"><Amp PreAmp="5.0" /></AmpA>
  <CabA Bypass="0" CabModel="uuid-cab" RoomType="Hall" />
  <StompA1 Bypass="0" Stomp0="uuid-stomp"><Slot0 Bypass="0" Level="5.0" /></StompA1>
</Program>`

	rawChunk := `<AU "AU: Test" "Test" "" 123
` + base64.StdEncoding.EncodeToString([]byte(xmlPayload)) + `
>`
	_ = os.MkdirAll(bridgeDir, 0o700)
	_ = os.WriteFile(filepath.Join(bridgeDir, "chunk_out.txt"), []byte(rawChunk), 0o600)

	cleanup := fakeBridgeServer(t, bridgeDir)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. PluginModels on a machine with 0 preset files
	slots, err := b.PluginModels(ctx, 1, 1, "")
	if err != nil {
		t.Fatalf("PluginModels on clean machine failed: %v", err)
	}
	if len(slots) == 0 {
		t.Fatalf("expected primary slots returned on clean machine")
	}

	for _, s := range slots {
		if strings.Contains(s.Slot, "Stomp") {
			hasEmpty := false
			for _, m := range s.Available {
				if m == "Empty" {
					hasEmpty = true
					break
				}
			}
			if !hasEmpty {
				t.Errorf("expected 'Empty' stomp model always available even on clean machine: %v", s.Available)
			}
		}
	}

	// 2. Presets on clean machine
	cur, list, err := b.Presets(ctx, 1, 1)
	if err != nil {
		t.Fatalf("Presets on clean machine failed: %v", err)
	}
	if cur != "Default" {
		t.Logf("Presets returned cur=%q, list=%v", cur, list)
	}
}

func TestBridgeInputRecordingReadback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	b := Bridge{Dir: filepath.Join(dir, "bridge")}
	if _, err := b.Install(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	stop := fakeBridgeServer(t, b.Dir)
	defer stop()
	if err := b.StartInput(context.Background(), 1, .1, "take-test"); err != nil {
		t.Fatal(err)
	}
	clip, err := b.FinishInput(context.Background(), 1, "take-test")
	if err != nil || clip.Path != "new-input.wav" || clip.PositionSec != 12 || clip.DurationSec != .1 {
		t.Fatalf("%+v %v", clip, err)
	}
	if err := b.StartInput(context.Background(), 1, 200, "bad"); err == nil {
		t.Fatal("unbounded recording accepted")
	}
}

func TestBridgeCommandTransportSerializesConcurrentCalls(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	b := Bridge{Dir: filepath.Join(dir, "bridge")}
	if _, err := b.Install(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	stop := fakeBridgeServer(t, b.Dir)
	defer stop()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := fmt.Sprintf("request-%d", i)
			got, _, err := b.call(context.Background(), "transport_echo", 1, token, 0)
			if err != nil || got != token {
				t.Errorf("request %s got %s, %v", token, got, err)
			}
		}(i)
	}
	wg.Wait()
	held := make(chan struct{}, 1)
	held <- struct{}{}
	key, _ := filepath.Abs(b.dir())
	old, _ := bridgeCallLocks.Load(key)
	bridgeCallLocks.Store(key, held)
	defer bridgeCallLocks.Store(key, old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := b.call(ctx, "transport_echo", 1, "canceled", 0); err != context.Canceled {
		t.Fatalf("waiting caller did not cancel: %v", err)
	}
}

// startFakeBridgeOps runs the fake REAPER bridge for one test.
func startFakeBridgeOps(t *testing.T) (Bridge, context.Context) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	bridgeDir := filepath.Join(dir, "bridge")
	b := Bridge{Dir: bridgeDir}
	if err := b.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto failed: %v", err)
	}
	cleanup := fakeBridgeServer(t, bridgeDir)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return b, ctx
}

func TestBridgeTrackState(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	got, err := b.TrackState(ctx, 1)
	if err != nil {
		t.Fatalf("TrackState failed: %v", err)
	}
	want := TrackState{Arm: true, Mute: false, Solo: false, Monitor: 0, Input: -1, Volume: 0.5, Pan: 0, Items: 2}
	if got.Arm != want.Arm || got.Mute != want.Mute || got.Solo != want.Solo ||
		got.Monitor != want.Monitor || got.Input != want.Input ||
		got.Volume != want.Volume || got.Pan != want.Pan || got.Items != want.Items {
		t.Errorf("TrackState = %+v, want %+v", got, want)
	}
}

func TestBridgeTransportState(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	got, err := b.TransportState(ctx)
	if err != nil {
		t.Fatalf("TransportState failed: %v", err)
	}
	if got.Tempo != 120 || !got.Playing || got.PosSec != 4 || got.LoopStartSec != 0 ||
		got.LoopEndSec != 8 || got.TimeSigNum != 4 || got.TimeSigDen != 4 {
		t.Errorf("TransportState = %+v", got)
	}
}

func TestBridgeSends(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	got, err := b.Sends(ctx, 1)
	if err != nil {
		t.Fatalf("Sends failed: %v", err)
	}
	if len(got) != 2 || got[0].Dest != 2 || got[0].Volume != 0.501187 || got[1].Dest != 3 || got[1].Volume != 1 {
		t.Errorf("Sends = %+v", got)
	}
}

func TestBridgeWet(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	got, err := b.Wet(ctx, 1, 1, nil)
	if err != nil {
		t.Fatalf("Wet get failed: %v", err)
	}
	if got != 0.25 {
		t.Errorf("Wet get = %v, want 0.25", got)
	}
	set := 0.4
	got, err = b.Wet(ctx, 1, 1, &set)
	if err != nil {
		t.Fatalf("Wet set failed: %v", err)
	}
	if got != 0.4 {
		t.Errorf("Wet set echoed %v, want 0.4", got)
	}
	bad := 1.5
	if _, err := b.Wet(ctx, 1, 1, &bad); err == nil {
		t.Errorf("Wet accepted 1.5; want an error before REAPER is asked")
	}
}

func TestBridgeParamReadout(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	got, err := b.ParamReadout(ctx, 1, 1, 3)
	if err != nil {
		t.Fatalf("ParamReadout failed: %v", err)
	}
	if got != "-6.0 dB" {
		t.Errorf("ParamReadout = %q, want %q", got, "-6.0 dB")
	}
}

func TestBridgeMoveFXAcceptsReadback(t *testing.T) {
	b, ctx := startFakeBridgeOps(t)
	if err := b.MoveFX(ctx, 1, 2, 1); err != nil {
		t.Fatalf("MoveFX failed: %v", err)
	}
}
