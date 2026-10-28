package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func tool(name, desc, schema string) core.Tool {
	return core.Tool{Name: name, Description: desc, Schema: json.RawMessage(schema)}
}

func mutationTool(name string) bool {
	switch name {
	case "apply_plan", "set_track", "add_plugin", "remove_plugin", "move_plugin", "load_preset", "program_change", "set_plugin_model", "manage_track", "insert_media", "route_send", "set_transport", "recall_snapshot":
		return true
	default:
		return false
	}
}

const trackOnly = `{"type":"object","properties":{"track":{"type":"integer","description":"track index from list_tracks"}},"required":["track"]}`

func (a *Agent) tools(mode Mode) []core.Tool {
	ts := []core.Tool{
		tool("list_tracks", "List the project's tracks with index, name and audio (true when the track holds audio rather than MIDI). Guitar audio lives on the audio tracks: put the amp, cabinet and effects on the audio track that holds the guitar, not on an empty MIDI track.", `{"type":"object","properties":{}}`),
		tool("describe_chain", "List every plugin on a track with all its parameters: id, name, kind (\"\" continuous, switch, discrete), the DAW's readout text and the normalised value 0..1.", trackOnly),
		tool("search_params", "Find parameters on a track by words, e.g. 'gain', 'low shelf', 'presence'. Returns the best 20.",
			`{"type":"object","properties":{"track":{"type":"integer"},"query":{"type":"string"}},"required":["track","query"]}`),
		tool("propose_tone", "Propose a whole tone as one plan. Does not change anything yet; validates ids and ranges and returns the plan with readable names.",
			`{"type":"object","properties":{"track":{"type":"integer"},"goal":{"type":"string"},"changes":{"type":"array","items":{"type":"object","properties":{"param_id":{"type":"string"},"value":{"type":"number","description":"normalised 0..1"},"target":{"type":"string","description":"desired readout string, e.g. \"-3 dB\", \"400 Hz\", \"7\", or choice name for switches/lists. Do not pass 'value' or 'text'."},"why":{"type":"string"}},"required":["param_id","why"]}}},"required":["track","goal","changes"]}`),
		tool("set_track", "Set a track's volume (0..1), pan (0..1, 0.5 = centre), mute or solo (0/1).",
			`{"type":"object","properties":{"track":{"type":"integer"},"property":{"type":"string","enum":["volume","pan","mute","solo"]},"value":{"type":"number"}},"required":["track","property","value"]}`),
	}
	if mode != Preview && a.inputRecorder() != nil {
		ts = append(ts, tool("record_input", "Record the user playing into the chosen track for a bounded duration; requires mode consent. Stops/disarms and verifies a new clip, used by listen.", `{"type":"object","properties":{"track":{"type":"integer"},"seconds":{"type":"number","minimum":0.1,"maximum":120}},"required":["track","seconds"]}`))
	}
	if mode != Preview {
		ts = append(ts,
			tool("apply_plan", "Apply a proposed plan exactly as stored. Returns per change what the DAW confirmed.",
				`{"type":"object","properties":{"plan_id":{"type":"string"}},"required":["plan_id"]}`),
			tool("undo", "Restore the values the last applied plan replaced.", `{"type":"object","properties":{}}`))
	}
	if a.d.Renderer != nil && a.d.Measurer != nil && a.d.Visualizer != nil && mode != Preview {
		ts = append(ts, tool("listen", "Render a few seconds of the track through its current chain, measure it and show you its spectrogram next to the previous render. Use after apply_plan to evaluate the result.", trackOnly))
	}
	if a.d.Capturer != nil {
		ts = append(ts, tool("learn_plugin", "Learn which on-screen control each unnamed parameter of a plugin moves, by moving them one at a time and looking at the plugin window. Saves a profile so names show up in describe_chain afterwards.",
			`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"},"params":{"type":"array","items":{"type":"string"},"description":"param ids to learn; empty = every unnamed one (max 16)"}},"required":["track","fx"]}`))
	}
	if a.d.Renderer != nil && a.d.Measurer != nil && a.d.Visualizer != nil {
		ts = append(ts, tool("match_reference", "Compare the track with the reference recording the user added: renders the track and returns the gap per band (reference minus now, loudness removed) and both spectrograms. Use it first for 'sound like this' requests, then plan to close the biggest gaps, then listen.", trackOnly))
	}
	if a.d.Segmenter != nil {
		ts = append(ts, tool("read_photo", "Read the knobs and switches in the photo the person added of their real amp or pedal: finds every control (SAM 3.1), reads each label and pointer position from a close-up, and says what the gear is. Then dial the same settings into the track's plugins with one propose_tone.", `{"type":"object","properties":{}}`))
	}
	if a.d.Mix != nil && a.d.Renderer != nil && a.d.Visualizer != nil && mode != Preview {
		ts = append(ts, tool("check_mix", "For 'sit in the mix', 'it gets lost', 'fights the bass': renders this track alone and the rest of the mix, and returns the frequency ranges where they mask each other, worst first, with both spectrograms.", trackOnly))
	}
	if a.d.Chain != nil && mode != Preview {
		ts = append(ts,
			tool("list_plugins", "Search the effects installed in the DAW that you may add (the DAW's own list, with its category when it has one). query: words that must all appear in the name or category, e.g. \"gate\", \"eq\", \"amp\", \"reverb\"; empty lists from the start.", `{"type":"object","properties":{"query":{"type":"string"}}}`),
			tool("add_plugin", "Add a plugin from list_plugins to a track. position is 1-based in the chain; 0 = at the end. Search list_plugins first and add only what the sound will use: never add a plugin just to look at it and remove it again. Use when the chain lacks something the sound needs (a gate, a drive, an amp/cab, an EQ), then tune it with propose_tone.",
				`{"type":"object","properties":{"track":{"type":"integer"},"plugin":{"type":"string"},"position":{"type":"integer"}},"required":["track","plugin"]}`),
			tool("remove_plugin", "Remove a plugin from a track by its fx index. Only remove plugins you added in this conversation unless the user asks.",
				`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer"}},"required":["track","fx"]}`),
			tool("move_plugin", "Move a plugin to a new position in the chain. to_position is 1-based.",
				`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"},"to_position":{"type":"integer","description":"new 1-based position in the chain"}},"required":["track","fx","to_position"]}`))
	}
	if a.d.Chain != nil || a.d.DAW != nil {
		ts = append(ts, tool("list_presets", "List the available presets for a plugin in the chain.",
			`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"}},"required":["track","fx"]}`))
		if mode != Preview {
			ts = append(ts, tool("program_change", "Switch a plugin to one of its own programs by MIDI program change: program 0-127, channel 1-16 (default 1). The numbers are the plugin's preset list; list_presets gives the names when they are readable. Use it when the plugin takes a preset or model change this way rather than as parameter changes. Listen after it.",
				`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"},"program":{"type":"integer","description":"0-127"},"channel":{"type":"integer","description":"1-16, default 1"}},"required":["track","fx","program"]}`))
			ts = append(ts, tool("load_preset", "Load a preset by name into a plugin.",
				`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"},"preset":{"type":"string","description":"preset name exactly as returned by list_presets"}},"required":["track","fx","preset"]}`))
			if _, ok := a.presetSaver(); ok {
				ts = append(ts, tool("save_preset_as", "Save a plugin's current settings as a named preset through the DAW.",
					`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer","description":"plugin index from describe_chain"},"name":{"type":"string"}},"required":["track","fx","name"]}`))
			}
		}
	}
	if a.d.Snapshots != nil {
		ts = append(ts,
			tool("save_snapshot", "Save the track's current tone under a name, e.g. 'clean verse', 'lead'.",
				`{"type":"object","properties":{"track":{"type":"integer"},"name":{"type":"string"}},"required":["track","name"]}`),
			tool("list_snapshots", "List saved tones. A 'Before: …' snapshot is saved automatically before each request's first change.", `{"type":"object","properties":{}}`))
		if mode != Preview {
			ts = append(ts, tool("recall_snapshot", "Bring a saved tone back (only the controls that differ; undoable).",
				`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`))
		}
	}
	if a.modelSwapper() != nil {
		ts = append(ts, tool("list_plugin_models", "List internal modular slots (e.g. AmpA, CabA, StompA1.Slot0, RackA.Slot0) and dynamically discovered models for a plugin.",
			`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer"},"slot":{"type":"string","description":"optional slot filter, e.g. AmpA or StompA1"}},"required":["track","fx"]}`))
		if mode != Preview {
			ts = append(ts, tool("set_plugin_model", "Swap the internal amp, cab, pedal, or routing model in a plugin slot (e.g. slot='AmpA', model='Diezel', or slot='StompA1.Slot0', model='Drive' or 'Empty') and return the newly exposed parameters.",
				`{"type":"object","properties":{"track":{"type":"integer"},"fx":{"type":"integer"},"slot":{"type":"string"},"model":{"type":"string"}},"required":["track","fx","slot","model"]}`))
		}
	}
	if a.sessionController() != nil && mode != Preview {
		ts = append(ts,
			tool("manage_track", "Create, duplicate, delete, or rename a track in the DAW.",
				`{"type":"object","properties":{"action":{"type":"string","enum":["create","duplicate","delete","rename"]},"track":{"type":"integer","description":"1-based track index (for duplicate, delete, rename; for create, 0 or omit appends to the end of the project)"},"name":{"type":"string"}},"required":["action"]}`),
			tool("insert_media", "Insert an existing audio file (WAV, AIFF, FLAC, MP3) onto a track at a position in seconds.",
				`{"type":"object","properties":{"track":{"type":"integer"},"path":{"type":"string"},"position_sec":{"type":"number"}},"required":["track","path"]}`),
			tool("route_send", "Create a send from one track to another (e.g. to a reverb/delay bus) at level_db.",
				`{"type":"object","properties":{"from_track":{"type":"integer"},"to_track":{"type":"integer"},"level_db":{"type":"number"}},"required":["from_track","to_track"]}`),
			tool("set_transport", "Control DAW transport, project tempo (BPM), cursor position, or loop range.",
				`{"type":"object","properties":{"action":{"type":"string","enum":["play","stop","tempo","loop","cursor"]},"bpm":{"type":"number"},"position_sec":{"type":"number"},"loop_start":{"type":"number"},"loop_end":{"type":"number"}},"required":["action"]}`),
		)
	}
	if mode != Preview {
		if a.hasSessionFeature("track_controls") {
			ts = append(ts, tool("set_track_input", "Set a track's record arm, monitoring mode, or input using values returned by the DAW.", `{"type":"object","properties":{"track":{"type":"integer"},"control":{"type":"string","enum":["arm","monitor","input"]},"control_value":{"type":"string"}},"required":["track","control","control_value"]}`))
		}
		if a.hasSessionFeature("routing") {
			ts = append(ts, tool("remove_send", "Remove a send by its 1-based index on the source track.", `{"type":"object","properties":{"from_track":{"type":"integer"},"send":{"type":"integer"}},"required":["from_track","send"]}`))
		}
		if a.hasSessionFeature("markers") {
			ts = append(ts,
				tool("list_markers", "List project markers and regions.", `{"type":"object","properties":{}}`),
				tool("create_marker", "Create a marker or region and confirm it from the DAW.", `{"type":"object","properties":{"position_sec":{"type":"number"},"end_sec":{"type":"number"},"name":{"type":"string"},"region":{"type":"boolean"}},"required":["position_sec","name"]}`),
				tool("goto_marker", "Move the cursor to a marker by its index.", `{"type":"object","properties":{"index":{"type":"integer"}},"required":["index"]}`))
		}
		if a.hasSessionFeature("meter") {
			ts = append(ts, tool("set_time_signature", "Set the project time signature.", `{"type":"object","properties":{"numerator":{"type":"integer"},"denominator":{"type":"integer"}},"required":["numerator","denominator"]}`))
		}
		if a.hasSessionFeature("project") {
			ts = append(ts, tool("save_project", "Save the current project.", `{"type":"object","properties":{}}`))
		}
	}
	if mode != Preview {
		ts = append(ts, tool("delegate_track_task", "Delegate a self-contained sound-design or chain-building task on a specific track to an isolated track subagent with its own clean context window and step budget. Use this when working on multi-track requests (e.g. dialing rhythm guitar on Track 1, lead guitar on Track 2, and a reverb bus on Track 3) so intermediate chain dumps and spectrograms do not crowd the main mix session context.",
			`{"type":"object","properties":{"track":{"type":"integer","description":"1-based track number to focus on"},"goal":{"type":"string","description":"clear instruction for what to achieve on that track"}},"required":["track","goal"]}`))
	}
	return ts
}

