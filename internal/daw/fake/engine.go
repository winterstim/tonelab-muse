package fake

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type sessionSnapshot struct {
	name string
	data []byte
}

type actionGroup struct {
	name     string
	snapshot []byte
}

// AudioEngineState tracks the state of the DAW audio engine.
type AudioEngineState struct {
	Running bool `json:"running"`
}

// Engine is an in-memory Fake DAW implementing all core interfaces and
// tracking rich, mutable session state with undo, rendering, and snapshots.
type Engine struct {
	mu sync.RWMutex

	SessionTracks  []*Track            `json:"tracks"`
	Transport      Transport           `json:"transport"`
	SessionMarkers []core.Marker       `json:"markers"`
	CatalogPlugins []core.CatalogEntry `json:"catalog_plugins"`
	AudioEngine    AudioEngineState    `json:"audio_engine"`
	ProjectSaved   bool                `json:"project_saved"`
	ProjectPath    string              `json:"project_path"`
	LastSaved      time.Time           `json:"last_saved"`

	turboEnabled bool
	mode         string // "review", "default", "turbo"

	undoStack     []sessionSnapshot
	redoStack     []sessionSnapshot
	currentAction *actionGroup

	recordingActive   bool
	recordingTrack    int
	recordingToken    string
	recordingDuration float64
	renderDir         string

	snapshots   map[string][]byte
	taste       core.Taste
	callHistory map[string]string

	server *Server
}

// FakeDAW is an alias for Engine.
type FakeDAW = Engine

var (
	_ core.DAW                  = (*Engine)(nil)
	_ core.SessionController    = (*Engine)(nil)
	_ core.SessionTrackControls = (*Engine)(nil)
	_ core.SessionRouting       = (*Engine)(nil)
	_ core.SessionMarkers       = (*Engine)(nil)
	_ core.SessionProject       = (*Engine)(nil)
	_ core.SessionMeter         = (*Engine)(nil)
	_ core.ChainEditor          = (*Engine)(nil)
	_ core.ChainMover           = (*Engine)(nil)
	_ core.Bypasser             = (*Engine)(nil)
	_ core.Presetter            = (*Engine)(nil)
	_ core.PresetSaver          = (*Engine)(nil)
	_ core.TurboChain           = (*Engine)(nil)
	_ core.ValueFinder          = (*Engine)(nil)
	_ core.ModelSwapper         = (*Engine)(nil)
	_ core.AudioEngineChecker   = (*Engine)(nil)
	_ core.Renderer             = (*Engine)(nil)
	_ core.RangeRenderer        = (*Engine)(nil)
	_ core.InputRecorder        = (*Engine)(nil)
	_ core.UndoActionGrouper    = (*Engine)(nil)
	_ core.RequestSnapshotter   = (*Engine)(nil)
	_ core.ParamReader          = (*Engine)(nil)
	_ core.Capturer             = (*Engine)(nil)
	_ core.ContractExecutor     = (*Engine)(nil)
)

// DefaultCatalog returns the standard installed plugin catalog.
func DefaultCatalog() []core.CatalogEntry {
	return []core.CatalogEntry{
		{Name: "Amp Sim (Test)", Kind: "amp"},
		{Name: "ReaEQ (Cockos)", Kind: "eq"},
		{Name: "ReaVerb (Cockos)", Kind: "reverb"},
		{Name: "ReaComp (Cockos)", Kind: "compressor"},
		{Name: "ReaDelay (Cockos)", Kind: "delay"},
		{Name: "Chorus (Test)", Kind: "modulation"},
		{Name: "Overdrive (Test)", Kind: "distortion"},
		{Name: "Cabinet Sim (Test)", Kind: "cab"},
		{Name: "Multi-FX (Test)", Kind: "multi-fx"},
		{Name: "ReadOnly FX (Test)", Kind: "utility"},
	}
}

// New creates a new fake DAW Engine initialized with real mutable state.
func New(tracks ...*Track) *Engine {
	e := &Engine{
		Transport: Transport{
			PlayState:          "stopped",
			PositionSec:        0,
			LoopEnabled:        false,
			LoopStartSec:       0,
			LoopEndSec:         4,
			BPM:                120,
			TimeSignatureNum:   4,
			TimeSignatureDenom: 4,
		},
		CatalogPlugins: DefaultCatalog(),
		AudioEngine:    AudioEngineState{Running: true},
		ProjectSaved:   true,
		ProjectPath:    filepath.Join(os.TempDir(), "fake-session.rpp"),
		LastSaved:      time.Now(),
		mode:           "default",
		renderDir:      filepath.Join(os.TempDir(), "tonelab-muse-fake-render"),
		snapshots:      make(map[string][]byte),
		callHistory:    make(map[string]string),
	}
	_ = os.MkdirAll(e.renderDir, 0o755)

	if len(tracks) == 0 {
		tracks = DemoStudio()
	}
	e.SessionTracks = make([]*Track, len(tracks))
	for i, tr := range tracks {
		cl := tr.Clone()
		if cl.ID == 0 {
			cl.ID = i + 1
		}
		if cl.Type == "" {
			if strings.Contains(strings.ToLower(cl.Name), "bus") {
				cl.Type = "bus"
			} else {
				cl.Type = "audio"
			}
		}
		if cl.Volume == 0 {
			cl.Volume = 1.0
		}
		if cl.Monitor == "" {
			cl.Monitor = "auto"
		}
		if cl.InputSelect == "" {
			cl.InputSelect = fmt.Sprintf("Input %d", i+1)
		}
		e.reindexFX(cl)
		e.SessionTracks[i] = cl
	}

	// Default project markers
	e.SessionMarkers = []core.Marker{
		{Index: 1, Name: "Intro", PositionSec: 0, EndSec: 8, Region: true},
		{Index: 2, Name: "Verse 1", PositionSec: 8, EndSec: 24, Region: true},
		{Index: 3, Name: "Chorus", PositionSec: 24, EndSec: 40, Region: true},
	}

	return e
}

