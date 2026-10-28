package reaper

import (
	"context"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// programReaders names a plugin's program numbers from the plugin's own
// program-change list on disk. One reader per vendor, chosen by a word in
// the plugin's chunk header; a plugin without a reader has no names.
var programReaders = []struct {
	plugin string
	read   func() map[int]string
}{
	{"AmpliTube", readAmpliTubePrograms},
}

// ProgramNames returns the program names of the plugin at fx, or nil when
// its program list is not readable.
func (b Bridge) ProgramNames(ctx context.Context, track, fx int) (map[int]string, error) {
	chunk, err := b.GetChunk(ctx, track, fx)
	if err != nil {
		return nil, err
	}
	header, _, _ := strings.Cut(chunk, "\n")
	for _, r := range programReaders {
		if strings.Contains(header, r.plugin) {
			if names := r.read(); len(names) > 0 {
				return names, nil
			}
		}
	}
	return nil, nil
}

var ampPC = regexp.MustCompile(`PC(\d+)="([^"]*)"`)

// readAmpliTubePrograms reads AmpliTube 5's ProgramChangePresetList.ik: one
// PCn="Folder/Preset.at5p" attribute per program. Names are the preset files
// without their folder or extension.
func readAmpliTubePrograms() map[int]string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(home, "Documents", "IK Multimedia", "AmpliTube 5", "ProgramChangePresetList.ik"))
	if err != nil {
		return nil
	}
	out := map[int]string{}
	for _, m := range ampPC.FindAllStringSubmatch(string(raw), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		name := html.UnescapeString(m[2])
		name = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		out[n] = name
	}
	return out
}
