package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// SupportedOperations lists all operations supported by the Fake DAW contract.
var SupportedOperations = append([]string{core.OpCapabilities}, append(core.ContractOperations, core.OpPluginWindows, core.OpListPluginWindows)...)

// Call is an alias for Execute.
func (e *Engine) Call(ctx context.Context, op string, args any) (core.ContractResult, error) {
	return e.Execute(ctx, op, args)
}

// Execute executes a contract operation by name against the real in-memory DAW state.
func (e *Engine) Execute(ctx context.Context, op string, args any) (core.ContractResult, error) {
	m, err := toArgsMap(args)
	if err != nil {
		return core.ContractResult{}, fmt.Errorf("bad args for op %s: %w", op, err)
	}

	normOp := strings.ToLower(strings.TrimSpace(op))
	switch normOp {
	case core.OpCapabilities:
		return core.ContractResult{
			Applied:  map[string]any{"op": "capabilities"},
			Readback: append([]string(nil), SupportedOperations...),
		}, nil

	case core.OpListTracks:
		return e.execListTracks(ctx, m)

	case core.OpManageTrack, "create_track", "duplicate_track", "delete_track", "rename_track":
		return e.execManageTrack(ctx, normOp, m)

	case core.OpSetTrackProp, "set_track", "set_track_input":
		return e.execSetTrackProp(ctx, m)

	case core.OpRouteSend, "remove_send":
		return e.execRouteSend(ctx, normOp, m)

	case core.OpSetTransport:
		return e.execSetTransport(ctx, m)

	case core.OpMarkers, "list_markers", "create_marker", "goto_marker":
		return e.execMarkers(ctx, normOp, m)

	case core.OpUndo, "redo", "begin_undo", "end_undo":
		return e.execUndo(ctx, normOp, m)

	case core.OpSaveProject, "reopen_project":
		return e.execSaveProject(ctx, m)

	case core.OpInsertMedia:
		return e.execInsertMedia(ctx, m)

	case core.OpStarterRiff:
		return e.execStarterRiff(ctx, m)

	case core.OpRenderTrack, "render_range":
		return e.execRenderTrack(ctx, m)

	case core.OpImportReference, "measure_reference":
		return e.execImportReference(ctx, m)

	case core.OpRecordInput, "start_input", "finish_input":
		return e.execRecordInput(ctx, normOp, m)

	case core.OpCatalogFX, "list_plugins":
		return e.execCatalogFX(ctx, m)

	case core.OpManageFX, "add_fx", "remove_fx", "move_fx", "set_bypass", "set_wet_dry":
		return e.execManageFX(ctx, normOp, m)

	case core.OpShowFXUI:
		return e.execShowFXUI(ctx, m)

	case core.OpPluginWindows, core.OpListPluginWindows:
		return e.execPluginWindows(ctx, m)

	case core.OpReadParams, "describe_chain", "get_params":
		return e.execReadParams(ctx, m)

	case core.OpSetParam, "find_value":
		return e.execSetParam(ctx, m)

	case core.OpLearnParam:
		return e.execLearnParam(ctx, m)

	case core.OpProjectState:
		return e.execProjectState(ctx, m)

	case core.OpVerifyWrite:
		return e.execVerifyWrite(ctx, m)

	case core.OpModelSwitch, "list_plugin_models", "set_plugin_model", "program_change":
		return e.execModelSwitch(ctx, normOp, m)

	case core.OpPresets, "list_presets", "load_preset", "save_preset_as":
		return e.execPresets(ctx, normOp, m)

	case core.OpGuideUser:
		return e.execGuideUser(ctx, m)

	case core.OpMeasureAudio:
		return e.execMeasureAudio(ctx, m)

	case core.OpMatchLoudness:
		return e.execMatchLoudness(ctx, m)

	case core.OpSpectrogram:
		return e.execSpectrogram(ctx, m)

	case core.OpMixClash:
		return e.execMixClash(ctx, m)

	case core.OpReferenceMatch:
		return e.execReferenceMatch(ctx, m)

	case core.OpDiagnoseSilence, "audio_engine":
		return e.execDiagnoseSilence(ctx, m)

	case core.OpSetMode:
		return e.execSetMode(ctx, m)

	case core.OpSnapshot:
		return e.execSnapshot(ctx, m)

	case core.OpDelegateTask:
		return e.execDelegateTask(ctx, m)

	case core.OpTokenBudget:
		return e.execTokenBudget(ctx, m)

	case core.OpCallGuard:
		return e.execCallGuard(ctx, m)

	case core.OpTasteMemory:
		return e.execTasteMemory(ctx, m)

	case core.OpProcessInput:
		return e.execProcessInput(ctx, m)

	case core.OpOnboardDAW:
		return e.execOnboardDAW(ctx, m)

	case core.OpPlatformSetup:
		return e.execPlatformSetup(ctx, m)

	case core.OpUIState:
		return e.execUIState(ctx, m)

	case core.OpReleaseSpec:
		return e.execReleaseSpec(ctx, m)

	default:
		return core.ContractResult{}, fmt.Errorf("unsupported contract operation: %s", op)
	}
}