// TracksRaw returns the underlying slice of mutable *Track pointers.
func (e *Engine) TracksRaw() []*Track {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.SessionTracks
}

func (e *Engine) reindexTracks() {
	for i, t := range e.SessionTracks {
		t.ID = i + 1
		e.reindexFX(t)
	}
	if e.server != nil {
		e.server.Tracks = e.SessionTracks
	}
}

func (e *Engine) reindexFX(t *Track) {
	for fIdx, f := range t.FX {
		f.ID = fIdx + 1
		if f.WetDry == 0 && !f.Bypassed {
			f.WetDry = 1.0
		}
		if len(f.Presets) == 0 {
			f.Presets = []string{"Default", "Warm", "Bright", "Custom"}
			f.CurrentPreset = "Default"
		}
		if f.Models == nil && strings.Contains(f.Name, "Amp") {
			f.Models = map[string][]string{
				"Amp": {"Clean", "Crunch", "Lead", "Plexi"},
				"Cab": {"4x12 Vintage", "2x12 British", "1x12 Tweed"},
			}
			f.CurrentModel = map[string]string{
				"Amp": "Clean",
				"Cab": "4x12 Vintage",
			}
		}
		for pIdx, p := range f.Params {
			p.ID = string(core.MakeParamID(f.ID, pIdx+1))
		}
	}
}

// SetAudioEngineRunning toggles the audio engine state for silence diagnosis tests.
func (e *Engine) SetAudioEngineRunning(running bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.AudioEngine.Running = running
}

// SetMode sets the operation mode ("review", "default", "turbo").
func (e *Engine) SetMode(mode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mode = mode
	if mode == "turbo" {
		e.turboEnabled = true
	}
}

// Mode returns the current mode.
func (e *Engine) Mode() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mode
}

// --- Snapshot and Undo internals ---

type serializableState struct {
	Tracks             []*Track            `json:"tracks"`
	Transport          Transport           `json:"transport"`
	Markers            []core.Marker       `json:"markers"`
	AudioEngineRunning bool                `json:"audio_engine_running"`
	ProjectSaved       bool                `json:"project_saved"`
	Mode               string              `json:"mode"`
	TurboEnabled       bool                `json:"turbo_enabled"`
	CatalogPlugins     []core.CatalogEntry `json:"catalog_plugins"`
}

func (e *Engine) captureSnapshot() []byte {
	st := serializableState{
		Transport:          e.Transport,
		Markers:            append([]core.Marker(nil), e.SessionMarkers...),
		AudioEngineRunning: e.AudioEngine.Running,
		ProjectSaved:       e.ProjectSaved,
		Mode:               e.mode,
		TurboEnabled:       e.turboEnabled,
		CatalogPlugins:     append([]core.CatalogEntry(nil), e.CatalogPlugins...),
	}
	st.Tracks = make([]*Track, len(e.SessionTracks))
	for i, t := range e.SessionTracks {
		st.Tracks[i] = t.Clone()
	}
	b, _ := json.Marshal(st)
	return b
}

func (e *Engine) restoreSnapshot(data []byte) error {
	var st serializableState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("restore snapshot: %w", err)
	}
	e.SessionTracks = st.Tracks
	e.Transport = st.Transport
	e.SessionMarkers = st.Markers
	e.AudioEngine.Running = st.AudioEngineRunning
	e.ProjectSaved = st.ProjectSaved
	e.mode = st.Mode
	e.turboEnabled = st.TurboEnabled
	if len(st.CatalogPlugins) > 0 {
		e.CatalogPlugins = st.CatalogPlugins
	}
	e.reindexTracks()
	return nil
}

func (e *Engine) pushUndo(name string) {
	if e.currentAction != nil {
		if e.currentAction.snapshot == nil {
			e.currentAction.snapshot = e.captureSnapshot()
		}
		return
	}
	snap := e.captureSnapshot()
	e.undoStack = append(e.undoStack, sessionSnapshot{name: name, data: snap})
	e.redoStack = nil
}

// --- core.DAW ---

