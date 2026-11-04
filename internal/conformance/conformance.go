// Package conformance runs reversible contract probes and formats their results.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/winterstim/tonelab-muse/internal/daw/fake"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

type Status string

const (
	Pass        Status = "pass"
	Fail        Status = "fail"
	Fallback    Status = "fallback"
	Unsupported Status = "unsupported"
)

type Result struct {
	Op     string `json:"op"`
	Status Status `json:"status"`
	Reason string `json:"reason"`
}

type Target struct {
	Executor   core.ContractExecutor
	Snapshot   core.RequestSnapshotter
	UndoGroups core.UndoActionGrouper
	Close      func() error
}

type Factory func() (Target, error)

type probe struct {
	args  map[string]any
	write bool
}

// FakeFactory returns a fresh empty audio session for each operation probe.
func FakeFactory() (Target, error) {
	engine := fake.New()
	for _, track := range engine.SessionTracks {
		track.HasClips = false
		track.MediaClips = nil
	}
	undo, _ := any(engine).(core.UndoActionGrouper)
	return Target{Executor: engine, Snapshot: engine, UndoGroups: undo}, nil
}

var probes = map[string]probe{
	core.OpListTracks:      {args: map[string]any{}},
	core.OpManageTrack:     {args: map[string]any{"action": "rename", "track": 1, "name": "Conformance Guitar"}, write: true},
	core.OpSetTrackProp:    {args: map[string]any{"track": 1, "prop": "volume", "value": 0.8}, write: true},
	core.OpRouteSend:       {args: map[string]any{"action": "create", "from_track": 1, "to_track": 2, "level_db": -12.0}, write: true},
	core.OpSetTransport:    {args: map[string]any{"action": "tempo", "bpm": 123.0}, write: true},
	core.OpMarkers:         {args: map[string]any{"action": "create", "position_sec": 1.0, "end_sec": 2.0, "name": "Conformance Marker", "region": true}, write: true},
	core.OpUndo:            {args: map[string]any{"action": "begin", "name": "conformance"}, write: true},
	core.OpSaveProject:     {args: map[string]any{}, write: true},
	core.OpInsertMedia:     {args: map[string]any{"track": 1, "path": "conformance.wav", "position_sec": 1.0}, write: true},
	core.OpStarterRiff:     {args: map[string]any{"track": 1}, write: true},
	core.OpRenderTrack:     {args: map[string]any{"track": 1, "seconds": 0.25}, write: true},
	core.OpImportReference: {args: map[string]any{"path": "reference-conformance.wav"}},
	core.OpRecordInput:     {args: map[string]any{"action": "record", "track": 1, "seconds": 0.1, "token": "conformance-take"}, write: true},
	core.OpCatalogFX:       {args: map[string]any{"query": "eq"}},
	core.OpManageFX:        {args: map[string]any{"action": "add", "track": 1, "name": "Overdrive (Test)", "pos": 1}, write: true},
	core.OpShowFXUI:        {args: map[string]any{"track": 1, "fx": 1, "show": true}, write: true},
	core.OpReadParams:      {args: map[string]any{"track": 1, "fx": 1}},
	core.OpSetParam:        {args: map[string]any{"track": 1, "fx": 1, "param": "fx1.p1", "value": 0.6}, write: true},
	core.OpLearnParam:      {args: map[string]any{"track": 1, "fx": 1, "param": 1, "label": "Conformance Gain"}, write: true},
	core.OpProjectState:    {args: map[string]any{"track": 1, "fx": 1}},
	core.OpVerifyWrite:     {args: map[string]any{"track": 1, "fx": 1, "param": "fx1.p1", "value": 0.6}, write: true},
	core.OpModelSwitch:     {args: map[string]any{"action": "set", "track": 1, "fx": 1, "slot": "Amp", "model": "Crunch"}, write: true},
	core.OpPresets:         {args: map[string]any{"action": "load", "track": 1, "fx": 2, "preset": "Vocal High Pass"}, write: true},
	core.OpGuideUser:       {args: map[string]any{}},
	core.OpMeasureAudio:    {args: map[string]any{}},
	core.OpMatchLoudness:   {args: map[string]any{"target_dbfs": -14.0}},
	core.OpSpectrogram:     {args: map[string]any{}},
	core.OpMixClash:        {args: map[string]any{"track": 1}},
	core.OpReferenceMatch:  {args: map[string]any{"track": 1}},
	core.OpDiagnoseSilence: {args: map[string]any{"track": 1}},
	core.OpSetMode:         {args: map[string]any{"mode": "review"}, write: true},
	core.OpSnapshot:        {args: map[string]any{"action": "capture", "name": "conformance"}, write: true},
	core.OpDelegateTask:    {args: map[string]any{"track": 1, "task": "verify readback"}},
	core.OpTokenBudget:     {args: map[string]any{"track_count": 1, "tokens_used": 100}},
	core.OpCallGuard:       {args: map[string]any{"call_id": "conformance-call", "error": ""}, write: true},
	core.OpTasteMemory:     {args: map[string]any{"action": "record", "control": "Gain", "dir": 1}, write: true},
	core.OpProcessInput:    {args: map[string]any{"kind": "text", "text": "hello"}},
	core.OpOnboardDAW:      {args: map[string]any{"daw": "Fake", "url": "https://example.invalid/docs"}},
	core.OpPlatformSetup:   {args: map[string]any{"os": "test"}},
	core.OpUIState:         {args: map[string]any{}},
	core.OpReleaseSpec:     {args: map[string]any{"os": "test"}},
}

