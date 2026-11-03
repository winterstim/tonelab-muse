package oscmap

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/oscmap/bundled"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
)

// TestBundledMapsValidation validates every built-in JSON map.
// Every map must pass structural validation (Check), have all required
// commands, cleanly handle optional commands, and all placeholders must
// substitute properly with their type tags.
func TestBundledMapsValidation(t *testing.T) {
	entries, err := bundled.FS.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read bundled maps: %v", err)
	}

	found := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		found++
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := bundled.FS.ReadFile(entry.Name())
			if err != nil {
				t.Fatalf("read %s: %v", entry.Name(), err)
			}
			var m Map
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("unmarshal %s: %v", entry.Name(), err)
			}
			if err := m.Check(); err != nil {
				t.Fatalf("%s failed Check(): %v", entry.Name(), err)
			}

			// Required commands must be present with non-empty send address
			required := []string{
				"ping", "track_count", "track_name", "device_names",
				"param_names", "param_values", "get_param", "set_param",
			}
			cmds := m.commands()
			for _, req := range required {
				c, ok := cmds[req]
				if !ok || len(c.Send) == 0 {
					t.Errorf("%s missing required command %q", entry.Name(), req)
				}
			}

			// Validate all defined commands can build args with sample values
			sample := vars{
				track:  1,
				fx:     2,
				param:  3,
				to:     4,
				slot:   5,
				value:  0.75,
				name:   "TestDevice",
				preset: "TestPreset",
			}

			for role, cmd := range cmds {
				if len(cmd.Send) == 0 {
					continue
				}
				addr, args, err := cmd.build(sample)
				if err != nil {
					t.Errorf("%s command %s build error: %v", entry.Name(), role, err)
					continue
				}
				if !strings.HasPrefix(addr, "/") {
					t.Errorf("%s command %s address %q must start with /", entry.Name(), role, addr)
				}

				// Check no unresolved placeholders remain
				for i, arg := range args {
					if s, ok := arg.(string); ok {
						if strings.Contains(s, "{") && strings.Contains(s, "}") {
							t.Errorf("%s command %s arg %d has unreplaced placeholder: %q", entry.Name(), role, i, s)
						}
					}
				}

				// Verify type tags match argument types
				for i, tmpl := range cmd.Send[1:] {
					kind, _, err := parseArg(tmpl)
					if err != nil {
						t.Errorf("%s command %s arg %d template %q invalid: %v", entry.Name(), role, i, tmpl, err)
						continue
					}
					if i >= len(args) {
						t.Errorf("%s command %s missing built arg %d", entry.Name(), role, i)
						continue
					}
					switch kind {
					case 'i':
						if _, ok := args[i].(int32); !ok {
							t.Errorf("%s command %s arg %d expected int32 for tag 'i', got %T (%v)", entry.Name(), role, i, args[i], args[i])
						}
					case 'f':
						if _, ok := args[i].(float32); !ok {
							t.Errorf("%s command %s arg %d expected float32 for tag 'f', got %T (%v)", entry.Name(), role, i, args[i], args[i])
						}
					case 's':
						if _, ok := args[i].(string); !ok {
							t.Errorf("%s command %s arg %d expected string for tag 's', got %T (%v)", entry.Name(), role, i, args[i], args[i])
						}
					}
				}
			}
		})
	}

	if found == 0 {
		t.Fatal("no bundled DAW maps found")
	}
}

