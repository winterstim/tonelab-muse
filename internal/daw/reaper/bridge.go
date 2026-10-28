package reaper

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

//go:embed bridge/startup.lua
var startupLua []byte

// Bridge adds and removes stock plugins through a small Lua script that
// REAPER runs at launch (OSC cannot change the FX chain). The script only
// accepts the plugins it lists; every change is one REAPER undo step.
type Bridge struct {
	Dir        string   // command/reply files; "" = ~/.tonelab-muse/bridge
	PresetDirs []string // optional custom directories to scan for plugin presets/models
}

var _ core.ChainEditor = Bridge{}
var _ core.ChainMover = Bridge{}
var _ core.Presetter = Bridge{}
var _ core.TurboChain = Bridge{}
var _ core.SessionController = Bridge{}
var _ core.UndoActionGrouper = Bridge{}
var _ core.SessionTrackControls = Bridge{}
var _ core.SessionRouting = Bridge{}
var _ core.SessionMarkers = Bridge{}
var _ core.SessionProject = Bridge{}
var _ core.SessionMeter = Bridge{}

func (b Bridge) dir() string {
	if b.Dir != "" {
		return b.Dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tonelab-muse", "bridge")
}

const marker = "-- Agent bridge:"
const bridgeScriptFilename = "tonelab_agent_bridge.lua"
const bridgeIncludeLine = `dofile(reaper.GetResourcePath() .. "/Scripts/tonelab_agent_bridge.lua") -- Tonelab Agent Bridge`

// resourceDir is REAPER's own settings folder.
func resourceDir() string {
	if custom := os.Getenv("REAPER_RESOURCE_PATH"); custom != "" {
		return custom
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "REAPER")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "REAPER")
	}
	return filepath.Join(home, "Library", "Application Support", "REAPER")
}

func startupPath() string { return filepath.Join(resourceDir(), "Scripts", "__startup.lua") }

func bridgeScriptPath() string {
	return filepath.Join(resourceDir(), "Scripts", bridgeScriptFilename)
}

// Install puts the agent bridge script in REAPER's Scripts folder. It always
// writes our full script to Scripts/tonelab_agent_bridge.lua. If
// Scripts/__startup.lua does not exist or starts with marker, it writes
// startupLua there too. If Scripts/__startup.lua exists and belongs to the
// user, it appends dofile(reaper.GetResourcePath() .. "/Scripts/tonelab_agent_bridge.lua")
// once without clobbering user code.
func (b Bridge) Install() (bool, error) {
	scriptDir := filepath.Join(resourceDir(), "Scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return false, err
	}

	// 1. Always write our full script to Scripts/tonelab_agent_bridge.lua
	bridgePath := bridgeScriptPath()
	oldBridge, err := os.ReadFile(bridgePath)
	bridgeChanged := err != nil || !bytes.Equal(oldBridge, startupLua)
	if bridgeChanged {
		if err := os.WriteFile(bridgePath, startupLua, 0o644); err != nil {
			return false, err
		}
	}

	// 2. Manage Scripts/__startup.lua
	path := startupPath()
	old, err := os.ReadFile(path)
	if err != nil {
		// Does not exist: write startupLua there too
		return true, os.WriteFile(path, startupLua, 0o644)
	}

	// If it starts with our marker, we own this file
	if bytes.HasPrefix(old, []byte(marker)) {
		if bytes.Equal(old, startupLua) {
			return bridgeChanged, nil
		}
		return true, os.WriteFile(path, startupLua, 0o644)
	}

	// User's own startup script: append dofile line if not already present
	if strings.Contains(string(old), bridgeScriptFilename) {
		return bridgeChanged, nil
	}

	newContent := string(old)
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += bridgeIncludeLine + "\n"
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func (b Bridge) installed() bool {
	// tonelab_agent_bridge.lua must exist and match startupLua
	bridgeBytes, err := os.ReadFile(bridgeScriptPath())
	if err != nil || !bytes.Equal(bridgeBytes, startupLua) {
		return false
	}
	// __startup.lua must exist and either equal startupLua or contain the include line
	cur, err := os.ReadFile(startupPath())
	if err != nil {
		return false
	}
	if bytes.Equal(cur, startupLua) {
		return true
	}
	return strings.Contains(string(cur), bridgeScriptFilename)
}

// Enabled reports whether the bridge script is installed. If installed,
// it ensures the "enabled" marker file exists for any legacy checks.
func (b Bridge) Enabled() bool {
	if b.installed() {
		_ = os.MkdirAll(b.dir(), 0o700)
		_ = os.WriteFile(filepath.Join(b.dir(), "enabled"), []byte("allowed by the user\n"), 0o600)
		return true
	}
	return false
}

// Enable is the user's one-time yes to plugin changes: the consent file the
// script checks, and the script itself.
func (b Bridge) Enable() (bool, error) {
	if err := os.MkdirAll(b.dir(), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(b.dir(), "enabled"), []byte("allowed by the user\n"), 0o600); err != nil {
		return false, err
	}
	return b.Install()
}

// EnableAuto is used in Turbo mode to auto-consent and install the bridge script.
func (b Bridge) EnableAuto() error {
	if err := os.MkdirAll(b.dir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.dir(), "enabled"), []byte("allowed by the user\n"), 0o600); err != nil {
		return err
	}
	_, err := b.Install()
	return err
}