// --- Operation Executors ---

func (e *Engine) execListTracks(ctx context.Context, _ map[string]any) (core.ContractResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	tracksCopy := make([]Track, len(e.SessionTracks))
	for i, t := range e.SessionTracks {
		tracksCopy[i] = *t.Clone()
	}
	return core.ContractResult{
		Applied:  map[string]any{"op": "list_tracks"},
		Readback: tracksCopy,
	}, nil
}

func (e *Engine) execManageTrack(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	track := getInt(m, "track")
	name := getString(m, "name")
	index := getInt(m, "index")
	if index == 0 {
		index = track
	}

	switch strings.ToLower(action) {
	case "create", "create_track":
		newID, err := e.CreateTrack(ctx, name, index)
		if err != nil {
			return core.ContractResult{}, err
		}
		tracks, _ := e.Tracks(ctx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "create", "track": newID, "name": name},
			Readback: tracks,
		}, nil

	case "duplicate", "duplicate_track":
		newID, err := e.DuplicateTrack(ctx, track, name)
		if err != nil {
			return core.ContractResult{}, err
		}
		tracks, _ := e.Tracks(ctx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "duplicate", "track": track, "new_track": newID, "name": name},
			Readback: tracks,
		}, nil

	case "delete", "delete_track":
		if err := e.DeleteTrack(ctx, track); err != nil {
			return core.ContractResult{}, err
		}
		tracks, _ := e.Tracks(ctx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "delete", "track": track},
			Readback: tracks,
		}, nil

	case "rename", "rename_track":
		if err := e.RenameTrack(ctx, track, name); err != nil {
			return core.ContractResult{}, err
		}
		e.mu.RLock()
		updatedName := e.SessionTracks[track-1].Name
		e.mu.RUnlock()
		return core.ContractResult{
			Applied:  map[string]any{"action": "rename", "track": track, "name": name},
			Readback: map[string]any{"track": track, "name": updatedName},
		}, nil

	default:
		return core.ContractResult{}, fmt.Errorf("unknown manage_track action: %s", action)
	}
}

func (e *Engine) execSetTrackProp(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	prop := getString(m, "prop")
	if prop == "" {
		prop = getString(m, "property")
	}
	if prop == "" {
		prop = getString(m, "control")
	}

	valFloat, isFloat := getFloat(m, "value")
	valStr := getString(m, "value")
	if valStr == "" {
		valStr = getString(m, "control_value")
	}

	switch strings.ToLower(prop) {
	case "volume", "pan", "mute", "solo", "arm":
		val := valFloat
		if !isFloat {
			if strings.EqualFold(valStr, "true") || strings.EqualFold(valStr, "on") || valStr == "1" {
				val = 1
			} else if strings.EqualFold(valStr, "false") || strings.EqualFold(valStr, "off") || valStr == "0" {
				val = 0
			}
		}
		confirmed, err := e.SetTrack(ctx, track, prop, val)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"track": track, "prop": prop, "value": val},
			Readback: map[string]any{"track": track, "prop": prop, "confirmed": confirmed},
		}, nil

	case "monitor", "input", "input_select":
		if valStr == "" && isFloat {
			valStr = strconv.Itoa(int(valFloat))
		}
		confirmed, err := e.SetTrackControl(ctx, track, prop, valStr)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"track": track, "prop": prop, "value": valStr},
			Readback: map[string]any{"track": track, "prop": prop, "confirmed": confirmed},
		}, nil

	default:
		return core.ContractResult{}, fmt.Errorf("unknown track property %q", prop)
	}
}

func (e *Engine) execRouteSend(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	fromTrack := getInt(m, "from_track")
	toTrack := getInt(m, "to_track")
	sendIdx := getInt(m, "send")
	levelDB, _ := getFloat(m, "level_db")

	if strings.ToLower(action) == "remove" || strings.ToLower(action) == "remove_send" {
		if err := e.RemoveSend(ctx, fromTrack, sendIdx); err != nil {
			return core.ContractResult{}, err
		}
		e.mu.RLock()
		sends := append([]Send(nil), e.SessionTracks[fromTrack-1].Sends...)
		e.mu.RUnlock()
		return core.ContractResult{
			Applied:  map[string]any{"action": "remove", "from_track": fromTrack, "send": sendIdx},
			Readback: sends,
		}, nil
	}

	if err := e.CreateSend(ctx, fromTrack, toTrack, levelDB); err != nil {
		return core.ContractResult{}, err
	}
	e.mu.RLock()
	sends := append([]Send(nil), e.SessionTracks[fromTrack-1].Sends...)
	e.mu.RUnlock()
	return core.ContractResult{
		Applied:  map[string]any{"action": "create", "from_track": fromTrack, "to_track": toTrack, "level_db": levelDB},
		Readback: sends,
	}, nil
}

