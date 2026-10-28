package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// A catalog belongs to one conversation. Discovery never adds a capability
// absent from that conversation's mode/dependency-filtered registry.
type toolCatalog struct {
	all     []core.Tool
	enabled map[string]bool
	results map[string][]byte
	next    int
}

const maxModelResultBytes = 4096
const maxStoredResultBytes = 4 << 20

func newToolCatalog(all []core.Tool, modes ...Mode) *toolCatalog {
	if len(modes) > 0 && modes[0] == Preview {
		var reads []core.Tool
		for _, t := range all {
			if readOnlyTools[t.Name] || t.Name == "describe_chain" || t.Name == "propose_tone" || t.Name == "match_reference" || t.Name == "read_photo" {
				reads = append(reads, t)
			}
		}
		all = reads
	}
	return &toolCatalog{all: briefTools(all), enabled: map[string]bool{}, results: map[string][]byte{}}
}

var briefDescriptions = map[string]string{
	"list_tracks":         "Read track indices, names and audio/MIDI state before choosing a track.",
	"describe_chain":      "Read plugin/control IDs, readouts and normalized values. Use search_params for omitted controls.",
	"search_params":       "Find up to 20 controls by words on one track.",
	"propose_tone":        "Validate and store one coordinated tone plan; does not apply it.",
	"apply_plan":          "Apply the stored plan and return DAW-confirmed values.",
	"undo":                "Restore the last applied plan's previous values.",
	"listen":              "Measure the track; request look=true only when a spectrogram is needed.",
	"set_track":           "Set a track level, pan, mute or solo and verify readback.",
	"list_plugins":        "Search the DAW's installed effect catalog; never guess plugin names.",
	"add_plugin":          "Add a discovered plugin; position is 1-based, zero appends.",
	"remove_plugin":       "Remove only an agent-added plugin unless the user explicitly requested removal.",
	"move_plugin":         "Move an existing plugin to a 1-based chain position.",
	"list_presets":        "Read a plugin's available preset names.",
	"load_preset":         "Load an exact discovered preset name; verify the resulting state.",
	"program_change":      "Send a discovered program number; mode consent applies.",
	"list_plugin_models":  "Discover the plugin's actual internal slots and model choices.",
	"set_plugin_model":    "Select a discovered slot/model pair and reread exposed controls.",
	"learn_plugin":        "Learn unnamed controls from window changes; mode consent applies.",
	"match_reference":     "Measure the track against the supplied reference and report band gaps.",
	"read_photo":          "Read controls in the supplied gear photo; report uncertainty.",
	"check_mix":           "Measure clashes between this track and the rest of the mix.",
	"save_snapshot":       "Save the track's current tone under a supplied name.",
	"list_snapshots":      "Read saved tone names, including the automatic Before snapshot.",
	"recall_snapshot":     "Recall a discovered saved tone; changes remain undoable.",
	"manage_track":        "Create/delete/rename/duplicate a track, then reread the session.",
	"route_send":          "Create a send between discovered tracks and verify routing.",
	"insert_media":        "Insert a user-supplied audio file on a track at a position.",
	"set_transport":       "Control transport, tempo, cursor or loop using supported actions.",
	"delegate_track_task": "Delegate one track's tone task to an isolated subagent after session structure is ready.",
}