// TestPlaceholdersAndTypeTags verifies that every supported command placeholder
// fills correctly with proper OSC type tags ("i:", "f:", "s:").
func TestPlaceholdersAndTypeTags(t *testing.T) {
	v := vars{
		track:  3,
		fx:     5,
		param:  12,
		to:     7,
		slot:   2,
		value:  0.875,
		name:   "EQ Eight",
		preset: "Bright Vocal",
	}

	cases := []struct {
		name     string
		send     []string
		wantAddr string
		wantArgs []any
	}{
		{
			name:     "integer track, fx, param",
			send:     []string{"/dev/param", "i:{track}", "i:{fx}", "i:{param}"},
			wantAddr: "/dev/param",
			wantArgs: []any{int32(3), int32(5), int32(12)},
		},
		{
			name:     "integer to, slot",
			send:     []string{"/track/move", "i:{to}", "i:{slot}"},
			wantAddr: "/track/move",
			wantArgs: []any{int32(7), int32(2)},
		},
		{
			name:     "float value",
			send:     []string{"/param/set", "i:{track}", "f:{value}"},
			wantAddr: "/param/set",
			wantArgs: []any{int32(3), float32(0.875)},
		},
		{
			name:     "integer value for boolean toggle",
			send:     []string{"/track/mute", "i:{track}", "i:{value}"},
			wantAddr: "/track/mute",
			wantArgs: []any{int32(3), int32(1)}, // round(0.875) = 1
		},
		{
			name:     "string preset and name",
			send:     []string{"/dev/load", "s:{preset}", "s:{name}"},
			wantAddr: "/dev/load",
			wantArgs: []any{"Bright Vocal", "EQ Eight"},
		},
		{
			name:     "literal values alongside placeholders",
			send:     []string{"/custom", "i:0", "f:1.5", "s:fixed", "i:{track}"},
			wantAddr: "/custom",
			wantArgs: []any{int32(0), float32(1.5), "fixed", int32(3)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := Command{Send: tc.send}
			addr, args, err := cmd.build(v)
			if err != nil {
				t.Fatalf("build error: %v", err)
			}
			if addr != tc.wantAddr {
				t.Errorf("got addr %q, want %q", addr, tc.wantAddr)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("got %d args, want %d", len(args), len(tc.wantArgs))
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Errorf("arg %d: got %v (%T), want %v (%T)", i, args[i], args[i], tc.wantArgs[i], tc.wantArgs[i])
				}
			}
		})
	}
}

// TestAdapterCommandsCleanlyOptional verifies that any command not in the required
// list is cleanly marked optional in Map and gracefully handled when absent.
func TestAdapterCommandsCleanlyOptional(t *testing.T) {
	// A minimal map that has only the required commands
	m := Map{
		Name:        "MinimalDAW",
		Port:        9990,
		ReplyPort:   9991,
		Base:        0,
		Ping:        Command{Send: []string{"/ping"}},
		TrackCount:  Command{Send: []string{"/tracks"}},
		TrackName:   Command{Send: []string{"/track/name", "i:{track}"}},
		DeviceNames: Command{Send: []string{"/devices", "i:{track}"}},
		ParamNames:  Command{Send: []string{"/params", "i:{track}", "i:{fx}"}},
		ParamValues: Command{Send: []string{"/values", "i:{track}", "i:{fx}"}},
		GetParam:    Command{Send: []string{"/param", "i:{track}", "i:{fx}", "i:{param}"}},
		SetParam:    Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
	}

	if err := m.Check(); err != nil {
		t.Fatalf("minimal map failed Check(): %v", err)
	}

	if m.Chains() {
		t.Error("expected Chains() to be false when device catalog is missing")
	}
	if m.Shows() {
		t.Error("expected Shows() to be false when show_device is missing")
	}

	// Create a fake server so we can Open the DAW adapter
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	m.Port = pc.LocalAddr().(*net.UDPAddr).Port

	free, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.ReplyPort = free.LocalAddr().(*net.UDPAddr).Port
	free.Close()

	d, err := Open(m)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Verify all optional adapter methods report unsupported gracefully
	if _, err := d.SetTrack(ctx, 1, "volume", 0.8); err == nil {
		t.Error("expected error for unconfigured SetVolume")
	}
	if _, err := d.SetTrack(ctx, 1, "pan", 0.0); err == nil {
		t.Error("expected error for unconfigured SetPan")
	}
	if _, err := d.SetTrack(ctx, 1, "mute", 1.0); err == nil {
		t.Error("expected error for unconfigured SetMute")
	}
	if _, err := d.SetTrack(ctx, 1, "solo", 1.0); err == nil {
		t.Error("expected error for unconfigured SetSolo")
	}
	if err := d.SetBypass(ctx, 1, 1, true); err == nil {
		t.Error("expected error for unconfigured SetBypass")
	}
	if _, _, err := d.Presets(ctx, 1, 1); err == nil {
		t.Error("expected error for unconfigured DevicePresets")
	}
	if err := d.SetPreset(ctx, 1, 1, "Lead"); err == nil {
		t.Error("expected error for unconfigured SetPreset")
	}
	if err := d.Undo(ctx); err == nil {
		t.Error("expected error for unconfigured Undo")
	}
	if err := d.Transport(ctx, true); err == nil {
		t.Error("expected error for unconfigured Play")
	}
	if err := d.Transport(ctx, false); err == nil {
		t.Error("expected error for unconfigured Stop")
	}
	if _, err := d.CreateTrack(ctx, "Audio", 1); err == nil {
		t.Error("expected error for unconfigured CreateTrack")
	}
	if _, err := d.DuplicateTrack(ctx, 1, "Audio 2"); err == nil {
		t.Error("expected error for unconfigured DuplicateTrack")
	}
	if err := d.DeleteTrack(ctx, 1); err == nil {
		t.Error("expected error for unconfigured DeleteTrack")
	}
	if err := d.RenameTrack(ctx, 1, "Lead"); err == nil {
		t.Error("expected error for unconfigured RenameTrack")
	}
	if err := d.InsertMedia(ctx, 1, "clip.wav", 0); err == nil {
		t.Error("expected error for unconfigured InsertMedia")
	}
	if err := d.CreateSend(ctx, 1, 2, -6.0); err == nil {
		t.Error("expected error for unconfigured CreateSend")
	}
	if err := d.SetTransport(ctx, "tempo", 120, 0, 0, 0); err == nil {
		t.Error("expected error for unconfigured SetTempo")
	}
	if err := d.SetTransport(ctx, "loop", 0, 0, 1, 4); err == nil {
		t.Error("expected error for unconfigured SetLoop")
	}
	if err := d.OpenUI(ctx, 1, 1, true); err == nil {
		t.Error("expected error for unconfigured ShowDevice")
	}

	// Starter cleanly declines without audio detection commands
	if tr, _, err := d.Starter(ctx, "riff.wav"); err != nil || tr != 0 {
		t.Errorf("Starter: got track %d, err %v; want 0, nil", tr, err)
	}

	// Chain adapter operations cleanly decline
	chain := &Chain{D: d}
	if _, err := chain.Catalog(ctx); err == nil {
		t.Error("expected error for unconfigured DeviceCatalog")
	}
	if err := chain.MoveFX(ctx, 1, 1, 2); err == nil {
		t.Error("expected error for unconfigured MoveDevice")
	}
}

