package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func hasTool(ts []core.Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}
func TestCatalogDiscoveryCannotEscalatePreview(t *testing.T) {
	all := []core.Tool{tool("list_tracks", "read", `{}`), tool("set_track", "write", `{}`), tool("apply_plan", "write", `{}`), tool("list_presets", "read", `{}`)}
	c := newToolCatalog(all, Preview)
	if hasTool(c.offered(nil, false), "set_track") || hasTool(c.offered(nil, false), "apply_plan") {
		t.Fatal("preview offered writes")
	}
	if _, _, err := c.local(core.ToolCall{Name: "more_tools", Args: json.RawMessage(`{"category":"session"}`)}); err == nil {
		t.Fatal("discovery enabled unavailable writes")
	}
	if _, _, err := c.local(core.ToolCall{Name: "more_tools", Args: json.RawMessage(`{"category":"chain"}`)}); err != nil {
		t.Fatal(err)
	}
	if !hasTool(c.offered(nil, false), "list_presets") {
		t.Fatal("discovered read unavailable")
	}
	if hasTool(c.offered(nil, true), "apply_plan") {
		t.Fatal("closing phase offered writes")
	}
}
func TestCatalogCompactSchemaPreservesPropertyNames(t *testing.T) {
	ts := briefTools([]core.Tool{tool("custom", "x", `{"type":"object","description":"long","properties":{"description":{"type":"string","description":"field docs"},"title":{"type":"integer"}},"required":["description"]}`)})
	var x map[string]any
	if err := json.Unmarshal(ts[0].Schema, &x); err != nil {
		t.Fatal(err)
	}
	if _, ok := x["description"]; ok {
		t.Fatal("retained schema prose")
	}
	props := x["properties"].(map[string]any)
	if props["description"] == nil || props["title"] == nil || len(x["required"].([]any)) != 1 {
		t.Fatal("schema semantics lost")
	}
}
func TestCatalogCapsAndRecoversNestedResults(t *testing.T) {
	c := newToolCatalog(nil)
	rows := make([]any, 100)
	for i := range rows {
		rows[i] = map[string]any{"id": fmt.Sprintf("p%d", i), "readout": strings.Repeat("音", 300)}
	}
	original := map[string]any{"plan_id": "p1", "fx": []any{map[string]any{"params": rows}}}
	raw := c.modelResult(original)
	if len(raw) > maxModelResultBytes || !json.Valid(raw) || !utf8.Valid(raw) {
		t.Fatalf("bad capped JSON: %d", len(raw))
	}
	var meta map[string]any
	_ = json.Unmarshal(raw, &meta)
	if meta["plan_id"] != "p1" || meta["_result_id"] != "r1" {
		t.Fatal("lost critical result metadata")
	}
	out, handled, err := c.local(core.ToolCall{Name: "read_tool_result", Args: json.RawMessage(`{"result_id":"r1","path":"/fx/0/params","offset":90,"limit":2}`)})
	if err != nil || !handled {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	items := page["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "p90" {
		t.Fatal("wrong recovered page")
	}
	if len(original["fx"].([]any)[0].(map[string]any)["params"].([]any)) != 100 {
		t.Fatal("full result mutated")
	}
	other := newToolCatalog(nil)
	if _, _, err := other.local(core.ToolCall{Name: "read_tool_result", Args: json.RawMessage(`{"result_id":"r1"}`)}); err == nil {
		t.Fatal("result leaked into another conversation")
	}
	for _, args := range []string{`{"result_id":"r1","offset":-1}`, `{"result_id":"r1","limit":33}`, `{"result_id":"r1","path":"/fx/999"}`} {
		if _, _, err := c.local(core.ToolCall{Name: "read_tool_result", Args: json.RawMessage(args)}); err == nil {
			t.Fatalf("accepted bad page %s", args)
		}
	}
}
func TestSubagentSystemStableAcrossTracks(t *testing.T) {
	if SubagentSystem(1) != SubagentSystem(8) {
		t.Fatal("track-specific system defeats prompt caching")
	}
}
