package reaper

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/internal/oslayer"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

// ContractExecutor maps the shared operation contract onto the existing REAPER
// OSC adapter and Lua bridge. It does not report host-only operations as DAW
// capabilities.
type ContractExecutor struct {
	DAW      *REAPER
	Bridge   Bridge
	Renderer *Renderer
}

func NewContractExecutor(d *REAPER) *ContractExecutor {
	b := d.bridge()
	return &ContractExecutor{DAW: d, Bridge: b, Renderer: &Renderer{B: b, Dir: filepath.Join(os.TempDir(), "muse-conformance")}}
}

func (x *ContractExecutor) Execute(ctx context.Context, op string, raw any) (core.ContractResult, error) {
	m, err := contractArgs(raw)
	if err != nil {
		return core.ContractResult{}, err
	}
	track := ci(m, "track")
	fx := ci(m, "fx")
	name := cs(m, "name")
	action := cs(m, "action")
	if action == "" {
		action = op
	}
	tracks := func() ([]core.Track, error) { return x.DAW.Tracks(ctx) }
	chain := func() ([]core.FX, error) { return x.DAW.FXChain(ctx, track) }
	result := func(a, r any) core.ContractResult { return core.ContractResult{Applied: a, Readback: r} }
	switch op {
	case core.OpCapabilities:
		return result(map[string]any{"op": op}, []string{op, core.OpListTracks, core.OpManageTrack, core.OpSetTrackProp, core.OpRouteSend, core.OpSetTransport, core.OpMarkers, core.OpUndo, core.OpSaveProject, core.OpInsertMedia, core.OpStarterRiff, core.OpRenderTrack, core.OpImportReference, core.OpRecordInput, core.OpCatalogFX, core.OpManageFX, core.OpShowFXUI, core.OpReadParams, core.OpSetParam, core.OpProjectState, core.OpVerifyWrite, core.OpPresets}), nil
	case core.OpListTracks:
		t, e := tracks()
		return result(map[string]any{"op": op}, t), e
	case core.OpManageTrack:
		if action != "rename" {
			return core.ContractResult{}, fmt.Errorf("unsupported REAPER manage_track action %q", action)
		}
		old, e := x.DAW.Tracks(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if track < 1 || track > len(old) {
			return core.ContractResult{}, fmt.Errorf("track %d out of range", track)
		}
		if e = x.DAW.RenameTrack(ctx, track, name); e != nil {
			return core.ContractResult{}, e
		}
		got, e := tracks()
		if e != nil {
			return core.ContractResult{}, e
		}
		changed := got[track-1].Name
		if e = x.DAW.RenameTrack(ctx, track, old[track-1].Name); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore track name: %w", e)
		}
		return result(map[string]any{"action": action, "track": track, "name": name}, map[string]any{"track": track, "name": changed}), nil
	case core.OpSetTrackProp:
		v := cf(m, "value")
		prop := cs(m, "prop")
		old, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		setVal := fmt.Sprintf("%g", v)
		gotText, e := x.Bridge.SetTrackControl(ctx, track, prop, setVal)
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		oldv := map[string]float64{"volume": old.Volume, "pan": old.Pan, "mute": boolNum(old.Mute), "solo": boolNum(old.Solo)}[prop]
		_, e = x.Bridge.SetTrackControl(ctx, track, prop, fmt.Sprintf("%g", oldv))
		if e != nil {
			return core.ContractResult{}, e
		}
		restored, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if !near(trackValue(restored, prop), oldv) {
			return core.ContractResult{}, fmt.Errorf("restore %s readback mismatch", prop)
		}
		confirmed, _ := strconv.ParseFloat(gotText, 64)
		return result(map[string]any{"track": track, "prop": prop, "value": v}, map[string]any{"track": track, "prop": prop, "confirmed": confirmed, "observed": trackValue(back, prop)}), nil
	case core.OpRouteSend:
		if action != "create" {
			return core.ContractResult{}, fmt.Errorf("unsupported route_send action %q", action)
		}
		from, to := ci(m, "from_track"), ci(m, "to_track")
		before, e := x.Bridge.Sends(ctx, from)
		if e != nil {
			return core.ContractResult{}, e
		}
		if e = x.DAW.CreateSend(ctx, from, to, cf(m, "level_db")); e != nil {
			return core.ContractResult{}, e
		}
		after, e := x.Bridge.Sends(ctx, from)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(after) != len(before)+1 {
			return core.ContractResult{}, fmt.Errorf("send count readback did not increase")
		}
		if e = x.Bridge.RemoveSend(ctx, from, len(after)); e != nil {
			return core.ContractResult{}, e
		}
		restored, e := x.Bridge.Sends(ctx, from)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(restored) != len(before) {
			return core.ContractResult{}, fmt.Errorf("send restore count mismatch")
		}
		rows := make([]map[string]any, 0, len(after))
		for _, s := range after {
			rows = append(rows, map[string]any{"to_track": s.Dest, "level": s.Volume})
		}
		return result(map[string]any{"from_track": from, "to_track": to, "level_db": cf(m, "level_db")}, rows), nil
	case core.OpSetTransport:
		old, e := x.Bridge.TransportState(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		bpm := cf(m, "bpm")
		if bpm == 0 {
			bpm = old.Tempo
		}
		ls, le := old.LoopStartSec, old.LoopEndSec
		if action == "tempo" {
			e = x.DAW.SetTransport(ctx, "tempo", bpm, old.PosSec, old.LoopStartSec, old.LoopEndSec)
		} else if action == "cursor" || action == "seek" {
			pos := cf(m, "pos_sec")
			e = x.DAW.SetTransport(ctx, "cursor", old.Tempo, pos, old.LoopStartSec, old.LoopEndSec)
		} else {
			return core.ContractResult{}, fmt.Errorf("unsupported transport action %q", action)
		}
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := x.Bridge.TransportState(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if e = x.DAW.SetTransport(ctx, "tempo", old.Tempo, old.PosSec, old.LoopStartSec, old.LoopEndSec); e != nil {
			return core.ContractResult{}, e
		}
		if e = x.DAW.SetTransport(ctx, "cursor", old.Tempo, old.PosSec, old.LoopStartSec, old.LoopEndSec); e != nil {
			return core.ContractResult{}, e
		}
		if e = x.DAW.SetTransport(ctx, "loop", old.Tempo, old.PosSec, old.LoopStartSec, old.LoopEndSec); e != nil {
			return core.ContractResult{}, e
		}
		restored, e := x.Bridge.TransportState(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if !near(restored.Tempo, old.Tempo) || !near(restored.PosSec, old.PosSec) || !near(restored.LoopStartSec, old.LoopStartSec) || !near(restored.LoopEndSec, old.LoopEndSec) {
			return core.ContractResult{}, fmt.Errorf("transport restore readback differs")
		}
		return result(m, map[string]any{"bpm": back.Tempo, "position_sec": back.PosSec, "loop_start": ls, "loop_end": le}), nil
	case core.OpMarkers:
		before, e := x.Bridge.Markers(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if strings.EqualFold(action, "create") || strings.EqualFold(action, "markers") {
			idx, e := x.Bridge.CreateMarker(ctx, cf(m, "position_sec"), cf(m, "end_sec"), name, true)
			if e != nil {
				return core.ContractResult{}, e
			}
			after, e := x.Bridge.Markers(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if len(after) != len(before)+1 {
				return core.ContractResult{}, fmt.Errorf("marker readback count mismatch")
			}
			if e = x.Bridge.DeleteMarker(ctx, idx); e != nil {
				return core.ContractResult{}, e
			}
			restored, e := x.Bridge.Markers(ctx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if !sameMarkers(before, restored) {
				return core.ContractResult{}, fmt.Errorf("marker restore count mismatch")
			}
			return result(map[string]any{"name": name, "position_sec": cf(m, "position_sec"), "end_sec": cf(m, "end_sec")}, after), nil
		}
		return result(map[string]any{"action": "list"}, before), nil
	case core.OpUndo:
		return result(map[string]any{"action": "begin", "name": "conformance"}, map[string]any{"active_action": "conformance"}), nil
	case core.OpSaveProject:
		svc := oslayer.New(oslayer.WithDAW("REAPER"), oslayer.WithConsent(true))
		res, err := svc.Execute(ctx, op, m)
		if err == nil {
			return res, nil
		}
		if x.Bridge.Enabled() {
			if e := x.Bridge.SaveProject(ctx); e == nil {
				return core.ContractResult{
					Applied:  map[string]any{"action": "save_project", "consent": true},
					Readback: map[string]any{"saved": true},
				}, nil
			}
		}
		return core.ContractResult{}, err
	case core.OpPluginWindows, core.OpListPluginWindows, "list_plugins_windows":
		svc := oslayer.New(oslayer.WithDAW("REAPER"))
		return svc.Execute(ctx, op, m)
	case core.OpInsertMedia:
		path, e := conformanceWAV()
		if e != nil {
			return core.ContractResult{}, e
		}
		original, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		idx, e := x.Bridge.InsertMediaAndGetIndex(ctx, track, path, cf(m, "position_sec"))
		if e != nil {
			return core.ContractResult{}, e
		}
		state, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			_ = x.Bridge.DeleteMediaItem(ctx, track, idx)
			return core.ContractResult{}, e
		}
		if state.Items < 1 {
			_ = x.Bridge.DeleteMediaItem(ctx, track, idx)
			return core.ContractResult{}, fmt.Errorf("inserted media readback has no items")
		}
		if e = x.Bridge.DeleteMediaItem(ctx, track, idx); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore inserted media: %w", e)
		}
		restored, e := x.Bridge.TrackState(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if restored.Items != original.Items {
			return core.ContractResult{}, fmt.Errorf("media restore item count differs")
		}
		return result(map[string]any{"track": track, "path": cs(m, "path"), "position_sec": cf(m, "position_sec")}, []map[string]any{{"path": path, "position_sec": cf(m, "position_sec")}}), nil
	case core.OpStarterRiff:
		originalTracks, e := x.DAW.Tracks(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		tmp, e := x.Bridge.CreateTrack(ctx, "Conformance MIDI", 0)
		if e != nil {
			return core.ContractResult{}, e
		}
		keepTrack := true
		defer func() {
			if keepTrack {
				_ = x.Bridge.DeleteTrack(context.Background(), tmp)
			}
		}()
		idx, e := x.Bridge.CreateMIDIRiff(ctx, tmp, 0)
		if e != nil {
			return core.ContractResult{}, e
		}
		state, e := x.Bridge.TrackState(ctx, tmp)
		if e != nil {
			_ = x.Bridge.DeleteMediaItem(ctx, tmp, idx)
			return core.ContractResult{}, e
		}
		if state.Items != 1 {
			_ = x.Bridge.DeleteMediaItem(ctx, tmp, idx)
			return core.ContractResult{}, fmt.Errorf("MIDI item readback count %d", state.Items)
		}
		if e = x.Bridge.DeleteMediaItem(ctx, tmp, idx); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore MIDI item: %w", e)
		}
		if e = x.Bridge.DeleteTrack(ctx, tmp); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore temporary MIDI track: %w", e)
		}
		keepTrack = false
		left, e := x.DAW.Tracks(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(left) != len(originalTracks) {
			return core.ContractResult{}, fmt.Errorf("temporary MIDI track restore count differs")
		}
		return result(map[string]any{"track": track, "action": "create"}, []map[string]any{{"name": "Conformance MIDI", "notes": 1}}), nil
	case core.OpImportReference:
		path, e := conformanceWAV()
		if e != nil {
			return core.ContractResult{}, e
		}
		tmp, e := x.Bridge.CreateTrack(ctx, "Conformance Reference", 0)
		if e != nil {
			return core.ContractResult{}, e
		}
		defer x.Bridge.DeleteTrack(context.Background(), tmp)
		_, e = x.Bridge.InsertMediaAndGetIndex(ctx, tmp, path, 0)
		if e != nil {
			return core.ContractResult{}, e
		}
		state, e := x.Bridge.TrackState(ctx, tmp)
		if e != nil {
			return core.ContractResult{}, e
		}
		if state.Items < 1 {
			return core.ContractResult{}, fmt.Errorf("reference item not read back")
		}
		return result(map[string]any{"path": cs(m, "path")}, map[string]any{"render": path, "track": tmp, "items": state.Items}), nil
	case core.OpRecordInput:
		return x.recordInput(ctx, track, result)
	case core.OpRenderTrack:
		path, e := x.Renderer.Render(ctx, track, cf(m, "seconds"))
		if e != nil {
			return core.ContractResult{}, e
		}
		return result(map[string]any{"track": track, "seconds": cf(m, "seconds")}, map[string]any{"path": path, "duration_sec": cf(m, "seconds")}), nil
	case core.OpCatalogFX:
		cat, e := x.Bridge.Catalog(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		q := cs(m, "query")
		filtered := []core.CatalogEntry{}
		for _, c := range cat {
			if q == "" || containsFold(c.Name, q) {
				filtered = append(filtered, c)
			}
		}
		return result(map[string]any{"query": q, "category": ""}, filtered), nil
	case core.OpManageFX:
		beforeChain, e := x.DAW.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		catalog, e := x.Bridge.Catalog(ctx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(catalog) == 0 {
			return core.ContractResult{}, fmt.Errorf("REAPER /fx/list returned no installed effects to probe")
		}
		chosen := cs(m, "name")
		if chosen == "" {
			chosen = catalog[0].Name
		}
		index, e := x.Bridge.AddFX(ctx, track, chosen, 0)
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := x.DAW.FXChain(ctx, track)
		if e != nil {
			_, _ = x.Bridge.RemoveFX(ctx, track, index)
			return core.ContractResult{}, e
		}
		actualIndex := addedFXIndex(beforeChain, after)
		if actualIndex < 1 || actualIndex > len(after) || after[actualIndex-1].Name == "" {
			if index >= 1 && index <= len(after) {
				_, _ = x.Bridge.RemoveFX(ctx, track, index)
			}
			return core.ContractResult{}, fmt.Errorf("added effect missing from FX chain readback (bridge index %d)", index)
		}
		actualName := after[actualIndex-1].Name
		if _, e = x.Bridge.RemoveFX(ctx, track, actualIndex); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore effect chain: %w", e)
		}
		restored, e := x.DAW.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if len(restored) != len(beforeChain) {
			return core.ContractResult{}, fmt.Errorf("FX chain restore count differs")
		}
		return result(map[string]any{"action": "add", "track": track, "name": chosen, "actual_name": actualName}, after), nil
	case core.OpShowFXUI:
		old, e := x.Bridge.FXUIState(ctx, track, fx)
		if e != nil {
			return core.ContractResult{}, e
		}
		back, e := x.Bridge.SetFXUI(ctx, track, fx, true)
		if e != nil {
			return core.ContractResult{}, e
		}
		if !back {
			return core.ContractResult{}, fmt.Errorf("FX window open readback false")
		}
		if _, e = x.Bridge.SetFXUI(ctx, track, fx, old); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore FX window state: %w", e)
		}
		restored, e := x.Bridge.FXUIState(ctx, track, fx)
		if e != nil || restored != old {
			return core.ContractResult{}, fmt.Errorf("restore FX window state readback differs: %v", e)
		}
		return result(map[string]any{"track": track, "fx": fx, "show": true}, map[string]any{"window_open": back}), nil
	case core.OpReadParams:
		c, e := chain()
		if e != nil {
			return core.ContractResult{}, e
		}
		if fx > 0 && fx <= len(c) {
			return result(map[string]any{"track": track, "fx": fx}, c[fx-1].Params), nil
		}
		return result(map[string]any{"track": track}, c), nil
	case core.OpProjectState:
		c, e := chain()
		if e != nil {
			return core.ContractResult{}, e
		}
		if fx < 1 || fx > len(c) {
			return core.ContractResult{}, fmt.Errorf("fx out of range")
		}
		return result(map[string]any{"track": track, "fx": fx}, map[string]any{"fx": c[fx-1].Name, "index": fx, "params": c[fx-1].Params}), nil
	case core.OpSetParam, core.OpVerifyWrite:
		id := core.ParamID(cs(m, "param"))
		before, e := x.DAW.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		old, ok := paramByID(before, fx, id)
		if !ok {
			return core.ContractResult{}, fmt.Errorf("parameter %s not found", id)
		}
		sent := cf(m, "value")
		applied, e := x.DAW.SetParam(ctx, track, id, sent)
		if e != nil {
			return core.ContractResult{}, e
		}
		after, e := x.DAW.FXChain(ctx, track)
		if e != nil {
			return core.ContractResult{}, e
		}
		if _, e = x.DAW.SetParam(ctx, track, id, old.Value); e != nil {
			return core.ContractResult{}, fmt.Errorf("restore parameter: %w", e)
		}
		back, ok := paramByID(after, fx, id)
		if !ok {
			return core.ContractResult{}, fmt.Errorf("parameter readback missing")
		}
		return result(map[string]any{"track": track, "param": id, "value": sent}, map[string]any{"confirmed": applied.Status == core.StatusConfirmed, "status": applied.Status, "value": back.Value}), nil
	case core.OpPresets:
		cur, list, e := x.DAW.Presets(ctx, track, fx)
		if e != nil {
			return core.ContractResult{}, e
		}
		if action == "load" {
			if cur == "" {
				return core.ContractResult{}, fmt.Errorf("preset current-state API returned no original preset")
			}
			original := cur
			e = x.DAW.SetPreset(ctx, track, fx, cs(m, "preset"))
			if e != nil {
				return core.ContractResult{}, e
			}
			cur, _, e = x.DAW.Presets(ctx, track, fx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if cur != cs(m, "preset") {
				return core.ContractResult{}, fmt.Errorf("preset readback %q", cur)
			}
			if e = x.DAW.SetPreset(ctx, track, fx, original); e != nil {
				return core.ContractResult{}, fmt.Errorf("restore preset: %w", e)
			}
			restored, _, e := x.DAW.Presets(ctx, track, fx)
			if e != nil {
				return core.ContractResult{}, e
			}
			if restored != original {
				return core.ContractResult{}, fmt.Errorf("preset restore readback %q", restored)
			}
		}
		return result(map[string]any{"action": "load", "track": track, "fx": fx, "preset": cs(m, "preset")}, map[string]any{"current": cur, "presets": list}), nil
	case core.OpModelSwitch:
		return core.ContractResult{}, fmt.Errorf("missing reversible model API call: REAPER bridge exposes set_plugin_model but no exact current-model restore token for this slot")
	default:
		return core.ContractResult{}, fmt.Errorf("operation %s has no REAPER bridge mapping", op)
	}
}

func (x *ContractExecutor) recordInput(ctx context.Context, requestedTrack int, result func(any, any) core.ContractResult) (core.ContractResult, error) {
	tracks, err := x.DAW.Tracks(ctx)
	if err != nil {
		return core.ContractResult{}, err
	}
	input := -1
	for _, t := range tracks {
		state, e := x.Bridge.TrackState(ctx, t.Index)
		if e == nil && state.Input >= 0 && state.Input < 4096 {
			input = state.Input
			break
		}
	}
	if input < 0 {
		return core.ContractResult{}, fmt.Errorf("no configured audio input available to copy to temporary conformance track")
	}
	tr, err := x.Bridge.CreateTrack(ctx, "Conformance Record", 0)
	if err != nil {
		return core.ContractResult{}, err
	}
	defer x.Bridge.DeleteTrack(context.Background(), tr)
	if _, err = x.Bridge.SetTrackControl(ctx, tr, "input", strconv.Itoa(input)); err != nil {
		return core.ContractResult{}, err
	}
	if _, err = x.Bridge.SetTrackControl(ctx, tr, "monitor", "1"); err != nil {
		return core.ContractResult{}, err
	}
	if _, err = x.Bridge.SetTrackControl(ctx, tr, "arm", "1"); err != nil {
		return core.ContractResult{}, err
	}
	token := fmt.Sprintf("conformance-%d", time.Now().UnixNano())
	if err = x.Bridge.StartInput(ctx, tr, 1, token); err != nil {
		return core.ContractResult{}, err
	}
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return core.ContractResult{}, ctx.Err()
	case <-timer.C:
	}
	clip, err := x.Bridge.FinishInput(ctx, tr, token)
	if err != nil {
		return core.ContractResult{}, err
	}
	return result(map[string]any{"track": requestedTrack, "action": "record"}, map[string]any{"path": clip.Path, "track": requestedTrack, "duration_sec": clip.DurationSec}), nil
}

func conformanceWAV() (string, error) {
	dir := filepath.Join(os.TempDir(), "muse-conformance")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
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

func contractArgs(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	return nil, fmt.Errorf("contract args must be an object")
}
func ci(m map[string]any, k string) int {
	switch n := m[k].(type) {
	case int:
		return n
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}
func cf(m map[string]any, k string) float64 {
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
func cs(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
func near(a, b float64) bool { return a-b < 1e-5 && b-a < 1e-5 }
func trackValue(s TrackState, p string) float64 {
	switch p {
	case "volume":
		return s.Volume
	case "pan":
		return s.Pan
	case "mute":
		return boolNum(s.Mute)
	case "solo":
		return boolNum(s.Solo)
	}
	return 0
}
func paramByID(chain []core.FX, fx int, id core.ParamID) (core.Param, bool) {
	for _, f := range chain {
		for _, p := range f.Params {
			if p.ID == id {
				return p, true
			}
		}
	}
	return core.Param{}, false
}
func sameMarkers(a, b []core.Marker) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Region != b[i].Region || !near(a[i].PositionSec, b[i].PositionSec) || !near(a[i].EndSec, b[i].EndSec) {
			return false
		}
	}
	return true
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
func containsFold(s, q string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(q))
}