func (e *Engine) Tracks(ctx context.Context) ([]core.Track, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]core.Track, len(e.SessionTracks))
	for i, t := range e.SessionTracks {
		res[i] = core.Track{
			Index: t.ID,
			Name:  t.Name,
			Audio: t.Type != "midi",
		}
	}
	return res, nil
}

func (e *Engine) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return nil, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	res := make([]core.FX, len(t.FX))
	for i, f := range t.FX {
		params := make([]core.Param, len(f.Params))
		for j, p := range f.Params {
			params[j] = core.Param{
				ID:      core.MakeParamID(f.ID, j+1),
				Name:    p.Name,
				Kind:    p.Kind,
				Text:    p.FormattedText(),
				Value:   p.Value,
				Unnamed: p.Dead || strings.HasPrefix(p.Name, "Param "),
				Steps:   p.Steps,
			}
		}
		res[i] = core.FX{
			Index:  f.ID,
			Name:   f.Name,
			Params: params,
		}
	}
	return res, nil
}

func (e *Engine) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return core.Applied{}, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	fIdx, pIdx, p, fx := e.findParam(t, id)
	if p == nil {
		return core.Applied{}, fmt.Errorf("parameter %s not found on track %d", id, track)
	}
	_ = fIdx
	_ = pIdx

	if p.ReadOnly || fx.ReadOnly {
		return core.Applied{
			Param:     id,
			Name:      p.Name,
			Sent:      v,
			Confirmed: p.Value,
			Text:      p.FormattedText(),
			Status:    core.StatusUnconfirmed,
		}, nil
	}
	if p.Dead {
		return core.Applied{
			Param:     id,
			Name:      p.Name,
			Sent:      v,
			Confirmed: 0,
			Text:      p.FormattedText(),
			Status:    core.StatusConfirmed,
		}, nil
	}

	e.pushUndo("set_param")
	v = math.Max(0, math.Min(1, v))
	if p.Steps > 1 {
		v = math.Round(v*float64(p.Steps-1)) / float64(p.Steps-1)
	}
	p.Value = v

	return core.Applied{
		Param:     id,
		Name:      p.Name,
		Sent:      v,
		Confirmed: p.Value,
		Text:      p.FormattedText(),
		Status:    core.StatusConfirmed,
	}, nil
}

func (e *Engine) findParam(t *Track, id core.ParamID) (int, int, *Param, *FX) {
	idStr := string(id)
	// Try parsing via core.ParamID ("fx1.p6")
	if fIdx, pIdx, err := id.Parse(); err == nil {
		if fIdx >= 1 && fIdx <= len(t.FX) {
			fx := t.FX[fIdx-1]
			if pIdx >= 1 && pIdx <= len(fx.Params) {
				return fIdx - 1, pIdx - 1, fx.Params[pIdx-1], fx
			}
		}
	}
	parts := strings.Split(idStr, "/")
	if len(parts) == 2 {
		fIdx, err1 := strconv.Atoi(parts[0])
		pIdx, err2 := strconv.Atoi(parts[1])
		if err1 == nil && err2 == nil && fIdx >= 1 && fIdx <= len(t.FX) {
			fx := t.FX[fIdx-1]
			if pIdx >= 1 && pIdx <= len(fx.Params) {
				return fIdx - 1, pIdx - 1, fx.Params[pIdx-1], fx
			}
		}
	}
	// Try single integer (1-based param index on first FX)
	if pIdx, err := strconv.Atoi(idStr); err == nil && len(t.FX) > 0 {
		if pIdx >= 1 && pIdx <= len(t.FX[0].Params) {
			return 0, pIdx - 1, t.FX[0].Params[pIdx-1], t.FX[0]
		}
	}
	for fIdx, fx := range t.FX {
		for pIdx, p := range fx.Params {
			if string(p.ID) == idStr || p.Name == idStr {
				return fIdx, pIdx, p, fx
			}
		}
	}
	return -1, -1, nil, nil
}

func (e *Engine) SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return 0, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	e.pushUndo("set_track_" + prop)
	switch strings.ToLower(prop) {
	case "volume":
		t.Volume = math.Max(0, v)
		return t.Volume, nil
	case "pan":
		t.Pan = math.Max(-1, math.Min(1, v))
		return t.Pan, nil
	case "mute":
		t.Muted = (v > 0.5)
		if t.Muted {
			return 1, nil
		}
		return 0, nil
	case "solo":
		t.Soloed = (v > 0.5)
		if t.Soloed {
			return 1, nil
		}
		return 0, nil
	case "arm":
		t.Armed = (v > 0.5)
		if t.Armed {
			return 1, nil
		}
		return 0, nil
	case "monitor":
		switch int(v) {
		case 0:
			t.Monitor = "off"
		case 1:
			t.Monitor = "auto"
		case 2:
			t.Monitor = "in"
		}
		return v, nil
	default:
		return 0, fmt.Errorf("unknown track property: %s", prop)
	}
}