// call runs one tool. It returns the JSON-able result and any images the
// model should see on its next turn. An identical call already made in this
// conversation is not run again: a failed one returns its error, and a
// read-only one says its answer is already above.
func (a *Agent) call(ctx context.Context, tc core.ToolCall) (any, []core.Part, error) {
	key := repeatKey(ctx, tc)
	if prev, ok := a.repeatOf(key); ok {
		if prev.err != "" {
			return nil, nil, errors.New("this exact call already failed in this conversation with: " + prev.err + ". Change the arguments or use another route.")
		}
		if readOnlyTools[tc.Name] {
			return map[string]any{"already_done": true, "note": "Same call as earlier in this conversation; its result is already above. Use that."}, nil, nil
		}
	}
	out, parts, err := a.callOnce(ctx, tc)
	e := ""
	if err != nil {
		e = err.Error()
	}
	if !transientError(e) {
		a.rememberCall(key, e)
	}
	return out, parts, err
}

func (a *Agent) callOnce(ctx context.Context, tc core.ToolCall) (any, []core.Part, error) {
	var in struct {
		Track       int           `json:"track"`
		Seconds     float64       `json:"seconds"`
		Query       string        `json:"query"`
		Goal        string        `json:"goal"`
		Changes     []core.Change `json:"changes"`
		PlanID      string        `json:"plan_id"`
		Property    string        `json:"property"`
		Value       float64       `json:"value"`
		FX          int           `json:"fx"`
		Name        string        `json:"name"`
		Plugin      string        `json:"plugin"`
		Position    int           `json:"position"`
		ToPosition  int           `json:"to_position"`
		Preset      string        `json:"preset"`
		Program     int           `json:"program"`
		Channel     int           `json:"channel"`
		Look        bool          `json:"look"`
		Params      []string      `json:"params"`
		Slot        string        `json:"slot"`
		Model       string        `json:"model"`
		Action      string        `json:"action"`
		Path        string        `json:"path"`
		PositionSec float64       `json:"position_sec"`
		FromTrack   int           `json:"from_track"`
		ToTrack     int           `json:"to_track"`
		Send        int           `json:"send"`
		Control     string        `json:"control"`
		StringValue string        `json:"control_value"`
		Index       int           `json:"index"`
		EndSec      float64       `json:"end_sec"`
		Region      bool          `json:"region"`
		Numerator   int           `json:"numerator"`
		Denominator int           `json:"denominator"`
		LevelDB     float64       `json:"level_db"`
		BPM         float64       `json:"bpm"`
		LoopStart   float64       `json:"loop_start"`
		LoopEnd     float64       `json:"loop_end"`
	}
	if len(tc.Args) > 0 {
		if err := json.Unmarshal(tc.Args, &in); err != nil {
			return nil, nil, fmt.Errorf("bad arguments: %w", err)
		}
	}
	if mutationTool(tc.Name) {
		track := in.Track
		if track == 0 {
			track = in.FromTrack
		}
		if track == 0 {
			track = 1
		}
		if err := a.autoSnapshot(ctx, track); err != nil {
			return nil, nil, err
		}
	}
	switch tc.Name {
	case "list_tracks":
		ts, err := a.d.DAW.Tracks(ctx)
		return map[string]any{"tracks": ts}, nil, err
	case "describe_chain":
		ch, err := a.chain(ctx, in.Track, true)
		if err != nil {
			return nil, nil, err
		}
		limit := 32
		if scopeOf(ctx) != "parent" {
			limit = subagentParamsPerPlugin
		}
		fxs := compactChainN(ch, limit)
		a.annotateReadOnly(fxs)
		fp := chainFingerprint(fxs)
		if a.seenChain(scopeOf(ctx), in.Track, fp) {
			return map[string]any{"track": in.Track, "unchanged": true,
				"note": "Unchanged since your last describe_chain of this track in this conversation. Use the values you already have, or search_params for one control."}, nil, nil
		}
		return map[string]any{"track": in.Track, "fx": fxs}, nil, nil
	case "search_params":
		ch, err := a.chain(ctx, in.Track, false)
		if err != nil {
			return nil, nil, err
		}
		hits := search(ch, in.Query, 20)
		for _, h := range hits {
			if name, _ := h["plugin"].(string); a.isReadOnly(name) {
				h["read_only"] = true
			}
		}
		return map[string]any{"params": hits}, nil, nil
	case "propose_tone":
		return a.propose(ctx, in.Track, in.Goal, in.Changes)
	case "apply_plan":
		out, err := a.applyPlan(ctx, in.PlanID)
		return out, nil, err
	case "undo": // the model's own undo says nothing about the user's taste
		out, err := a.undo(ctx)
		return out, nil, err
	case "set_track":
		v, err := a.d.DAW.SetTrack(ctx, in.Track, in.Property, in.Value)
		return map[string]any{"property": in.Property, "confirmed": v}, nil, err
	case "listen":
		out, parts, err := a.listen(ctx, in.Track)
		if scopeOf(ctx) != "parent" {
			if m, ok := out.(map[string]any); ok && !in.Look {
				delete(m, "spectrogram")
				m["spectrogram"] = "not shown: call listen with look true to see it"
			}
			if !in.Look {
				parts = nil
			}
		}
		return out, parts, err
	case "list_plugins":
		out, err := a.findPlugins(ctx, in.Query, 60)
		return out, nil, err
	case "add_plugin":
		out, err := a.addPlugin(ctx, in.Track, in.Plugin, in.Position)
		return out, nil, err
	case "remove_plugin":
		out, err := a.removePlugin(ctx, in.Track, in.FX)
		return out, nil, err
	case "move_plugin":
		toPos := in.ToPosition
		if toPos == 0 {
			toPos = in.Position
		}
		out, err := a.movePlugin(ctx, in.Track, in.FX, toPos)
		return out, nil, err
	case "list_presets":
		out, err := a.listPresets(ctx, in.Track, in.FX)
		return out, nil, err
	case "record_input":
		out, err := a.recordInput(ctx, in.Track, in.Seconds)
		return out, nil, err
	case "program_change":
		out, err := a.programChange(ctx, in.Track, in.FX, in.Program, in.Channel)
		return out, nil, err
	case "load_preset":
		out, err := a.loadPreset(ctx, in.Track, in.FX, in.Preset)
		return out, nil, err
	case "save_preset_as":
		out, err := a.savePresetAs(ctx, in.Track, in.FX, in.Name)
		return out, nil, err
	case "check_mix":
		return a.checkMix(ctx, in.Track)
	case "read_photo":
		return a.readPhoto(ctx)
	case "match_reference":
		return a.matchReference(ctx, in.Track)
	case "save_snapshot":
		out, err := a.saveSnapshot(ctx, in.Track, in.Name)
		return out, nil, err
	case "list_snapshots":
		if a.d.Snapshots == nil {
			return nil, nil, errors.New("snapshots are not set up")
		}
		list, err := a.d.Snapshots.Snapshots()
		var names []map[string]any
		for _, s := range list {
			scope := any(s.Track)
			if len(s.Tracks) > 0 {
				scope = "all tracks"
			}
			names = append(names, map[string]any{"name": s.Name, "track": scope, "saved": s.Saved.Format("15:04")})
		}
		return names, nil, err
	case "recall_snapshot":
		out, err := a.RecallSnapshot(ctx, in.Name)
		return out, nil, err
	case "learn_plugin":
		return a.learn(ctx, in.Track, in.FX, in.Params)
	case "list_plugin_models":
		out, err := a.listPluginModels(ctx, in.Track, in.FX, in.Slot)
		return out, nil, err
	case "set_plugin_model":
		out, err := a.setPluginModel(ctx, in.Track, in.FX, in.Slot, in.Model)
		return out, nil, err
	case "manage_track":
		out, err := a.manageTrack(ctx, in.Action, in.Track, in.Name)
		return out, nil, err
	case "insert_media":
		out, err := a.insertMedia(ctx, in.Track, in.Path, in.PositionSec)
		return out, nil, err
	case "route_send":
		out, err := a.routeSend(ctx, in.FromTrack, in.ToTrack, in.LevelDB)
		return out, nil, err
	case "set_transport":
		out, err := a.setTransport(ctx, in.Action, in.BPM, in.PositionSec, in.LoopStart, in.LoopEnd)
		return out, nil, err
	case "set_track_input":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not change track input controls")
		}
		ctrl, ok := a.sessionInterface("track_controls").(core.SessionTrackControls)
		if !ok {
			return nil, nil, fmt.Errorf("track controls unsupported")
		}
		got, err := ctrl.SetTrackControl(ctx, in.Track, in.Control, in.StringValue)
		return map[string]any{"track": in.Track, "control": in.Control, "confirmed": got}, nil, err
	case "remove_send":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not remove sends")
		}
		if a.currentMode() != Turbo && !a.isAllowed("session") {
			a.emit("session_consent", map[string]any{"action": "remove_send", "kind": "session", "question": "Remove this send from the project?"})
			return needsPermission("The user sees a one-time card to allow session changes. Stop and explain which send you want removed and why."), nil, nil
		}
		route, ok := a.sessionInterface("routing").(core.SessionRouting)
		if !ok {
			return nil, nil, fmt.Errorf("send removal unsupported")
		}
		err := route.RemoveSend(ctx, in.FromTrack, in.Send)
		return map[string]any{"removed_send": in.Send, "from_track": in.FromTrack}, nil, err
	case "list_markers":
		markers, ok := a.sessionInterface("markers").(core.SessionMarkers)
		if !ok {
			return nil, nil, fmt.Errorf("markers unsupported")
		}
		out, err := markers.Markers(ctx)
		return map[string]any{"markers": out}, nil, err
	case "create_marker":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not change project markers")
		}
		markers, ok := a.sessionInterface("markers").(core.SessionMarkers)
		if !ok {
			return nil, nil, fmt.Errorf("markers unsupported")
		}
		i, err := markers.CreateMarker(ctx, in.PositionSec, in.EndSec, in.Name, in.Region)
		return map[string]any{"index": i, "name": in.Name}, nil, err
	case "goto_marker":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not move the project cursor")
		}
		markers, ok := a.sessionInterface("markers").(core.SessionMarkers)
		if !ok {
			return nil, nil, fmt.Errorf("markers unsupported")
		}
		err := markers.GoToMarker(ctx, in.Index)
		return map[string]any{"index": in.Index}, nil, err
	case "set_time_signature":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not change project settings")
		}
		meter, ok := a.sessionInterface("meter").(core.SessionMeter)
		if !ok {
			return nil, nil, fmt.Errorf("time signature unsupported")
		}
		err := meter.SetTimeSignature(ctx, in.Numerator, in.Denominator)
		return map[string]any{"numerator": in.Numerator, "denominator": in.Denominator}, nil, err
	case "save_project":
		if a.currentMode() == Preview {
			return nil, nil, errors.New("review mode does not save project changes")
		}
		if a.currentMode() != Turbo && !a.isAllowed("session") {
			a.emit("session_consent", map[string]any{"action": "save_project", "kind": "session", "question": "Save the current project to its existing file?"})
			return needsPermission("The user sees a one-time card to allow session changes. Stop and explain that this saves the current session to its existing file."), nil, nil
		}
		p, ok := a.sessionInterface("project").(core.SessionProject)
		if !ok {
			return nil, nil, fmt.Errorf("project save unsupported")
		}
		err := p.SaveProject(ctx)
		return map[string]any{"saved": err == nil}, nil, err
	case "delegate_track_task":
		if in.Track <= 0 {
			return nil, nil, errors.New("track must be >= 1")
		}
		if in.Goal == "" {
			return nil, nil, errors.New("goal is required")
		}
		out, err := a.runSubagent(ctx, in.Track, in.Goal)
		return out, nil, err
	}
	return nil, nil, fmt.Errorf("unknown tool %q", tc.Name)
}