// These operations cannot be probed safely without a whole-session restore;
// their operation-specific executor must return the precise missing API.
var safeRestoreOps = map[string]bool{
	core.OpInsertMedia: true, core.OpStarterRiff: true, core.OpRecordInput: true,
	core.OpManageFX: true, core.OpPresets: true, core.OpModelSwitch: true,
}

// Run executes all canonical capability operations in contract order.
func Run(ctx context.Context, daw string, open Factory) []Result {
	if len(core.ContractOperations) != len(core.CapabilityIDs) {
		return []Result{{Op: "contract", Status: Fail, Reason: fmt.Sprintf("operation list has %d entries for %d capability IDs", len(core.ContractOperations), len(core.CapabilityIDs))}}
	}
	seen := map[string]bool{}
	for _, op := range core.ContractOperations {
		if seen[op] {
			return []Result{{Op: "contract", Status: Fail, Reason: "duplicate operation " + op}}
		}
		seen[op] = true
		if _, ok := probes[op]; !ok {
			return []Result{{Op: "contract", Status: Fail, Reason: "missing probe for " + op}}
		}
	}

	base, err := open()
	if err != nil {
		return unsupportedAll(err.Error())
	}
	capabilities := []string{}
	if base.Executor != nil {
		result, capErr := base.Executor.Execute(ctx, core.OpCapabilities, nil)
		if capErr == nil {
			// Intersect contract rows with real DAW capabilities. Host-local
			// operations are explicitly exercised by the local executor below.
			advertised := stringList(result.Readback)
			capabilities = append(capabilities, advertised...)
		}
	}
	closeTarget(base)
	// These operations are host orchestration or local media analysis, so the
	// DAW's capability list is not a valid reason to skip exercising them.
	for _, op := range core.ContractOperations {
		if isLocalOperation(op) {
			capabilities = append(capabilities, op)
		}
	}
	available := make(map[string]bool, len(capabilities))
	for _, op := range capabilities {
		available[op] = true
	}

	results := make([]Result, 0, len(core.ContractOperations))
	for _, op := range core.ContractOperations {
		results = append(results, runOne(ctx, daw, op, probes[op], available[op], open))
	}
	return results
}

func unsupportedAll(reason string) []Result {
	out := make([]Result, 0, len(core.ContractOperations))
	for _, op := range core.ContractOperations {
		out = append(out, Result{Op: op, Status: Unsupported, Reason: reason})
	}
	return out
}