func (e *Engine) Undo(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.undoStack) == 0 {
		return errors.New("nothing to undo")
	}
	lastIdx := len(e.undoStack) - 1
	top := e.undoStack[lastIdx]
	e.undoStack = e.undoStack[:lastIdx]

	// Push current state to redo stack
	e.redoStack = append(e.redoStack, sessionSnapshot{name: top.name, data: e.captureSnapshot()})
	return e.restoreSnapshot(top.data)
}

func (e *Engine) Redo(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.redoStack) == 0 {
		return errors.New("nothing to redo")
	}
	lastIdx := len(e.redoStack) - 1
	top := e.redoStack[lastIdx]
	e.redoStack = e.redoStack[:lastIdx]

	e.undoStack = append(e.undoStack, sessionSnapshot{name: top.name, data: e.captureSnapshot()})
	return e.restoreSnapshot(top.data)
}

func (e *Engine) Alive(ctx context.Context) error {
	return nil
}

// --- core.SessionController ---

func (e *Engine) CreateTrack(ctx context.Context, name string, index int) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pushUndo("create_track")
	newTr := &Track{
		Name:        name,
		Type:        "audio",
		Volume:      1.0,
		Pan:         0.0,
		Monitor:     "auto",
		InputSelect: fmt.Sprintf("Input %d", len(e.SessionTracks)+1),
	}
	if index <= 0 || index > len(e.SessionTracks) {
		e.SessionTracks = append(e.SessionTracks, newTr)
	} else {
		idx := index - 1
		e.SessionTracks = append(e.SessionTracks[:idx], append([]*Track{newTr}, e.SessionTracks[idx:]...)...)
	}
	e.reindexTracks()
	return newTr.ID, nil
}

func (e *Engine) DuplicateTrack(ctx context.Context, track int, name string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return 0, fmt.Errorf("track %d out of range", track)
	}
	e.pushUndo("duplicate_track")
	orig := e.SessionTracks[track-1]
	clone := orig.Clone()
	if name != "" {
		clone.Name = name
	} else {
		clone.Name = orig.Name + " Copy"
	}
	idx := track
	e.SessionTracks = append(e.SessionTracks[:idx], append([]*Track{clone}, e.SessionTracks[idx:]...)...)
	e.reindexTracks()
	return clone.ID, nil
}

func (e *Engine) DeleteTrack(ctx context.Context, track int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	e.pushUndo("delete_track")
	idx := track - 1
	e.SessionTracks = append(e.SessionTracks[:idx], e.SessionTracks[idx+1:]...)

	// Clean up sends pointing to deleted track and shift targets
	for _, t := range e.SessionTracks {
		var kept []Send
		for _, s := range t.Sends {
			if s.ToTrack == track {
				continue
			}
			if s.ToTrack > track {
				s.ToTrack--
			}
			kept = append(kept, s)
		}
		t.Sends = kept
	}
	e.reindexTracks()
	return nil
}

func (e *Engine) RenameTrack(ctx context.Context, track int, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	e.pushUndo("rename_track")
	e.SessionTracks[track-1].Name = name
	return nil
}

func (e *Engine) InsertMedia(ctx context.Context, track int, path string, positionSec float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	e.pushUndo("insert_media")
	t := e.SessionTracks[track-1]
	t.MediaClips = append(t.MediaClips, MediaClip{
		Path:        path,
		PositionSec: positionSec,
		DurationSec: 4.0,
	})
	t.HasClips = true
	return nil
}

func (e *Engine) CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fromTrack < 1 || fromTrack > len(e.SessionTracks) {
		return fmt.Errorf("source track %d out of range", fromTrack)
	}
	if toTrack < 1 || toTrack > len(e.SessionTracks) {
		return fmt.Errorf("target track %d out of range", toTrack)
	}
	if fromTrack == toTrack {
		return errors.New("cannot create send from a track to itself")
	}
	e.pushUndo("create_send")
	t := e.SessionTracks[fromTrack-1]
	t.Sends = append(t.Sends, Send{ToTrack: toTrack, LevelDB: levelDB})
	return nil
}

func (e *Engine) SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pushUndo("set_transport")
	switch strings.ToLower(action) {
	case "play":
		e.Transport.PlayState = "playing"
	case "stop":
		e.Transport.PlayState = "stopped"
	case "pause":
		e.Transport.PlayState = "paused"
	case "position":
		e.Transport.PositionSec = posSec
	case "tempo":
		if bpm > 0 {
			e.Transport.BPM = bpm
		}
	case "loop":
		e.Transport.LoopStartSec = loopStart
		e.Transport.LoopEndSec = loopEnd
		e.Transport.LoopEnabled = true
	case "loop_off":
		e.Transport.LoopEnabled = false
	}
	if bpm > 0 && action != "tempo" {
		e.Transport.BPM = bpm
	}
	if posSec >= 0 && action != "position" {
		e.Transport.PositionSec = posSec
	}
	if loopEnd > loopStart && action != "loop" {
		e.Transport.LoopStartSec = loopStart
		e.Transport.LoopEndSec = loopEnd
	}
	return nil
}