func (e *Engine) execSetTransport(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	bpm, _ := getFloat(m, "bpm")
	pos, _ := getFloat(m, "pos_sec")
	if pos == 0 {
		pos, _ = getFloat(m, "position_sec")
	}
	loopStart, _ := getFloat(m, "loop_start")
	loopEnd, _ := getFloat(m, "loop_end")
	num := getInt(m, "numerator")
	denom := getInt(m, "denominator")

	if strings.ToLower(action) == "time_signature" || (num > 0 && denom > 0) {
		if err := e.SetTimeSignature(ctx, num, denom); err != nil {
			return core.ContractResult{}, err
		}
	}
	if action != "time_signature" {
		if err := e.SetTransport(ctx, action, bpm, pos, loopStart, loopEnd); err != nil {
			return core.ContractResult{}, err
		}
	}

	e.mu.RLock()
	trans := e.Transport
	e.mu.RUnlock()
	return core.ContractResult{
		Applied:  m,
		Readback: trans,
	}, nil
}

func (e *Engine) execMarkers(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	switch strings.ToLower(action) {
	case "create", "create_marker":
		pos, _ := getFloat(m, "position_sec")
		end, _ := getFloat(m, "end_sec")
		name := getString(m, "name")
		region, _ := m["region"].(bool)
		idx, err := e.CreateMarker(ctx, pos, end, name, region)
		if err != nil {
			return core.ContractResult{}, err
		}
		markers, _ := e.Markers(ctx)
		return core.ContractResult{
			Applied:  map[string]any{"index": idx, "name": name, "position_sec": pos, "end_sec": end, "region": region},
			Readback: markers,
		}, nil

	case "goto", "goto_marker":
		idx := getInt(m, "index")
		if err := e.GoToMarker(ctx, idx); err != nil {
			return core.ContractResult{}, err
		}
		e.mu.RLock()
		curPos := e.Transport.PositionSec
		e.mu.RUnlock()
		return core.ContractResult{
			Applied:  map[string]any{"action": "goto", "index": idx},
			Readback: map[string]any{"position_sec": curPos},
		}, nil

	default: // "list", "markers"
		markers, _ := e.Markers(ctx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "list"},
			Readback: markers,
		}, nil
	}
}

func (e *Engine) execUndo(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	name := getString(m, "name")

	switch strings.ToLower(action) {
	case "begin", "begin_undo":
		if err := e.BeginUndoAction(ctx, name); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "begin", "name": name},
			Readback: map[string]any{"active_action": name},
		}, nil

	case "end", "end_undo":
		commit := true
		if c, ok := m["commit"].(bool); ok {
			commit = c
		}
		if err := e.EndUndoAction(ctx, name, commit); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "end", "name": name, "commit": commit},
			Readback: map[string]any{"active_action": nil},
		}, nil

	case "redo":
		if err := e.Redo(ctx); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "redo"},
			Readback: map[string]any{"restored": true},
		}, nil

	default:
		if err := e.Undo(ctx); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "undo"},
			Readback: map[string]any{"restored": true},
		}, nil
	}
}

func (e *Engine) execSaveProject(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	if err := e.SaveProject(ctx); err != nil {
		return core.ContractResult{}, err
	}
	e.mu.RLock()
	path := e.ProjectPath
	lastSaved := e.LastSaved
	e.mu.RUnlock()
	return core.ContractResult{
		Applied:  map[string]any{"action": "save_project"},
		Readback: map[string]any{"path": path, "last_saved": lastSaved, "saved": true},
	}, nil
}

func (e *Engine) execInsertMedia(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	path := getString(m, "path")
	pos, _ := getFloat(m, "position_sec")

	if err := e.InsertMedia(ctx, track, path, pos); err != nil {
		return core.ContractResult{}, err
	}
	e.mu.RLock()
	clips := append([]MediaClip(nil), e.SessionTracks[track-1].MediaClips...)
	e.mu.RUnlock()
	return core.ContractResult{
		Applied:  map[string]any{"track": track, "path": path, "position_sec": pos},
		Readback: clips,
	}, nil
}

func (e *Engine) execStarterRiff(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	e.mu.Lock()
	track := getInt(m, "track")
	if track <= 0 {
		track = 1
	}
	if track > len(e.SessionTracks) {
		e.mu.Unlock()
		return core.ContractResult{}, fmt.Errorf("track %d out of range", track)
	}
	// Starter riff runs only into empty set
	for _, t := range e.SessionTracks {
		if t.HasClips || len(t.MediaClips) > 0 {
			e.mu.Unlock()
			return core.ContractResult{}, errors.New("cannot insert starter riff into a set that already contains audio")
		}
	}
	e.mu.Unlock()

	riffPath := getString(m, "path")
	if riffPath == "" {
		riffPath = filepath.Join(e.renderDir, "starter-riff.wav")
		_ = writeWAVFile(riffPath, 4.0, 220.0)
	}
	if err := e.InsertMedia(ctx, track, riffPath, 0); err != nil {
		return core.ContractResult{}, err
	}
	e.mu.RLock()
	clips := append([]MediaClip(nil), e.SessionTracks[track-1].MediaClips...)
	e.mu.RUnlock()
	return core.ContractResult{
		Applied:  map[string]any{"track": track, "starter_riff": riffPath},
		Readback: clips,
	}, nil
}

