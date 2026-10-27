// Package core holds the domain types and the ports every other package
// talks through. It imports nothing from this repository.
package core

import "time"

// Track is a DAW track. Index is the DAW's own 1-based number.
type Track struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Audio bool   `json:"audio,omitempty"` // the DAW says this track holds audio (not MIDI)
}

// Param is one plugin parameter as the DAW reports it. Value is normalised
// 0..1; Text is the DAW's own readout ("7.8", "On", "-3.0 dB").
type Param struct {
	ID      ParamID `json:"id"`
	Name    string  `json:"name"`
	Kind    string  `json:"kind"` // "" continuous, "switch", "discrete"
	Text    string  `json:"text"`
	Value   float64 `json:"value"`
	Unnamed bool    `json:"unnamed,omitempty"` // "Param N" with no profile label
	Steps   int     `json:"steps,omitempty"`   // stepped: the value moves in this many equal steps across 0..1; 0 = continuous
}

// FX is one plugin on a track, in chain order.
type FX struct {
	Index   int      `json:"fx"`
	Name    string   `json:"name"`
	Params  []Param  `json:"params"`
	Profile *Profile `json:"-"`
}

// Change is one value the model wants set, with its reason.
type Change struct {
	Param ParamID `json:"param_id"`
	Value float64 `json:"value"`
	Why   string  `json:"why"`
	// Target, when set, is a readout to land on ("-3 dB", "400 Hz", "7");
	// the agent finds Value by searching the DAW's own readout.
	Target string `json:"target,omitempty"`
}

// Plan is a whole tone proposed at once. It is validated and stored before
// anything touches the DAW; apply runs the stored plan, not a new model reply.
type Plan struct {
	ID      string   `json:"plan_id"`
	Track   int      `json:"track"`
	Goal    string   `json:"goal"`
	Changes []Change `json:"changes"`
}

// Applied is what happened to one change. Confirmed is the value the DAW
// reported back, not the one sent.
type Applied struct {
	Param     ParamID `json:"param_id"`
	Name      string  `json:"name"`
	Sent      float64 `json:"sent"`
	Confirmed float64 `json:"confirmed"`
	Text      string  `json:"text"`
	Status    string  `json:"status"` // StatusConfirmed, StatusUnconfirmed, StatusFailed
}

const (
	StatusConfirmed   = "confirmed"
	StatusUnconfirmed = "unconfirmed"
	StatusFailed      = "failed"
)

// Bands are energy shares per band in dB, loudness-matched, so two renders
// compare by shape only.
type Bands struct {
	Low      float64 `json:"low"`      // 60–150 Hz
	Mud      float64 `json:"mud"`      // 150–400 Hz
	Mid      float64 `json:"mid"`      // 400–1000 Hz
	Upper    float64 `json:"upper"`    // 1–3 kHz
	Presence float64 `json:"presence"` // 3–6 kHz
	Air      float64 `json:"air"`      // 6–10 kHz
}

// Measure is the numeric view of one render.
type Measure struct {
	Bands    Bands   `json:"bands_db"`
	Centroid float64 `json:"centroid_hz"`
	Crest    float64 `json:"crest_db"`
	Level    float64 `json:"level_dbfs"` // RMS; bands are relative to it
	WAV      string  `json:"render"`
}

// Control is one control Muse saw in a plugin window. X and Y are 0..1.
type Control struct {
	Label   string  `json:"label"`
	Kind    string  `json:"kind"` // knob, slider, switch, menu, button
	Section string  `json:"section"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

// Profile is what the agent learned about a plugin: parameter index → label.
type Profile struct {
	Plugin  string               `json:"plugin"`
	Map     map[int]ProfileEntry `json:"map"`
	Learned time.Time            `json:"learned"`
	// ReadOnly: the plugin kept none of a write to its internal controls, so
	// the next run knows it from the first read.
	ReadOnly bool `json:"read_only,omitempty"`
}

type ProfileEntry struct {
	Label          string        `json:"label"`
	Kind           string        `json:"kind"`
	Section        string        `json:"section"`
	Unit           string        `json:"unit,omitempty"`
	Confidence     float64       `json:"confidence"`
	Choices        []ParamChoice `json:"choices,omitempty"`
	ChoicesPartial bool          `json:"choices_partial,omitempty"`
}

// ParamChoice is a readout and normalized position learned from the DAW.
type ParamChoice struct {
	Text  string  `json:"text"`
	Value float64 `json:"value"`
}

// Event is everything the UI log shows and the session file records.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // tool_call, change, measure, text, error
	Data any       `json:"data"`
}

// Snapshot is a whole track's tone by name: every named parameter of every
// plugin on it. Recalling one is an ordinary plan, so it is confirmed and
// undoable like any other.
type Snapshot struct {
	Name         string              `json:"name"`
	Track        int                 `json:"track"`
	Values       map[ParamID]float64 `json:"values"`
	Names        map[ParamID]string  `json:"names"`
	Saved        time.Time           `json:"saved"`
	Tracks       []SnapshotTrack     `json:"tracks,omitempty"`
	SessionState []byte              `json:"session_state,omitempty"`
}

// SnapshotTrack contains one track's parameter values in a whole-request snapshot.
type SnapshotTrack struct {
	Track  int                 `json:"track"`
	Name   string              `json:"name"`
	Values map[ParamID]float64 `json:"values"`
	Names  map[ParamID]string  `json:"names"`
}

// Clash is a frequency range where the track and the rest of the mix are
// both strong, so they mask each other. Levels are absolute dBFS per band.
type Clash struct {
	Lo, Hi, Center float64 // Hz
	TrackDB        float64 `json:"track_db"`
	RestDB         float64 `json:"rest_db"`
	Score          float64 `json:"score"` // higher = worse; 0 = no clash
}

// Taste is what the user tends to change after the agent: one counter per
// control and direction, so a habit shows up and a one-off fades.
type Taste struct {
	Leanings []Leaning `json:"leanings"`
}

// Leaning: after the agent's plans, the user moved Control in Dir (+1 up,
// -1 down) Count times; Undone counts plans undone that had moved it the
// other way.
type Leaning struct {
	Control string    `json:"control"` // "ReaEQ / Gain-High Shelf 4"
	Dir     int       `json:"dir"`
	Count   int       `json:"count"`
	Last    time.Time `json:"last"`
}
