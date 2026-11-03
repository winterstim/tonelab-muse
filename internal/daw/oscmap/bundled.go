package oscmap

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MergeBundled tops up a saved map from the bundled map of the same DAW (the
// file of the same base name in bundled). Every top-level key the bundled map
// has and the saved one lacks is copied in; keys the saved map already has are
// never changed. The file is rewritten only when something was added. It
// returns the keys added, sorted.
func MergeBundled(path string, bundled fs.FS) ([]string, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".json") + ".json"
	b, err := fs.ReadFile(bundled, name)
	if err != nil {
		return nil, nil // no bundled map for this DAW: nothing to add
	}
	var ours, theirs map[string]json.RawMessage
	if err := json.Unmarshal(b, &theirs); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &ours); err != nil {
		return nil, err
	}
	var added []string
	for k, v := range theirs {
		if _, ok := ours[k]; !ok {
			ours[k] = v
			added = append(added, k)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	sort.Strings(added)
	out, err := json.MarshalIndent(ours, "", "  ")
	if err != nil {
		return nil, err
	}
	return added, os.WriteFile(path, out, 0o644)
}