func (e *Engine) execRenderTrack(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	sec, ok := getFloat(m, "seconds")
	if !ok || sec <= 0 {
		sec = 2.0
	}
	start, _ := getFloat(m, "start")

	wavPath, err := e.RenderRange(ctx, track, start, sec)
	if err != nil {
		return core.ContractResult{}, err
	}
	return core.ContractResult{
		Applied:  map[string]any{"track": track, "seconds": sec, "start": start},
		Readback: map[string]any{"path": wavPath, "duration_sec": sec},
	}, nil
}

func (e *Engine) execImportReference(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	path := getString(m, "path")
	if path == "" {
		path = filepath.Join(e.renderDir, "reference.wav")
		_ = writeWAVFile(path, 3.0, 440.0)
	}
	measure := core.Measure{
		WAV:      path,
		Centroid: 1250.0,
		Crest:    12.4,
		Level:    -14.2,
		Bands: core.Bands{
			Low:      -18.0,
			Mud:      -20.5,
			Mid:      -16.2,
			Upper:    -15.0,
			Presence: -19.4,
			Air:      -24.1,
		},
	}
	return core.ContractResult{
		Applied:  map[string]any{"path": path},
		Readback: measure,
	}, nil
}

func (e *Engine) execRecordInput(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	track := getInt(m, "track")
	sec, ok := getFloat(m, "seconds")
	if !ok || sec <= 0 {
		sec = 2.0
	}
	token := getString(m, "token")
	if token == "" {
		token = fmt.Sprintf("take-%d", time.Now().UnixNano())
	}

	switch strings.ToLower(action) {
	case "start", "start_input":
		if err := e.StartInput(ctx, track, sec, token); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "start", "track": track, "seconds": sec, "token": token},
			Readback: map[string]any{"recording": true, "track": track},
		}, nil

	case "finish", "finish_input":
		clip, err := e.FinishInput(ctx, track, token)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "finish", "track": track, "token": token},
			Readback: clip,
		}, nil

	default: // full take
		clip, err := core.RecordInputFor(ctx, e, track, sec, token)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "record", "track": track, "seconds": sec, "token": token},
			Readback: clip,
		}, nil
	}
}

func (e *Engine) execCatalogFX(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	cat, err := e.Catalog(ctx)
	if err != nil {
		return core.ContractResult{}, err
	}
	q := strings.ToLower(getString(m, "query"))
	category := strings.ToLower(getString(m, "category"))

	var filtered []core.CatalogEntry
	for _, c := range cat {
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
			continue
		}
		if category != "" && !strings.EqualFold(c.Kind, category) {
			continue
		}
		filtered = append(filtered, c)
	}
	return core.ContractResult{
		Applied:  map[string]any{"query": q, "category": category},
		Readback: filtered,
	}, nil
}

func (e *Engine) execManageFX(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	name := getString(m, "name")
	pos := getInt(m, "pos")
	toPos := getInt(m, "to_pos")
	force, _ := m["force"].(bool)
	if force {
		_ = e.EnableAuto()
	}

	switch strings.ToLower(action) {
	case "add", "add_fx":
		newIdx, err := e.AddFX(ctx, track, name, pos)
		if err != nil {
			return core.ContractResult{}, err
		}
		chain, _ := e.FXChain(ctx, track)
		return core.ContractResult{
			Applied:  map[string]any{"action": "add", "track": track, "name": name, "fx": newIdx},
			Readback: chain,
		}, nil

	case "remove", "remove_fx":
		var remName string
		var err error
		if force {
			remName, err = e.ForceRemoveFX(ctx, track, fxIdx)
		} else {
			remName, err = e.RemoveFX(ctx, track, fxIdx)
		}
		if err != nil {
			return core.ContractResult{}, err
		}
		chain, _ := e.FXChain(ctx, track)
		return core.ContractResult{
			Applied:  map[string]any{"action": "remove", "track": track, "fx": fxIdx, "name": remName},
			Readback: chain,
		}, nil

	case "move", "move_fx":
		if err := e.MoveFX(ctx, track, fxIdx, toPos); err != nil {
			return core.ContractResult{}, err
		}
		chain, _ := e.FXChain(ctx, track)
		return core.ContractResult{
			Applied:  map[string]any{"action": "move", "track": track, "from": fxIdx, "to": toPos},
			Readback: chain,
		}, nil

	case "bypass", "set_bypass":
		bypassed, _ := m["bypassed"].(bool)
		if err := e.SetBypass(ctx, track, fxIdx, bypassed); err != nil {
			return core.ContractResult{}, err
		}
		chain, _ := e.FXChain(ctx, track)
		return core.ContractResult{
			Applied:  map[string]any{"action": "bypass", "track": track, "fx": fxIdx, "bypassed": bypassed},
			Readback: chain,
		}, nil

	case "wet_dry", "set_wet_dry":
		wd, _ := getFloat(m, "wet_dry")
		e.mu.Lock()
		if track < 1 || track > len(e.SessionTracks) || fxIdx < 1 || fxIdx > len(e.SessionTracks[track-1].FX) {
			e.mu.Unlock()
			return core.ContractResult{}, fmt.Errorf("track %d or fx %d out of range", track, fxIdx)
		}
		e.pushUndo("set_wet_dry")
		e.SessionTracks[track-1].FX[fxIdx-1].WetDry = wd
		e.mu.Unlock()
		return core.ContractResult{
			Applied:  map[string]any{"action": "wet_dry", "track": track, "fx": fxIdx, "wet_dry": wd},
			Readback: map[string]any{"wet_dry": wd},
		}, nil

	default:
		return core.ContractResult{}, fmt.Errorf("unknown manage_fx action: %s", action)
	}
}

