package oscmap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

const onboardRounds = 3

const schema = `You connect an audio agent to a DAW it has never seen. Read the DAW's remote-control documentation and write a command map as JSON:

{
  "name": "<DAW name and API>",
  "port": <int: port the DAW listens on>, "reply_port": <int: port it replies to>,
  "base": <0 or 1: index of the first track/device/parameter in this API>,
  "app": "<the DAW's macOS bundle id if you know it, e.g. com.ableton.live; else empty>",
  "ping":         {"send": ["/addr", ...args], "reply": "/addr", "skip": 0},
  "track_count":  {...reply: the count},
  "track_name":   {...args use {track}; reply after skip: the name},
  "device_names": {...{track}; reply after skip: list of device names},
  "param_names":  {...{track},{fx}; reply after skip: list},
  "param_values": {...{track},{fx}; reply after skip: list of current values},
  "param_mins":   {...list of minimums, or "send": [] if the API has none (then 0)},
  "param_maxs":   {...list of maximums, or "send": [] (then 1)},
  "param_text":   {...{track},{fx},{param}; reply after skip: readable value like "2500 Hz"; "send": [] if none},
  "get_param":    {...{track},{fx},{param}; reply after skip: one value},
  "set_param":    {"send": [..., "f:{value}"], "reply": ""},
  "set_volume":   {...{track}, "f:{value}" 0..1; optional},
  "set_pan":      {...{track}, "f:{value}"; optional},
  "set_mute":     {...{track}, "i:{value}" or "f:{value}" 0 or 1; optional},
  "set_solo":     {...{track}, "i:{value}" or "f:{value}" 0 or 1; optional},
  "set_bypass":   {...{track},{fx}, "i:{value}" or "f:{value}" (1=bypass, 0=active; or if addr has active/enable, 1=active, 0=bypass); optional},
  "undo":         {...optional}, "play": {...optional}, "stop": {...optional},
  "device_catalog": {...reply after skip: list of names of the effects/devices that can be added to a track; optional},
  "insert_device":  {...{track}, "s:{name}", {fx} = position (the device count = at the end); reply after skip: the new device's index; optional},
  "remove_device":  {...{track},{fx}; optional},
  "move_device":    {...{track}, {fx} = source, {to} = destination index; optional},
  "device_presets": {...{track},{fx}; reply after skip: list of preset names; optional},
  "set_preset":     {...{track},{fx}, "s:{preset}"; optional},
  "begin_undo_action": {..."s:{name}"; optional, starts one grouped agent action; set "success" to the exact reply that confirms it started},
  "end_undo_action": {..."s:{name}", "i:{value}" (1=commit, 0=cancel); optional; set "success" and "cancel_success" to exact commit/cancel replies},
  "undo_action_current": {..."s:{name}"; optional reply is the current undo description},
  "capture_session_snapshot": {...reply one opaque, exact-restorable session token; optional},
  "restore_session_snapshot": {..."s:{name}" opaque token; reply confirms exact restore; set "success" to the exact confirmation text; optional},
  "show_device":    {...{track},{fx}; opens, selects or focuses that device's window or view on screen without changing it; optional},
  "track_audio":    {...{track}; reply after skip: 1/true if audio track, 0/false if MIDI; optional},
  "scene_count":    {...reply after skip: number of scenes/rows; optional},
  "has_clip":       {...{track}, {slot}; reply after skip: 1/true if clip slot has a clip, 0/false if empty; optional},
  "create_track":   {...{track} position or -1 for end, "s:{name}"; optional},
  "duplicate_track":{...{track}; optional},
  "delete_track":   {...{track}; optional},
  "rename_track":   {...{track}, "s:{name}"; optional},
  "insert_media":   {...{track}, "s:{name}" path, "f:{value}" position in seconds; optional},
  "create_send":    {...{track} fromTrack, {to} toTrack, "f:{value}" level in dB; optional},
  "set_tempo":      {..."f:{value}" tempo in BPM; optional},
  "set_loop":       {..."f:{value}" loop start, {to} loop end; optional},
  "set_time_signature": {...{numerator},{denominator}; reply after skip must equal the configured "success" token; optional},
  "remove_send":    {...{track},{send}; reply after skip must equal the configured "success" token; optional},
  "list_markers":   {...reply after skip is groups of index,name,start,end,region; optional},
  "create_marker":  {...{start},{end},{flag},{name}; reply after skip is the new index; optional},
  "goto_marker":    {...{marker}; reply after skip must equal the configured "success" token; optional},
  "save_project":   {...reply after skip must equal the configured "success" token; optional},
  "extra":          {"<name>": {"send": [...], "reply": ...}; optional map of DAW-specific OSC commands}
}

Args after the address are typed strings: "i:{track}", "i:{fx}", "i:{to}", "i:{slot}", "i:{param}", "f:{value}", "s:{name}", "s:{preset}", or literals like "i:0", "f:1.0", "s:text". {value} for set_param is in the parameter's own units (between its min and max). "skip" is how many leading reply args echo the request (e.g. a reply "track_id, device_id, [names...]" has skip 2). Use only reading commands, single-parameter sets, track controls (volume/pan/mute/solo), device bypass, presets, undo, transport (play/stop/tempo/loop), track management (create/duplicate/delete/rename tracks), media insertion, sends/routing, adding, removing, and moving effect devices on a track and showing or selecting one device if the API can; never anything that exports, records or loads whole projects; save only through save_project. For set_time_signature, remove_send, goto_marker and save_project, include a "success" string on the command with the exact positive status returned after skip; never infer success from a non-empty reply. Add session snapshot commands only if the DAW can capture and restore the complete session exactly; partial state must not be reported as a full request snapshot. Leave optional commands empty ("send": []) only if the documentation has no way to control them; if a command fails a check, fix it from the DAW's answer rather than dropping it.
Answer with the JSON object only.`

// Onboard has Muse read the docs and write a map, then checks it against
// the running DAW and hands the failures back until it passes. progress
// sees each round's checks.
func Onboard(ctx context.Context, llm core.LLM, model, docs string, progress func(round int, m Map, cs []Check)) (Map, []Check, error) {
	msgs := []core.Msg{core.Text("system", schema), core.Text("user", "Documentation:\n\n"+docs)}
	var last []Check
	for round := 1; round <= onboardRounds; round++ {
		r, err := llm.Chat(ctx, model, msgs, nil)
		if err != nil {
			return Map{}, nil, err
		}
		msgs = append(msgs, core.Text("assistant", r.Text))
		var m Map
		feedback := ""
		if err := json.Unmarshal([]byte(jsonOf(r.Text)), &m); err != nil {
			feedback = "That is not valid JSON for the map: " + err.Error()
		} else if err := m.Check(); err != nil {
			feedback = "The map was refused before running: " + err.Error()
		} else {
			d, err := Open(m)
			if err != nil {
				feedback = "Could not open the ports: " + err.Error()
			} else {
				last = Verify(ctx, d)
				d.Close()
				if progress != nil {
					progress(round, m, last)
				}
				if Passed(last) {
					return m, last, nil
				}
				b, _ := json.MarshalIndent(last, "", "  ")
				feedback = "I ran every command against the DAW. Results, with what the DAW actually answered:\n" + string(b) + "\nFix the failing commands and send the whole corrected map."
			}
		}
		msgs = append(msgs, core.Text("user", feedback))
	}
	return Map{}, last, fmt.Errorf("the map still fails after %d rounds", onboardRounds)
}

func jsonOf(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