// --- core.SessionTrackControls ---

func (e *Engine) SetTrackControl(ctx context.Context, track int, control string, value string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	e.pushUndo("set_track_control_" + control)
	switch strings.ToLower(control) {
	case "input", "input_select":
		t.InputSelect = value
		return value, nil
	case "monitor":
		t.Monitor = value
		return value, nil
	case "arm":
		t.Armed = (value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "on"))
		if t.Armed {
			return "1", nil
		}
		return "0", nil
	case "mute":
		t.Muted = (value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "on"))
		if t.Muted {
			return "1", nil
		}
		return "0", nil
	case "solo":
		t.Soloed = (value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "on"))
		if t.Soloed {
			return "1", nil
		}
		return "0", nil
	default:
		return "", fmt.Errorf("unknown track control: %s", control)
	}
}

// --- core.SessionRouting ---

func (e *Engine) RemoveSend(ctx context.Context, fromTrack, send int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fromTrack < 1 || fromTrack > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", fromTrack)
	}
	t := e.SessionTracks[fromTrack-1]
	if send < 1 || send > len(t.Sends) {
		return fmt.Errorf("send %d out of range on track %d", send, fromTrack)
	}
	e.pushUndo("remove_send")
	idx := send - 1
	t.Sends = append(t.Sends[:idx], t.Sends[idx+1:]...)
	return nil
}

// --- core.SessionMarkers ---

func (e *Engine) Markers(ctx context.Context) ([]core.Marker, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]core.Marker(nil), e.SessionMarkers...), nil
}

func (e *Engine) CreateMarker(ctx context.Context, positionSec, endSec float64, name string, region bool) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pushUndo("create_marker")
	idx := len(e.SessionMarkers) + 1
	e.SessionMarkers = append(e.SessionMarkers, core.Marker{
		Index:       idx,
		Name:        name,
		PositionSec: positionSec,
		EndSec:      endSec,
		Region:      region,
	})
	return idx, nil
}

func (e *Engine) GoToMarker(ctx context.Context, index int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.SessionMarkers {
		if m.Index == index {
			e.Transport.PositionSec = m.PositionSec
			return nil
		}
	}
	return fmt.Errorf("marker index %d not found", index)
}

// --- core.SessionProject ---

func (e *Engine) SaveProject(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ProjectSaved = true
	e.LastSaved = time.Now()
	return nil
}

// --- core.SessionMeter ---

func (e *Engine) SetTimeSignature(ctx context.Context, numerator, denominator int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if numerator <= 0 || denominator <= 0 {
		return errors.New("time signature numerator and denominator must be positive")
	}
	e.pushUndo("set_time_signature")
	e.Transport.TimeSignatureNum = numerator
	e.Transport.TimeSignatureDenom = denominator
	return nil
}

// --- core.ChainEditor ---

func (e *Engine) Catalog(ctx context.Context) ([]core.CatalogEntry, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]core.CatalogEntry(nil), e.CatalogPlugins...), nil
}

func (e *Engine) AddFX(ctx context.Context, track int, name string, pos int) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return 0, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	e.pushUndo("add_fx")

	fx := e.instantiateFX(name)
	fx.UserAdded = true

	if pos <= 0 || pos > len(t.FX) {
		t.FX = append(t.FX, fx)
	} else {
		idx := pos - 1
		t.FX = append(t.FX[:idx], append([]*FX{fx}, t.FX[idx:]...)...)
	}
	e.reindexFX(t)
	return fx.ID, nil
}

func (e *Engine) instantiateFX(name string) *FX {
	pct := func(v float64) string { return strconv.Itoa(int(v * 10)) }
	db := func(v float64) string { return strconv.FormatFloat((v-0.5)*48, 'f', 1, 64) }

	switch name {
	case "Amp Sim (Test)":
		return &FX{
			Name: name,
			Kind: "amp",
			Params: []*Param{
				{Name: "Param 1", Dead: true},
				{Name: "Gain", Value: 0.5, Text: pct},
				{Name: "Bass", Value: 0.5, Text: pct},
				{Name: "Treble", Value: 0.5, Text: pct},
				{Name: "Master", Value: 0.7, Text: pct},
				{Name: "Bright", Value: 0, EnumValues: []string{"Off", "On"}, Kind: "switch"},
			},
			Presets:       []string{"Default", "Clean Sparkle", "Warm Crunch", "Heavy Lead"},
			CurrentPreset: "Default",
			Models: map[string][]string{
				"Amp": {"Clean", "Crunch", "Lead", "Plexi"},
				"Cab": {"4x12 Vintage", "2x12 British", "1x12 Tweed"},
			},
			CurrentModel: map[string]string{
				"Amp": "Clean",
				"Cab": "4x12 Vintage",
			},
		}
	case "ReaEQ (Cockos)":
		return &FX{
			Name: name,
			Kind: "eq",
			Params: []*Param{
				{Name: "Freq-Low Shelf", Value: 0.14, TextVal: "100.0 Hz", Unit: "Hz"},
				{Name: "Gain-Low Shelf", Value: 0.5, Text: db, Unit: "dB"},
				{Name: "Freq-Band 2", Value: 0.29, TextVal: "1000.0 Hz", Unit: "Hz"},
				{Name: "Gain-Band 2", Value: 0.5, Text: db, Unit: "dB"},
				{Name: "Freq-High Shelf 4", Value: 0.74, TextVal: "5000.0 Hz", Unit: "Hz"},
				{Name: "Gain-High Shelf 4", Value: 0.5, Text: db, Unit: "dB"},
			},
			Presets:       []string{"Default", "Vocal High Pass", "Mid Scoop"},
			CurrentPreset: "Default",
		}
	case "ReadOnly FX (Test)":
		return &FX{
			Name:     name,
			Kind:     "utility",
			ReadOnly: true,
			Params: []*Param{
				{Name: "Locked Param", Value: 0.3, ReadOnly: true},
			},
		}
	default:
		return &FX{
			Name: name,
			Kind: "generic",
			Params: []*Param{
				{Name: "Mix", Value: 1.0, Text: pct},
				{Name: "Amount", Value: 0.5, Text: pct},
			},
			Presets:       []string{"Default", "User 1"},
			CurrentPreset: "Default",
		}
	}
}