func briefTools(all []core.Tool) []core.Tool {
	out := make([]core.Tool, 0, len(all))
	for _, t := range all {
		if description, ok := briefDescriptions[t.Name]; ok {
			t.Description = description
		} else {
			t.Description = shortText(t.Description, 160)
		}
		if t.Name == "listen" {
			t.Schema = json.RawMessage(subagentListenSchema)
		}
		var schema any
		if json.Unmarshal(t.Schema, &schema) == nil {
			t.Schema, _ = json.Marshal(trimSchema(schema, false))
		}
		out = append(out, t)
	}
	return out
}
func trimSchema(v any, propertyNames bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, value := range x {
			if !propertyNames && (key == "description" || key == "title" || key == "examples") {
				continue
			}
			out[key] = trimSchema(value, key == "properties" || key == "$defs" || key == "definitions")
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = trimSchema(value, false)
		}
		return out
	default:
		return v
	}
}
func toolGroup(name string) string {
	switch name {
	case "list_tracks", "describe_chain", "search_params", "propose_tone", "apply_plan", "listen", "delegate_track_task", "manage_track", "route_send", "list_plugins", "add_plugin", "read_photo", "check_mix":
		return "core"
	case "remove_plugin", "move_plugin", "list_plugin_models", "set_plugin_model", "list_presets", "load_preset", "program_change", "save_preset_as":
		return "chain"
	case "insert_media", "set_transport", "set_track", "remove_send", "save_project", "manage_marker", "set_time_signature", "set_track_control":
		return "session"
	case "save_snapshot", "list_snapshots", "recall_snapshot", "undo":
		return "history"
	case "learn_plugin", "learn_param_choices", "match_reference":
		return "analysis"
	default:
		return "advanced"
	}
}
func (c *toolCatalog) offered(msgs []core.Msg, closing bool) []core.Tool {
	if closing {
		out := checkTools(c.all)
		if len(c.results) > 0 {
			out = append(out, resultPageTool())
		}
		return out
	}
	// Recent observations, not language-specific request keywords, select a phase.
	active := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(msgs[i].ToolCalls) > 0 {
			active = toolGroup(msgs[i].ToolCalls[len(msgs[i].ToolCalls)-1].Name)
			break
		}
	}
	var out []core.Tool
	for _, t := range c.all {
		group := toolGroup(t.Name)
		if group == "core" || c.enabled[group] || (active != "core" && group == active) {
			out = append(out, t)
		}
	}
	groups := map[string]bool{}
	for _, t := range c.all {
		if group := toolGroup(t.Name); group != "core" {
			groups[group] = true
		}
	}
	var choices []string
	for group := range groups {
		choices = append(choices, group)
	}
	sort.Strings(choices)
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"category": map[string]any{"type": "string", "enum": choices}}, "required": []string{"category"}})
	out = append(out, core.Tool{Name: "more_tools", Description: "Discover and enable chain, session, history, analysis or advanced capability tools.", Schema: raw})
	if len(c.results) > 0 {
		out = append(out, resultPageTool())
	}
	return out
}
func (c *toolCatalog) local(tc core.ToolCall) (any, bool, error) {
	switch tc.Name {
	case "more_tools":
		var in struct {
			Category string `json:"category"`
		}
		if err := json.Unmarshal(tc.Args, &in); err != nil {
			return nil, true, fmt.Errorf("invalid discovery arguments")
		}
		var found []core.Tool
		for _, t := range c.all {
			if toolGroup(t.Name) == in.Category {
				found = append(found, t)
			}
		}
		if len(found) == 0 || in.Category == "core" {
			return nil, true, fmt.Errorf("category unavailable in this mode")
		}
		c.enabled[in.Category] = true
		names := make([]string, 0, len(found))
		for _, t := range found {
			names = append(names, t.Name)
		}
		return map[string]any{"category": in.Category, "tools": names}, true, nil
	case "read_tool_result":
		var in struct {
			ID     string `json:"result_id"`
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if json.Unmarshal(tc.Args, &in) != nil || in.Offset < 0 || in.Limit < 0 || in.Limit > 32 {
			return nil, true, fmt.Errorf("invalid result page")
		}
		raw, ok := c.results[in.ID]
		if !ok {
			return nil, true, fmt.Errorf("result not found in this conversation")
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return nil, true, fmt.Errorf("stored result unavailable")
		}
		if in.Path != "" {
			if !strings.HasPrefix(in.Path, "/") {
				return nil, true, fmt.Errorf("path must be a JSON pointer")
			}
			for _, part := range strings.Split(in.Path[1:], "/") {
				part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
				switch x := value.(type) {
				case map[string]any:
					var present bool
					value, present = x[part]
					if !present {
						return nil, true, fmt.Errorf("path not found")
					}
				case []any:
					index, err := strconv.Atoi(part)
					if err != nil || index < 0 || index >= len(x) || strconv.Itoa(index) != part {
						return nil, true, fmt.Errorf("array path index not found")
					}
					value = x[index]
				default:
					return nil, true, fmt.Errorf("path must select an object field")
				}
			}
		}
		if rows, ok := value.([]any); ok {
			if in.Offset > len(rows) {
				return nil, true, fmt.Errorf("offset outside result")
			}
			limit := in.Limit
			if limit == 0 {
				limit = 8
			}
			end := in.Offset + limit
			if end > len(rows) {
				end = len(rows)
			}
			return map[string]any{"result_id": in.ID, "path": in.Path, "items": rows[in.Offset:end], "total": len(rows), "next_offset": end}, true, nil
		}
		if in.Offset != 0 {
			return nil, true, fmt.Errorf("offset requires an array")
		}
		return map[string]any{"result_id": in.ID, "path": in.Path, "data": value}, true, nil
	}
	return nil, false, nil
}
func shortText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.ValidString(s[:max]) {
		max--
	}
	return s[:max] + "…"
}
func pruneResult(v any, depth, items, text int) any {
	switch x := v.(type) {
	case string:
		return shortText(x, text)
	case []any:
		count := items
		if depth > 1 && count > 4 {
			count = 4
		}
		if len(x) < count {
			count = len(x)
		}
		out := make([]any, count)
		for i := range out {
			out[i] = pruneResult(x[i], depth+1, items, text)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, value := range x {
			out[key] = pruneResult(value, depth+1, items, text)
		}
		return out
	default:
		return v
	}
}

// modelResult caps only model-visible JSON. The event/store keeps the full
// result; a conversation-local page tool recovers omitted IDs and readouts.
func (c *toolCatalog) modelResult(out any) []byte {
	raw, err := json.Marshal(out)
	if err != nil {
		return []byte(`{"error":"tool result could not be encoded"}`)
	}
	if len(raw) <= maxModelResultBytes {
		return raw
	}
	c.next++
	id := fmt.Sprintf("r%d", c.next)
	stored := len(raw) <= maxStoredResultBytes
	if stored {
		c.results[id] = raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	for _, limit := range []int{8, 4, 2, 1} {
		pruned := pruneResult(value, 0, limit, 256)
		object, ok := pruned.(map[string]any)
		if !ok {
			object = map[string]any{"items": pruned}
		}
		object["_truncated"] = true
		object["_original_bytes"] = len(raw)
		if stored {
			object["_result_id"] = id
			object["_read_more"] = "read_tool_result with result_id, JSON pointer path, offset, limit"
		} else {
			object["_read_more"] = "Result exceeds retained budget; use a narrower source query."
		}
		encoded, err := json.Marshal(object)
		if err == nil && len(encoded) <= maxModelResultBytes {
			return encoded
		}
	}
	small := map[string]any{"_truncated": true, "_original_bytes": len(raw), "_result_id": id, "note": "Use read_tool_result for selected object fields or array pages."}
	if !stored {
		delete(small, "_result_id")
		small["note"] = "Use a narrower source query; result exceeds retained budget."
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"plan_id", "track", "error", "needs_permission"} {
			if x, exists := object[key]; exists {
				small[key] = pruneResult(x, 0, 1, 128)
			}
		}
	}
	encoded, _ := json.Marshal(small)
	return encoded
}

func requestedPicture(tc core.ToolCall) bool {
	var in struct {
		Look bool `json:"look"`
	}
	return json.Unmarshal(tc.Args, &in) == nil && in.Look
}

func resultPageTool() core.Tool {
	return tool("read_tool_result", "Read omitted data from this conversation's capped result by JSON pointer, offset and limit.", `{"type":"object","properties":{"result_id":{"type":"string"},"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["result_id"]}`)
}
