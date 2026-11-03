// Package oscmap drives any DAW with a request/reply OSC API from a map of
// commands. The agent writes the map itself from the DAW's documentation
// (see Onboard), checks every command against the running DAW, and fixes
// what fails; the adapter then works like a hand-written one.
package oscmap

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Command is one request and the reply that answers it. Args are typed
// templates: "i:{track}", "i:{fx}", "i:{param}", "f:{value}", or literals
// such as "i:0", "f:0.5", "s:text". Placeholders are already in the DAW's
// own index base (see Map.Base).
type Command struct {
	Send          []string `json:"send"`                     // address first, then args
	Reply         string   `json:"reply,omitempty"`          // address of the answer; "" = none
	Skip          int      `json:"skip,omitempty"`           // leading reply args that echo the request
	Success       string   `json:"success,omitempty"`        // exact first reply text that confirms this optional mutation
	CancelSuccess string   `json:"cancel_success,omitempty"` // exact first reply text for an aborted undo group
}

// Map is everything the adapter needs to know about one DAW.
type Map struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`       // the DAW listens here (localhost)
	ReplyPort int    `json:"reply_port"` // the DAW answers here
	Base      int    `json:"base"`       // 0 or 1: index of the first track/device/param
	App       string `json:"app"`        // macOS bundle id, for listening to its output

	Ping        Command `json:"ping"`
	TrackCount  Command `json:"track_count"`
	TrackName   Command `json:"track_name"`
	DeviceNames Command `json:"device_names"` // reply: list of names
	ParamNames  Command `json:"param_names"`  // reply: list
	ParamValues Command `json:"param_values"` // reply: list, native units
	ParamMins   Command `json:"param_mins"`   // reply: list; empty Send = all 0
	ParamMaxs   Command `json:"param_maxs"`   // reply: list; empty Send = all 1
	ParamText   Command `json:"param_text"`   // reply: one readable value; optional
	GetParam    Command `json:"get_param"`    // reply: one value
	SetParam    Command `json:"set_param"`    // no reply needed
	SetVolume   Command `json:"set_volume"`   // optional, value 0..1
	SetPan      Command `json:"set_pan,omitempty"`
	SetMute     Command `json:"set_mute,omitempty"`
	SetSolo     Command `json:"set_solo,omitempty"`
	SetBypass   Command `json:"set_bypass,omitempty"`
	Undo        Command `json:"undo"` // optional
	Play        Command `json:"play"` // optional
	Stop        Command `json:"stop"` // optional

	// Building chains, optional: what can be added, adding it at {fx} on
	// {track} by {name} (reply: where it went), taking {fx} off {track}.
	DeviceCatalog Command `json:"device_catalog"` // reply: list of names
	InsertDevice  Command `json:"insert_device"`
	RemoveDevice  Command `json:"remove_device"`
	MoveDevice    Command `json:"move_device,omitempty"`
	DevicePresets Command `json:"device_presets,omitempty"`
	SetPreset     Command `json:"set_preset,omitempty"`
	SavePresetAs  Command `json:"save_preset_as,omitempty"`

	// Reading the session, optional. TrackAudio: reply 1 when the track holds
	// audio (not MIDI), args {track}. SceneCount: reply the number of scenes.
	// HasClip: reply 1 when clip slot {slot} of {track} holds a clip.
	// AudioEngine: reply 1 when the DAW's audio engine is running.
	TrackAudio  Command `json:"track_audio,omitempty"`
	SceneCount  Command `json:"scene_count,omitempty"`
	HasClip     Command `json:"has_clip,omitempty"`
	AudioEngine Command `json:"audio_engine,omitempty"`

	// Session and track management, optional:
	CreateTrack            *Command `json:"create_track,omitempty"`
	CreateMIDITrack        *Command `json:"create_midi_track,omitempty"`
	DuplicateTrack         *Command `json:"duplicate_track,omitempty"`
	DeleteTrack            *Command `json:"delete_track,omitempty"`
	RenameTrack            *Command `json:"rename_track,omitempty"`
	InsertMedia            *Command `json:"insert_media,omitempty"`
	CreateSend             *Command `json:"create_send,omitempty"`
	SetTempo               *Command `json:"set_tempo,omitempty"`
	SetLoop                *Command `json:"set_loop,omitempty"`
	BeginUndoAction        *Command `json:"begin_undo_action,omitempty"`
	EndUndoAction          *Command `json:"end_undo_action,omitempty"`
	UndoActionCurrent      *Command `json:"undo_action_current,omitempty"`
	CaptureSessionSnapshot *Command `json:"capture_session_snapshot,omitempty"`
	RestoreSessionSnapshot *Command `json:"restore_session_snapshot,omitempty"`
	SetTimeSignature       *Command `json:"set_time_signature,omitempty"`
	SetTrackArm            *Command `json:"set_track_arm,omitempty"`
	SetTrackMonitor        *Command `json:"set_track_monitor,omitempty"`
	SetTrackInput          *Command `json:"set_track_input,omitempty"`
	RemoveSend             *Command `json:"remove_send,omitempty"`
	ListMarkers            *Command `json:"list_markers,omitempty"`
	CreateMarker           *Command `json:"create_marker,omitempty"`
	GoToMarker             *Command `json:"goto_marker,omitempty"`
	DeleteMarker           *Command `json:"delete_marker,omitempty"`
	SaveProject            *Command `json:"save_project,omitempty"`

	RecordInputStart  *Command `json:"record_input_start,omitempty"`
	RecordInputFinish *Command `json:"record_input_finish,omitempty"`
	RenderRange       *Command `json:"render_range,omitempty"`
	// RenderTrack starts a native in-session render; RenderResult returns
	// "pending" until the audio file is finalized, then its path.
	RenderTrackStart  *Command `json:"render_track_start,omitempty"`
	RenderTrackResult *Command `json:"render_track_result,omitempty"`

	// ShowDevice opens or focuses device {fx} of {track} on screen (its
	// plugin window, or the device view), so Muse can see it. Optional.
	ShowDevice Command `json:"show_device"`

	// Extra allows custom DAW-specific OSC commands discovered during onboarding.
	Extra map[string]Command `json:"extra,omitempty"`
}

// Shows reports whether the map can put a device on screen.
func (m Map) Shows() bool { return len(m.ShowDevice.Send) > 0 }

// Chains reports whether the map can add and remove devices.
func (m Map) Chains() bool {
	return len(m.DeviceCatalog.Send) > 0 && len(m.InsertDevice.Send) > 0 && len(m.RemoveDevice.Send) > 0
}

// Words in an address that no role here ever needs. A map that sends one
// of them is refused, whoever wrote it.
var forbidden = regexp.MustCompile(`(?i)(delete|remove|quit|save|export|create|duplicate|clear|load|record|arm)`)

func (m Map) commands() map[string]Command {
	out := map[string]Command{
		"ping": m.Ping, "track_count": m.TrackCount, "track_name": m.TrackName,
		"device_names": m.DeviceNames, "param_names": m.ParamNames, "param_values": m.ParamValues,
		"param_mins": m.ParamMins, "param_maxs": m.ParamMaxs, "param_text": m.ParamText,
		"get_param": m.GetParam, "set_param": m.SetParam, "set_volume": m.SetVolume,
		"set_pan": m.SetPan, "set_mute": m.SetMute, "set_solo": m.SetSolo,
		"set_bypass": m.SetBypass, "move_device": m.MoveDevice,
		"device_presets": m.DevicePresets, "set_preset": m.SetPreset, "save_preset_as": m.SavePresetAs,
		"undo": m.Undo, "play": m.Play, "stop": m.Stop,
		"device_catalog": m.DeviceCatalog, "insert_device": m.InsertDevice, "remove_device": m.RemoveDevice,
		"show_device": m.ShowDevice,
		"track_audio": m.TrackAudio, "scene_count": m.SceneCount, "has_clip": m.HasClip,
		"audio_engine": m.AudioEngine,
	}
	if m.CreateTrack != nil {
		out["create_track"] = *m.CreateTrack
	}
	if m.CreateMIDITrack != nil {
		out["create_midi_track"] = *m.CreateMIDITrack
	}
	if m.DuplicateTrack != nil {
		out["duplicate_track"] = *m.DuplicateTrack
	}
	if m.DeleteTrack != nil {
		out["delete_track"] = *m.DeleteTrack
	}
	if m.RenameTrack != nil {
		out["rename_track"] = *m.RenameTrack
	}
	if m.InsertMedia != nil {
		out["insert_media"] = *m.InsertMedia
	}
	if m.CreateSend != nil {
		out["create_send"] = *m.CreateSend
	}
	if m.SetTempo != nil {
		out["set_tempo"] = *m.SetTempo
	}
	if m.SetLoop != nil {
		out["set_loop"] = *m.SetLoop
	}
	for role, c := range map[string]*Command{"set_time_signature": m.SetTimeSignature, "set_track_arm": m.SetTrackArm, "set_track_monitor": m.SetTrackMonitor, "set_track_input": m.SetTrackInput, "remove_send": m.RemoveSend, "list_markers": m.ListMarkers, "create_marker": m.CreateMarker, "goto_marker": m.GoToMarker, "delete_marker": m.DeleteMarker, "save_project": m.SaveProject, "begin_undo_action": m.BeginUndoAction, "end_undo_action": m.EndUndoAction, "undo_action_current": m.UndoActionCurrent, "capture_session_snapshot": m.CaptureSessionSnapshot, "restore_session_snapshot": m.RestoreSessionSnapshot} {
		if c != nil {
			out[role] = *c
		}
	}
	for k, v := range m.Extra {
		out[k] = v
	}
	for role, c := range map[string]*Command{"record_input_start": m.RecordInputStart, "record_input_finish": m.RecordInputFinish, "render_range": m.RenderRange, "render_track_start": m.RenderTrackStart, "render_track_result": m.RenderTrackResult} {
		if c != nil {
			out[role] = *c
		}
	}
	return out
}

// Check rejects maps that could do more than read and set controls.
func (m Map) Check() error {
	if m.Port <= 0 || m.ReplyPort <= 0 {
		return fmt.Errorf("ports missing")
	}
	if m.Base != 0 && m.Base != 1 {
		return fmt.Errorf("base must be 0 or 1")
	}
	for role, c := range m.commands() {
		if role == "set_time_signature" || role == "remove_send" || role == "goto_marker" || role == "delete_marker" || role == "save_project" {
			if c.Reply == "" || c.Success == "" {
				return fmt.Errorf("%s: reply address and exact success token are required", role)
			}
		}
		if len(c.Send) == 0 {
			continue
		}
		if !strings.HasPrefix(c.Send[0], "/") {
			return fmt.Errorf("%s: address must start with /", role)
		}
		if role == "record_input_start" || role == "record_input_finish" {
			if c.Reply == "" {
				return fmt.Errorf("%s requires a confirmed reply", role)
			}
			if forbidden.MatchString(strings.NewReplacer("record", "", "arm", "").Replace(strings.ToLower(c.Send[0]))) {
				return fmt.Errorf("%s: unrelated destructive recording address", role)
			}
		} else if role == "remove_device" || role == "delete_track" || role == "delete_marker" || role == "midi_clip_delete" { // deletion roles
			if forbidden.MatchString(strings.NewReplacer("delete", "", "remove", "").Replace(strings.ToLower(c.Send[0]))) {
				return fmt.Errorf("%s: %s is not allowed", role, c.Send[0])
			}
		} else if role == "set_preset" || role == "device_presets" || role == "save_preset_as" || role == "insert_device" || role == "midi_clip_create" ||
			role == "create_track" || role == "create_midi_track" || role == "duplicate_track" || role == "insert_media" || role == "create_send" {
			address := strings.NewReplacer("load", "", "create", "", "duplicate", "").Replace(strings.ToLower(c.Send[0]))
			if role == "save_preset_as" {
				address = strings.ReplaceAll(address, "save", "")
			}
			if forbidden.MatchString(address) {
				return fmt.Errorf("%s: %s is not allowed", role, c.Send[0])
			}
		} else if role == "remove_send" || role == "save_project" || role == "create_marker" || role == "set_time_signature" || role == "set_track_arm" || role == "set_track_monitor" || role == "set_track_input" {
			if forbidden.MatchString(strings.NewReplacer("remove", "", "save", "", "create", "", "arm", "").Replace(strings.ToLower(c.Send[0]))) {
				return fmt.Errorf("%s: %s is not allowed", role, c.Send[0])
			}
		} else if forbidden.MatchString(c.Send[0]) {
			return fmt.Errorf("%s: %s is not allowed (only reads, parameter sets, volume, undo and transport)", role, c.Send[0])
		}
		for _, a := range c.Send[1:] {
			if _, _, err := parseArg(a); err != nil {
				return fmt.Errorf("%s: %w", role, err)
			}
		}
	}
	for _, role := range []string{"ping", "track_count", "track_name", "device_names", "param_names", "param_values", "set_param", "get_param"} {
		if len(m.commands()[role].Send) == 0 {
			return fmt.Errorf("%s is required", role)
		}
	}
	return nil
}

// vars fills the placeholders of one call.
type vars struct {
	track, fx, param             int
	to, slot                     int
	flag, marker                 int
	send, numerator, denominator int
	recordStart                  float64
	start, end                   float64
	value                        float64
	name                         string
	preset                       string
}

func parseArg(a string) (byte, string, error) {
	if len(a) < 2 || a[1] != ':' || !strings.ContainsRune("ifs", rune(a[0])) {
		return 0, "", fmt.Errorf("arg %q must use i, f, or s type prefix", a)
	}
	return a[0], a[2:], nil
}

// build turns a command into an address and typed args.
func (c Command) build(v vars) (string, []any, error) {
	var args []any
	for _, a := range c.Send[1:] {
		kind, body, err := parseArg(a)
		if err != nil {
			return "", nil, err
		}
		body = strings.NewReplacer(
			"{record_start}", strconv.FormatFloat(v.recordStart, 'g', -1, 64),
			"{track}", strconv.Itoa(v.track),
			"{fx}", strconv.Itoa(v.fx),
			"{to}", strconv.Itoa(v.to),
			"{slot}", strconv.Itoa(v.slot),
			"{param}", strconv.Itoa(v.param),
			"{name}", v.name,
			"{preset}", v.preset,
			"{marker}", strconv.Itoa(v.marker),
			"{send}", strconv.Itoa(v.send),
			"{numerator}", strconv.Itoa(v.numerator),
			"{denominator}", strconv.Itoa(v.denominator),
			"{flag}", strconv.Itoa(v.flag),
			"{start}", strconv.FormatFloat(v.start, 'g', -1, 64),
			"{end}", strconv.FormatFloat(v.end, 'g', -1, 64),
			"{value}", strconv.FormatFloat(v.value, 'g', -1, 64),
		).Replace(body)
		switch kind {
		case 'i':
			n, err := strconv.Atoi(body)
			if err != nil {
				f, ferr := strconv.ParseFloat(body, 64)
				if ferr != nil {
					return "", nil, fmt.Errorf("arg %q is not an int", a)
				}
				n = int(math.Round(f))
			}
			args = append(args, int32(n))
		case 'f':
			f, err := strconv.ParseFloat(body, 64)
			if err != nil {
				return "", nil, fmt.Errorf("arg %q is not a float", a)
			}
			args = append(args, float32(f))
		case 's':
			args = append(args, body)
		}
	}
	return c.Send[0], args, nil
}

// Load reads a saved map.
func Load(path string) (Map, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Map{}, err
	}
	var m Map
	if err := json.Unmarshal(b, &m); err != nil {
		return Map{}, err
	}
	return m, m.Check()
}

// Save writes a map for later runs.
func Save(m Map, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