func (e *Engine) RemoveFX(ctx context.Context, track, fx int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return "", fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	inst := t.FX[fx-1]
	if !e.turboEnabled && !inst.UserAdded {
		return "", errors.New("cannot remove pre-existing plugin without Turbo mode")
	}
	e.pushUndo("remove_fx")
	name := inst.Name
	idx := fx - 1
	t.FX = append(t.FX[:idx], t.FX[idx+1:]...)
	e.reindexFX(t)
	return name, nil
}

// --- core.ChainMover ---

func (e *Engine) MoveFX(ctx context.Context, track, fromFX, toFX int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fromFX < 1 || fromFX > len(t.FX) || toFX < 1 || toFX > len(t.FX) {
		return fmt.Errorf("fx indices out of range: %d -> %d", fromFX, toFX)
	}
	if fromFX == toFX {
		return nil
	}
	e.pushUndo("move_fx")
	item := t.FX[fromFX-1]
	t.FX = append(t.FX[:fromFX-1], t.FX[fromFX:]...)
	idx := toFX - 1
	t.FX = append(t.FX[:idx], append([]*FX{item}, t.FX[idx:]...)...)
	e.reindexFX(t)
	return nil
}

// --- core.Bypasser ---

func (e *Engine) SetBypass(ctx context.Context, track, fx int, bypassed bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	e.pushUndo("set_bypass")
	t.FX[fx-1].Bypassed = bypassed
	return nil
}

// --- core.Presetter ---

func (e *Engine) Presets(ctx context.Context, track, fx int) (current string, list []string, err error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", nil, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return "", nil, fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	f := t.FX[fx-1]
	return f.CurrentPreset, append([]string(nil), f.Presets...), nil
}

func (e *Engine) SetPreset(ctx context.Context, track, fx int, preset string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	f := t.FX[fx-1]
	found := false
	for _, p := range f.Presets {
		if strings.EqualFold(p, preset) {
			preset = p
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("preset %q not found on fx %d", preset, fx)
	}
	e.pushUndo("set_preset")
	f.CurrentPreset = preset
	return nil
}

// --- core.PresetSaver ---

func (e *Engine) SavePresetAs(ctx context.Context, track, fx int, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("preset name cannot be empty")
	}
	e.pushUndo("save_preset_as")
	f := t.FX[fx-1]
	exists := false
	for _, p := range f.Presets {
		if p == name {
			exists = true
			break
		}
	}
	if !exists {
		f.Presets = append(f.Presets, name)
	}
	f.CurrentPreset = name
	return nil
}

// --- core.TurboChain ---

func (e *Engine) EnableAuto() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turboEnabled = true
	return nil
}

func (e *Engine) ForceRemoveFX(ctx context.Context, track, fx int) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return "", fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	e.pushUndo("force_remove_fx")
	name := t.FX[fx-1].Name
	idx := fx - 1
	t.FX = append(t.FX[:idx], t.FX[idx+1:]...)
	e.reindexFX(t)
	return name, nil
}

// --- core.ValueFinder ---