func (e *Engine) execShowFXUI(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	show, _ := m["show"].(bool)
	if _, ok := m["show"]; !ok {
		show = true
	}

	e.mu.Lock()
	if track < 1 || track > len(e.SessionTracks) || fxIdx < 1 || fxIdx > len(e.SessionTracks[track-1].FX) {
		e.mu.Unlock()
		return core.ContractResult{}, fmt.Errorf("track %d or fx %d out of range", track, fxIdx)
	}
	e.SessionTracks[track-1].FX[fxIdx-1].WindowOpen = show
	e.mu.Unlock()

	return core.ContractResult{
		Applied:  map[string]any{"track": track, "fx": fxIdx, "show": show},
		Readback: map[string]any{"window_open": show},
	}, nil
}

func (e *Engine) execPluginWindows(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var wins []core.PluginWindow
	for _, t := range e.SessionTracks {
		for _, fx := range t.FX {
			if fx.WindowOpen {
				wins = append(wins, core.PluginWindow{
					Title:     fmt.Sprintf("FX: Track %d [%s]", t.ID, fx.Name),
					OwningApp: "FakeDAW",
				})
			}
		}
	}
	if len(wins) == 0 {
		for _, t := range e.SessionTracks {
			for _, fx := range t.FX {
				wins = append(wins, core.PluginWindow{
					Title:     fmt.Sprintf("FX: Track %d [%s]", t.ID, fx.Name),
					OwningApp: "FakeDAW",
				})
			}
		}
	}
	return core.ContractResult{
		Applied:  map[string]any{"action": "list_plugin_windows"},
		Readback: wins,
	}, nil
}

func (e *Engine) execReadParams(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")

	chain, err := e.FXChain(ctx, track)
	if err != nil {
		return core.ContractResult{}, err
	}
	if fxIdx > 0 {
		if fxIdx > len(chain) {
			return core.ContractResult{}, fmt.Errorf("fx %d out of range on track %d", fxIdx, track)
		}
		return core.ContractResult{
			Applied:  map[string]any{"track": track, "fx": fxIdx},
			Readback: chain[fxIdx-1].Params,
		}, nil
	}
	return core.ContractResult{
		Applied:  map[string]any{"track": track},
		Readback: chain,
	}, nil
}

func (e *Engine) execSetParam(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	paramIDStr := getString(m, "param")
	if paramIDStr == "" {
		paramIDStr = getString(m, "id")
	}
	if paramIDStr == "" && fxIdx > 0 {
		pIdx := getInt(m, "p")
		if pIdx > 0 {
			paramIDStr = fmt.Sprintf("%d/%d", fxIdx, pIdx)
		}
	}

	id := core.ParamID(paramIDStr)
	target := getString(m, "target")
	val, hasVal := getFloat(m, "value")

	if target != "" && !hasVal {
		foundVal, err := e.FindValue(ctx, track, id, target)
		if err != nil {
			return core.ContractResult{}, err
		}
		val = foundVal
	}

	applied, err := e.SetParam(ctx, track, id, val)
	if err != nil {
		return core.ContractResult{}, err
	}
	return core.ContractResult{
		Applied:  map[string]any{"track": track, "param": paramIDStr, "value": val, "target": target},
		Readback: applied,
	}, nil
}

func (e *Engine) execLearnParam(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	pIdx := getInt(m, "param")
	label := getString(m, "label")

	e.mu.Lock()
	if track < 1 || track > len(e.SessionTracks) || fxIdx < 1 || fxIdx > len(e.SessionTracks[track-1].FX) {
		e.mu.Unlock()
		return core.ContractResult{}, errors.New("track or fx out of range")
	}
	fx := e.SessionTracks[track-1].FX[fxIdx-1]
	if pIdx < 1 || pIdx > len(fx.Params) {
		e.mu.Unlock()
		return core.ContractResult{}, errors.New("param index out of range")
	}
	p := fx.Params[pIdx-1]
	p.Name = label
	p.Dead = false
	pCopy := *p
	e.mu.Unlock()

	return core.ContractResult{
		Applied:  map[string]any{"track": track, "fx": fxIdx, "param": pIdx, "label": label},
		Readback: pCopy,
	}, nil
}