func runOne(ctx context.Context, daw, op string, p probe, advertised bool, open Factory) (result Result) {
	p.args = cloneArgs(p.args)
	if !advertised {
		return Result{Op: op, Status: Fallback, Reason: missingAPIFallback(op)}
	}
	if daw == "reaper" && op == core.OpRecordInput {
		return guidedFallback(daw, op, "live probe disabled after REAPER stopped responding")
	}
	if isLocalOperation(op) {
		local, err := FakeFactory()
		if err != nil {
			return Result{Op: op, Status: Fail, Reason: "open local executor: " + err.Error()}
		}
		defer closeTarget(local)
		got, err := local.Executor.Execute(ctx, op, p.args)
		if err != nil {
			return Result{Op: op, Status: Fail, Reason: "local executor: " + err.Error()}
		}
		if reason := compare(op, p.args, got); reason != "" {
			return Result{Op: op, Status: Fail, Reason: reason}
		}
		cleanupGenerated(got.Readback)
		return Result{Op: op, Status: Pass, Reason: "local contract executor readback matched"}
	}
	t, err := open()
	if err != nil {
		return Result{Op: op, Status: Fail, Reason: "open DAW: " + err.Error()}
	}
	defer closeTarget(t)
	if t.Executor == nil {
		return Result{Op: op, Status: Fallback, Reason: missingAPIFallback(op)}
	}
	type fixturePreparer interface {
		PrepareConformanceFixture(context.Context, string, map[string]any) (func() error, bool, error)
	}
	var fixtureCleanup func() error
	preparedFixture := false
	if preparer, ok := t.Executor.(fixturePreparer); ok {
		cleanup, prepared, setupErr := preparer.PrepareConformanceFixture(ctx, op, p.args)
		if setupErr != nil {
			return Result{Op: op, Status: Fail, Reason: "prepare DAW fixture: " + setupErr.Error()}
		}
		fixtureCleanup = cleanup
		if fixtureCleanup != nil {
			defer func() {
				if cleanupErr := fixtureCleanup(); cleanupErr != nil {
					result.Status = Fail
					result.Reason = strings.TrimSpace(result.Reason + "; cleanup fixture: " + cleanupErr.Error())
				}
			}()
		}
		preparedFixture = prepared
	}
	if !preparedFixture {
		// Select real installed targets for reversible probes rather than relying
		// on placeholders that vary across local installations.
		if op == core.OpManageFX {
			catalog, e := t.Executor.Execute(ctx, core.OpCatalogFX, map[string]any{"query": ""})
			if e == nil && len(array(catalog.Readback)) > 0 {
				if name := object(array(catalog.Readback)[0])["name"]; name != nil {
					p.args["name"] = fmt.Sprint(name)
				}
			}
		}
		prepareDeviceProbe(ctx, t.Executor, op, p.args)
	}

	var before []byte
	if t.Snapshot == nil || !t.Snapshot.SupportsRequestSnapshot() {
		if p.write && safeRestoreOps[op] {
			// The executor must either perform a fully reversible operation or
			// return a named missing API call. Do not skip because whole-session
			// snapshots are not implemented.
		}
	} else {
		before, err = t.Snapshot.CaptureRequestSnapshot(ctx)
		if err != nil {
			return Result{Op: op, Status: Fail, Reason: "capture before state: " + err.Error()}
		}
	}
	got, execErr := t.Executor.Execute(ctx, op, p.args)
	checkReason := ""
	if execErr != nil {
		checkReason = execErr.Error()
	} else {
		checkReason = compare(op, p.args, got)
	}

	if op == core.OpUndo && t.UndoGroups != nil {
		if err := t.UndoGroups.EndUndoAction(ctx, "conformance", false); err != nil {
			checkReason = errors.Join(errors.New(checkReason), fmt.Errorf("close undo group: %w", err)).Error()
		}
	}
	cleanupGenerated(got.Readback)
	if len(before) > 0 {
		restoreErr := t.Snapshot.RestoreRequestSnapshot(ctx, before)
		after, captureErr := t.Snapshot.CaptureRequestSnapshot(ctx)
		if restoreErr != nil {
			checkReason = errors.Join(errors.New(checkReason), fmt.Errorf("restore state: %w", restoreErr)).Error()
		} else if captureErr != nil {
			checkReason = errors.Join(errors.New(checkReason), fmt.Errorf("capture restored state: %w", captureErr)).Error()
		} else if !bytes.Equal(before, after) {
			checkReason = errors.Join(errors.New(checkReason), errors.New("restored state differs from captured state")).Error()
		}
	}
	if checkReason != "" {
		return Result{Op: op, Status: Fail, Reason: checkReason}
	}
	if fallbackResult(got) {
		return Result{Op: op, Status: Fallback, Reason: fallbackCause(op)}
	}
	if op == core.OpSetTrackProp {
		return Result{Op: op, Status: Pass, Reason: fmt.Sprintf("requested=%g read=%g", numValue(p.args["value"]), numValue(object(got.Readback)["confirmed"]))}
	}
	if len(before) > 0 {
		return Result{Op: op, Status: Pass, Reason: "readback matched; state restored"}
	}
	return Result{Op: op, Status: Pass, Reason: "readback matched"}
}