func (e *Engine) FindValue(ctx context.Context, track int, id core.ParamID, target string) (float64, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return 0, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	_, _, p, _ := e.findParam(t, id)
	if p == nil {
		return 0, fmt.Errorf("parameter %s not found on track %d", id, track)
	}

	targetTrimmed := strings.TrimSpace(strings.ToLower(target))
	if len(p.EnumValues) > 0 {
		for i, v := range p.EnumValues {
			if strings.EqualFold(strings.TrimSpace(v), targetTrimmed) {
				if len(p.EnumValues) == 1 {
					return 0, nil
				}
				return float64(i) / float64(len(p.EnumValues)-1), nil
			}
		}
	}

	// Continuous value search over steps
	bestVal := 0.0
	bestDist := math.MaxFloat64
	foundExact := false

	for step := 0; step <= 200; step++ {
		candidate := float64(step) / 200.0
		valStr := strings.TrimSpace(strings.ToLower(p.FormattedTextCandidate(candidate)))
		if valStr == targetTrimmed {
			return candidate, nil
		}
		// Match prefixes or contains
		if strings.Contains(valStr, targetTrimmed) || strings.Contains(targetTrimmed, valStr) {
			foundExact = true
			bestVal = candidate
			break
		}
	}
	if foundExact {
		return bestVal, nil
	}

	// Try numeric parsing
	numTarget, err := strconv.ParseFloat(strings.Trim(targetTrimmed, " %dbhz"), 64)
	if err == nil {
		for step := 0; step <= 200; step++ {
			candidate := float64(step) / 200.0
			valStr := strings.Trim(strings.ToLower(p.FormattedTextCandidate(candidate)), " %dbhz")
			valNum, err2 := strconv.ParseFloat(valStr, 64)
			if err2 == nil {
				dist := math.Abs(valNum - numTarget)
				if dist < bestDist {
					bestDist = dist
					bestVal = candidate
				}
			}
		}
		if bestDist < 1.0 {
			return bestVal, nil
		}
	}

	return 0, fmt.Errorf("target readout %q could not be matched for %s", target, id)
}

func (p *Param) FormattedTextCandidate(v float64) string {
	if p.Text != nil {
		return p.Text(v)
	}
	if len(p.EnumValues) > 0 {
		idx := int(math.Round(v * float64(len(p.EnumValues)-1)))
		if idx >= 0 && idx < len(p.EnumValues) {
			return p.EnumValues[idx]
		}
	}
	if p.Unit != "" {
		return fmt.Sprintf("%.1f %s", v, p.Unit)
	}
	return fmt.Sprintf("%.2f", v)
}

// --- core.ModelSwapper ---

func (e *Engine) PluginModels(ctx context.Context, track, fx int, slotQuery string) ([]core.PluginSlot, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return nil, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return nil, fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	f := t.FX[fx-1]
	if f.Models == nil {
		return nil, nil
	}

	var slots []core.PluginSlot
	slotQ := strings.ToLower(slotQuery)
	for slot, avail := range f.Models {
		if slotQ == "" || strings.Contains(strings.ToLower(slot), slotQ) {
			slots = append(slots, core.PluginSlot{
				Slot:      slot,
				Current:   f.CurrentModel[slot],
				Available: append([]string(nil), avail...),
			})
		}
	}
	return slots, nil
}

func (e *Engine) SetPluginModel(ctx context.Context, track, fx int, slot, model string) ([]core.Param, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return nil, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return nil, fmt.Errorf("fx %d out of range on track %d", fx, track)
	}
	f := t.FX[fx-1]
	avail, ok := f.Models[slot]
	if !ok {
		return nil, fmt.Errorf("slot %q not found on plugin", slot)
	}
	found := false
	for _, m := range avail {
		if strings.EqualFold(m, model) {
			model = m
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("model %q not available for slot %q", model, slot)
	}

	e.pushUndo("set_plugin_model")
	f.CurrentModel[slot] = model

	// Update params list to return
	params := make([]core.Param, len(f.Params))
	for j, p := range f.Params {
		params[j] = core.Param{
			ID:    core.ParamID(p.ID),
			Name:  p.Name,
			Kind:  p.Kind,
			Text:  p.FormattedText(),
			Value: p.Value,
			Steps: p.Steps,
		}
	}
	return params, nil
}

// --- core.AudioEngineChecker ---

func (e *Engine) AudioEngineRunning(ctx context.Context) (bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.AudioEngine.Running, nil
}

// --- core.Renderer and core.RangeRenderer ---

func (e *Engine) Render(ctx context.Context, track int, seconds float64) (string, error) {
	return e.RenderRange(ctx, track, 0, seconds)
}

func (e *Engine) RenderRange(ctx context.Context, track int, start, seconds float64) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	if seconds <= 0 {
		seconds = 2.0
	}

	// Isolate track with solo/mute preservation and guaranteed restoration
	origStates := make(map[int][2]bool, len(e.SessionTracks))
	for _, tr := range e.SessionTracks {
		origStates[tr.ID] = [2]bool{tr.Soloed, tr.Muted}
	}
	defer func() {
		for _, tr := range e.SessionTracks {
			if st, ok := origStates[tr.ID]; ok {
				tr.Soloed = st[0]
				tr.Muted = st[1]
			}
		}
	}()

	for _, tr := range e.SessionTracks {
		tr.Soloed = (tr.ID == track)
		tr.Muted = (tr.ID != track)
	}

	wavPath := filepath.Join(e.renderDir, fmt.Sprintf("render-track-%d-%d.wav", track, time.Now().UnixNano()))
	if err := writeWAVFile(wavPath, seconds, 440.0); err != nil {
		return "", fmt.Errorf("render wav: %w", err)
	}
	return wavPath, nil
}