func (e *Engine) execProjectState(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")

	chain, err := e.FXChain(ctx, track)
	if err != nil {
		return core.ContractResult{}, err
	}
	if fxIdx < 1 || fxIdx > len(chain) {
		return core.ContractResult{}, errors.New("fx out of range")
	}
	fx := chain[fxIdx-1]
	projected := map[string]any{
		"fx":     fx.Name,
		"index":  fxIdx,
		"params": fx.Params,
	}
	return core.ContractResult{
		Applied:  map[string]any{"track": track, "fx": fxIdx},
		Readback: projected,
	}, nil
}

func (e *Engine) execVerifyWrite(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	paramIDStr := getString(m, "param")
	fxIdx := getInt(m, "fx")
	if fxIdx > 0 && !strings.Contains(paramIDStr, "/") && !strings.HasPrefix(paramIDStr, "fx") {
		pIdx := getInt(m, "param")
		if pIdx == 0 {
			pIdx = 1
		}
		paramIDStr = string(core.MakeParamID(fxIdx, pIdx))
	}
	paramID := core.ParamID(paramIDStr)
	val, _ := getFloat(m, "value")

	applied, err := e.SetParam(ctx, track, paramID, val)
	if err != nil {
		return core.ContractResult{}, err
	}
	verified := (applied.Status == core.StatusConfirmed)
	isReadOnly := (applied.Status == core.StatusUnconfirmed)
	return core.ContractResult{
		Applied: map[string]any{"track": track, "param": paramID, "value": val},
		Readback: map[string]any{
			"confirmed": verified,
			"read_only": isReadOnly,
			"applied":   applied,
		},
	}, nil
}

func (e *Engine) execModelSwitch(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	slot := getString(m, "slot")
	model := getString(m, "model")

	switch strings.ToLower(action) {
	case "set", "set_plugin_model":
		params, err := e.SetPluginModel(ctx, track, fxIdx, slot, model)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "set", "track": track, "fx": fxIdx, "slot": slot, "model": model},
			Readback: params,
		}, nil

	case "program_change":
		prog := getInt(m, "program")
		chanNum := getInt(m, "channel")
		return core.ContractResult{
			Applied:  map[string]any{"action": "program_change", "program": prog, "channel": chanNum},
			Readback: map[string]any{"switched": true, "program": prog},
		}, nil

	default: // "list", "list_plugin_models"
		slots, err := e.PluginModels(ctx, track, fxIdx, slot)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "list", "track": track, "fx": fxIdx, "slot": slot},
			Readback: slots,
		}, nil
	}
}

func (e *Engine) execPresets(ctx context.Context, op string, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	if action == "" {
		action = op
	}
	track := getInt(m, "track")
	fxIdx := getInt(m, "fx")
	preset := getString(m, "preset")
	name := getString(m, "name")

	switch strings.ToLower(action) {
	case "load", "load_preset":
		if err := e.SetPreset(ctx, track, fxIdx, preset); err != nil {
			return core.ContractResult{}, err
		}
		cur, list, _ := e.Presets(ctx, track, fxIdx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "load", "track": track, "fx": fxIdx, "preset": preset},
			Readback: map[string]any{"current": cur, "presets": list},
		}, nil

	case "save", "save_preset_as":
		saveName := name
		if saveName == "" {
			saveName = preset
		}
		if err := e.SavePresetAs(ctx, track, fxIdx, saveName); err != nil {
			return core.ContractResult{}, err
		}
		cur, list, _ := e.Presets(ctx, track, fxIdx)
		return core.ContractResult{
			Applied:  map[string]any{"action": "save", "track": track, "fx": fxIdx, "name": saveName},
			Readback: map[string]any{"current": cur, "presets": list},
		}, nil

	default: // "list", "list_presets"
		cur, list, err := e.Presets(ctx, track, fxIdx)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "list", "track": track, "fx": fxIdx},
			Readback: map[string]any{"current": cur, "presets": list},
		}, nil
	}
}

func (e *Engine) execGuideUser(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	steps := []string{
		"1. Click the plugin window to bring it into focus.",
		"2. Select your desired preset or move the tone control manually.",
		"3. Tell Muse when you are ready to continue listening.",
	}
	return core.ContractResult{
		Applied:  m,
		Readback: map[string]any{"guidance_steps": steps, "max_steps": 3},
	}, nil
}

func (e *Engine) execMeasureAudio(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	path := getString(m, "path")
	if path == "" {
		path = filepath.Join(e.renderDir, "measured.wav")
		_ = writeWAVFile(path, 2.0, 440.0)
	}
	measure := core.Measure{
		WAV:      path,
		Centroid: 1400.0,
		Crest:    10.5,
		Level:    -12.0,
		Bands: core.Bands{
			Low:      -16.0,
			Mud:      -18.0,
			Mid:      -14.0,
			Upper:    -15.0,
			Presence: -17.0,
			Air:      -22.0,
		},
	}
	return core.ContractResult{
		Applied:  map[string]any{"path": path},
		Readback: measure,
	}, nil
}

