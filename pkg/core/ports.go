package core

import "context"

// DAW is a digital audio workstation the agent can read and change.
// Every setter returns what the DAW confirmed, never what was sent.
type DAW interface {
	Tracks(ctx context.Context) ([]Track, error)
	FXChain(ctx context.Context, track int) ([]FX, error)
	SetParam(ctx context.Context, track int, id ParamID, v float64) (Applied, error)
	SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error)
	Undo(ctx context.Context) error
	Alive(ctx context.Context) error
}

// Renderer renders a track to a WAV file and returns its path.
type Renderer interface {
	Render(ctx context.Context, track int, seconds float64) (string, error)
}

// Capturer screenshots a plugin window and returns the PNG path.
type Capturer interface {
	CaptureFX(ctx context.Context, track, fx int) (string, error)
}

// Measurer turns a WAV into numbers.
type Measurer interface {
	Measure(wavPath string) (Measure, error)
}

// LLM is one chat endpoint for every Muse model.
type LLM interface {
	Chat(ctx context.Context, model string, msgs []Msg, tools []Tool) (Reply, error)
}

// Transcriber turns recorded speech into text.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}

// Store keeps learned profiles and session logs.
type Store interface {
	SaveProfile(p Profile) error
	Profile(plugin string) (*Profile, error)
	Append(session string, e Event) error
}

// Sink receives events as they happen (the UI stream, or a test).
type Sink interface {
	Emit(e Event)
}

// Visualizer draws labelled spectrograms of WAVs, stacked in order, and
// returns the PNG path. Muse reads sound through it.
type Visualizer interface {
	Spectrogram(wavs, labels []string) (string, error)
}

// Bypasser switches a plugin off and on. DAW adapters that can implement it;
// the agent uses it to find which plugin silences a track.
type Bypasser interface {
	SetBypass(ctx context.Context, track, fx int, bypassed bool) error
}

// AudioEngineChecker reports whether the DAW's audio engine is running and
// connected to an output device. Optional: adapters that can query it implement
// it so silence diagnosis can distinguish between an off engine and an empty track.
type AudioEngineChecker interface {
	AudioEngineRunning(ctx context.Context) (bool, error)
}

// Snapshots keeps named tones. Optional.
type Snapshots interface {
	SaveSnapshot(s Snapshot) error
	Snapshot(name string) (*Snapshot, error)
	Snapshots() ([]Snapshot, error)
}

// RequestSnapshotter captures and restores opaque, exact session state.
// Implementations must reject states they cannot restore completely.
type RequestSnapshotter interface {
	SupportsRequestSnapshot() bool
	CaptureRequestSnapshot(ctx context.Context) ([]byte, error)
	RestoreRequestSnapshot(ctx context.Context, state []byte) error
}

// UndoActionGrouper brackets one multi-write agent action into a single DAW
// undo entry. It is optional; SupportsUndoActionGrouping is false when a
// discovered command map does not provide both boundary operations.
type UndoActionGrouper interface {
	SupportsUndoActionGrouping() bool
	BeginUndoAction(ctx context.Context, name string) error
	EndUndoAction(ctx context.Context, name string, commit bool) error
	UndoActionIsCurrent(ctx context.Context, name string) (bool, error)
}

// Segmenter finds objects in a photo from a text prompt ("knob"). Optional.
type Segmenter interface {
	Segment(ctx context.Context, image, prompt string) ([]Box, error)
}

// Box is an object's bounds in the image's own pixels.
type Box struct{ X1, Y1, X2, Y2 int }

// MixAnalyzer finds where one track and the rest of the mix fight.
type MixAnalyzer interface {
	Clashes(trackWAV, restWAV string) ([]Clash, error)
}

// ChainEditor adds and removes plugins. The catalog is what the DAW itself
// reports as installed. Optional: without it the agent only adjusts the
// plugins already there.
type ChainEditor interface {
	Catalog(ctx context.Context) ([]CatalogEntry, error)
	AddFX(ctx context.Context, track int, name string, pos int) (int, error)
	RemoveFX(ctx context.Context, track, fx int) (string, error)
}

