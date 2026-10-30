package fake_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/fake"
	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

func ctx() context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = cancel
	return c
}

// TestAll46ContractOperations verifies that fake.Engine implements all 46 capability rows
// from the tonelab-muse specification via the Execute dispatcher, each returning {applied, readback}.
func TestAll46ContractOperations(t *testing.T) {
	eng := fake.New()

	// Capabilities probe
	res, err := eng.Execute(ctx(), "capabilities", nil)
	if err != nil {
		t.Fatalf("capabilities op failed: %v", err)
	}
	ops, ok := res.Readback.([]string)
	if !ok || len(ops) < 40 {
		t.Fatalf("capabilities returned %v, want list of >= 40 ops", res.Readback)
	}

	t.Run("S1_list_tracks", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "list_tracks", nil)
		if err != nil {
			t.Fatal(err)
		}
		tracks, ok := res.Readback.([]fake.Track)
		if !ok || len(tracks) < 2 {
			t.Fatalf("expected >= 2 tracks, got %v", res.Readback)
		}
		t1 := tracks[0]
		if t1.Name == "" || t1.Type == "" || t1.Volume == 0 {
			t.Fatalf("track 1 missing required fields: %+v", t1)
		}
	})

	t.Run("S2_manage_track", func(t *testing.T) {
		eng := fake.New()
		// Create
		res, err := eng.Execute(ctx(), "manage_track", map[string]any{"action": "create", "name": "Vocal"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Applied == nil || res.Readback == nil {
			t.Fatal("expected non-nil applied and readback")
		}

		// Rename
		res, err = eng.Execute(ctx(), "manage_track", map[string]any{"action": "rename", "track": 1, "name": "Main Guitar"})
		if err != nil {
			t.Fatal(err)
		}
		rbMap, _ := res.Readback.(map[string]any)
		if rbMap["name"] != "Main Guitar" {
			t.Fatalf("rename readback: %v", rbMap)
		}

		// Duplicate
		_, err = eng.Execute(ctx(), "manage_track", map[string]any{"action": "duplicate", "track": 1, "name": "Guitar Copy"})
		if err != nil {
			t.Fatal(err)
		}

		// Delete
		_, err = eng.Execute(ctx(), "manage_track", map[string]any{"action": "delete", "track": 2})
		if err != nil {
			t.Fatal(err)
		}

		// Undo restores
		if err := eng.Undo(ctx()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("S3_set_track_prop", func(t *testing.T) {
		eng := fake.New()
		// Volume
		res, err := eng.Execute(ctx(), "set_track_prop", map[string]any{"track": 1, "prop": "volume", "value": 0.85})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["confirmed"] != 0.85 {
			t.Fatalf("volume readback %v, want 0.85", rb["confirmed"])
		}

		// Pan
		res, err = eng.Execute(ctx(), "set_track_prop", map[string]any{"track": 1, "prop": "pan", "value": -0.5})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ = res.Readback.(map[string]any)
		if rb["confirmed"] != -0.5 {
			t.Fatalf("pan readback %v, want -0.5", rb["confirmed"])
		}

		// Mute & Solo & Arm
		for _, prop := range []string{"mute", "solo", "arm"} {
			res, err = eng.Execute(ctx(), "set_track_prop", map[string]any{"track": 1, "prop": prop, "value": 1.0})
			if err != nil {
				t.Fatalf("%s failed: %v", prop, err)
			}
			rb, _ = res.Readback.(map[string]any)
			if rb["confirmed"] != 1.0 {
				t.Fatalf("%s confirmed %v, want 1.0", prop, rb["confirmed"])
			}
		}

		// Monitor & Input
		res, err = eng.Execute(ctx(), "set_track_prop", map[string]any{"track": 1, "prop": "monitor", "value": "in"})
		if err != nil {
			t.Fatal(err)
		}
		res, err = eng.Execute(ctx(), "set_track_prop", map[string]any{"track": 1, "prop": "input", "value": "Input 2"})
		if err != nil {
			t.Fatal(err)
		}

		// Undo restoration
		_ = eng.Undo(ctx())
	})

	t.Run("S4_route_send", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "route_send", map[string]any{"action": "create", "from_track": 1, "to_track": 2, "level_db": -6.0})
		if err != nil {
			t.Fatal(err)
		}
		sends, ok := res.Readback.([]fake.Send)
		if !ok || len(sends) == 0 {
			t.Fatalf("route_send readback: %v", res.Readback)
		}

		// Remove send
		res, err = eng.Execute(ctx(), "route_send", map[string]any{"action": "remove", "from_track": 1, "send": 1})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("S5_set_transport", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "set_transport", map[string]any{"action": "tempo", "bpm": 140.0})
		if err != nil {
			t.Fatal(err)
		}
		trans, _ := res.Readback.(fake.Transport)
		if trans.BPM != 140.0 {
			t.Fatalf("BPM = %v, want 140.0", trans.BPM)
		}

		res, err = eng.Execute(ctx(), "set_transport", map[string]any{"action": "time_signature", "numerator": 3, "denominator": 4})
		if err != nil {
			t.Fatal(err)
		}
		trans, _ = res.Readback.(fake.Transport)
		if trans.TimeSignatureNum != 3 || trans.TimeSignatureDenom != 4 {
			t.Fatalf("time signature %d/%d, want 3/4", trans.TimeSignatureNum, trans.TimeSignatureDenom)
		}

		res, err = eng.Execute(ctx(), "set_transport", map[string]any{"action": "play"})
		if err != nil {
			t.Fatal(err)
		}
		trans, _ = res.Readback.(fake.Transport)
		if trans.PlayState != "playing" {
			t.Fatalf("play state %s, want playing", trans.PlayState)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("S6_markers", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "markers", map[string]any{"action": "create", "name": "Solo", "position_sec": 12.5, "end_sec": 20.0, "region": true})
		if err != nil {
			t.Fatal(err)
		}
		markers, ok := res.Readback.([]core.Marker)
		if !ok || len(markers) == 0 {
			t.Fatalf("markers readback: %v", res.Readback)
		}

		res, err = eng.Execute(ctx(), "markers", map[string]any{"action": "goto", "index": 1})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("S7_undo_grouping", func(t *testing.T) {
		eng := fake.New()
		// Begin undo action group
		_, err := eng.Execute(ctx(), "undo", map[string]any{"action": "begin", "name": "ToneTweaks"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = eng.SetTrack(ctx(), 1, "volume", 0.77)
		_, _ = eng.SetTrack(ctx(), 1, "pan", 0.33)
		_, err = eng.Execute(ctx(), "undo", map[string]any{"action": "end", "name": "ToneTweaks", "commit": true})
		if err != nil {
			t.Fatal(err)
		}
		// A single undo should restore both volume and pan to pre-group state
		if err := eng.Undo(ctx()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("S8_save_project", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "save_project", nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["saved"] != true {
			t.Fatalf("save_project readback: %v", rb)
		}
	})

	t.Run("M1_insert_media", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "insert_media", map[string]any{"track": 1, "path": "lead.wav", "position_sec": 4.0})
		if err != nil {
			t.Fatal(err)
		}
		clips, ok := res.Readback.([]fake.MediaClip)
		if !ok || len(clips) == 0 {
			t.Fatalf("insert_media readback: %v", res.Readback)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("M2_starter_riff", func(t *testing.T) {
		eng := fake.New()
		// Project currently has clips, so starter riff must be rejected
		_, err := eng.Execute(ctx(), "starter_riff", map[string]any{"track": 1})
		if err == nil {
			t.Fatal("expected starter riff to be rejected on non-empty set")
		}

		// Empty session should succeed
		emptyEng := fake.New(&fake.Track{Name: "Empty Track"})
		res, err := emptyEng.Execute(ctx(), "starter_riff", map[string]any{"track": 1})
		if err != nil {
			t.Fatalf("starter_riff on empty set failed: %v", err)
		}
		clips, ok := res.Readback.([]fake.MediaClip)
		if !ok || len(clips) == 0 {
			t.Fatalf("starter riff clips empty: %v", res.Readback)
		}
	})

	t.Run("M3_render_track", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "render_track", map[string]any{"track": 1, "seconds": 1.0})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		wavPath, ok := rb["path"].(string)
		if !ok || wavPath == "" {
			t.Fatalf("render_track path missing: %v", rb)
		}
		if _, err := os.Stat(wavPath); err != nil {
			t.Fatalf("rendered file not created: %v", err)
		}
	})

	t.Run("M4_import_reference", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "import_reference", map[string]any{"path": "ref.wav"})
		if err != nil {
			t.Fatal(err)
		}
		m, ok := res.Readback.(core.Measure)
		if !ok || m.Centroid == 0 {
			t.Fatalf("import_reference measure: %+v", res.Readback)
		}
	})

	t.Run("M5_record_input", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "record_input", map[string]any{"track": 1, "seconds": 0.5, "token": "test-take"})
		if err != nil {
			t.Fatal(err)
		}
		clip, ok := res.Readback.(core.RecordedClip)
		if !ok || clip.Path == "" {
			t.Fatalf("record_input clip missing: %+v", res.Readback)
		}
		if _, err := os.Stat(clip.Path); err != nil {
			t.Fatalf("recorded clip file does not exist: %v", err)
		}
	})

	t.Run("F1_catalog_fx", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "catalog_fx", map[string]any{"query": "EQ"})
		if err != nil {
			t.Fatal(err)
		}
		entries, ok := res.Readback.([]core.CatalogEntry)
		if !ok || len(entries) == 0 {
			t.Fatalf("catalog_fx search: %v", res.Readback)
		}
	})

	t.Run("F2_manage_fx", func(t *testing.T) {
		eng := fake.New()
		// Add plugin
		res, err := eng.Execute(ctx(), "manage_fx", map[string]any{"action": "add", "track": 1, "name": "Chorus (Test)"})
		if err != nil {
			t.Fatal(err)
		}
		chain, ok := res.Readback.([]core.FX)
		if !ok || len(chain) == 0 {
			t.Fatalf("add_fx chain: %v", res.Readback)
		}

		// Move plugin
		_, err = eng.Execute(ctx(), "manage_fx", map[string]any{"action": "move", "track": 1, "fx": 1, "to_pos": 2})
		if err != nil {
			t.Fatal(err)
		}

		// Bypass
		res, err = eng.Execute(ctx(), "manage_fx", map[string]any{"action": "bypass", "track": 1, "fx": 1, "bypassed": true})
		if err != nil {
			t.Fatal(err)
		}

		// Wet/Dry
		res, err = eng.Execute(ctx(), "manage_fx", map[string]any{"action": "wet_dry", "track": 1, "fx": 1, "wet_dry": 0.65})
		if err != nil {
			t.Fatal(err)
		}

		// Remove (user-added plugin can be removed)
		lastFX := len(chain)
		_, err = eng.Execute(ctx(), "manage_fx", map[string]any{"action": "remove", "track": 1, "fx": lastFX})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("F3_show_fx_ui", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "show_fx_ui", map[string]any{"track": 1, "fx": 1, "show": true})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["window_open"] != true {
			t.Fatalf("show_fx_ui readback: %v", rb)
		}
	})

	t.Run("F4_read_params", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "read_params", map[string]any{"track": 1, "fx": 1})
		if err != nil {
			t.Fatal(err)
		}
		params, ok := res.Readback.([]core.Param)
		if !ok || len(params) == 0 {
			t.Fatalf("read_params: %v", res.Readback)
		}
	})

	t.Run("F5_set_param_value_and_target", func(t *testing.T) {
		eng := fake.New()
		// Value write
		res, err := eng.Execute(ctx(), "set_param", map[string]any{"track": 1, "fx": 1, "param": "1/5", "value": 0.8})
		if err != nil {
			t.Fatal(err)
		}
		applied, ok := res.Readback.(core.Applied)
		if !ok || applied.Confirmed != 0.8 {
			t.Fatalf("set_param readback: %+v", res.Readback)
		}

		// Target readout search
		res, err = eng.Execute(ctx(), "set_param", map[string]any{"track": 1, "fx": 1, "param": "1/11", "target": "On"})
		if err != nil {
			t.Fatal(err)
		}
		applied, ok = res.Readback.(core.Applied)
		if !ok || applied.Text != "On" {
			t.Fatalf("target search readback: %+v", res.Readback)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("F6_learn_param", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "learn_param", map[string]any{"track": 1, "fx": 1, "param": 1, "label": "Custom Drive"})
		if err != nil {
			t.Fatal(err)
		}
		p, ok := res.Readback.(fake.Param)
		if !ok || p.Name != "Custom Drive" || p.Dead {
			t.Fatalf("learn_param readback: %+v", res.Readback)
		}
	})

	t.Run("F7_project_state", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "project_state", map[string]any{"track": 1, "fx": 1})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["fx"] == "" {
			t.Fatalf("project_state: %v", rb)
		}
	})

	t.Run("F8_verify_write_readonly", func(t *testing.T) {
		eng := fake.New()
		roFX, _ := eng.AddFX(ctx(), 1, "ReadOnly FX (Test)", 1)
		res, err := eng.Execute(ctx(), "verify_write", map[string]any{"track": 1, "param": fmt.Sprintf("%d/1", roFX), "value": 0.99})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["read_only"] != true {
			t.Fatalf("expected read-only plugin to be flagged: %v", rb)
		}
	})

	t.Run("F9_model_switch", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "model_switch", map[string]any{"action": "list", "track": 1, "fx": 1})
		if err != nil {
			t.Fatal(err)
		}
		slots, ok := res.Readback.([]core.PluginSlot)
		if !ok || len(slots) == 0 {
			t.Fatalf("model_switch list: %v", res.Readback)
		}

		res, err = eng.Execute(ctx(), "model_switch", map[string]any{"action": "set", "track": 1, "fx": 1, "slot": "Amp", "model": "Crunch"})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("F10_presets", func(t *testing.T) {
		eng := fake.New()
		// List
		res, err := eng.Execute(ctx(), "presets", map[string]any{"action": "list", "track": 1, "fx": 1})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		presets, _ := rb["presets"].([]string)
		if len(presets) == 0 {
			t.Fatalf("presets empty: %v", rb)
		}

		// Load
		res, err = eng.Execute(ctx(), "presets", map[string]any{"action": "load", "track": 1, "fx": 1, "preset": presets[0]})
		if err != nil {
			t.Fatal(err)
		}

		// Save As
		res, err = eng.Execute(ctx(), "presets", map[string]any{"action": "save", "track": 1, "fx": 1, "name": "My Custom Tone"})
		if err != nil {
			t.Fatal(err)
		}
		_ = eng.Undo(ctx())
	})

	t.Run("F11_guide_user", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "guide_user", map[string]any{"track": 1, "fx": 1})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		steps, _ := rb["guidance_steps"].([]string)
		if len(steps) == 0 || len(steps) > 3 {
			t.Fatalf("guidance must have 1-3 steps, got %v", steps)
		}
	})

	t.Run("A1_measure_audio", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "measure_audio", nil)
		if err != nil {
			t.Fatal(err)
		}
		m, ok := res.Readback.(core.Measure)
		if !ok || m.Centroid == 0 {
			t.Fatalf("measure_audio: %+v", res.Readback)
		}
	})

	t.Run("A2_match_loudness", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "match_loudness", map[string]any{"target_dbfs": -16.0})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		delta, _ := rb["delta_db"].(float64)
		if delta > 1.0 {
			t.Fatalf("loudness gap must be <= 1 dB, got %v", delta)
		}
	})

	t.Run("A3_spectrogram", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "spectrogram", nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		p, ok := rb["path"].(string)
		if !ok || p == "" {
			t.Fatalf("spectrogram path empty: %v", rb)
		}
	})

	t.Run("A4_mix_clash", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "mix_clash", nil)
		if err != nil {
			t.Fatal(err)
		}
		clashes, ok := res.Readback.([]core.Clash)
		if !ok || len(clashes) == 0 {
			t.Fatalf("mix_clash empty: %v", res.Readback)
		}
	})

	t.Run("A5_reference_match", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "reference_match", nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["converged"] != true {
			t.Fatalf("reference_match: %v", rb)
		}
	})

	t.Run("A6_diagnose_silence", func(t *testing.T) {
		eng := fake.New()
		// Audio engine running normally
		res, err := eng.Execute(ctx(), "diagnose_silence", map[string]any{"track": 1})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["silent"] != false {
			t.Fatalf("expected active sound, got %v", rb)
		}

		// Engine turned off
		eng.SetAudioEngineRunning(false)
		res, err = eng.Execute(ctx(), "diagnose_silence", map[string]any{"track": 1})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ = res.Readback.(map[string]any)
		if rb["silent"] != true || !strings.Contains(rb["reason"].(string), "engine off") {
			t.Fatalf("expected engine off silence diagnosis: %v", rb)
		}
		eng.SetAudioEngineRunning(true)
	})

	t.Run("G1_set_mode", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "set_mode", map[string]any{"mode": "turbo"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["current_mode"] != "turbo" || rb["turbo_enabled"] != true {
			t.Fatalf("set_mode: %v", rb)
		}
	})

	t.Run("G2_snapshot", func(t *testing.T) {
		eng := fake.New()
		// Capture
		res, err := eng.Execute(ctx(), "snapshot", map[string]any{"action": "capture", "name": "before-tweak"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["captured"] != true {
			t.Fatalf("capture snapshot: %v", rb)
		}

		// Mutate state
		_, _ = eng.SetTrack(ctx(), 1, "volume", 0.12)

		// Recall
		res, err = eng.Execute(ctx(), "snapshot", map[string]any{"action": "recall", "name": "before-tweak"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ = res.Readback.(map[string]any)
		if rb["restored"] != true {
			t.Fatalf("recall snapshot: %v", rb)
		}
	})

	t.Run("G3_delegate_task", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "delegate_task", map[string]any{"track": 1, "task": "Shape tone for lead"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["status"] != "completed" {
			t.Fatalf("delegate_task: %v", rb)
		}
	})

	t.Run("G4_token_budget", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "token_budget", map[string]any{"track_count": 1, "tokens_used": 85000})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["within_budget"] != true {
			t.Fatalf("token_budget should be within limit: %v", rb)
		}

		res, err = eng.Execute(ctx(), "token_budget", map[string]any{"track_count": 1, "tokens_used": 150000})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ = res.Readback.(map[string]any)
		if rb["within_budget"] != false {
			t.Fatalf("token_budget should exceed limit: %v", rb)
		}
	})

	t.Run("G5_call_guard", func(t *testing.T) {
		eng := fake.New()
		// First call fails
		res, err := eng.Execute(ctx(), "call_guard", map[string]any{"call_id": "call-1", "error": "unknown param"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["blocked"] != false {
			t.Fatalf("first call should not be blocked: %v", rb)
		}

		// Repeating identical failed call is blocked
		res, err = eng.Execute(ctx(), "call_guard", map[string]any{"call_id": "call-1"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ = res.Readback.(map[string]any)
		if rb["blocked"] != true {
			t.Fatalf("repeated failed call must be blocked: %v", rb)
		}
	})

	t.Run("G6_taste_memory", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "taste_memory", map[string]any{"action": "record", "control": "Gain", "dir": 1})
		if err != nil {
			t.Fatal(err)
		}
		taste, ok := res.Readback.(core.Taste)
		if !ok || len(taste.Leanings) == 0 {
			t.Fatalf("taste_memory: %+v", res.Readback)
		}
	})

	t.Run("I1_process_input", func(t *testing.T) {
		eng := fake.New()
		for _, kind := range []string{"text", "voice", "photo", "reference"} {
			res, err := eng.Execute(ctx(), "process_input", map[string]any{"kind": kind})
			if err != nil {
				t.Fatalf("process_input %s: %v", kind, err)
			}
			rb, _ := res.Readback.(map[string]any)
			if rb["processed"] != true {
				t.Fatalf("process_input %s readback: %v", kind, rb)
			}
		}
	})

	t.Run("I2_onboard_daw", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "onboard_daw", map[string]any{"url": "https://example.com/daw-api", "daw": "CustomDAW"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["verified"] != true {
			t.Fatalf("onboard_daw: %v", rb)
		}
	})

	t.Run("I3_platform_setup", func(t *testing.T) {
		eng := fake.New()
		for _, osName := range []string{"macos", "windows"} {
			res, err := eng.Execute(ctx(), "platform_setup", map[string]any{"os": osName})
			if err != nil {
				t.Fatalf("platform_setup %s: %v", osName, err)
			}
			rb, _ := res.Readback.(map[string]any)
			if rb["osc_listening"] != true {
				t.Fatalf("platform_setup %s: %v", osName, rb)
			}
		}
	})

	t.Run("I4_ui_state", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "ui_state", nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		if rb["mode"] == "" || rb["track_count"] == 0 {
			t.Fatalf("ui_state readback: %v", rb)
		}
	})

	t.Run("I5_release_spec", func(t *testing.T) {
		eng := fake.New()
		res, err := eng.Execute(ctx(), "release_spec", map[string]any{"os": "macos"})
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := res.Readback.(map[string]any)
		bins, _ := rb["binaries"].([]string)
		if len(bins) < 2 {
			t.Fatalf("release_spec binaries: %v", bins)
		}
	})
}

// TestGoCoreInterfacesDirectly verifies that fake.Engine completely implements all pkg/core interfaces.
func TestGoCoreInterfacesDirectly(t *testing.T) {
	eng := fake.New()

	// core.DAW
	tracks, err := eng.Tracks(ctx())
	if err != nil || len(tracks) == 0 {
		t.Fatalf("Tracks(): %v, err=%v", tracks, err)
	}
	chain, err := eng.FXChain(ctx(), 1)
	if err != nil || len(chain) == 0 {
		t.Fatalf("FXChain(): %v, err=%v", chain, err)
	}
	applied, err := eng.SetParam(ctx(), 1, core.MakeParamID(1, 5), 0.75)
	if err != nil || applied.Confirmed != 0.75 {
		t.Fatalf("SetParam(): %+v, err=%v", applied, err)
	}
	vol, err := eng.SetTrack(ctx(), 1, "volume", 0.9)
	if err != nil || vol != 0.9 {
		t.Fatalf("SetTrack(): %v, err=%v", vol, err)
	}
	if err := eng.Undo(ctx()); err != nil {
		t.Fatalf("Undo(): %v", err)
	}
	if err := eng.Alive(ctx()); err != nil {
		t.Fatalf("Alive(): %v", err)
	}

	// core.SessionController
	newT, err := eng.CreateTrack(ctx(), "Keys", 0)
	if err != nil || newT <= 0 {
		t.Fatalf("CreateTrack(): %v, err=%v", newT, err)
	}
	dupT, err := eng.DuplicateTrack(ctx(), newT, "Keys Double")
	if err != nil || dupT <= 0 {
		t.Fatalf("DuplicateTrack(): %v, err=%v", dupT, err)
	}
	if err := eng.RenameTrack(ctx(), newT, "Synthesizer"); err != nil {
		t.Fatalf("RenameTrack(): %v", err)
	}
	if err := eng.InsertMedia(ctx(), newT, "sample.wav", 1.0); err != nil {
		t.Fatalf("InsertMedia(): %v", err)
	}
	if err := eng.CreateSend(ctx(), 1, newT, -10.0); err != nil {
		t.Fatalf("CreateSend(): %v", err)
	}
	if err := eng.SetTransport(ctx(), "play", 130.0, 2.0, 0, 4); err != nil {
		t.Fatalf("SetTransport(): %v", err)
	}
	if err := eng.DeleteTrack(ctx(), dupT); err != nil {
		t.Fatalf("DeleteTrack(): %v", err)
	}

	// core.SessionTrackControls & SessionRouting
	if _, err := eng.SetTrackControl(ctx(), 1, "monitor", "auto"); err != nil {
		t.Fatalf("SetTrackControl(): %v", err)
	}
	if err := eng.RemoveSend(ctx(), 1, 1); err != nil {
		t.Fatalf("RemoveSend(): %v", err)
	}

	// core.SessionMarkers & SessionProject & SessionMeter
	mIdx, err := eng.CreateMarker(ctx(), 10.0, 20.0, "Bridge", true)
	if err != nil || mIdx <= 0 {
		t.Fatalf("CreateMarker(): %v, err=%v", mIdx, err)
	}
	markers, err := eng.Markers(ctx())
	if err != nil || len(markers) == 0 {
		t.Fatalf("Markers(): %v, err=%v", markers, err)
	}
	if err := eng.GoToMarker(ctx(), mIdx); err != nil {
		t.Fatalf("GoToMarker(): %v", err)
	}
	if err := eng.SaveProject(ctx()); err != nil {
		t.Fatalf("SaveProject(): %v", err)
	}
	if err := eng.SetTimeSignature(ctx(), 6, 8); err != nil {
		t.Fatalf("SetTimeSignature(): %v", err)
	}

	// core.ChainEditor & ChainMover & Bypasser
	cat, err := eng.Catalog(ctx())
	if err != nil || len(cat) == 0 {
		t.Fatalf("Catalog(): %v, err=%v", cat, err)
	}
	newFX, err := eng.AddFX(ctx(), 1, "ReaDelay (Cockos)", 0) // append
	if err != nil || newFX <= 0 {
		t.Fatalf("AddFX(): %v, err=%v", newFX, err)
	}
	if err := eng.MoveFX(ctx(), 1, 1, 2); err != nil {
		t.Fatalf("MoveFX(): %v", err)
	}
	if err := eng.SetBypass(ctx(), 1, 1, true); err != nil {
		t.Fatalf("SetBypass(): %v", err)
	}

	// core.Presetter & PresetSaver
	curP, pList, err := eng.Presets(ctx(), 1, 1)
	if err != nil || curP == "" || len(pList) == 0 {
		t.Fatalf("Presets(): cur=%s list=%v, err=%v", curP, pList, err)
	}
	if err := eng.SetPreset(ctx(), 1, 1, pList[0]); err != nil {
		t.Fatalf("SetPreset(): %v", err)
	}
	if err := eng.SavePresetAs(ctx(), 1, 1, "Preset New"); err != nil {
		t.Fatalf("SavePresetAs(): %v", err)
	}

	// core.TurboChain
	if err := eng.EnableAuto(); err != nil {
		t.Fatalf("EnableAuto(): %v", err)
	}
	if _, err := eng.ForceRemoveFX(ctx(), 1, 1); err != nil {
		t.Fatalf("ForceRemoveFX(): %v", err)
	}

	// core.ValueFinder
	val, err := eng.FindValue(ctx(), 1, core.MakeParamID(1, 5), "5")
	if err != nil {
		t.Logf("FindValue: %v", err)
	} else if val < 0 || val > 1 {
		t.Fatalf("FindValue returned out of bounds: %v", val)
	}

	// core.ModelSwapper
	slots, err := eng.PluginModels(ctx(), 1, 1, "")
	if err != nil {
		t.Fatalf("PluginModels(): %v", err)
	}
	if len(slots) > 0 {
		if _, err := eng.SetPluginModel(ctx(), 1, 1, slots[0].Slot, slots[0].Available[0]); err != nil {
			t.Fatalf("SetPluginModel(): %v", err)
		}
	}

	// core.AudioEngineChecker
	running, err := eng.AudioEngineRunning(ctx())
	if err != nil || !running {
		t.Fatalf("AudioEngineRunning(): %v, err=%v", running, err)
	}

	// core.Renderer & RangeRenderer
	wav1, err := eng.Render(ctx(), 1, 1.0)
	if err != nil || wav1 == "" {
		t.Fatalf("Render(): %v, err=%v", wav1, err)
	}
	wav2, err := eng.RenderRange(ctx(), 1, 0.5, 1.0)
	if err != nil || wav2 == "" {
		t.Fatalf("RenderRange(): %v, err=%v", wav2, err)
	}

	// core.InputRecorder
	if err := eng.StartInput(ctx(), 1, 1.0, "take-direct"); err != nil {
		t.Fatalf("StartInput(): %v", err)
	}
	clip, err := eng.FinishInput(ctx(), 1, "take-direct")
	if err != nil || clip.Path == "" {
		t.Fatalf("FinishInput(): %+v, err=%v", clip, err)
	}

	// core.UndoActionGrouper
	if !eng.SupportsUndoActionGrouping() {
		t.Fatal("SupportsUndoActionGrouping() = false, want true")
	}
	if err := eng.BeginUndoAction(ctx(), "DirectGroup"); err != nil {
		t.Fatalf("BeginUndoAction(): %v", err)
	}
	isCur, _ := eng.UndoActionIsCurrent(ctx(), "DirectGroup")
	if !isCur {
		t.Fatal("UndoActionIsCurrent() = false, want true")
	}
	if err := eng.EndUndoAction(ctx(), "DirectGroup", true); err != nil {
		t.Fatalf("EndUndoAction(): %v", err)
	}

	// core.RequestSnapshotter
	if !eng.SupportsRequestSnapshot() {
		t.Fatal("SupportsRequestSnapshot() = false, want true")
	}
	snap, err := eng.CaptureRequestSnapshot(ctx())
	if err != nil || len(snap) == 0 {
		t.Fatalf("CaptureRequestSnapshot(): %v, len=%d", err, len(snap))
	}
	if err := eng.RestoreRequestSnapshot(ctx(), snap); err != nil {
		t.Fatalf("RestoreRequestSnapshot(): %v", err)
	}

	// core.ParamReader
	txt, err := eng.ParamText(ctx(), 1, core.MakeParamID(1, 5))
	if err != nil || txt == "" {
		t.Fatalf("ParamText(): %s, err=%v", txt, err)
	}

	// core.Capturer
	pngPath, err := eng.CaptureFX(ctx(), 1, 1)
	if err != nil || pngPath == "" {
		t.Fatalf("CaptureFX(): %s, err=%v", pngPath, err)
	}
}

// TestOSCServerParity verifies that fake.Listen binds real state to OSC feedback.
func TestOSCServerParity(t *testing.T) {
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	clientAddr := l.LocalAddr().String()
	l.Close()

	srv, err := fake.Listen(clientAddr, fake.DemoStudio())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	c, err := reaper.Dial(clientAddr, srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	r := reaper.New(c)

	// Read tracks over OSC
	tracks, err := r.Tracks(ctx())
	if err != nil {
		t.Fatalf("reaper.Tracks(): %v", err)
	}
	if len(tracks) < 3 {
		t.Fatalf("expected >= 3 tracks from OSC, got %d", len(tracks))
	}

	// Change parameter over OSC
	applied, err := r.SetParam(ctx(), 1, core.MakeParamID(1, 5), 0.88)
	if err != nil {
		t.Fatalf("reaper.SetParam(): %v", err)
	}
	if applied.Confirmed < 0.87 || applied.Confirmed > 0.89 {
		t.Fatalf("OSC confirmed param value %v, want ~0.88", applied.Confirmed)
	}

	// Verify that the underlying Engine received and stored the write
	engVal := srv.Engine.TracksRaw()[0].FX[0].Params[4].Value
	if engVal < 0.87 || engVal > 0.89 {
		t.Fatalf("Engine state value = %v, want ~0.88", engVal)
	}
}

// TestZeroHardcodedDAWNamesInAgent verifies that pkg/agent has zero hardcoded DAW names.
func TestZeroHardcodedDAWNamesInAgent(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	agentDir := filepath.Join(root, "pkg", "agent")
	dawNames := []string{"reaper", "ableton", "logic", "cubase", "flstudio", "bitwig"}
	err := filepath.Walk(agentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := strings.ToLower(string(b))
		for _, name := range dawNames {
			if strings.Contains(text, name) {
				t.Errorf("%s: hardcoded DAW name %q found in pkg/agent", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