func cloneArgs(args map[string]any) map[string]any {
	copy := make(map[string]any, len(args))
	for key, value := range args {
		copy[key] = value
	}
	return copy
}

func fallbackCause(op string) string {
	if op == core.OpSaveProject {
		return "the save action did not confirm the saved project state"
	}
	return missingAPIFallback(op)
}

func isLocalOperation(op string) bool {
	switch op {
	case core.OpGuideUser, core.OpMeasureAudio, core.OpMatchLoudness, core.OpSpectrogram, core.OpMixClash, core.OpReferenceMatch, core.OpDiagnoseSilence,
		core.OpSetMode, core.OpSnapshot, core.OpDelegateTask, core.OpTokenBudget, core.OpCallGuard, core.OpTasteMemory, core.OpProcessInput,
		core.OpOnboardDAW, core.OpPlatformSetup, core.OpUIState, core.OpReleaseSpec:
		return true
	default:
		return false
	}
}

func missingAPIFallback(op string) string {
	if op == core.OpSaveProject {
		return "the save action did not confirm the saved project state"
	}
	if op == core.OpRouteSend {
		return "missing DAW API call: /live/song/delete_return_track is absent; creating a missing return track cannot be restored exactly"
	}
	if op == core.OpShowFXUI {
		return "missing DAW API call: /live/view/get/selected_device is absent, so the previous device selection cannot be restored"
	}
	if op == core.OpInsertMedia {
		return "missing DAW API call: exact inserted-clip deletion endpoint is absent"
	}
	if op == core.OpModelSwitch {
		return "missing DAW API call: current model readback/restore token for the selected slot is absent"
	}
	if op == core.OpLearnParam {
		return "missing DAW API call: parameter mapping learn/unlearn endpoint is absent"
	}
	if op == core.OpRecordInput {
		return "missing DAW API call: safe /live/muse/record/start and /live/muse/record/finish endpoints with exact temporary-track cleanup are absent"
	}
	if op == core.OpStarterRiff {
		return "missing DAW API call: /live/muse/midi_clip/create and /live/muse/midi_clip/delete endpoints are absent"
	}
	if op == core.OpImportReference {
		return "missing DAW API call: reference-track import with exact track deletion endpoint is absent"
	}
	if op == core.OpStarterRiff {
		return "missing DAW API call: MIDI clip creation and exact clip deletion endpoints are absent"
	}
	return "missing DAW API call: no DAW operation exists for " + op
}