// Disable withdraws consent to plugin changes; rendering keeps working.
func (b Bridge) Disable() error {
	err := os.Remove(filepath.Join(b.dir(), "enabled"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ErrNoConsent means the user has not allowed plugin changes yet.
var ErrNoConsent = fmt.Errorf("%w: adding plugins in REAPER", core.ErrNeedsConsent)

var bridgeCallLocks sync.Map // absolute bridge directory -> command channel

func (b Bridge) call(ctx context.Context, op string, track int, arg string, pos int) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	key, err := filepath.Abs(b.dir())
	if err != nil {
		return "", "", err
	}
	value, _ := bridgeCallLocks.LoadOrStore(key, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return "", "", ctx.Err()
	}

	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if !b.installed() {
		_, _ = b.Install()
	}
	wait := 3 * time.Second
	switch op {
	case "render", "render_range":
		wait = 90 * time.Second // plugins that only run in real time render slowly
	case "reinstantiate", "reload_offline", "reload_offline_b", "reload_project_with_state":
		wait = 60 * time.Second // the plugin loads again from its state
	case "program_change":
		wait = 10 * time.Second
	case "add":
		wait = 15 * time.Second
	}
	d := b.dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", "", err
	}
	os.Remove(filepath.Join(d, "reply.txt"))
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	line := strings.Join([]string{id, op, strconv.Itoa(track), arg, strconv.Itoa(pos)}, "\t") + "\n"
	tmp := filepath.Join(d, "cmd.tmp")
	if err := os.WriteFile(tmp, []byte(line), 0o600); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, filepath.Join(d, "cmd.txt")); err != nil {
		return "", "", err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		raw, err := os.ReadFile(filepath.Join(d, "reply.txt"))
		if err != nil {
			continue
		}
		f := strings.Split(strings.TrimRight(string(raw), "\n"), "\t")
		if len(f) < 4 || f[0] != id {
			continue
		}
		if f[1] != "ok" {
			return "", "", errors.New(f[2])
		}
		return f[2], f[3], nil
	}
	os.Remove(filepath.Join(d, "cmd.txt"))
	return "", "", errors.New("REAPER did not answer the plugin bridge: restart REAPER once so it loads Scripts/__startup.lua")
}

func (Bridge) SupportsUndoActionGrouping() bool { return true }

func (b Bridge) BeginUndoAction(ctx context.Context, name string) error {
	name = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(strings.TrimSpace(name))
	if name == "" {
		return errors.New("undo action name is required")
	}
	_, _, err := b.call(ctx, "begin_undo_action", 0, name, 0)
	return err
}