// chain reads a track's FX and merges learned profile labels in.
func (a *Agent) chain(ctx context.Context, track int, fresh bool) ([]core.FX, error) {
	a.mu.Lock()
	cached, ok := a.chains[track]
	a.mu.Unlock()
	if ok && !fresh {
		return cached, nil
	}
	ch, err := a.d.DAW.FXChain(ctx, track)
	if err != nil {
		return nil, err
	}
	for i := range ch {
		var prof *core.Profile
		if a.d.Store != nil {
			prof, _ = a.d.Store.Profile(ch[i].Name)
		}
		if prof == nil {
			continue
		}
		for j, p := range ch[i].Params {
			_, n, _ := p.ID.Parse()
			if e, ok := prof.Map[n]; ok {
				ch[i].Params[j].Name = e.Label
				ch[i].Params[j].Unnamed = false
				if e.Kind == "switch" {
					ch[i].Params[j].Kind = "switch"
				}
			}
		}
	}
	a.mu.Lock()
	a.chains[track] = ch
	a.mu.Unlock()
	return ch, nil
}

func find(ch []core.FX, id core.ParamID) (core.Param, bool) {
	for _, f := range ch {
		for _, p := range f.Params {
			if p.ID == id {
				return p, true
			}
		}
	}
	return core.Param{}, false
}