// --- core.InputRecorder ---

func (e *Engine) StartInput(ctx context.Context, track int, seconds float64, token string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return fmt.Errorf("track %d out of range", track)
	}
	if token == "" {
		return errors.New("recording requires token")
	}
	e.pushUndo("start_input")
	t := e.SessionTracks[track-1]
	t.Armed = true
	e.recordingActive = true
	e.recordingTrack = track
	e.recordingToken = token
	e.recordingDuration = seconds
	return nil
}

func (e *Engine) FinishInput(ctx context.Context, track int, token string) (core.RecordedClip, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if track < 1 || track > len(e.SessionTracks) {
		return core.RecordedClip{}, fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	t.Armed = false
	e.recordingActive = false

	duration := e.recordingDuration
	if duration <= 0 {
		duration = 2.0
	}
	clipPath := filepath.Join(e.renderDir, fmt.Sprintf("input-track-%d-%s.wav", track, token))
	if err := writeWAVFile(clipPath, duration, 330.0); err != nil {
		return core.RecordedClip{}, fmt.Errorf("recording wav: %w", err)
	}

	pos := e.Transport.PositionSec
	clip := core.RecordedClip{
		Track:       track,
		Path:        clipPath,
		PositionSec: pos,
		DurationSec: duration,
	}
	t.MediaClips = append(t.MediaClips, MediaClip{
		Path:        clipPath,
		PositionSec: pos,
		DurationSec: duration,
	})
	t.HasClips = true
	return clip, nil
}

// --- core.UndoActionGrouper ---

func (e *Engine) SupportsUndoActionGrouping() bool {
	return true
}

func (e *Engine) BeginUndoAction(ctx context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.currentAction != nil {
		return errors.New("undo action already active")
	}
	snap := e.captureSnapshot()
	e.currentAction = &actionGroup{name: name, snapshot: snap}
	return nil
}

func (e *Engine) EndUndoAction(ctx context.Context, name string, commit bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.currentAction == nil || e.currentAction.name != name {
		return fmt.Errorf("no matching active undo action: %s", name)
	}
	if commit {
		e.undoStack = append(e.undoStack, sessionSnapshot{name: name, data: e.currentAction.snapshot})
		e.redoStack = nil
	} else {
		_ = e.restoreSnapshot(e.currentAction.snapshot)
	}
	e.currentAction = nil
	return nil
}

func (e *Engine) UndoActionIsCurrent(ctx context.Context, name string) (bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.currentAction != nil && e.currentAction.name == name {
		return true, nil
	}
	if len(e.undoStack) > 0 && e.undoStack[len(e.undoStack)-1].name == name {
		return true, nil
	}
	return false, nil
}

// --- core.RequestSnapshotter ---

func (e *Engine) SupportsRequestSnapshot() bool {
	return true
}

func (e *Engine) CaptureRequestSnapshot(ctx context.Context) ([]byte, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.captureSnapshot(), nil
}

func (e *Engine) RestoreRequestSnapshot(ctx context.Context, state []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.restoreSnapshot(state)
}

// --- core.ParamReader ---

func (e *Engine) ParamText(ctx context.Context, track int, id core.ParamID) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	_, _, p, _ := e.findParam(t, id)
	if p == nil {
		return "", fmt.Errorf("param %s not found", id)
	}
	return p.FormattedText(), nil
}

// --- core.Capturer ---

func (e *Engine) CaptureFX(ctx context.Context, track, fx int) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if track < 1 || track > len(e.SessionTracks) {
		return "", fmt.Errorf("track %d out of range", track)
	}
	t := e.SessionTracks[track-1]
	if fx < 1 || fx > len(t.FX) {
		return "", fmt.Errorf("fx %d out of range", fx)
	}
	pngPath := filepath.Join(e.renderDir, fmt.Sprintf("fx-window-%d-%d.png", track, fx))
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 100, B: 150, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	if err := os.WriteFile(pngPath, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return pngPath, nil
}

// --- Audio WAV writer helper ---

func writeWAVFile(path string, seconds float64, freq float64) error {
	sampleRate := 44100
	numSamples := int(seconds * float64(sampleRate))
	if numSamples < 441 {
		numSamples = 441
	}
	dataSize := numSamples * 2
	fileSize := 36 + dataSize

	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(fileSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)                   // Subchunk1Size
	binary.LittleEndian.PutUint16(buf[20:22], 1)                    // AudioFormat = PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)                    // NumChannels = 1
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))   // SampleRate
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2)) // ByteRate
	binary.LittleEndian.PutUint16(buf[32:34], 2)                    // BlockAlign
	binary.LittleEndian.PutUint16(buf[34:36], 16)                   // BitsPerSample
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		val := int16(math.Sin(2*math.Pi*freq*t) * 16000)
		binary.LittleEndian.PutUint16(buf[44+i*2:46+i*2], uint16(val))
	}
	return os.WriteFile(path, buf, 0o644)
}