// TestBooleanOSCDecoding verifies that boolean OSC answers decode properly
// across integers (0/1), floats (0.0/1.0), and booleans (true/false),
// including OSC binary type tags 'T' and 'F'.
func TestBooleanOSCDecoding(t *testing.T) {
	// 1. OSC binary decoding for tags 'T' and 'F'
	rawBool := []byte("/test\x00\x00\x00" + ",TFi\x00\x00\x00\x00" + "\x00\x00\x00\x2a")
	msgs, err := reaper.Decode(rawBool)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Decode OSC booleans: %v (err %v)", msgs, err)
	}
	if len(msgs[0].Args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(msgs[0].Args))
	}
	// 'T' decodes as 1, 'F' as 0
	if msgs[0].Args[0] != int32(1) || msgs[0].Args[1] != int32(0) || msgs[0].Args[2] != int32(42) {
		t.Errorf("OSC boolean tags decoded to %#v, want [1 0 42]", msgs[0].Args)
	}

	// 2. asBool unit decoding
	cases := []struct {
		input    any
		wantBool bool
		wantOK   bool
	}{
		// Booleans
		{true, true, true},
		{false, false, true},
		// Integers
		{int32(1), true, true},
		{int32(0), false, true},
		{int(1), true, true},
		{int(0), false, true},
		{int64(1), true, true},
		{int64(0), false, true},
		// Floats
		{float32(1.0), true, true},
		{float32(0.0), false, true},
		{float64(1.0), true, true},
		{float64(0.0), false, true},
		// Non-booleans
		{"true", false, false},
		{nil, false, false},
	}

	for _, tc := range cases {
		got, ok := asBool(tc.input)
		if ok != tc.wantOK || got != tc.wantBool {
			t.Errorf("asBool(%v [%T]) = (%v, %v), want (%v, %v)", tc.input, tc.input, got, ok, tc.wantBool, tc.wantOK)
		}
	}

	// 3. num() helper handles bool and int
	if n, ok := num(true); !ok || n != 1.0 {
		t.Errorf("num(true) = (%v, %v), want (1.0, true)", n, ok)
	}
	if n, ok := num(false); !ok || n != 0.0 {
		t.Errorf("num(false) = (%v, %v), want (0.0, true)", n, ok)
	}
	if n, ok := num(int(5)); !ok || n != 5.0 {
		t.Errorf("num(int(5)) = (%v, %v), want (5.0, true)", n, ok)
	}

	// 4. Live network test: DAW.Tracks() with TrackAudio returning boolean variants
	for _, format := range []string{"osc_tags", "int32", "float32"} {
		t.Run("TrackAudio_"+format, func(t *testing.T) {
			srv, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()

			free, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			replyPort := free.LocalAddr().(*net.UDPAddr).Port
			free.Close()

			toAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: replyPort}

			m := Map{
				Name:        "BoolTestDAW",
				Port:        srv.LocalAddr().(*net.UDPAddr).Port,
				ReplyPort:   replyPort,
				Base:        0,
				Ping:        Command{Send: []string{"/ping"}},
				TrackCount:  Command{Send: []string{"/tracks"}, Reply: "/tracks"},
				TrackName:   Command{Send: []string{"/track/name", "i:{track}"}, Reply: "/track/name", Skip: 1},
				TrackAudio:  Command{Send: []string{"/track/audio", "i:{track}"}, Reply: "/track/audio", Skip: 1},
				DeviceNames: Command{Send: []string{"/devices", "i:{track}"}},
				ParamNames:  Command{Send: []string{"/params", "i:{track}", "i:{fx}"}},
				ParamValues: Command{Send: []string{"/values", "i:{track}", "i:{fx}"}},
				GetParam:    Command{Send: []string{"/param", "i:{track}", "i:{fx}", "i:{param}"}},
				SetParam:    Command{Send: []string{"/set", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"}},
			}

			go func() {
				buf := make([]byte, 4096)
				for {
					n, _, err := srv.ReadFrom(buf)
					if err != nil {
						return
					}
					decoded, _ := reaper.Decode(buf[:n])
					for _, req := range decoded {
						switch req.Addr {
						case "/tracks":
							b, _ := reaper.Encode(reaper.Msg{Addr: "/tracks", Args: []any{int32(2)}})
							srv.WriteTo(b, toAddr)
						case "/track/name":
							tr := req.Args[0].(int32)
							b, _ := reaper.Encode(reaper.Msg{Addr: "/track/name", Args: []any{tr, fmt.Sprintf("Track %d", tr+1)}})
							srv.WriteTo(b, toAddr)
						case "/track/audio":
							tr := req.Args[0].(int32)
							isAudio := tr == 0 // track 0 is audio, track 1 is MIDI
							var respMsg reaper.Msg
							switch format {
							case "osc_tags":
								// Encode manually with OSC boolean tags (T / F)
								tag := "F"
								if isAudio {
									tag = "T"
								}
								raw := []byte("/track/audio\x00\x00\x00\x00" + ",i" + tag + "\x00" +
									string([]byte{byte(tr >> 24), byte(tr >> 16), byte(tr >> 8), byte(tr)}))
								srv.WriteTo(raw, toAddr)
								continue
							case "int32":
								v := int32(0)
								if isAudio {
									v = 1
								}
								respMsg = reaper.Msg{Addr: "/track/audio", Args: []any{tr, v}}
							case "float32":
								v := float32(0.0)
								if isAudio {
									v = 1.0
								}
								respMsg = reaper.Msg{Addr: "/track/audio", Args: []any{tr, v}}
							}
							b, _ := reaper.Encode(respMsg)
							srv.WriteTo(b, toAddr)
						}
					}
				}
			}()

			d, err := Open(m)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			defer d.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			tracks, err := d.Tracks(ctx)
			if err != nil {
				t.Fatalf("Tracks error: %v", err)
			}
			if len(tracks) != 2 {
				t.Fatalf("expected 2 tracks, got %d", len(tracks))
			}
			if !tracks[0].Audio {
				t.Errorf("track 0: expected Audio=true, got false")
			}
			if tracks[1].Audio {
				t.Errorf("track 1: expected Audio=false, got true")
			}
		})
	}
}