func search(ch []core.FX, query string, n int) []map[string]any {
	words := strings.Fields(strings.ToLower(query))
	type hit struct {
		score int
		fx    string
		p     core.Param
	}
	var hits []hit
	for _, f := range ch {
		for _, p := range f.Params {
			name := strings.ToLower(p.Name + " " + f.Name)
			s := 0
			for _, w := range words {
				if strings.Contains(name, w) {
					s += 2
					if strings.HasPrefix(strings.ToLower(p.Name), w) {
						s++
					}
				}
			}
			if s > 0 {
				hits = append(hits, hit{s, f.Name, p})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []map[string]any
	for i, h := range hits {
		if i == n {
			break
		}
		out = append(out, map[string]any{"id": h.p.ID, "name": h.p.Name, "plugin": h.fx, "kind": h.p.Kind, "text": h.p.Text, "value": h.p.Value})
	}
	return out
}

// propose validates and stores a plan; nothing touches the DAW.
func (a *Agent) propose(ctx context.Context, track int, goal string, changes []core.Change) (any, []core.Part, error) {
	if len(changes) == 0 {
		return nil, nil, errors.New("a plan needs at least one change")
	}
	ch, err := a.chain(ctx, track, false)
	if err != nil {
		return nil, nil, err
	}
	for i, c := range changes {
		if c.Target == "" {
			continue
		}
		if c.Target == "value" || c.Target == "text" {
			return nil, nil, fmt.Errorf("%s target %q: do not pass the word %q; specify the desired readout string (e.g. \"3.0 dB\") or use the normalized value number (0..1)", c.Param, c.Target, c.Target)
		}
		v, err := a.findValue(ctx, track, c.Param, c.Target)
		if err != nil {
			if errors.Is(err, core.ErrNeedsConsent) {
				return needsPermission(probeQ), nil, nil
			}
			return nil, nil, fmt.Errorf("%s target %q: %w", c.Param, c.Target, err)
		}
		changes[i].Value = v
	}
	for _, c := range changes {
		if plugin := pluginOf(ch, c.Param); a.isReadOnly(plugin) {
			return nil, nil, errors.New(readOnlyMessage(plugin))
		}
	}
	plan, warnings, err := Validate(track, goal, changes, ch)
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	a.nextID++
	plan.ID = fmt.Sprintf("p%d", a.nextID)
	a.plans[plan.ID] = plan
	a.mu.Unlock()
	var rows []map[string]any
	for _, c := range plan.Changes {
		p, _ := find(ch, c.Param)
		rows = append(rows, map[string]any{"param_id": c.Param, "name": p.Name, "from": p.Value, "from_text": p.Text, "to": c.Value, "why": c.Why})
	}
	out := map[string]any{"plan_id": plan.ID, "track": track, "goal": goal, "changes": rows, "warnings": warnings}
	a.emit("plan", out)
	return out, nil, nil
}

// Validate checks a plan against the chain: ids exist, values in 0..1,
// switches 0 or 1. It clamps and snaps rather than refusing, and says so.
func Validate(track int, goal string, changes []core.Change, ch []core.FX) (*core.Plan, []string, error) {
	plan := &core.Plan{Track: track, Goal: goal}
	var warnings []string
	seen := map[core.ParamID]bool{}
	for _, c := range changes {
		p, ok := find(ch, c.Param)
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s (use describe_chain ids)", core.ErrUnknownParam, c.Param)
		}
		if seen[c.Param] {
			warnings = append(warnings, fmt.Sprintf("%s listed twice: kept the last value", c.Param))
			for i := range plan.Changes {
				if plan.Changes[i].Param == c.Param {
					plan.Changes = append(plan.Changes[:i], plan.Changes[i+1:]...)
					break
				}
			}
		}
		if p.Unnamed {
			return nil, nil, fmt.Errorf("%s (%s) is an unnamed slot: nothing known is connected to it. Leave it out, or run learn_plugin to find out what it moves", c.Param, p.Name)
		}
		seen[c.Param] = true
		v := c.Value
		if math.IsNaN(v) {
			return nil, nil, fmt.Errorf("%s: value is not a number", c.Param)
		}
		if v < 0 || v > 1 {
			warnings = append(warnings, fmt.Sprintf("%s: %.3f clamped to 0..1", p.Name, v))
			v = math.Max(0, math.Min(1, v))
		}
		if p.Kind == "switch" && v != 0 && v != 1 {
			s := math.Round(v)
			warnings = append(warnings, fmt.Sprintf("%s is a switch: %.2f snapped to %.0f", p.Name, v, s))
			v = s
		}
		if p.Steps > 0 { // a stepped or enum control lands only on its steps
			s := math.Round(v*float64(p.Steps)) / float64(p.Steps)
			if math.Abs(s-v) > 1e-9 {
				warnings = append(warnings, fmt.Sprintf("%s has %d steps: %.3f snapped to %.3f", p.Name, p.Steps, v, s))
				v = s
			}
		}
		plan.Changes = append(plan.Changes, core.Change{Param: c.Param, Value: v, Why: c.Why, Target: c.Target})
	}
	return plan, warnings, nil
}

type batchSetter interface {
	SetParams(ctx context.Context, track int, changes []core.Change) ([]core.Applied, error)
}

func (a *Agent) applyPlan(ctx context.Context, id string) (any, error) {
	a.undoMu.Lock()
	defer a.undoMu.Unlock()
	a.mu.Lock()
	plan, ok := a.plans[id]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no plan %q: call propose_tone first", id)
	}
	ch, err := a.chain(ctx, plan.Track, false)
	if err != nil {
		return nil, err
	}
	// the first listen needs something to compare with: capture the sound
	// as it was before anything changed
	a.mu.Lock()
	needBaseline := a.lastWAV == "" && a.d.Renderer != nil
	a.mu.Unlock()
	if needBaseline {
		if wav, err := a.renderAlone(ctx, plan.Track); err == nil {
			a.mu.Lock()
			a.lastWAV = wav
			a.mu.Unlock()
		}
	}
	if !strings.HasPrefix(plan.Goal, "Recall ") {
		if err := a.autoSnapshot(ctx, plan.Track); err != nil {
			return nil, err
		}
	}
	h := applied{Plan: plan, Track: plan.Track, Previous: map[core.ParamID]float64{}, Names: map[core.ParamID]string{}}
	for _, c := range plan.Changes {
		p, _ := find(ch, c.Param)
		h.Previous[c.Param] = p.Value
		h.Names[c.Param] = pluginOf(ch, c.Param) + " / " + p.Name
	}
	for _, c := range plan.Changes {
		if plugin := pluginOf(ch, c.Param); a.isReadOnly(plugin) {
			return nil, errors.New(readOnlyMessage(plugin))
		}
	}
	group := a.undoActionGrouper()
	groupLabel := "Agent: apply plan " + id
	groupOpen := false
	groupCommit := false
	if group != nil {
		if err := group.BeginUndoAction(ctx, groupLabel); err != nil {
			return nil, fmt.Errorf("begin one-step undo action: %w", err)
		}
		groupOpen = true
	}
	h.Grouped = group != nil
	if group != nil {
		h.UndoLabel = groupLabel
	}
	defer func() {
		if groupOpen {
			if err := group.EndUndoAction(context.WithoutCancel(ctx), groupLabel, groupCommit); err != nil {
				a.emit("undo_group_error", map[string]any{"action": groupLabel, "error": err.Error()})
			}
		}
	}()
	var rawResults []core.Applied
	if bs, ok := a.d.DAW.(batchSetter); ok {
		var err error
		rawResults, err = bs.SetParams(ctx, plan.Track, plan.Changes)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, core.ErrNeedsConsent) {
			return needsPermission(reloadNote), nil
		}
		if err != nil {
			return nil, a.readOnlyFailure(ch, err)
		}
	} else {
		var roErr error
		needs := false
		for _, c := range plan.Changes {
			r, err := a.d.DAW.SetParam(ctx, plan.Track, c.Param, c.Value)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, core.ErrNeedsConsent) {
				needs = true
			}
			if err != nil && !errors.Is(err, core.ErrUnconfirmed) {
				r.Status = core.StatusFailed
				if roErr == nil && errors.Is(err, core.ErrReadOnly) {
					roErr = a.readOnlyFailure(ch, err)
				}
			}
			rawResults = append(rawResults, r)
		}
		if roErr != nil {
			return nil, roErr
		}
		if needs {
			return needsPermission(reloadNote), nil
		}
	}
	for _, c := range plan.Changes {
		if c.Target == "" {
			continue
		}
		shown, readErr := a.text(ctx, plan.Track, c.Param)
		if readErr == nil && targetReadoutMatches(shown, c.Target) {
			continue
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		var rollbackErr error
		for _, restore := range plan.Changes {
			p, found := find(ch, restore.Param)
			if !found {
				continue
			}
			got, e := a.d.DAW.SetParam(cleanup, plan.Track, restore.Param, p.Value)
			if e != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s: %w", restore.Param, e))
			} else if got.Status != core.StatusConfirmed || math.Abs(got.Confirmed-p.Value) > 0.0001 {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback %s was not confirmed at original value %.4f (status %q, readback %.4f)", restore.Param, p.Value, got.Status, got.Confirmed))
			}
		}
		cancel()
		verifyErr := fmt.Errorf("parameter %s read back %q instead of target %q; applied value was rolled back", c.Param, shown, c.Target)
		if readErr != nil {
			verifyErr = fmt.Errorf("could not verify parameter %s target %q: %w", c.Param, c.Target, readErr)
		}
		return nil, errors.Join(verifyErr, rollbackErr)
	}
	var results []core.Applied
	var froms []string // the readout each change started from
	counts := map[string]int{}
	for i, c := range plan.Changes {
		p, _ := find(ch, c.Param)
		var r core.Applied
		if i < len(rawResults) {
			r = rawResults[i]
		}
		if r.Param == "" {
			r.Param = c.Param
		}
		r.Name = p.Name
		if r.Status == core.StatusConfirmed && math.Abs(r.Confirmed-c.Value) > 0.02 {
			// a stepped control lands on its nearest step: it moved, toward
			// the value sent. One that stayed put refused the change.
			moved := math.Abs(r.Confirmed-p.Value) > 0.005
			toward := (c.Value-p.Value)*(r.Confirmed-p.Value) > 0
			if moved && toward {
				r.Status = "snapped"
			} else {
				r.Status = "rejected"
			}
		}
		results = append(results, r)
		froms = append(froms, p.Text)
		counts[r.Status]++
	}
	// Check if any results[i].Text is missing; if all populated, skip full re-read!
	needRefresh := false
	for _, r := range results {
		if r.Text == "" {
			needRefresh = true
			break
		}
	}
	if needRefresh {
		if fresh, err := a.chain(ctx, plan.Track, true); err == nil {
			for i := range results {
				if p, ok := find(fresh, results[i].Param); ok {
					results[i].Text = p.Text
				}
			}
		}
	} else {
		a.mu.Lock()
		if cached, ok := a.chains[plan.Track]; ok {
			for _, r := range results {
				for i := range cached {
					for j := range cached[i].Params {
						if cached[i].Params[j].ID == r.Param {
							cached[i].Params[j].Value = r.Confirmed
							cached[i].Params[j].Text = r.Text
						}
					}
				}
			}
		}
		a.mu.Unlock()
	}
	// the change events carry the DAW's own readout after the set
	for i, r := range results {
		a.emit("change", map[string]any{"name": r.Name, "from": froms[i], "sent": r.Sent, "confirmed": r.Confirmed, "text": r.Text, "status": r.Status})
	}
	a.mu.Lock()
	a.history = append(a.history, h)
	a.mu.Unlock()
	if a.cfg.SettleDelay > 0 {
		time.Sleep(a.cfg.SettleDelay)
	}
	if groupOpen {
		if err := group.EndUndoAction(ctx, groupLabel, true); err != nil {
			a.emit("undo_group_error", map[string]any{"action": groupLabel, "error": err.Error()})
		}
		groupCommit = true
		groupOpen = false
	}
	return map[string]any{"plan_id": id, "applied": results, "counts": counts, "undo_step": group != nil}, nil
}