// CatalogEntry is a plugin the agent may add; Kind is the DAW's own
// category for it, when the DAW has one.
type CatalogEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// TasteStore keeps the user's leanings across sessions. Optional.
type TasteStore interface {
	Taste() (Taste, error)
	SaveTaste(t Taste) error
}

// ParamReader reads one parameter's readout without reading the whole
// chain. Optional: DAWs that report everything at once do not need it.
type ParamReader interface {
	ParamText(ctx context.Context, track int, id ParamID) (string, error)
}

// ChainMover reorders a plugin in a track's FX chain. Optional.
type ChainMover interface {
	MoveFX(ctx context.Context, track, fromFX, toFX int) error
}

// Presetter lists and selects presets on a plugin. Optional.
type Presetter interface {
	Presets(ctx context.Context, track, fx int) (current string, list []string, err error)
	SetPreset(ctx context.Context, track, fx int, preset string) error
}

// PresetSaver saves a device's current state under a named preset. Optional.
type PresetSaver interface {
	SavePresetAs(ctx context.Context, track, fx int, name string) error
}

// TurboChain enables full autopilot for chain edits, permitting removal
// or replacement of any plugin and auto-enabling consent. Optional.
type TurboChain interface {
	EnableAuto() error
	ForceRemoveFX(ctx context.Context, track, fx int) (string, error)
}

// ValueFinder finds the normalized parameter value corresponding to a target
// readout directly in the DAW without stepping through individual SetParam calls. Optional.
type ValueFinder interface {
	FindValue(ctx context.Context, track int, id ParamID, target string) (float64, error)
}

// PluginSlot describes one modular slot inside a multi-FX/amp plugin (e.g.
// "AmpA", "CabA", "StompA1.Slot0") and the models dynamically discovered for it.
type PluginSlot struct {
	Slot      string   `json:"slot"`
	Current   string   `json:"current"`
	Available []string `json:"available"`
}

// ModelSwapper discovers and swaps internal sub-models (amps, cabs, stomps,
// racks, routing chains, IRs) inside a plugin state chunk dynamically. Optional.
type ModelSwapper interface {
	PluginModels(ctx context.Context, track, fx int, slotQuery string) ([]PluginSlot, error)
	SetPluginModel(ctx context.Context, track, fx int, slot, model string) ([]Param, error)
}

// SessionController manages DAW tracks, media items, sends/routing, and
// project transport/tempo/loop dynamically. Optional.
type SessionController interface {
	CreateTrack(ctx context.Context, name string, index int) (int, error)
	DuplicateTrack(ctx context.Context, track int, name string) (int, error)
	DeleteTrack(ctx context.Context, track int) error
	RenameTrack(ctx context.Context, track int, name string) error
	InsertMedia(ctx context.Context, track int, path string, positionSec float64) error
	CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error
	SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error
}

// SessionTrackControls provides optional recording input controls.
type SessionTrackControls interface {
	SetTrackControl(ctx context.Context, track int, control string, value string) (string, error)
}

// SessionRouting removes a send by its 1-based index on a source track.
type SessionRouting interface {
	RemoveSend(ctx context.Context, fromTrack, send int) error
}

// SessionMarkers reads and edits project markers and regions.
type SessionMarkers interface {
	Markers(ctx context.Context) ([]Marker, error)
	CreateMarker(ctx context.Context, positionSec, endSec float64, name string, region bool) (int, error)
	GoToMarker(ctx context.Context, index int) error
}

// SessionProject saves the current project.
type SessionProject interface {
	SaveProject(ctx context.Context) error
}

// SessionMeter controls the project's time signature.
type SessionMeter interface {
	SetTimeSignature(ctx context.Context, numerator, denominator int) error
}

// Marker is a project marker or region returned by the DAW.
type Marker struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	PositionSec float64 `json:"position_sec"`
	EndSec      float64 `json:"end_sec,omitempty"`
	Region      bool    `json:"region,omitempty"`
}