// TestMergeBundledNeverOverwritesUserCommands verifies that MergeBundled
// never overwrites any existing command configured by the user, only filling
// in missing commands.
func TestMergeBundledNeverOverwritesUserCommands(t *testing.T) {
	tmpDir := t.TempDir()
	mapPath := filepath.Join(tmpDir, "ableton.json")

	// Custom map created by user:
	// - Customized set_volume with custom address
	// - Customized ping with custom address
	// - Missing set_pan, set_mute, set_solo, set_bypass, insert_media
	userMap := map[string]any{
		"name":       "My Custom Ableton Setup",
		"port":       12345,
		"reply_port": 12346,
		"base":       0,
		"ping": map[string]any{
			"send":  []string{"/user/custom/ping"},
			"reply": "/user/custom/ping",
		},
		"track_count": map[string]any{
			"send":  []string{"/live/song/get/num_tracks"},
			"reply": "/live/song/get/num_tracks",
		},
		"track_name": map[string]any{
			"send":  []string{"/live/track/get/name", "i:{track}"},
			"reply": "/live/track/get/name",
			"skip":  1,
		},
		"device_names": map[string]any{
			"send":  []string{"/live/track/get/devices/name", "i:{track}"},
			"reply": "/live/track/get/devices/name",
			"skip":  1,
		},
		"param_names": map[string]any{
			"send":  []string{"/live/device/get/parameters/name", "i:{track}", "i:{fx}"},
			"reply": "/live/device/get/parameters/name",
			"skip":  2,
		},
		"param_values": map[string]any{
			"send":  []string{"/live/device/get/parameters/value", "i:{track}", "i:{fx}"},
			"reply": "/live/device/get/parameters/value",
			"skip":  2,
		},
		"get_param": map[string]any{
			"send":  []string{"/live/device/get/parameter/value", "i:{track}", "i:{fx}", "i:{param}"},
			"reply": "/live/device/get/parameter/value",
			"skip":  3,
		},
		"set_param": map[string]any{
			"send": []string{"/live/device/set/parameter/value", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"},
		},
		"set_volume": map[string]any{
			"send": []string{"/user/custom/volume", "i:{track}", "f:{value}"},
		},
	}

	raw, err := json.MarshalIndent(userMap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mapPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// First merge: should fill missing commands
	added, err := MergeBundled(mapPath, bundled.FS)
	if err != nil {
		t.Fatalf("MergeBundled failed: %v", err)
	}
	if len(added) == 0 {
		t.Fatal("expected commands to be added from bundled map")
	}

	// Verify that user's existing commands were NOT added to 'added'
	for _, cmd := range added {
		if cmd == "set_volume" || cmd == "ping" || cmd == "name" || cmd == "port" || cmd == "reply_port" {
			t.Errorf("MergeBundled reported existing key %q in added list", cmd)
		}
	}

	// Read back and verify user values are intact
	updatedRaw, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Map
	if err := json.Unmarshal(updatedRaw, &loaded); err != nil {
		t.Fatalf("unmarshal merged map: %v", err)
	}

	// User's custom commands must be unchanged
	if loaded.Name != "My Custom Ableton Setup" {
		t.Errorf("Name overwritten: got %q, want 'My Custom Ableton Setup'", loaded.Name)
	}
	if loaded.Port != 12345 {
		t.Errorf("Port overwritten: got %d, want 12345", loaded.Port)
	}
	if len(loaded.Ping.Send) != 1 || loaded.Ping.Send[0] != "/user/custom/ping" {
		t.Errorf("Ping overwritten: got %v", loaded.Ping.Send)
	}
	if len(loaded.SetVolume.Send) != 3 || loaded.SetVolume.Send[0] != "/user/custom/volume" {
		t.Errorf("SetVolume overwritten: got %v", loaded.SetVolume.Send)
	}

	// Missing commands must be filled in
	if len(loaded.SetMute.Send) == 0 {
		t.Error("expected SetMute to be filled from bundled map")
	}
	if len(loaded.SetBypass.Send) == 0 {
		t.Error("expected SetBypass to be filled from bundled map")
	}
	if loaded.InsertMedia == nil || len(loaded.InsertMedia.Send) == 0 {
		t.Error("expected InsertMedia to be filled from bundled map")
	}

	// Second merge: should be a no-op since all bundled keys exist now
	added2, err := MergeBundled(mapPath, bundled.FS)
	if err != nil {
		t.Fatalf("second MergeBundled failed: %v", err)
	}
	if len(added2) != 0 {
		t.Errorf("expected 0 commands added on second merge, got %v", added2)
	}
}

// TestOnboardingPromptSchemaCompleteness verifies that the schema in onboard.go
// explicitly asks for every optional command in tonelab-spec.md S/M/F sections.
func TestOnboardingPromptSchemaCompleteness(t *testing.T) {
	requiredOptionalCommands := []string{
		// S. Session
		"set_volume",
		"set_pan",
		"set_mute",
		"set_solo",
		"set_bypass",
		"undo",
		"play",
		"stop",
		"track_audio",
		"scene_count",
		"has_clip",
		"create_track",
		"duplicate_track",
		"delete_track",
		"rename_track",
		"create_send",
		"set_tempo",
		"set_loop",

		// M. Media
		"insert_media",

		// F. Chain and plugins
		"device_catalog",
		"insert_device",
		"remove_device",
		"move_device",
		"device_presets",
		"set_preset",
		"show_device",

		// Extensibility
		"extra",
	}

	for _, cmd := range requiredOptionalCommands {
		quoted := fmt.Sprintf("%q", cmd)
		if !strings.Contains(schema, quoted) {
			t.Errorf("onboarding prompt schema does not ask for optional command %s", quoted)
		}
	}

	// Verify that a JSON conforming to the schema can be parsed into Map
	conformingJSON := `{
		"name": "Test DAW Spec Conforming",
		"port": 9000,
		"reply_port": 9001,
		"base": 0,
		"app": "com.test.daw",
		"ping": {"send": ["/ping"], "reply": "/ping", "skip": 0},
		"track_count": {"send": ["/track_count"], "reply": "/track_count"},
		"track_name": {"send": ["/track_name", "i:{track}"], "reply": "/track_name", "skip": 1},
		"device_names": {"send": ["/device_names", "i:{track}"], "reply": "/device_names", "skip": 1},
		"param_names": {"send": ["/param_names", "i:{track}", "i:{fx}"], "reply": "/param_names", "skip": 2},
		"param_values": {"send": ["/param_values", "i:{track}", "i:{fx}"], "reply": "/param_values", "skip": 2},
		"param_mins": {"send": []},
		"param_maxs": {"send": []},
		"param_text": {"send": ["/param_text", "i:{track}", "i:{fx}", "i:{param}"], "reply": "/param_text", "skip": 3},
		"get_param": {"send": ["/get_param", "i:{track}", "i:{fx}", "i:{param}"], "reply": "/get_param", "skip": 3},
		"set_param": {"send": ["/set_param", "i:{track}", "i:{fx}", "i:{param}", "f:{value}"], "reply": ""},
		"set_volume": {"send": ["/set_volume", "i:{track}", "f:{value}"]},
		"set_pan": {"send": ["/set_pan", "i:{track}", "f:{value}"]},
		"set_mute": {"send": ["/set_mute", "i:{track}", "i:{value}"]},
		"set_solo": {"send": ["/set_solo", "i:{track}", "i:{value}"]},
		"set_bypass": {"send": ["/set_bypass", "i:{track}", "i:{fx}", "i:{value}"]},
		"undo": {"send": ["/undo"]},
		"play": {"send": ["/play"]},
		"stop": {"send": ["/stop"]},
		"device_catalog": {"send": ["/device_catalog"], "reply": "/device_catalog"},
		"insert_device": {"send": ["/insert_device", "i:{track}", "s:{name}", "i:{fx}"], "reply": "/insert_device", "skip": 1},
		"remove_device": {"send": ["/remove_device", "i:{track}", "i:{fx}"]},
		"move_device": {"send": ["/move_device", "i:{track}", "i:{fx}", "i:{to}"]},
		"device_presets": {"send": ["/device_presets", "i:{track}", "i:{fx}"], "reply": "/device_presets", "skip": 2},
		"set_preset": {"send": ["/set_preset", "i:{track}", "i:{fx}", "s:{preset}"]},
		"show_device": {"send": ["/show_device", "i:{track}", "i:{fx}"]},
		"track_audio": {"send": ["/track_audio", "i:{track}"], "reply": "/track_audio", "skip": 1},
		"scene_count": {"send": ["/scene_count"], "reply": "/scene_count"},
		"has_clip": {"send": ["/has_clip", "i:{track}", "i:{slot}"], "reply": "/has_clip", "skip": 2},
		"create_track": {"send": ["/create_track", "i:{track}", "s:{name}"]},
		"duplicate_track": {"send": ["/duplicate_track", "i:{track}"]},
		"delete_track": {"send": ["/delete_track", "i:{track}"]},
		"rename_track": {"send": ["/rename_track", "i:{track}", "s:{name}"]},
		"insert_media": {"send": ["/insert_media", "i:{track}", "s:{name}", "f:{value}"], "reply": "/insert_media", "skip": 1},
		"create_send": {"send": ["/create_send", "i:{track}", "i:{to}", "f:{value}"]},
		"set_tempo": {"send": ["/set_tempo", "f:{value}"]},
		"set_loop": {"send": ["/set_loop", "f:{value}", "i:{to}"]},
		"extra": {"custom": {"send": ["/extra/cmd"]}}
	}`

	var parsed Map
	if err := json.Unmarshal([]byte(conformingJSON), &parsed); err != nil {
		t.Fatalf("conforming JSON unmarshal: %v", err)
	}
	if err := parsed.Check(); err != nil {
		t.Fatalf("conforming map failed Check(): %v", err)
	}
}