func (b Bridge) EndUndoAction(ctx context.Context, name string, commit bool) error {
	name = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(strings.TrimSpace(name))
	flag := "0"
	if commit {
		flag = "1"
	}
	got, _, err := b.call(ctx, "end_undo_action", 0, flag+":"+name, 0)
	if err != nil {
		return err
	}
	want := "cancelled"
	if commit {
		want = "committed"
	}
	if got != want {
		return fmt.Errorf("undo action returned %q; expected %q", got, want)
	}
	return nil
}

func (b Bridge) UndoActionIsCurrent(ctx context.Context, name string) (bool, error) {
	got, _, err := b.call(ctx, "undo_action_current", 0, name, 0)
	if err != nil {
		return false, err
	}
	return got == name, nil
}

// Starter puts the riff at path on a new "Guitar" track when the project
// has no audio yet. It reports whether it did.
func (b Bridge) Starter(ctx context.Context, path string) (bool, error) {
	v, _, err := b.call(ctx, "starter", 0, path, 0)
	return v == "created", err
}

// AddFX inserts an installed effect at pos (1-based; 0 = at the end) and
// returns its index in the chain.
func (b Bridge) AddFX(ctx context.Context, track int, name string, pos int) (int, error) {
	idx, got, err := b.call(ctx, "add", track, name, pos)
	if err != nil {
		return 0, err
	}
	added.Lock()
	added.n[fmt.Sprintf("%d/%s", track, got)]++
	added.Unlock()
	return strconv.Atoi(idx)
}

// added counts what the agent put on each track this session ("track/name"):
// only those may be taken off again in default mode.
var added = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

// RemoveFX deletes the plugin at index fx, if the agent added one of that
// name on that track, and returns its name.
func (b Bridge) RemoveFX(ctx context.Context, track, fx int) (string, error) {
	// the name first, so a plugin the user put there is never touched
	_, name, err := b.call(ctx, "name", track, strconv.Itoa(fx), 0)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%d/%s", track, name)
	added.Lock()
	n := added.n[key]
	added.Unlock()
	if n == 0 {
		return "", fmt.Errorf("%s was not added by the agent; only plugins it added can be removed", name)
	}
	_, name, err = b.call(ctx, "remove", track, strconv.Itoa(fx), 0)
	if err != nil {
		return "", err
	}
	added.Lock()
	added.n[key]--
	added.Unlock()
	return name, nil
}

// ForceRemoveFX deletes the plugin at index fx regardless of session added map
// (used in Turbo mode) and returns its name.
func (b Bridge) ForceRemoveFX(ctx context.Context, track, fx int) (string, error) {
	_, name, err := b.call(ctx, "force_remove", track, strconv.Itoa(fx), 0)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%d/%s", track, name)
	added.Lock()
	if added.n[key] > 0 {
		added.n[key]--
	}
	added.Unlock()
	return name, nil
}

// MoveFX reorders an effect in the track's chain.
func (b Bridge) MoveFX(ctx context.Context, track, fromFX, toFX int) error {
	_, _, err := b.call(ctx, "move", track, strconv.Itoa(fromFX), toFX)
	return err
}

// Presets lists available presets on an effect.
func (b Bridge) Presets(ctx context.Context, track, fx int) (string, []string, error) {
	cur, rawList, _ := b.call(ctx, "presets", track, strconv.Itoa(fx), 0)
	var list []string
	if rawList != "" {
		list = strings.Split(rawList, "|")
	}

	// Disk preset fallback
	chunk, err := b.GetChunk(ctx, track, fx)
	if err == nil && chunk != "" {
		dirs := findPresetDirs(chunk, b.PresetDirs)
		if len(dirs) > 0 {
			cat := loadModelCatalog(dirs)
			if len(cat.presets) > 0 {
				if len(list) == 0 {
					list = cat.presets
					if cur == "" {
						cur = extractPresetName(chunk)
						if cur == "" {
							cur = "Default"
						}
					}
				} else {
					seen := map[string]bool{}
					for _, p := range list {
						seen[p] = true
					}
					for _, dp := range cat.presets {
						if !seen[dp] {
							list = append(list, dp)
							seen[dp] = true
						}
					}
				}
			}
		}
	}
	return cur, list, nil
}

