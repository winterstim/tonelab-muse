package fake

import (
	"fmt"
	"math"
)

// Send represents an audio routing send from one track to another.
type Send struct {
	ToTrack int     `json:"to_track"`
	LevelDB float64 `json:"level_db"`
}

// MediaClip represents an audio clip placed on a track timeline.
type MediaClip struct {
	Path        string  `json:"path"`
	PositionSec float64 `json:"position_sec"`
	DurationSec float64 `json:"duration_sec"`
}

// Transport represents the DAW playhead, loop, tempo and time signature state.
type Transport struct {
	PlayState          string  `json:"play_state"` // "stopped", "playing", "paused"
	PositionSec        float64 `json:"position_sec"`
	LoopEnabled        bool    `json:"loop_enabled"`
	LoopStartSec       float64 `json:"loop_start_sec"`
	LoopEndSec         float64 `json:"loop_end_sec"`
	BPM                float64 `json:"bpm"`
	TimeSignatureNum   int     `json:"time_signature_num"`
	TimeSignatureDenom int     `json:"time_signature_denom"`
}

// Param is one plugin parameter. Dead parameters keep their value at 0.
type Param struct {
	ID         string                 `json:"id,omitempty"`
	Name       string                 `json:"name"`
	Value      float64                `json:"value"`
	Text       func(v float64) string `json:"-"`
	TextVal    string                 `json:"text,omitempty"`
	Unit       string                 `json:"unit,omitempty"`
	Kind       string                 `json:"kind,omitempty"` // "" continuous, "switch", "discrete"
	Steps      int                    `json:"steps,omitempty"`
	EnumValues []string               `json:"enum_values,omitempty"`
	Dead       bool                   `json:"dead,omitempty"`
	ReadOnly   bool                   `json:"read_only,omitempty"`
}

// FormattedText returns the human-readable readout text for the parameter value.
func (p *Param) FormattedText() string {
	if p.Text != nil {
		return p.Text(p.Value)
	}
	if len(p.EnumValues) > 0 {
		idx := int(math.Round(p.Value * float64(len(p.EnumValues)-1)))
		if idx >= 0 && idx < len(p.EnumValues) {
			return p.EnumValues[idx]
		}
	}
	if p.TextVal != "" {
		return p.TextVal
	}
	if p.Unit != "" {
		return fmt.Sprintf("%.1f %s", p.Value, p.Unit)
	}
	return fmt.Sprintf("%.2f", p.Value)
}

// Clone creates a deep copy of the parameter.
func (p *Param) Clone() *Param {
	if p == nil {
		return nil
	}
	cp := *p
	if len(p.EnumValues) > 0 {
		cp.EnumValues = append([]string(nil), p.EnumValues...)
	}
	return &cp
}

// FX is one plugin instance on a track.
type FX struct {
	ID            int                 `json:"id"`
	Name          string              `json:"name"`
	Kind          string              `json:"kind,omitempty"`
	Bypassed      bool                `json:"bypassed"`
	WetDry        float64             `json:"wet_dry"`
	WindowOpen    bool                `json:"window_open"`
	Params        []*Param            `json:"params"`
	Presets       []string            `json:"presets,omitempty"`
	CurrentPreset string              `json:"current_preset,omitempty"`
	Models        map[string][]string `json:"models,omitempty"`
	CurrentModel  map[string]string   `json:"current_model,omitempty"`
	ReadOnly      bool                `json:"read_only,omitempty"`
	UserAdded     bool                `json:"user_added,omitempty"`
}

// Clone creates a deep copy of the FX instance.
func (f *FX) Clone() *FX {
	if f == nil {
		return nil
	}
	cp := *f
	if len(f.Params) > 0 {
		cp.Params = make([]*Param, len(f.Params))
		for i, p := range f.Params {
			cp.Params[i] = p.Clone()
		}
	}
	if len(f.Presets) > 0 {
		cp.Presets = append([]string(nil), f.Presets...)
	}
	if len(f.Models) > 0 {
		cp.Models = make(map[string][]string, len(f.Models))
		for k, v := range f.Models {
			cp.Models[k] = append([]string(nil), v...)
		}
	}
	if len(f.CurrentModel) > 0 {
		cp.CurrentModel = make(map[string]string, len(f.CurrentModel))
		for k, v := range f.CurrentModel {
			cp.CurrentModel[k] = v
		}
	}
	return &cp
}

// Track represents a track in the DAW session.
type Track struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Type        string      `json:"type"` // "audio", "midi", "bus"
	HasClips    bool        `json:"has_clips"`
	Armed       bool        `json:"armed"`
	Muted       bool        `json:"muted"`
	Soloed      bool        `json:"soloed"`
	Volume      float64     `json:"volume"`
	Pan         float64     `json:"pan"`
	Monitor     string      `json:"monitor"` // "in", "auto", "off"
	InputSelect string      `json:"input_select"`
	FX          []*FX       `json:"fx"`
	Sends       []Send      `json:"sends,omitempty"`
	MediaClips  []MediaClip `json:"media_clips,omitempty"`
}

// FXChain returns the track's FX chain.
func (t *Track) FXChain() []*FX {
	return t.FX
}

// Clone creates a deep copy of the track.
func (t *Track) Clone() *Track {
	if t == nil {
		return nil
	}
	cp := *t
	if len(t.FX) > 0 {
		cp.FX = make([]*FX, len(t.FX))
		for i, f := range t.FX {
			cp.FX[i] = f.Clone()
		}
	}
	if len(t.Sends) > 0 {
		cp.Sends = append([]Send(nil), t.Sends...)
	}
	if len(t.MediaClips) > 0 {
		cp.MediaClips = append([]MediaClip(nil), t.MediaClips...)
	}
	return &cp
}