func guidedFallback(daw, op, cause string) Result {
	var steps []string
	switch op {
	case core.OpSaveProject:
		steps = []string{
			"approve the OS save prompt when available",
			"save from the DAW interface",
			"confirm the unsaved marker clears",
		}
	case core.OpRecordInput:
		steps = []string{
			"arm the track",
			"select the input",
			"press record",
		}
	case core.OpRenderTrack:
		steps = []string{
			"select the source track",
			"render it through the DAW interface",
			"confirm the output file path",
		}
	case core.OpMeasureAudio, core.OpMatchLoudness, core.OpSpectrogram,
		core.OpMixClash, core.OpReferenceMatch, core.OpDiagnoseSilence:
		steps = []string{
			"render or record the target audio",
			"run local audio analysis on that file",
			"compare the report with the requested target",
		}
	case core.OpSetMode, core.OpSnapshot, core.OpDelegateTask, core.OpTokenBudget,
		core.OpCallGuard, core.OpTasteMemory, core.OpProcessInput,
		core.OpOnboardDAW, core.OpPlatformSetup, core.OpUIState,
		core.OpReleaseSpec, core.OpGuideUser:
		steps = []string{
			"use the host agent's matching control or setup flow",
			"review the proposed result",
			"confirm before making DAW changes",
		}
	case core.OpCatalogFX, core.OpManageFX, core.OpShowFXUI, core.OpReadParams,
		core.OpSetParam, core.OpLearnParam, core.OpProjectState,
		core.OpVerifyWrite, core.OpModelSwitch, core.OpPresets:
		steps = []string{
			"select the target track and device",
			"make the change in the device controls or browser",
			"read back the device or parameter value",
		}
	case core.OpInsertMedia, core.OpStarterRiff, core.OpImportReference:
		steps = []string{
			"select the destination track",
			"import or create the media through the DAW interface",
			"confirm the clip and source path",
		}
	default:
		steps = []string{
			"select the intended track or session item",
			"perform the operation in the DAW interface",
			"verify the resulting state before continuing",
		}
	}
	return Result{
		Op:     op,
		Status: Fallback,
		Reason: fmt.Sprintf("%s; guidance: %s", cause, strings.Join(numbered(steps), " ")),
	}
}

func numbered(steps []string) []string {
	out := make([]string, len(steps))
	for i, step := range steps {
		out[i] = fmt.Sprintf("%d. %s", i+1, step)
	}
	return out
}

func fallbackResult(result core.ContractResult) bool {
	for _, value := range []any{result.Applied, result.Readback} {
		m := object(value)
		if m["fallback"] == true || strings.EqualFold(fmt.Sprint(m["status"]), "fallback") {
			return true
		}
	}
	return false
}

func closeTarget(t Target) {
	if t.Close != nil {
		_ = t.Close()
	}
}

func prepareDeviceProbe(ctx context.Context, exec core.ContractExecutor, op string, args map[string]any) {
	switch op {
	case core.OpShowFXUI, core.OpReadParams, core.OpSetParam, core.OpProjectState, core.OpVerifyWrite, core.OpPresets:
	default:
		return
	}
	tracksResult, err := exec.Execute(ctx, core.OpListTracks, map[string]any{})
	if err != nil {
		return
	}
	for _, row := range array(tracksResult.Readback) {
		track := int(numValue(object(row)["index"]))
		if track < 1 {
			continue
		}
		chainResult, err := exec.Execute(ctx, core.OpReadParams, map[string]any{"track": track})
		if err != nil {
			continue
		}
		for fxIndex, fxValue := range array(chainResult.Readback) {
			fx := fxIndex + 1
			state := object(fxValue)
			if fmt.Sprint(state["name"]) == "" {
				continue
			}
			args["track"], args["fx"] = track, fx
			if op == core.OpPresets {
				list, err := exec.Execute(ctx, core.OpPresets, map[string]any{"action": "list", "track": track, "fx": fx})
				if err != nil {
					continue
				}
				for _, value := range array(object(list.Readback)["presets"]) {
					if preset, ok := value.(string); ok && preset != "" {
						args["preset"] = preset
						return
					}
				}
				continue
			}
			if op == core.OpSetParam || op == core.OpVerifyWrite {
				params := array(state["params"])
				if len(params) == 0 {
					continue
				}
				if id, ok := object(params[0])["id"].(string); ok && id != "" {
					args["param"] = id
					return
				}
				continue
			}
			return
		}
	}
}

func numValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	}
	return 0
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func compare(op string, args map[string]any, result core.ContractResult) string {
	if object(result.Applied)["fallback"] == true || object(result.Readback)["fallback"] == true {
		return ""
	}
	if result.Applied == nil || result.Readback == nil {
		return "operation did not return applied and readback values"
	}
	readback := object(result.Readback)
	applied := object(result.Applied)
	arr := array(result.Readback)
	str := func(v any) string { s, _ := v.(string); return s }
	num := func(v any) float64 {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		default:
			return 0
		}
	}
	if reason := compareApplied(op, args, applied, str, num); reason != "" {
		return reason
	}
	switch op {
	case core.OpListTracks:
		if len(arr) < 1 {
			return "track readback missing"
		}
	case core.OpManageTrack:
		if readback["name"] != args["name"] {
			return "renamed track readback differs"
		}
	case core.OpSetTrackProp:
		if math.Abs(num(readback["confirmed"])-num(args["value"])) > 1e-3 {
			return fmt.Sprintf("track property %s readback differs: requested=%g read=%g", str(args["prop"]), num(args["value"]), num(readback["confirmed"]))
		}
	case core.OpRouteSend:
		found := false
		for _, item := range arr {
			if object(item)["to_track"] == float64(2) {
				found = true
			}
		}
		if !found {
			return "created send missing from readback"
		}
	case core.OpSetTransport:
		if num(readback["bpm"]) != num(args["bpm"]) {
			return "tempo readback differs"
		}
	case core.OpMarkers:
		found := false
		for _, item := range arr {
			marker := object(item)
			if marker["name"] == args["name"] && math.Abs(num(marker["position_sec"])-num(args["position_sec"])) <= 0.001 {
				found = true
			}
		}
		if !found {
			return "created marker missing from readback"
		}
	case core.OpUndo:
		if str(readback["active_action"]) != "conformance" {
			return "undo group start not confirmed"
		}
	case core.OpSaveProject:
		if readback["saved"] != true {
			return "save readback is not saved"
		}
	case core.OpInsertMedia:
		if len(arr) == 0 {
			return "inserted media missing from readback"
		}
	case core.OpRenderTrack:
		path := str(readback["path"])
		if path == "" {
			return "render path missing from readback"
		}
		info, err := os.Stat(path)
		if err != nil {
			return "render path is unreadable: " + err.Error()
		}
		if info.IsDir() || info.Size() <= 44 {
			return fmt.Sprintf("render WAV is not a complete file: size=%d bytes", info.Size())
		}
	case core.OpImportReference, core.OpMeasureAudio:
		if str(readback["render"]) == "" {
			return "audio measurement path missing"
		}
	case core.OpRecordInput:
		if str(readback["path"]) == "" || num(readback["track"]) != num(args["track"]) {
			return "recorded clip readback differs"
		}
	case core.OpCatalogFX:
		if len(arr) == 0 {
			return "FX catalog readback empty"
		}
	case core.OpManageFX:
		found := false
		wantName := str(applied["actual_name"])
		if wantName == "" {
			wantName = fmt.Sprint(args["name"])
		}
		for _, item := range arr {
			if object(item)["name"] == wantName {
				found = true
			}
		}
		if !found {
			return "added FX missing from chain readback"
		}
	case core.OpStarterRiff:
		if readback["created"] != nil && (readback["created"] != true || readback["restored"] != true) {
			return "MIDI clip creation or restoration was not confirmed"
		} else if readback["created"] == nil && len(arr) == 0 {
			return "inserted MIDI clip missing from readback"
		}
	case core.OpShowFXUI:
		if readback["selected_track"] != nil {
			if num(readback["selected_track"]) != num(args["track"]) || num(readback["selected_fx"]) != num(args["fx"]) || readback["restored"] != true {
				return "FX device selection readback or restoration differs"
			}
		} else if readback["window_open"] != true {
			return "FX device selection readback or restoration differs"
		}
	case core.OpReadParams:
		if len(arr) == 0 {
			return "parameter readback empty"
		}
	case core.OpSetParam, core.OpVerifyWrite:
		status := strings.ToLower(str(readback["status"]))
		if readback["confirmed"] != true && status != "confirmed" {
			return "parameter write was not confirmed"
		}
	case core.OpLearnParam:
		if readback["name"] != args["label"] {
			return "learned label readback differs"
		}
	case core.OpProjectState:
		if str(readback["fx"]) == "" || len(array(readback["params"])) == 0 {
			return "project state readback incomplete"
		}
	case core.OpModelSwitch:
		if len(arr) == 0 {
			return "model readback empty"
		}
	case core.OpPresets:
		if readback["loaded"] != true && str(readback["current"]) != str(args["preset"]) {
			return "preset readback differs"
		}
		if readback["loaded"] == true && readback["restored"] != true {
			return "preset target state was not restored"
		}
	case core.OpGuideUser:
		if len(array(readback["guidance_steps"])) > 3 {
			return "guidance exceeds three steps"
		}
	case core.OpMatchLoudness:
		if num(readback["matched_dbfs"]) != num(args["target_dbfs"]) {
			return "loudness target readback differs"
		}
	case core.OpSpectrogram:
		if str(readback["path"]) == "" {
			return "spectrogram path missing"
		}
	case core.OpMixClash:
		if len(arr) == 0 {
			return "mix clash readback empty"
		}
	case core.OpReferenceMatch:
		if readback["converged"] != true || num(readback["final_gap_db"]) >= num(readback["initial_gap_db"]) {
			return "reference match did not converge"
		}
	case core.OpDiagnoseSilence:
		if readback["engine_running"] != true {
			return "audio engine state missing"
		}
	case core.OpSetMode:
		if str(readback["current_mode"]) != str(args["mode"]) {
			return "mode readback differs"
		}
	case core.OpSnapshot:
		if readback["captured"] != true {
			return "snapshot capture not confirmed"
		}
	case core.OpDelegateTask:
		if str(readback["task"]) != str(args["task"]) || str(readback["status"]) != "completed" {
			return "delegated task readback differs"
		}
	case core.OpTokenBudget:
		if readback["within_budget"] != true {
			return "token budget check failed"
		}
	case core.OpCallGuard:
		if str(readback["call_id"]) != str(args["call_id"]) {
			return "call guard readback differs"
		}
	case core.OpTasteMemory:
		found := false
		for _, item := range array(readback["leanings"]) {
			if object(item)["control"] == args["control"] {
				found = true
			}
		}
		if !found {
			return "taste memory readback missing the recorded leaning"
		}
	case core.OpProcessInput:
		if readback["processed"] != true {
			return "input processing not confirmed"
		}
	case core.OpOnboardDAW:
		if readback["verified"] != true {
			return "DAW onboarding not verified"
		}
	case core.OpPlatformSetup:
		if readback["osc_listening"] != true {
			return "platform setup readback incomplete"
		}
	case core.OpUIState:
		if num(readback["track_count"]) <= 0 {
			return "UI state track count missing"
		}
	case core.OpReleaseSpec:
		if readback["ready"] != true {
			return "release spec not ready"
		}
	}
	return ""
}