// SetPreset selects a preset by name on an effect, checking disk preset files first.
func (b Bridge) SetPreset(ctx context.Context, track, fx int, preset string) error {
	chunk, err := b.GetChunk(ctx, track, fx)
	if err == nil && chunk != "" {
		dirs := findPresetDirs(chunk, b.PresetDirs)
		if len(dirs) > 0 {
			cat := loadModelCatalog(dirs)
			if filePath, ok := cat.presetFiles[preset]; ok {
				updatedChunk, err := applyDiskPreset(chunk, filePath, preset)
				if err == nil {
					return b.keepWrite(ctx, track, fx, updatedChunk, func(back string) error {
						return checkKept(fx, chunk, updatedChunk, back)
					})
				}
			}
		}
	}
	_, _, err = b.call(ctx, "set_preset", track, fmt.Sprintf("%d:%s", fx, preset), 0)
	return err
}

// CreateTrack inserts a new track at 1-based index (0 = at the end).
func (b Bridge) CreateTrack(ctx context.Context, name string, index int) (int, error) {
	res, _, err := b.call(ctx, "create_track", 0, name, index)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(res)
}

// DuplicateTrack duplicates the track at index and names the duplicate.
func (b Bridge) DuplicateTrack(ctx context.Context, track int, name string) (int, error) {
	res, _, err := b.call(ctx, "duplicate_track", track, name, 0)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(res)
}

// DeleteTrack deletes the track at index.
func (b Bridge) DeleteTrack(ctx context.Context, track int) error {
	_, _, err := b.call(ctx, "delete_track", track, "", 0)
	return err
}

// RenameTrack changes the name of the track at index.
func (b Bridge) RenameTrack(ctx context.Context, track int, name string) error {
	_, _, err := b.call(ctx, "rename_track", track, name, 0)
	return err
}

// InsertMedia adds the media item at path to the track at positionSec.
func (b Bridge) InsertMedia(ctx context.Context, track int, path string, positionSec float64) error {
	_, _, err := b.call(ctx, "insert_media", track, path, int(positionSec*1000))
	return err
}

// InsertMediaAndGetIndex inserts media and returns the exact 1-based item
// index reported by the bridge, for a caller that must remove that item.
func (b Bridge) InsertMediaAndGetIndex(ctx context.Context, track int, path string, positionSec float64) (int, error) {
	_, raw, err := b.call(ctx, "insert_media", track, path, int(positionSec*1000))
	if err != nil {
		return 0, err
	}
	idx, err := strconv.Atoi(raw)
	if err != nil || idx < 1 {
		return 0, fmt.Errorf("invalid inserted media index %q", raw)
	}
	return idx, nil
}

// DeleteMediaItem removes one track item by its 1-based index.
func (b Bridge) DeleteMediaItem(ctx context.Context, track, item int) error {
	if item < 1 {
		return fmt.Errorf("invalid media item index %d", item)
	}
	_, _, err := b.call(ctx, "delete_media_item", track, strconv.Itoa(item), 0)
	return err
}

// DeleteMediaByPath removes only items whose active take reads the exact path.
func (b Bridge) DeleteMediaByPath(ctx context.Context, track int, path string) (int, error) {
	count, _, err := b.call(ctx, "delete_media_path", track, path, 0)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(count)
	return n, err
}

// CreateMIDIRiff creates one MIDI item and note, returning the item index for exact restoration.
func (b Bridge) CreateMIDIRiff(ctx context.Context, track int, startSec float64) (int, error) {
	idx, _, err := b.call(ctx, "create_midi_riff", track, strconv.FormatFloat(startSec, 'f', 3, 64), 0)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(idx)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid MIDI item readback %q", idx)
	}
	return n, nil
}

func (b Bridge) FXUIState(ctx context.Context, track, fx int) (bool, error) {
	got, _, err := b.call(ctx, "fx_ui_state", track, strconv.Itoa(fx), 0)
	if err != nil {
		return false, err
	}
	if got != "0" && got != "1" {
		return false, fmt.Errorf("invalid FX UI state %q", got)
	}
	return got == "1", nil
}