func (e *Engine) execMatchLoudness(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	target, _ := getFloat(m, "target_dbfs")
	if target == 0 {
		target = -14.0
	}
	return core.ContractResult{
		Applied: map[string]any{"target_dbfs": target},
		Readback: map[string]any{
			"matched_dbfs": target,
			"delta_db":     0.2, // within 1 dB
			"status":       "matched",
		},
	}, nil
}

func (e *Engine) execSpectrogram(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	pngPath := filepath.Join(e.renderDir, fmt.Sprintf("spectrogram-%d.png", time.Now().UnixNano()))
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 8), B: 120, A: 255})
		}
	}
	f, err := os.Create(pngPath)
	if err != nil {
		return core.ContractResult{}, err
	}
	defer f.Close()
	_ = png.Encode(f, img)

	return core.ContractResult{
		Applied:  m,
		Readback: map[string]any{"path": pngPath},
	}, nil
}

func (e *Engine) execMixClash(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	clashes := []core.Clash{
		{
			Lo:      200,
			Hi:      450,
			Center:  320,
			TrackDB: -14.0,
			RestDB:  -13.5,
			Score:   0.65,
		},
	}
	return core.ContractResult{
		Applied:  m,
		Readback: clashes,
	}, nil
}

func (e *Engine) execReferenceMatch(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	return core.ContractResult{
		Applied: m,
		Readback: map[string]any{
			"initial_gap_db": 5.4,
			"final_gap_db":   0.6,
			"converged":      true,
			"rounds":         2,
		},
	}, nil
}

func (e *Engine) execDiagnoseSilence(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	running := e.AudioEngine.Running
	if !running {
		return core.ContractResult{
			Applied: m,
			Readback: map[string]any{
				"silent":         true,
				"reason":         "audio engine off / no output device",
				"engine_running": false,
			},
		}, nil
	}

	track := getInt(m, "track")
	if track > 0 && track <= len(e.SessionTracks) {
		t := e.SessionTracks[track-1]
		if t.Type == "midi" {
			return core.ContractResult{
				Applied: m,
				Readback: map[string]any{
					"silent":         true,
					"reason":         "MIDI track with no instrument",
					"engine_running": true,
				},
			}, nil
		}
		if t.Muted {
			return core.ContractResult{
				Applied: m,
				Readback: map[string]any{
					"silent":         true,
					"reason":         "track is muted",
					"engine_running": true,
				},
			}, nil
		}
		if !t.HasClips && len(t.MediaClips) == 0 {
			return core.ContractResult{
				Applied: m,
				Readback: map[string]any{
					"silent":         true,
					"reason":         "no audio clips on track",
					"engine_running": true,
				},
			}, nil
		}
		for _, fx := range t.FX {
			if fx.Bypassed {
				continue
			}
			for _, p := range fx.Params {
				if strings.Contains(strings.ToLower(p.Name), "gain") && p.Value == 0 {
					return core.ContractResult{
						Applied: m,
						Readback: map[string]any{
							"silent":         true,
							"reason":         fmt.Sprintf("plugin %s is silencing the track", fx.Name),
							"engine_running": true,
						},
					}, nil
				}
			}
		}
	}

	return core.ContractResult{
		Applied: m,
		Readback: map[string]any{
			"silent":         false,
			"reason":         "track is producing sound",
			"engine_running": true,
		},
	}, nil
}

func (e *Engine) execSetMode(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	mode := getString(m, "mode")
	if mode == "" {
		mode = "default"
	}
	e.SetMode(mode)
	return core.ContractResult{
		Applied:  map[string]any{"mode": mode},
		Readback: map[string]any{"current_mode": e.Mode(), "turbo_enabled": e.turboEnabled},
	}, nil
}

func (e *Engine) execSnapshot(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	name := getString(m, "name")
	if name == "" {
		name = "before-request"
	}

	switch strings.ToLower(action) {
	case "recall", "restore":
		e.mu.Lock()
		data, ok := e.snapshots[name]
		e.mu.Unlock()
		if !ok {
			return core.ContractResult{}, fmt.Errorf("snapshot %q not found", name)
		}
		if err := e.RestoreRequestSnapshot(ctx, data); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "recall", "name": name},
			Readback: map[string]any{"restored": true, "name": name},
		}, nil

	default: // capture
		snap, err := e.CaptureRequestSnapshot(ctx)
		if err != nil {
			return core.ContractResult{}, err
		}
		e.mu.Lock()
		e.snapshots[name] = snap
		e.mu.Unlock()
		return core.ContractResult{
			Applied:  map[string]any{"action": "capture", "name": name},
			Readback: map[string]any{"captured": true, "name": name, "bytes": len(snap)},
		}, nil
	}
}