func compareApplied(op string, args, applied map[string]any, str func(any) string, num func(any) float64) string {
	stringField := func(field string) bool { return str(applied[field]) == str(args[field]) }
	numberField := func(field string) bool { return num(applied[field]) == num(args[field]) }
	checkStrings := func(fields ...string) bool {
		for _, field := range fields {
			if !stringField(field) {
				return false
			}
		}
		return true
	}
	checkNumbers := func(fields ...string) bool {
		for _, field := range fields {
			if !numberField(field) {
				return false
			}
		}
		return true
	}
	switch op {
	case core.OpManageTrack:
		if !stringField("name") || !numberField("track") {
			return "applied track rename differs from request"
		}
	case core.OpSetTrackProp:
		if !checkStrings("prop") || !checkNumbers("track", "value") {
			return "applied track property differs from request"
		}
	case core.OpRouteSend:
		if !checkNumbers("from_track", "to_track", "level_db") {
			return "applied send differs from request"
		}
	case core.OpSetTransport:
		if !checkStrings("action") || !numberField("bpm") {
			return "applied transport differs from request"
		}
	case core.OpMarkers:
		if !stringField("name") || !checkNumbers("position_sec", "end_sec") {
			return "applied marker differs from request"
		}
	case core.OpUndo:
		if str(applied["action"]) != "begin" || str(applied["name"]) != "conformance" {
			return "applied undo group differs from probe"
		}
	case core.OpSaveProject:
		if str(applied["action"]) != "save_project" {
			return "applied save action differs"
		}
	case core.OpInsertMedia:
		if !checkNumbers("track", "position_sec") || !stringField("path") {
			return "applied media insert differs from request"
		}
	case core.OpStarterRiff:
		if !numberField("track") {
			return "applied starter riff track differs from request"
		}
	case core.OpRenderTrack:
		// Rendering uses a disposable source track created inside the operation;
		// its DAW index intentionally differs from the requested probe index.
	case core.OpRecordInput:
		if !numberField("track") || str(applied["action"]) != "record" {
			return "applied input recording differs from request"
		}
	case core.OpManageFX:
		if str(applied["action"]) != "add" || !numberField("track") || !stringField("name") {
			return "applied FX insert differs from request"
		}
	case core.OpShowFXUI:
		if !checkNumbers("track", "fx") || applied["show"] != true {
			return "applied FX UI state differs from request"
		}
	case core.OpSetParam, core.OpVerifyWrite:
		if !numberField("track") || !numberField("value") {
			return "applied parameter write differs from request"
		}
	case core.OpLearnParam:
		if !checkNumbers("track", "fx", "param") || !stringField("label") {
			return "applied parameter label differs from request"
		}
	case core.OpModelSwitch:
		if !checkStrings("action", "slot", "model") || !checkNumbers("track", "fx") {
			return "applied model switch differs from request"
		}
	case core.OpPresets:
		if !stringField("preset") || !checkNumbers("track", "fx") {
			return "applied preset differs from request"
		}
	case core.OpSetMode:
		if !stringField("mode") {
			return "applied mode differs from request"
		}
	case core.OpSnapshot:
		if str(applied["action"]) != "capture" || str(applied["name"]) != "conformance" {
			return "applied snapshot action differs from probe"
		}
	case core.OpCallGuard:
		if !stringField("call_id") {
			return "applied call guard differs from request"
		}
	case core.OpTasteMemory:
		if !stringField("control") || !numberField("dir") {
			return "applied taste entry differs from request"
		}
	}
	return ""
}