func (b Bridge) SetFXUI(ctx context.Context, track, fx int, visible bool) (bool, error) {
	v := "0"
	if visible {
		v = "1"
	}
	got, _, err := b.call(ctx, "set_fx_ui", track, fmt.Sprintf("%d:%s", fx, v), 0)
	if err != nil {
		return false, err
	}
	if got != "0" && got != "1" {
		return false, fmt.Errorf("invalid FX UI readback %q", got)
	}
	return got == "1", nil
}

// CreateSend routes fromTrack to toTrack with the given level in dB.
func (b Bridge) CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error {
	_, _, err := b.call(ctx, "create_send", fromTrack, fmt.Sprintf("%d:%g", toTrack, levelDB), 0)
	return err
}

// SetTransport controls playback, tempo, cursor, and loop range.
func (b Bridge) SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error {
	arg := fmt.Sprintf("%s:%g:%g:%g:%g", action, bpm, posSec, loopStart, loopEnd)
	_, _, err := b.call(ctx, "set_transport", 0, arg, 0)
	return err
}

func (b Bridge) SetTrackControl(ctx context.Context, track int, control string, value string) (string, error) {
	got, _, err := b.call(ctx, "track_control", track, control+":"+value, 0)
	return got, err
}

func (b Bridge) RemoveSend(ctx context.Context, fromTrack, send int) error {
	_, _, err := b.call(ctx, "remove_send", fromTrack, strconv.Itoa(send), 0)
	return err
}

// TrackState is one track's record, mute, solo, monitor, input and level
// state, and how many items it holds.
type TrackState struct {
	Arm, Mute, Solo bool
	Monitor, Input  int     // I_RECMON, I_RECINPUT
	Volume, Pan     float64 // D_VOL (linear), D_PAN
	Items           int
}

// TrackState reads the state of the track at index (1-based).
func (b Bridge) TrackState(ctx context.Context, track int) (TrackState, error) {
	raw, _, err := b.call(ctx, "track_state", track, "", 0)
	if err != nil {
		return TrackState{}, err
	}
	v, err := parseFloats(raw, 8)
	if err != nil {
		return TrackState{}, fmt.Errorf("invalid track state readback %q: %w", raw, err)
	}
	return TrackState{
		Arm: v[0] != 0, Mute: v[1] != 0, Solo: v[2] != 0,
		Monitor: int(v[3]), Input: int(v[4]),
		Volume: v[5], Pan: v[6], Items: int(v[7]),
	}, nil
}

// TransportState is the project's tempo, play state, cursor, loop range
// (seconds) and the time signature at the cursor.
type TransportState struct {
	Tempo                    float64
	Playing                  bool
	PosSec                   float64
	LoopStartSec, LoopEndSec float64
	TimeSigNum, TimeSigDen   int
}

// TransportState reads the project's transport state.
func (b Bridge) TransportState(ctx context.Context) (TransportState, error) {
	raw, _, err := b.call(ctx, "transport_state", 0, "", 0)
	if err != nil {
		return TransportState{}, err
	}
	v, err := parseFloats(raw, 7)
	if err != nil {
		return TransportState{}, fmt.Errorf("invalid transport state readback %q: %w", raw, err)
	}
	return TransportState{
		Tempo: v[0], Playing: v[1] != 0, PosSec: v[2],
		LoopStartSec: v[3], LoopEndSec: v[4],
		TimeSigNum: int(v[5]), TimeSigDen: int(v[6]),
	}, nil
}

// Send is one send from a track: the destination track (1-based) and the
// send's level as REAPER reports it (D_VOL, linear gain).
type Send struct {
	Dest   int
	Volume float64
}

// Sends lists the sends of the track at index (1-based).
func (b Bridge) Sends(ctx context.Context, track int) ([]Send, error) {
	raw, _, err := b.call(ctx, "send_list", track, "", 0)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var sends []Send
	for _, row := range strings.Split(raw, "|") {
		dest, vol, ok := strings.Cut(row, ":")
		if !ok {
			return nil, fmt.Errorf("invalid send readback %q", row)
		}
		d, err := strconv.Atoi(dest)
		if err != nil {
			return nil, fmt.Errorf("invalid send destination %q: %w", dest, err)
		}
		v, err := strconv.ParseFloat(vol, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid send volume %q: %w", vol, err)
		}
		sends = append(sends, Send{Dest: d, Volume: v})
	}
	return sends, nil
}