func (e *Engine) execDelegateTask(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	track := getInt(m, "track")
	task := getString(m, "task")
	return core.ContractResult{
		Applied: map[string]any{"track": track, "task": task},
		Readback: map[string]any{
			"track":   track,
			"task":    task,
			"status":  "completed",
			"summary": fmt.Sprintf("Subagent successfully performed task on track %d", track),
		},
	}, nil
}

func (e *Engine) execTokenBudget(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	trackCount := getInt(m, "track_count")
	if trackCount <= 0 {
		trackCount = 1
	}
	used := getInt(m, "tokens_used")
	limit := 100000
	if trackCount > 1 {
		limit = 200000
	}
	within := (used <= limit)
	return core.ContractResult{
		Applied: m,
		Readback: map[string]any{
			"within_budget": within,
			"limit":         limit,
			"tokens_used":   used,
		},
	}, nil
}

func (e *Engine) execCallGuard(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	callID := getString(m, "call_id")
	errStr := getString(m, "error")

	e.mu.Lock()
	prevErr, seen := e.callHistory[callID]
	if errStr != "" {
		e.callHistory[callID] = errStr
	}
	e.mu.Unlock()

	blocked := (seen && prevErr != "")
	return core.ContractResult{
		Applied: m,
		Readback: map[string]any{
			"call_id":        callID,
			"already_failed": seen,
			"blocked":        blocked,
		},
	}, nil
}

func (e *Engine) execTasteMemory(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	action := getString(m, "action")
	control := getString(m, "control")
	dir := getInt(m, "dir")

	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.ToLower(action) == "record" && control != "" {
		found := false
		for i, l := range e.taste.Leanings {
			if l.Control == control && l.Dir == dir {
				e.taste.Leanings[i].Count++
				e.taste.Leanings[i].Last = time.Now()
				found = true
				break
			}
		}
		if !found {
			e.taste.Leanings = append(e.taste.Leanings, core.Leaning{
				Control: control,
				Dir:     dir,
				Count:   1,
				Last:    time.Now(),
			})
		}
	}

	return core.ContractResult{
		Applied:  m,
		Readback: e.taste,
	}, nil
}

func (e *Engine) execProcessInput(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	kind := getString(m, "kind")
	return core.ContractResult{
		Applied: m,
		Readback: map[string]any{
			"kind":      kind,
			"processed": true,
			"ready":     true,
		},
	}, nil
}

func (e *Engine) execOnboardDAW(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	url := getString(m, "url")
	daw := getString(m, "daw")
	return core.ContractResult{
		Applied: map[string]any{"url": url, "daw": daw},
		Readback: map[string]any{
			"verified":           true,
			"commands_supported": 46,
			"status":             "ready",
		},
	}, nil
}

func (e *Engine) execPlatformSetup(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	osName := getString(m, "os")
	return core.ContractResult{
		Applied: map[string]any{"os": osName},
		Readback: map[string]any{
			"os":             osName,
			"osc_listening":  true,
			"helper_ready":   true,
			"scripts_loaded": true,
		},
	}, nil
}

func (e *Engine) execUIState(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return core.ContractResult{
		Applied: map[string]any{"action": "ui_state"},
		Readback: map[string]any{
			"mode":             e.mode,
			"transport":        e.Transport,
			"track_count":      len(e.SessionTracks),
			"audio_engine_on":  e.AudioEngine.Running,
			"consent_required": (e.mode != "turbo"),
		},
	}, nil
}

func (e *Engine) execReleaseSpec(ctx context.Context, m map[string]any) (core.ContractResult, error) {
	osName := getString(m, "os")
	return core.ContractResult{
		Applied: map[string]any{"os": osName},
		Readback: map[string]any{
			"binaries": []string{"tonelab-muse", "tonelab-muse.exe"},
			"scripts":  []string{"start.command", "start.bat"},
			"ready":    true,
		},
	}, nil
}

// --- Helper Arg Parsers ---

func toArgsMap(args any) (map[string]any, error) {
	if args == nil {
		return map[string]any{}, nil
	}
	switch a := args.(type) {
	case map[string]any:
		return a, nil
	case []byte:
		if len(a) == 0 {
			return map[string]any{}, nil
		}
		var m map[string]any
		err := json.Unmarshal(a, &m)
		return m, err
	case string:
		if a == "" {
			return map[string]any{}, nil
		}
		var m map[string]any
		err := json.Unmarshal([]byte(a), &m)
		return m, err
	default:
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		err = json.Unmarshal(b, &m)
		return m, err
	}
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case int:
			return n
		case float64:
			return int(n)
		case string:
			i, _ := strconv.Atoi(n)
			return i
		}
	}
	return 0
}

func getFloat(m map[string]any, key string) (float64, bool) {
	if v, ok := m[key]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			return n, true
		case int:
			return float64(n), true
		case string:
			f, err := strconv.ParseFloat(n, 64)
			return f, err == nil
		}
	}
	return 0, false
}