func object(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func array(v any) []any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []any
	_ = json.Unmarshal(b, &out)
	return out
}

func cleanupGenerated(v any) {
	root := filepath.Clean(filepath.Join(os.TempDir(), "tonelab-muse-fake-render")) + string(os.PathSeparator)
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, value := range x {
				if strings.EqualFold(key, "path") {
					if path, ok := value.(string); ok {
						clean := filepath.Clean(path)
						if strings.HasPrefix(clean, root) {
							_ = os.Remove(clean)
						}
					}
				}
				walk(value)
			}
		case []any:
			for _, value := range x {
				walk(value)
			}
		}
	}
	walk(jsonValue(v))
}

// Markdown returns the human-readable conformance table.
func Markdown(results []Result) string {
	var b strings.Builder
	b.WriteString("| op | status | reason |\n|---|---|---|\n")
	for _, result := range results {
		reason := strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ").Replace(result.Reason)
		fmt.Fprintf(&b, "| %s | %s | %s |\n", result.Op, result.Status, reason)
	}
	return b.String()
}

// WriteJSON writes results as an indented JSON array.
func WriteJSON(path string, results []Result) error {
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// AllPass reports whether exactly 46 operation results passed.
func AllPass(results []Result) bool {
	if len(results) != len(core.CapabilityIDs) {
		return false
	}
	for _, result := range results {
		if result.Status != Pass {
			return false
		}
	}
	return true
}