// Wet reads the wet (mix) level of the effect at fx (1-based), or sets it
// when set is not nil. The level is 0..1 and the value returned is the one
// REAPER holds afterwards.
func (b Bridge) Wet(ctx context.Context, track, fx int, set *float64) (float64, error) {
	arg := "get"
	if set != nil {
		if !(*set >= 0 && *set <= 1) {
			return 0, errors.New("wet must be between 0 and 1")
		}
		arg = strconv.FormatFloat(*set, 'f', -1, 64)
	}
	raw, _, err := b.call(ctx, "fx_wet", track, arg, fx)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid wet readback %q: %w", raw, err)
	}
	return v, nil
}

// ParamReadout returns REAPER's display text for parameter param of the
// effect at fx (both 1-based for fx, 0-based for param), such as "-6.0 dB".
func (b Bridge) ParamReadout(ctx context.Context, track, fx, param int) (string, error) {
	raw, _, err := b.call(ctx, "param_readout", track, strconv.Itoa(param), fx)
	return raw, err
}

// parseFloats splits a colon-separated reply into n numbers.
func parseFloats(raw string, n int) ([]float64, error) {
	f := strings.Split(raw, ":")
	if len(f) != n {
		return nil, fmt.Errorf("want %d fields, got %d", n, len(f))
	}
	out := make([]float64, n)
	for i, x := range f {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (b Bridge) Markers(ctx context.Context) ([]core.Marker, error) {
	_, raw, err := b.call(ctx, "list_markers", 1, "", 0)
	if err != nil {
		return nil, err
	}
	var markers []core.Marker
	for _, row := range strings.Split(raw, "|") {
		f := strings.SplitN(row, ":", 5)
		if len(f) != 5 {
			continue
		}
		i, _ := strconv.Atoi(f[0])
		pos, _ := strconv.ParseFloat(f[2], 64)
		end, _ := strconv.ParseFloat(f[3], 64)
		markers = append(markers, core.Marker{Index: i, Name: f[1], PositionSec: pos, EndSec: end, Region: f[4] == "1"})
	}
	return markers, nil
}

func (b Bridge) CreateMarker(ctx context.Context, positionSec, endSec float64, name string, region bool) (int, error) {
	typeFlag := 0
	if region {
		typeFlag = 1
	}
	got, _, err := b.call(ctx, "create_marker", 1, fmt.Sprintf("%g:%g:%d:%s", positionSec, endSec, typeFlag, name), 0)
	i, parseErr := strconv.Atoi(got)
	if err != nil {
		return 0, err
	}
	if parseErr != nil {
		return 0, fmt.Errorf("invalid marker index readback %q", got)
	}
	return i, nil
}

// DeleteMarker removes a marker or region by REAPER's enumerated project index.
func (b Bridge) DeleteMarker(ctx context.Context, index int) error {
	if index < 1 {
		return fmt.Errorf("invalid marker index %d", index)
	}
	_, _, err := b.call(ctx, "delete_marker", 1, strconv.Itoa(index), 0)
	return err
}

// Reload refreshes the installed bridge script in the running REAPER process.
func (b Bridge) Reload(ctx context.Context) error {
	// The current bridge dispatch resolves a track before reaching reload.
	// Track 1 is used only to pass that existing guard; reload itself is global.
	_, _, err := b.call(ctx, "reload", 1, "", 0)
	return err
}

func (b Bridge) GoToMarker(ctx context.Context, index int) error {
	_, _, err := b.call(ctx, "goto_marker", 0, strconv.Itoa(index), 0)
	return err
}

func (b Bridge) SaveProject(ctx context.Context) error {
	_, _, err := b.call(ctx, "save_project", 0, "", 0)
	return err
}

func (b Bridge) SetTimeSignature(ctx context.Context, numerator, denominator int) error {
	if numerator <= 0 || denominator <= 0 {
		return errors.New("time signature values must be positive")
	}
	_, _, err := b.call(ctx, "set_timesig", 0, fmt.Sprintf("%d:%d", numerator, denominator), 0)
	return err
}

// GetChunk reads the state chunk for an effect (fx > 0) or the whole track (fx == 0).
func (b Bridge) GetChunk(ctx context.Context, track, fx int) (string, error) {
	d := b.dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	outFile := filepath.Join(d, "chunk_out.txt")
	os.Remove(outFile)
	if _, _, err := b.call(ctx, "get_chunk", track, strconv.Itoa(fx), 0); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		return "", fmt.Errorf("reading chunk_out.txt: %w", err)
	}
	return string(raw), nil
}

// SetChunk writes the state chunk for an effect (fx > 0) or the whole track (fx == 0).
func (b Bridge) SetChunk(ctx context.Context, track, fx int, chunk string) error {
	d := b.dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	inFile := filepath.Join(d, "chunk_in.txt")
	if err := os.WriteFile(inFile, []byte(chunk), 0o600); err != nil {
		return err
	}
	_, _, err := b.call(ctx, "set_chunk", track, strconv.Itoa(fx), 0)
	return err
}

// Reinstantiate gives the plugin at fx (1-based) the saved state in state
// (base64 lines, as fxState reads it) and returns its name. REAPER loads the
// plugin again from that state; the change is one undo step.
func (b Bridge) Reinstantiate(ctx context.Context, track, fx int, state string) (string, error) {
	path, err := b.writeState(state)
	if err != nil {
		return "", err
	}
	name, _, err := b.call(ctx, "reinstantiate", track, path, fx)
	return name, err
}

// ReloadOffline sets the plugin at fx (1-based) offline, writes state into
// its track chunk with the FXID kept, and brings it back online. It returns
// the plugin's name.
func (b Bridge) ReloadOffline(ctx context.Context, track, fx int, state string) (string, error) {
	path, err := b.writeState(state)
	if err != nil {
		return "", err
	}
	name, _, err := b.call(ctx, "reload_offline", track, path, fx)
	return name, err
}

// ReloadOfflineNamed is ReloadOffline with the state set through the plugin's
// vst_chunk named parameter while it is offline.
func (b Bridge) ReloadOfflineNamed(ctx context.Context, track, fx int, state string) (string, error) {
	path, err := b.writeState(state)
	if err != nil {
		return "", err
	}
	name, _, err := b.call(ctx, "reload_offline_b", track, path, fx)
	return name, err
}

// ProgramChange sends a MIDI program change on channel (1-16) to the track.
// The plugin switches to its program; the caller listens afterwards.
func (b Bridge) ProgramChange(ctx context.Context, track, channel, program int) error {
	_, _, err := b.call(ctx, "program_change", track, fmt.Sprintf("%d:%d", channel, program), 0)
	return err
}

// ReopenProject reopens the current project from its file without a prompt.
// It is how a project file put back from a backup is loaded again.
func (b Bridge) ReopenProject(ctx context.Context) error {
	if _, _, err := b.call(ctx, "reopen_project", 0, "", 0); err != nil {
		return err
	}
	return b.waitBack(ctx)
}

// ReloadProjectWithState writes state into the plugin's block in the saved
// project file and reopens the project. REAPER's undo history starts fresh;
// a backup of the file is kept in the bridge folder. It waits until REAPER
// answers again.
func (b Bridge) ReloadProjectWithState(ctx context.Context, track, fx int, state string) error {
	path, err := b.writeState(state)
	if err != nil {
		return err
	}
	if _, _, err := b.call(ctx, "reload_project_with_state", track, path, fx); err != nil {
		return err
	}
	return b.waitBack(ctx)
}

// waitBack waits until REAPER answers again after a project reopen.
func (b Bridge) waitBack(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := b.Ping(ctx); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("REAPER did not come back after reopening the project: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// keepWrite writes updated over orig and checks that REAPER kept it with
// check. If the plugin kept none of it, the policy in ctx decides whether
// the project may be reopened with the state in its file; if that also fails
// the error stays a read-only error. ErrNeedsConsent comes back as is.
func (b Bridge) keepWrite(ctx context.Context, track, fx int, updated string, check func(back string) error) error {
	if err := b.SetChunk(ctx, track, fx, updated); err != nil {
		return err
	}
	back, err := b.GetChunk(ctx, track, fx)
	if err != nil {
		return err
	}
	err = check(back)
	if err == nil || !errors.Is(err, core.ErrReadOnly) {
		return err
	}
	policy := core.ReloadPolicyFrom(ctx)
	if policy == nil {
		return err
	}
	if perr := policy(ctx, track, fx, fxState(updated)); perr != nil {
		if errors.Is(perr, core.ErrNeedsConsent) {
			return perr
		}
		return err
	}
	if rerr := b.ReloadProjectWithState(ctx, track, fx, fxState(updated)); rerr != nil {
		return fmt.Errorf("%w (reopening the project: %v)", err, rerr)
	}
	back, err = b.GetChunk(ctx, track, fx)
	if err != nil {
		return err
	}
	return check(back)
}

// writeState puts state in the bridge folder for the Lua side to read and
// returns its path.
func (b Bridge) writeState(state string) (string, error) {
	d := b.dir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(d, "reinstate_in.txt")
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		return "", err
	}
	return filepath.ToSlash(path), nil
}

// Undo reverts REAPER's last undo step.
func (b Bridge) Undo(ctx context.Context) error {
	_, _, err := b.call(ctx, "undo", 0, "", 0)
	return err
}

// fxState returns the saved state of an FX chunk as base64 lines: the lines
// that are one unbroken base64 run. Headers, FXID, WAK and the closing ">"
// are left out.
func fxState(chunk string) string {
	var out []string
	for _, l := range strings.Split(chunk, "\n") {
		t := strings.TrimSpace(l)
		if len(t) >= 8 && base64Line.MatchString(t) {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

var base64Line = regexp.MustCompile(`^[A-Za-z0-9+/=]+$`)

// FindValue performs the Illinois false-position search for target inside a single
// Lua tick and returns the found normalized value. For chunk parameters (p >= 1000),
// it resolves the target value in-memory from the plugin chunk in 0ms.
func (b Bridge) FindValue(ctx context.Context, track int, id core.ParamID, target string) (float64, error) {
	fx, p, err := id.Parse()
	if err != nil {
		return 0, err
	}
	if p >= 1000 {
		rawChunk, err := b.GetChunk(ctx, track, fx)
		if err != nil {
			return 0, fmt.Errorf("getting chunk for %s: %w", id, err)
		}
		return FindChunkValue(rawChunk, p-1000, target)
	}
	valStr, _, err := b.call(ctx, "find_value", track, fmt.Sprintf("%d:%d:%s", fx, p, target), 0)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid find_value response %q: %w", valStr, err)
	}
	return v, nil
}

// Ping reports REAPER's version when the bridge is loaded.
func (b Bridge) Ping(ctx context.Context) (string, error) {
	v, _, err := b.call(ctx, "ping", 0, "", 0)
	return v, err
}

// Catalog is every effect REAPER reports as installed, with the category
// REAPER itself keeps for it (reaper-fxtags.ini) when there is one.
func (b Bridge) Catalog(ctx context.Context) ([]core.CatalogEntry, error) {
	if _, _, err := b.call(ctx, "list", 0, "", 0); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(b.dir(), "catalog.txt"))
	if err != nil {
		return nil, err
	}
	kinds := fxTags()
	var out []core.CatalogEntry
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		name, ident, _ := strings.Cut(l, "\t")
		if name == "" {
			continue
		}
		out = append(out, core.CatalogEntry{Name: name, Kind: kinds[filepath.Base(ident)]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// fxTags reads the categories REAPER keeps per plugin file.
func fxTags() map[string]string {
	raw, err := os.ReadFile(filepath.Join(resourceDir(), "reaper-fxtags.ini"))
	out := map[string]string{}
	if err != nil {
		return out
	}
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			in = l == "[category]"
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && in {
			out[k] = strings.ReplaceAll(v, "|", ", ")
		}
	}
	return out
}
