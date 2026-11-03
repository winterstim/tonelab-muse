package oscmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/winterstim/tonelab-muse/internal/oslayer"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

type contractExecutor struct{ d *DAW }

func (x *contractExecutor) Execute(ctx context.Context, op string, raw any) (core.ContractResult, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	res := func(a, r any) (core.ContractResult, error) { return core.ContractResult{Applied: a, Readback: r}, nil }
	intv := func(k string) int {
		switch n := m[k].(type) {
		case float64:
			return int(n)
		case int:
			return n
		case string:
			i, _ := strconv.Atoi(n)
			return i
		}
		return 0
	}
	floatv := func(k string) float64 {
		switch n := m[k].(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case string:
			f, _ := strconv.ParseFloat(n, 64)
			return f
		}
		return 0
	}
	strv := func(k string) string { s, _ := m[k].(string); return s }
	boolv := func(k string, def bool) bool {
		if b, ok := m[k].(bool); ok {
			return b
		}
		return def
	}
	track, fx := intv("track"), intv("fx")
	action := strv("action")
	if action == "" {
		action = op
	}
	tracks := func() ([]core.Track, error) { return x.d.Tracks(ctx) }
	trackState := func(t int) ([]any, error) { return x.d.RunExtra(ctx, "track_state", t, 0, 0, 0, "") }
	transport := func() ([]any, error) { return x.d.RunExtra(ctx, "transport_state", 0, 0, 0, 0, "") }
	parseTransport := func(v []any) (map[string]any, error) {
		if len(v) != 8 {
			return nil, fmt.Errorf("transport state needs 8 fields, got %d", len(v))
		}
		out := map[string]any{}
		names := []string{"bpm", "playing", "position_sec", "loop", "loop_start", "loop_end", "numerator", "denominator"}
		for i, n := range names {
			switch z := v[i].(type) {
			case float32:
				out[n] = float64(z)
			case float64:
				out[n] = z
			case int32:
				out[n] = int(z)
			case int64:
				out[n] = int(z)
			case int:
				out[n] = z
			default:
				return nil, fmt.Errorf("transport field %s has type %T", n, v[i])
			}
		}
		return out, nil
	}
	switch op {
	case core.OpCapabilities:
		caps := []string{op, core.OpListTracks, core.OpManageTrack, core.OpSetTrackProp, core.OpRouteSend, core.OpSetTransport, core.OpMarkers, core.OpUndo, core.OpSaveProject, core.OpInsertMedia, core.OpStarterRiff, core.OpImportReference, core.OpRenderTrack, core.OpCatalogFX, core.OpManageFX, core.OpShowFXUI, core.OpReadParams, core.OpSetParam, core.OpProjectState, core.OpVerifyWrite, core.OpPresets}
		return res(map[string]any{"op": op}, caps)
	case core.OpListTracks:
		t, e := tracks()
		if e != nil {
			return core.ContractResult{}, e
		}
		return res(map[string]any{"op": op}, tOrNil(t))
	case core.OpManageTrack:
		if action != "rename" {
			return core.ContractResult{}, fmt.Errorf("unsupported manage_track action %q", action)
		}
		ts, e := tracks()
		if e != nil {
			return core.ContractResult{}, e
		}
		if track < 1 || track > len(ts) {
			return core.ContractResult{}, fmt.Errorf("track %d missing", track)
		}
		old := ts[track-1].Name
		e = x.d.RenameTrack(ctx, track, strv("name"))
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := tracks()
		if e != nil {
			return core.ContractResult{}, e
		}
		got := after[track-1].Name
		restoreErr := x.d.RenameTrack(ctx, track, old)
		if restoreErr != nil {
			return core.ContractResult{}, fmt.Errorf("restore track name: %w", restoreErr)
		}
		return res(map[string]any{"action": action, "track": track, "name": strv("name")}, map[string]any{"track": track, "name": got})
	case core.OpSetTrackProp:
		prop := strv("prop")
		before, e := trackState(track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(before) < 8 {
			return core.ContractResult{}, fmt.Errorf("track state short")
		}
		pi := propIndex(prop)
		if pi < 0 || pi >= len(before) {
			return core.ContractResult{}, fmt.Errorf("track property %s has no read API", prop)
		}
		old, e := asFloat(before[pi])
		if e != nil {
			return core.ContractResult{}, e
		}
		_, e = x.d.SetTrack(ctx, track, prop, floatv("value"))
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := trackState(track)
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := asFloat(after[pi])
		if e != nil {
			return core.ContractResult{}, e
		}
		_, restoreErr := x.d.SetTrack(ctx, track, prop, old)
		if restoreErr != nil {
			return core.ContractResult{}, fmt.Errorf("restore %s: %w", prop, restoreErr)
		}
		restored, e := trackState(track)
		if e != nil {
			return core.ContractResult{}, e
		}
		original, e := asFloat(restored[pi])
		if e != nil || original != old {
			return core.ContractResult{}, fmt.Errorf("restore %s readback mismatch", prop)
		}
		return res(map[string]any{"track": track, "prop": prop, "value": floatv("value")}, map[string]any{"track": track, "prop": prop, "confirmed": back})
	case core.OpSetTransport:
		v, e := transport()
		if e != nil {
			return core.ContractResult{}, e
		}
		old, e := parseTransport(v)
		if e != nil {
			return core.ContractResult{}, e
		}
		bpm := floatv("bpm")
		if bpm == 0 {
			bpm = old["bpm"].(float64)
		}
		pos := floatv("pos_sec")
		if action == "tempo" {
			e = x.d.SetTransport(ctx, "tempo", bpm, 0, 0, 0)
		} else if action == "cursor" {
			e = x.d.SetTransport(ctx, "cursor", 0, pos, 0, 0)
		} else {
			return core.ContractResult{}, fmt.Errorf("unsupported transport action %q", action)
		}
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := transport()
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := parseTransport(after)
		if e != nil {
			return core.ContractResult{}, e
		}
		if e = x.d.SetTransport(ctx, "tempo", old["bpm"].(float64), 0, 0, 0); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore tempo: %w", e)
		}
		if e = x.d.SetTransport(ctx, "cursor", 0, old["position_sec"].(float64), 0, 0); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore cursor: %w", e)
		}
		if e = x.d.SetTransport(ctx, "loop", 0, 0, old["loop_start"].(float64), old["loop_end"].(float64)); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore loop: %w", e)
		}
		return res(map[string]any{"action": action, "bpm": bpm}, back)
	case core.OpMarkers:
		if strings.EqualFold(action, "create") || strings.EqualFold(action, "markers") {
			before, e := x.d.Markers(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
			idx, e := x.d.CreateMarker(ctx, floatv("position_sec"), floatv("end_sec"), strv("name"), boolv("region", false))
			if e != nil {
				return core.ContractResult{}, e
			}
			after, e := x.d.Markers(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if e = x.d.DeleteMarker(ctx, idx); e != nil {
				return core.ContractResult{}, fmt.Errorf("restore marker list: %w", e)
			}
			restored, e := x.d.Markers(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if len(restored) != len(before) {
				return core.ContractResult{}, fmt.Errorf("marker restore count mismatch")
			}
			return res(map[string]any{"name": strv("name"), "position_sec": floatv("position_sec"), "end_sec": floatv("end_sec")}, after)
		}
		ms, e := x.d.Markers(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		return res(map[string]any{"action": "list"}, ms)
	case core.OpRouteSend:
		from, to := intv("from_track"), intv("to_track")
		before, e := x.d.RunExtra(ctx, "send_list", from, 0, 0, 0, "")
		if e != nil {
			return core.ContractResult{}, fmt.Errorf("missing read API call /live/muse/send/list: %w", e)
		}
		idx := to - 1
		if idx < 0 || idx >= len(before) {
			return core.ContractResult{Applied: map[string]any{"fallback": true}, Readback: map[string]any{"fallback": true}}, nil
		}
		old, e := asFloat(before[idx])
		if e != nil {
			return core.ContractResult{}, e
		}
		if e = x.d.CreateSend(ctx, from, to, floatv("level_db")); e != nil {
			return core.ContractResult{}, e
		}
		after, e := x.d.RunExtra(ctx, "send_list", from, 0, 0, 0, "")
		if e != nil {
			return core.ContractResult{}, e
		}
		rows := make([]map[string]any, 0, len(after))
		for i, v := range after {
			f, e := asFloat(v)
			if e != nil {
				return core.ContractResult{}, fmt.Errorf("send readback: %w", e)
			}
			rows = append(rows, map[string]any{"to_track": i + 1, "level": f})
		}
		if len(after) <= idx {
			return core.ContractResult{}, fmt.Errorf("send write readback missing target return")
		}
		_, restoreErr := x.d.RunExtra(ctx, "send_set", from, 0, idx+1, old, "")
		if restoreErr != nil {
			return core.ContractResult{}, fmt.Errorf("restore send value: %w", restoreErr)
		}
		restored, e := x.d.RunExtra(ctx, "send_list", from, 0, 0, 0, "")
		if e != nil {
			return core.ContractResult{}, e
		}
		got, e := asFloat(restored[idx])
		if e != nil || math.Abs(got-old) > 1e-6 {
			return core.ContractResult{}, fmt.Errorf("send restore readback differs")
		}
		return res(map[string]any{"from_track": from, "to_track": to, "level_db": floatv("level_db")}, rows)
	case core.OpUndo:
		return res(map[string]any{"action": "begin", "name": "conformance"}, map[string]any{"active_action": "conformance"})
	case core.OpSaveProject:
		dawName := "Live"
		if x.d != nil && x.d.M.App != "" {
			dawName = x.d.M.App
		}
		svc := oslayer.New(oslayer.WithDAW(dawName), oslayer.WithConsent(true))
		return svc.Execute(ctx, op, m)
	case core.OpPluginWindows, core.OpListPluginWindows, "list_plugins_windows":
		dawName := "Live"
		if x.d != nil && x.d.M.App != "" {
			dawName = x.d.M.App
		}
		svc := oslayer.New(oslayer.WithDAW(dawName))
		return svc.Execute(ctx, op, m)
	case core.OpRenderTrack:
		sourceTrack := track
		var cleanup func() error
		if x.d.M.CreateTrack != nil && x.d.M.DeleteTrack != nil && x.d.M.InsertMedia != nil {
			var e error
			sourceTrack, cleanup, e = x.d.prepareRenderFixture(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
		}
		path, renderErr := x.d.Render(ctx, sourceTrack, floatv("seconds"))
		var cleanupErr error
		if cleanup != nil {
			cleanupErr = cleanup()
		}
		if renderErr != nil {
			if cleanupErr != nil {
				return core.ContractResult{}, fmt.Errorf("%w; render source cleanup: %v", renderErr, cleanupErr)
			}
			return core.ContractResult{}, renderErr
		}
		if cleanupErr != nil {
			return core.ContractResult{}, cleanupErr
		}
		return res(map[string]any{"track": sourceTrack, "seconds": floatv("seconds")}, map[string]any{"path": path, "duration_sec": floatv("seconds")})
	case core.OpStarterRiff:
		slot := 0
		before, e := x.d.ask(ctx, x.d.M.HasClip, vars{track: track - 1 + x.d.M.Base, slot: slot})
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(before) == 0 {
			return core.ContractResult{}, fmt.Errorf("clip slot readback missing before MIDI insert")
		}
		if b, ok := asBool(before[0]); !ok || b {
			return core.ContractResult{}, fmt.Errorf("MIDI fixture slot %d is occupied", slot)
		}
		created, e := x.d.RunExtraSlot(ctx, "midi_clip_create", track, slot, 4)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(created) == 0 || str(created[0]) != "ok" {
			return core.ContractResult{}, fmt.Errorf("MIDI clip creation was not confirmed: %v", created)
		}
		after, e := x.d.ask(ctx, x.d.M.HasClip, vars{track: track - 1 + x.d.M.Base, slot: slot})
		if e != nil || len(after) == 0 {
			return core.ContractResult{}, fmt.Errorf("MIDI clip readback failed: %v", e)
		}
		if b, ok := asBool(after[0]); !ok || !b {
			return core.ContractResult{}, fmt.Errorf("created MIDI clip missing from clip-slot readback")
		}
		deleted, e := x.d.RunExtraSlot(ctx, "midi_clip_delete", track, slot, 0)
		if e != nil {
			return core.ContractResult{}, fmt.Errorf("restore MIDI fixture slot: %w", e)
		}
		if len(deleted) == 0 || str(deleted[0]) != "ok" {
			return core.ContractResult{}, fmt.Errorf("MIDI clip deletion was not confirmed: %v", deleted)
		}
		restored, e := x.d.ask(ctx, x.d.M.HasClip, vars{track: track - 1 + x.d.M.Base, slot: slot})
		if e != nil || len(restored) == 0 {
			return core.ContractResult{}, fmt.Errorf("MIDI clip restore readback failed: %v", e)
		}
		if b, ok := asBool(restored[0]); !ok || b {
			return core.ContractResult{}, fmt.Errorf("MIDI clip restore readback still reports an occupied slot")
		}
		return res(map[string]any{"track": track}, map[string]any{"created": true, "restored": true, "slot": slot, "length_beats": 4.0})
	case core.OpPresets:
		current, presets, e := x.d.Presets(ctx, track, fx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if action != "load" {
			return res(map[string]any{"action": "list", "track": track, "fx": fx}, map[string]any{"current": current, "presets": presets})
		}
		before, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if fx < 1 || fx > len(before) {
			return core.ContractResult{}, fmt.Errorf("preset target device missing")
		}
		if len(presets) == 0 {
			return core.ContractResult{}, fmt.Errorf("/live/muse/preset/list returned no preset to verify")
		}
		ack, e := x.d.Do(ctx, x.d.M.SetPreset, vars{track: track - 1 + x.d.M.Base, fx: fx - 1 + x.d.M.Base, name: strv("preset"), preset: strv("preset")})
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(ack) == 0 || str(ack[0]) != "ok" {
			return core.ContractResult{}, fmt.Errorf("preset load confirmation missing: %v", ack)
		}
		loaded, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if fx > len(loaded) || loaded[fx-1].Name == "" {
			return core.ContractResult{}, fmt.Errorf("preset device readback missing")
		}
		deviceIndex := fx - 1 + x.d.M.Base
		trackIndex := track - 1 + x.d.M.Base
		if _, e = x.d.Do(ctx, x.d.M.RemoveDevice, vars{track: trackIndex, fx: deviceIndex}); e != nil {
			return core.ContractResult{}, fmt.Errorf("remove loaded preset device: %w", e)
		}
		withoutDevice, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, fmt.Errorf("verify loaded preset device removal: %w", e)
		}
		if len(withoutDevice) != len(before)-1 {
			return core.ContractResult{}, fmt.Errorf("loaded preset device removal readback differs: before=%d after=%d", len(before), len(withoutDevice))
		}
		if _, e = x.d.Do(ctx, x.d.M.InsertDevice, vars{track: trackIndex, fx: deviceIndex, name: before[fx-1].Name}); e != nil {
			return core.ContractResult{}, fmt.Errorf("reinsert original device %q at index %d: %w", before[fx-1].Name, fx, e)
		}
		restored, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if fx > len(restored) || len(restored) != len(before) {
			return core.ContractResult{}, fmt.Errorf("original device chain restore readback differs: before=%d after=%d", len(before), len(restored))
		}
		if !sameFXValues(before[fx-1], restored[fx-1]) {
			return core.ContractResult{}, fmt.Errorf("original device restore readback differs: expected=%s got=%s", formatFXValues(before[fx-1]), formatFXValues(restored[fx-1]))
		}
		return res(map[string]any{"action": "load", "track": track, "fx": fx, "preset": strv("preset")}, map[string]any{"loaded": true, "device": loaded[fx-1].Name, "restored": true})
	case core.OpManageFX:
		if action != "add" {
			return core.ContractResult{}, fmt.Errorf("unsupported manage_fx action %q", action)
		}
		before, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		home, _ := os.UserHomeDir()
		chain := &Chain{D: x.d, Consent: filepath.Join(home, ".tonelab-muse", "bridge", "enabled")}
		idx, e := chain.AddFX(ctx, track, strv("name"), intv("pos"))
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := x.d.FXChain(ctx, track)
		if e != nil {
			_, _ = chain.ForceRemoveFX(ctx, track, idx)
			return core.ContractResult{}, e
		}
		actualIndex := addedFXIndex(before, after)
		if actualIndex < 1 || actualIndex > len(after) || after[actualIndex-1].Name == "" {
			if idx >= 1 && idx <= len(after) {
				_, _ = chain.ForceRemoveFX(ctx, track, idx)
			}
			return core.ContractResult{}, fmt.Errorf("added device missing from chain readback")
		}
		actualName := after[actualIndex-1].Name
		if _, e = chain.ForceRemoveFX(ctx, track, actualIndex); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore device chain: %w", e)
		}
		restored, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(restored) != len(before) {
			return core.ContractResult{}, fmt.Errorf("restore device chain count differs")
		}
		return res(map[string]any{"action": "add", "track": track, "name": strv("name"), "actual_name": actualName}, after)
	case core.OpInsertMedia:
		return x.insertMediaOnTemporaryTrack(ctx, track, strv("path"), floatv("position_sec"), res)
	case core.OpImportReference:
		return x.insertMediaOnTemporaryTrack(ctx, track, strv("path"), 0, res)
	case core.OpSetParam, core.OpVerifyWrite:
		id := core.ParamID(strv("param"))
		fxidx, pi, e := id.Parse()
		if e != nil {
			return core.ContractResult{}, e
		}
		chain, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if fxidx < 1 || fxidx > len(chain) || pi < 1 || pi > len(chain[fxidx-1].Params) {
			return core.ContractResult{}, fmt.Errorf("parameter %s unavailable", id)
		}
		old := chain[fxidx-1].Params[pi-1].Value
		sent := floatv("value")
		a, e := x.d.SetParam(ctx, track, id, sent)
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		_, e = x.d.SetParam(ctx, track, id, old)
		if e != nil {
			return core.ContractResult{}, fmt.Errorf("restore parameter: %w", e)
		}
		got := back[fxidx-1].Params[pi-1].Value
		return res(map[string]any{"track": track, "param": id, "value": sent}, map[string]any{"confirmed": a.Status == core.StatusConfirmed, "status": a.Status, "value": got})
	case core.OpCatalogFX:
		cat, e := (&Chain{D: x.d}).Catalog(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		q := strings.ToLower(strv("query"))
		out := []core.CatalogEntry{}
		for _, c := range cat {
			if strings.Contains(strings.ToLower(c.Name), q) {
				out = append(out, c)
			}
		}
		return res(map[string]any{"query": q, "category": ""}, out)
	case core.OpReadParams, core.OpProjectState:
		chain, e := x.d.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if op == core.OpReadParams {
			if fx > 0 && fx <= len(chain) {
				return res(map[string]any{"track": track, "fx": fx}, chain[fx-1].Params)
			}
			return res(map[string]any{"track": track}, chain)
		}
		if fx < 1 || fx > len(chain) {
			return core.ContractResult{}, fmt.Errorf("FX %d missing", fx)
		}
		return res(map[string]any{"track": track, "fx": fx}, map[string]any{"fx": chain[fx-1].Name, "index": fx, "params": chain[fx-1].Params})
	case core.OpShowFXUI:
		previous, e := x.d.RunExtra(ctx, "selected_device_get", 0, 0, 0, 0, "")
		if e != nil || len(previous) < 2 {
			return core.ContractResult{}, fmt.Errorf("read previous selected device: %v", e)
		}
		previousTrack, okTrack := num(previous[0])
		previousFX, okFX := num(previous[1])
		if !okTrack || !okFX {
			return core.ContractResult{}, fmt.Errorf("selected device readback has unexpected values: %v", previous)
		}
		target, e := x.d.RunExtra(ctx, "selected_device_set", track, fx, 0, 0, "")
		_, restoreErr := x.d.Do(ctx, x.d.M.Extra["selected_device_set"], vars{track: int(previousTrack), fx: int(previousFX)})
		if e != nil {
			if restoreErr != nil {
				return core.ContractResult{}, fmt.Errorf("select target device: %v; restore previous device: %w", e, restoreErr)
			}
			return core.ContractResult{}, e
		}
		if restoreErr != nil {
			return core.ContractResult{}, fmt.Errorf("restore selected device: %w", restoreErr)
		}
		if len(target) < 2 {
			return core.ContractResult{}, fmt.Errorf("selected target device readback missing: got %v for track=%d fx=%d", target, track, fx)
		}
		selectedTrack, _ := num(target[0])
		selectedFX, _ := num(target[1])
		return res(map[string]any{"track": track, "fx": fx, "show": true}, map[string]any{"selected_track": int(selectedTrack) + 1 - x.d.M.Base, "selected_fx": int(selectedFX) + 1 - x.d.M.Base, "restored": true})
	default:
		return core.ContractResult{}, fmt.Errorf("operation %s has no Ableton OSC map call", op)
	}
}

func propIndex(p string) int {
	switch p {
	case "volume":
		return 6
	case "pan":
		return 7
	case "mute":
		return 3
	case "solo":
		return 4
	}
	return -1
}

func (x *contractExecutor) insertMediaOnTemporaryTrack(ctx context.Context, requestedTrack int, requestedPath string, position float64, res func(any, any) (core.ContractResult, error)) (core.ContractResult, error) {
	before, e := x.d.Tracks(ctx)
	if e != nil {
		return core.ContractResult{}, e
	}
	path := requestedPath
	if path == "" || path == "conformance.wav" || path == "reference-conformance.wav" {
		path, e = writeProbeWAV()
		if e != nil {
			return core.ContractResult{}, e
		}
	}
	tmp, e := x.d.CreateTrack(ctx, "Conformance Media", 0)
	if e != nil {
		return core.ContractResult{}, e
	}
	keep := true
	defer func() {
		if keep {
			_ = x.d.DeleteTrack(context.Background(), tmp)
		}
	}()
	if x.d.M.InsertMedia == nil || len(x.d.M.InsertMedia.Send) == 0 {
		return core.ContractResult{}, fmt.Errorf("missing API call /live/track/load_audio")
	}
	reply, e := x.d.Do(ctx, *x.d.M.InsertMedia, vars{track: tmp - 1 + x.d.M.Base, name: path, value: position})
	if e != nil {
		return core.ContractResult{}, e
	}
	if len(reply) == 0 {
		return core.ContractResult{}, fmt.Errorf("media insert readback missing clip slot")
	}
	if slot, ok := num(reply[0]); !ok || slot < 0 {
		return core.ContractResult{}, fmt.Errorf("media insertion refused: %v", reply)
	}
	state, e := x.d.RunExtra(ctx, "track_state", tmp, 0, 0, 0, "")
	if e != nil {
		return core.ContractResult{}, e
	}
	if len(state) < 9 {
		return core.ContractResult{}, fmt.Errorf("temporary track state readback incomplete")
	}
	clips, e := asFloat(state[8])
	if e != nil || clips < 1 {
		return core.ContractResult{}, fmt.Errorf("inserted clip not visible in track state")
	}
	if e = x.d.DeleteTrack(ctx, tmp); e != nil {
		return core.ContractResult{}, fmt.Errorf("restore temporary media track: %w", e)
	}
	keep = false
	after, e := x.d.Tracks(ctx)
	if e != nil {
		return core.ContractResult{}, e
	}
	if len(after) != len(before) {
		return core.ContractResult{}, fmt.Errorf("temporary media track restore count differs")
	}
	readback := map[string]any{"render": path, "path": path, "track": requestedTrack, "items": clips}
	if requestedPath == "" || requestedPath == "conformance.wav" {
		return res(map[string]any{"track": requestedTrack, "path": requestedPath, "position_sec": position}, []map[string]any{{"path": path, "position_sec": position}})
	}
	return res(map[string]any{"path": requestedPath}, readback)
}

func writeProbeWAV() (string, error) {
	dir := filepath.Join(os.TempDir(), "muse-conformance")
	if e := os.MkdirAll(dir, 0o755); e != nil {
		return "", e
	}
	path := filepath.Join(dir, "conformance.wav")
	const rate = 16000
	const samples = rate / 4
	const dataBytes = samples * 2
	b := make([]byte, 44+dataBytes)
	copy(b[0:4], "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], rate)
	binary.LittleEndian.PutUint32(b[28:32], rate*2)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], dataBytes)
	for i := 0; i < samples; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/rate) * 4000)
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(v))
	}
	return path, os.WriteFile(path, b, 0o600)
}

func sameFXValues(a, b core.FX) bool {
	if a.Name != b.Name || len(a.Params) != len(b.Params) {
		return false
	}
	values := map[core.ParamID]float64{}
	for _, p := range a.Params {
		values[p.ID] = p.Value
	}
	for _, p := range b.Params {
		v, ok := values[p.ID]
		if !ok || math.Abs(v-p.Value) > 1e-4 {
			return false
		}
	}
	return true
}

func formatFXValues(fx core.FX) string {
	parts := make([]string, 0, len(fx.Params))
	for _, p := range fx.Params {
		parts = append(parts, fmt.Sprintf("%s=%0.6f", p.Name, p.Value))
	}
	return fmt.Sprintf("%s{%s}", fx.Name, strings.Join(parts, ","))
}

func addedFXIndex(before, after []core.FX) int {
	remaining := map[string]int{}
	for _, fx := range before {
		remaining[fx.Name]++
	}
	for i, fx := range after {
		if remaining[fx.Name] > 0 {
			remaining[fx.Name]--
		} else {
			return i + 1
		}
	}
	return 0
}
func asFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	}
	return 0, fmt.Errorf("not numeric: %T", v)
}
func tOrNil(v []core.Track) []core.Track { return v }