// Listen runs listening analysis on a track for callers outside the tool loop.
func (a *Agent) Listen(ctx context.Context, track int) (any, error) {
	out, _, err := a.listen(ctx, track)
	return out, err
}

func (a *Agent) listen(ctx context.Context, track int) (any, []core.Part, error) {
	wav, err := a.renderAlone(ctx, track)
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	m, err := a.d.Measurer.Measure(wav)
	if errors.Is(err, core.ErrSilent) {
		out, err := a.diagnoseSilence(ctx, track)
		return out, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	prev := a.lastWAV
	a.lastWAV = wav
	a.mu.Unlock()
	out := map[string]any{"measured": m}
	a.mu.Lock()
	if a.reference != "" {
		out["gap_to_reference_db"] = diffBands(m.Bands, a.refMeasure.Bands)
	}
	a.mu.Unlock()
	wavs, labels := []string{wav}, []string{"Now"}
	if prev != "" {
		pm, err := a.d.Measurer.Measure(prev)
		if err == nil {
			out["change_db"] = diffBands(pm.Bands, m.Bands)
			// bands are shares of the total, so change_db is tone only;
			// loudness is reported apart, since louder always sounds "better"
			out["level_change_db"] = math.Round((m.Level-pm.Level)*10) / 10
			// against the sound before this request, so a correction
			// round still knows how far loudness is from where it began
			a.mu.Lock()
			if a.startLevel == nil {
				l := pm.Level
				a.startLevel = &l
			}
			drift := math.Round((m.Level-*a.startLevel)*10) / 10
			a.mu.Unlock()
			out["level_vs_start_db"] = drift
			if math.Abs(drift) > 1.5 {
				out["level_note"] = fmt.Sprintf("Loudness is %+.1f dB from where this request began. Unless the user asked for louder or quieter, turn the output/master/level control %s by about %.1f dB in the next plan (if it reads in dB, target its current reading %+.1f dB; otherwise move it a little and listen: level_vs_start_db tells you what is left).", drift, map[bool]string{true: "down", false: "up"}[drift > 0], math.Abs(drift), -drift)
			}
			out["previous"] = pm
		}
		wavs, labels = []string{prev, wav}, []string{"Before", "Now"}
	}
	png, err := a.d.Visualizer.Spectrogram(wavs, labels)
	if err != nil {
		return nil, nil, err
	}
	out["spectrogram"] = png
	var distanceDB float64
	hasDist := false
	a.mu.Lock()
	hasRef := a.reference != ""
	refM := a.refMeasure
	a.mu.Unlock()
	if hasRef {
		distanceDB = rmsBands(m.Bands, refM.Bands)
		hasDist = true
	} else if prev != "" {
		if cdb, ok := out["change_db"].(map[string]float64); ok {
			distanceDB = rmsMap(cdb)
			if distanceDB == 0 {
				if d, ok := out["level_vs_start_db"].(float64); ok {
					distanceDB = math.Round(math.Abs(d)*10) / 10
				}
			}
			hasDist = true
		}
	}
	if !hasDist {
		distanceDB = 0.0
	}
	out["distance_db"] = distanceDB
	a.mu.Lock()
	a.rounds = append(a.rounds, map[string]any{
		"round":       len(a.rounds) + 1,
		"distance_db": distanceDB,
		"label":       fmt.Sprintf("Round %d", len(a.rounds)+1),
	})
	roundsCopy := make([]map[string]any, len(a.rounds))
	copy(roundsCopy, a.rounds)
	a.mu.Unlock()
	out["convergence"] = roundsCopy
	a.emit("listen", out)
	img, err := imagePart(png)
	if err != nil {
		return nil, nil, err
	}
	note := core.Part{Type: "text", Text: "Spectrogram of the render(s) from listen, top to bottom: " + strings.Join(labels, ", ") + ". X = time, Y = log frequency, colour = level in dBFS (legend at right)."}
	return out, []core.Part{note, img}, nil
}

func diffBands(a, b core.Bands) map[string]float64 {
	r := func(x float64) float64 { return math.Round(x*10) / 10 }
	return map[string]float64{
		"low": r(b.Low - a.Low), "mud": r(b.Mud - a.Mud), "mid": r(b.Mid - a.Mid),
		"upper": r(b.Upper - a.Upper), "presence": r(b.Presence - a.Presence), "air": r(b.Air - a.Air),
	}
}

func rmsBands(b1, b2 core.Bands) float64 {
	d := []float64{
		b1.Low - b2.Low,
		b1.Mud - b2.Mud,
		b1.Mid - b2.Mid,
		b1.Upper - b2.Upper,
		b1.Presence - b2.Presence,
		b1.Air - b2.Air,
	}
	sumSq := 0.0
	for _, v := range d {
		sumSq += v * v
	}
	return math.Round(math.Sqrt(sumSq/6.0)*10) / 10
}

func rmsMap(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	sumSq := 0.0
	for _, v := range m {
		sumSq += v * v
	}
	return math.Round(math.Sqrt(sumSq/float64(len(m)))*10) / 10
}

// pluginOf names the plugin a parameter belongs to.
func pluginOf(ch []core.FX, id core.ParamID) string {
	fx, _, _ := id.Parse()
	for _, f := range ch {
		if f.Index == fx {
			return f.Name
		}
	}
	return ""
}

// compactChain serializes an FX chain compactly for LLM context optimization:
// omitting unnamed params (counting them instead), rounding values to 3 decimals,
// and capping each FX at 40 named parameters with a search hint.
func compactChain(ch []core.FX) []map[string]any {
	return compactChainN(ch, 40)
}

// compactChainN is compactChain with at most maxNamed named controls per plugin.
func compactChainN(ch []core.FX, maxNamed int) []map[string]any {
	var out []map[string]any
	for _, f := range ch {
		fxMap := map[string]any{
			"index": f.Index,
			"name":  f.Name,
		}
		var named []map[string]any
		unnamedCount := 0
		for _, p := range f.Params {
			if p.Unnamed {
				unnamedCount++
				continue
			}
			pMap := map[string]any{
				"id":    p.ID,
				"name":  p.Name,
				"value": math.Round(p.Value*1000) / 1000,
			}
			if p.Kind != "" {
				pMap["kind"] = p.Kind
			}
			if p.Text != "" {
				pMap["text"] = p.Text
			}
			named = append(named, pMap)
		}
		if len(named) > maxNamed {
			more := len(named) - maxNamed
			fxMap["params"] = named[:maxNamed]
			fxMap["more_params"] = more
			fxMap["hint"] = "use search_params to find specific controls"
		} else {
			fxMap["params"] = named
		}
		if unnamedCount > 0 {
			fxMap["unnamed_count"] = unnamedCount
		}
		out = append(out, fxMap)
	}
	return out
}
