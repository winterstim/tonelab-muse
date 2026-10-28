// Package fake is a stand-in REAPER that speaks OSC over UDP the way the
// real one does with the Default pattern (recorded from REAPER 7.79):
// selecting the device track first clears the previous strings with "",
// then reports the track and every FX parameter; setting a parameter by
// its absolute address echoes only that address.
package fake

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Server is the fake REAPER. Sent records every address it received.
type Server struct {
	pc     net.PacketConn
	client net.Addr

	mu     sync.Mutex
	Tracks []*Track
	Sent   []string
	device int
	count  int

	Engine *Engine
}

// Listen starts the fake on a free local port; client is where feedback goes.
func Listen(client string, tracks []*Track) (*Server, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ca, err := net.ResolveUDPAddr("udp", client)
	if err != nil {
		pc.Close()
		return nil, err
	}
	if len(tracks) == 0 {
		tracks = Studio()
	}
	eng := New(tracks...)
	s := &Server{
		pc:     pc,
		client: ca,
		Tracks: eng.SessionTracks,
		count:  16,
		Engine: eng,
	}
	eng.server = s
	go s.serve()
	return s, nil
}

func (s *Server) Addr() string { return s.pc.LocalAddr().String() }
func (s *Server) Close() error { return s.pc.Close() }

// Value reads a parameter's current value (1-based indices).
func (s *Server) Value(track, fx, p int) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Tracks[track-1].FX[fx-1].Params[p-1].Value
}

// Received copies the addresses received so far.
func (s *Server) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Sent...)
}

func (s *Server) serve() {
	buf := make([]byte, 65536)
	for {
		n, _, err := s.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ms, err := reaper.Decode(buf[:n])
		if err != nil {
			continue
		}
		for _, m := range ms {
			s.handle(m)
		}
	}
}

func (s *Server) handle(m reaper.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Sent = append(s.Sent, m.Addr)
	parts := strings.Split(strings.Trim(m.Addr, "/"), "/")
	switch {
	case m.Addr == "/device/fxparam/count":
		if v, ok := m.Float(0); ok {
			s.count = int(v)
		}
	case m.Addr == "/device/track/select":
		v, _ := m.Float(0)
		s.selectTrack(int(v))
	case len(parts) == 7 && parts[0] == "track" && parts[2] == "fx" && parts[4] == "fxparam" && parts[6] == "value":
		t, _ := strconv.Atoi(parts[1])
		f, _ := strconv.Atoi(parts[3])
		p, _ := strconv.Atoi(parts[5])
		v, _ := m.Float(0)
		if s.Engine != nil {
			id := core.ParamID(fmt.Sprintf("%d/%d", f, p))
			applied, err := s.Engine.SetParam(context.Background(), t, id, v)
			if err == nil {
				s.Tracks = s.Engine.SessionTracks
				s.send(m.Addr, float32(applied.Confirmed))
				if applied.Text != "" {
					s.send(m.Addr+"/str", applied.Text)
				}
				return
			}
		}
		if pp := s.param(t, f, p); pp != nil {
			if !pp.Dead && !pp.ReadOnly {
				pp.Value = clamp(v)
			}
			s.send(m.Addr, float32(pp.Value))
			if pp.Text != nil {
				s.send(m.Addr+"/str", pp.Text(pp.Value))
			}
		}
	case len(parts) == 3 && parts[0] == "track" && (parts[2] == "volume" || parts[2] == "pan" || parts[2] == "mute" || parts[2] == "solo"):
		t, _ := strconv.Atoi(parts[1])
		prop := parts[2]
		v, _ := m.Float(0)
		if s.Engine != nil {
			confirmed, err := s.Engine.SetTrack(context.Background(), t, prop, v)
			if err == nil {
				s.Tracks = s.Engine.SessionTracks
				s.send(m.Addr, float32(confirmed))
				return
			}
		}
		s.send(m.Addr, float32(v))
	case m.Addr == "/play":
		if s.Engine != nil {
			_ = s.Engine.SetTransport(context.Background(), "play", 0, 0, 0, 0)
		}
	case m.Addr == "/stop":
		if s.Engine != nil {
			_ = s.Engine.SetTransport(context.Background(), "stop", 0, 0, 0, 0)
		}
	case m.Addr == "/undo" || m.Addr == "/action/40029":
		if s.Engine != nil {
			_ = s.Engine.Undo(context.Background())
			s.Tracks = s.Engine.SessionTracks
		}
	}
}

func (s *Server) selectTrack(t int) {
	if t < 1 || t > len(s.Tracks) || t == s.device {
		return // REAPER reports only changes
	}
	if s.device != 0 { // clear the previous track's strings first
		s.send("/track/name", "")
		for f, fx := range s.Tracks[s.device-1].FX {
			s.send(fmt.Sprintf("/fx/%d/name", f+1), "")
			for p := range fx.Params {
				s.send(fmt.Sprintf("/fx/%d/fxparam/%d/name", f+1, p+1), "")
			}
		}
	}
	s.device = t
	tr := s.Tracks[t-1]
	s.send("/track/name", tr.Name)
	s.send("/track/number/str", strconv.Itoa(t))
	for f, fx := range tr.FX {
		s.send(fmt.Sprintf("/fx/%d/name", f+1), fx.Name)
		for p, pp := range fx.Params {
			if p >= s.count {
				break
			}
			base := fmt.Sprintf("/fx/%d/fxparam/%d/", f+1, p+1)
			s.send(base+"name", pp.Name)
			if pp.Dead {
				continue // placeholder slots report a name and nothing else
			}
			s.send(base+"value", float32(pp.Value))
			if pp.Text != nil {
				s.send(base+"value/str", pp.Text(pp.Value))
			}
		}
	}
}

func (s *Server) param(t, f, p int) *Param {
	if t < 1 || t > len(s.Tracks) || f < 1 || f > len(s.Tracks[t-1].FX) {
		return nil
	}
	ps := s.Tracks[t-1].FX[f-1].Params
	if p < 1 || p > len(ps) {
		return nil
	}
	return ps[p-1]
}

func (s *Server) send(addr string, args ...any) {
	b, err := reaper.Encode(reaper.Msg{Addr: addr, Args: args})
	if err == nil {
		s.pc.WriteTo(b, s.client)
	}
}

func clamp(v float64) float64 { return max(0, min(1, v)) }

// Studio is the recorded test session: a guitar track with an amp whose
// first slots are unassigned placeholders, and ReaEQ.
func Studio() []*Track {
	db := func(v float64) string { return strconv.FormatFloat((v-0.5)*48, 'f', 1, 64) }
	hz := func(v float64) string { return strconv.FormatFloat(20*math.Pow(1000, v), 'f', 1, 64) }
	amp := &FX{
		ID:   1,
		Name: "Amp Sim (Test)",
		Kind: "amp",
		Presets: []string{
			"Default", "Clean Sparkle", "Warm Crunch", "Heavy Lead",
		},
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
	for i := 1; i <= 4; i++ {
		amp.Params = append(amp.Params, &Param{Name: fmt.Sprintf("Param %d", i), Dead: true})
	}
	pct := func(v float64) string { return strconv.Itoa(int(v * 10)) }
	for _, n := range []string{"Gain", "Bass", "Middle", "Treble", "Presence", "Master"} {
		amp.Params = append(amp.Params, &Param{Name: n, Value: 0.5, Text: pct})
	}
	amp.Params = append(amp.Params, &Param{Name: "Bright", Value: 0, Text: func(v float64) string {
		if v >= 0.5 {
			return "On"
		}
		return "Off"
	}, EnumValues: []string{"Off", "On"}, Kind: "switch"})

	eq := &FX{
		ID:   2,
		Name: "ReaEQ (Cockos)",
		Kind: "eq",
		Presets: []string{
			"Default", "Vocal High Pass", "Mid Scoop",
		},
		CurrentPreset: "Default",
		Params: []*Param{
			{Name: "Freq-Low Shelf", Value: 0.14, Text: hz, Unit: "Hz"},
			{Name: "Gain-Low Shelf", Value: 0.5, Text: db, Unit: "dB"},
			{Name: "Freq-Band 2", Value: 0.29, Text: hz, Unit: "Hz"},
			{Name: "Gain-Band 2", Value: 0.5, Text: db, Unit: "dB"},
			{Name: "Freq-High Shelf 4", Value: 0.74, Text: hz, Unit: "Hz"},
			{Name: "Gain-High Shelf 4", Value: 0.5, Text: db, Unit: "dB"},
		},
	}
	return []*Track{
		{
			ID:          1,
			Name:        "Guitar",
			Type:        "audio",
			HasClips:    true,
			Volume:      1.0,
			Pan:         0.0,
			Monitor:     "auto",
			InputSelect: "Input 1",
			FX:          []*FX{amp, eq},
		},
		{
			ID:          2,
			Name:        "Park",
			Type:        "audio",
			HasClips:    false,
			Volume:      1.0,
			Pan:         0.0,
			Monitor:     "off",
			InputSelect: "Input 2",
		},
	}
}

// DemoStudio provides a multi-track sandbox session for examples:
// Track 1: Rhythm Guitar (Amp Sim + ReaEQ)
// Track 2: Lead Guitar (Amp Sim + ReaEQ)
// Track 3: Reverb Bus (ReaVerb)
func DemoStudio() []*Track {
	db := func(v float64) string { return strconv.FormatFloat((v-0.5)*48, 'f', 1, 64) }
	hz := func(v float64) string { return strconv.FormatFloat(20*math.Pow(1000, v), 'f', 1, 64) }
	pct := func(v float64) string { return strconv.Itoa(int(v * 10)) }

	makeAmp := func(id int) *FX {
		amp := &FX{
			ID:   id,
			Name: "Amp Sim (Test)",
			Kind: "amp",
			Presets: []string{
				"Default", "Clean Sparkle", "Warm Crunch", "Heavy Lead",
			},
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
		for i := 1; i <= 4; i++ {
			amp.Params = append(amp.Params, &Param{Name: fmt.Sprintf("Param %d", i), Dead: true})
		}
		for _, n := range []string{"Gain", "Bass", "Middle", "Treble", "Presence", "Master"} {
			amp.Params = append(amp.Params, &Param{Name: n, Value: 0.5, Text: pct})
		}
		amp.Params = append(amp.Params, &Param{Name: "Bright", Value: 0, Text: func(v float64) string {
			if v >= 0.5 {
				return "On"
			}
			return "Off"
		}, EnumValues: []string{"Off", "On"}, Kind: "switch"})
		return amp
	}

	makeEQ := func(id int) *FX {
		return &FX{
			ID:   id,
			Name: "ReaEQ (Cockos)",
			Kind: "eq",
			Presets: []string{
				"Default", "Vocal High Pass", "Mid Scoop",
			},
			CurrentPreset: "Default",
			Params: []*Param{
				{Name: "Freq-Low Shelf", Value: 0.14, Text: hz, Unit: "Hz"},
				{Name: "Gain-Low Shelf", Value: 0.5, Text: db, Unit: "dB"},
				{Name: "Freq-Band 2", Value: 0.29, Text: hz, Unit: "Hz"},
				{Name: "Gain-Band 2", Value: 0.5, Text: db, Unit: "dB"},
				{Name: "Freq-High Shelf 4", Value: 0.74, Text: hz, Unit: "Hz"},
				{Name: "Gain-High Shelf 4", Value: 0.5, Text: db, Unit: "dB"},
			},
		}
	}

	verb := &FX{
		ID:   1,
		Name: "ReaVerb (Cockos)",
		Kind: "reverb",
		Presets: []string{
			"Default", "Small Room", "Large Hall", "Plate",
		},
		CurrentPreset: "Default",
		Params: []*Param{
			{Name: "Wet", Value: 0.3, Text: db, Unit: "dB"},
			{Name: "Dry", Value: 0.7, Text: db, Unit: "dB"},
		},
	}

	return []*Track{
		{
			ID:          1,
			Name:        "Rhythm Guitar",
			Type:        "audio",
			HasClips:    true,
			Volume:      1.0,
			Pan:         -0.2,
			Monitor:     "auto",
			InputSelect: "Input 1",
			FX:          []*FX{makeAmp(1), makeEQ(2)},
			Sends:       []Send{{ToTrack: 3, LevelDB: -6.0}},
		},
		{
			ID:          2,
			Name:        "Lead Guitar",
			Type:        "audio",
			HasClips:    true,
			Volume:      1.0,
			Pan:         0.2,
			Monitor:     "auto",
			InputSelect: "Input 2",
			FX:          []*FX{makeAmp(1), makeEQ(2)},
			Sends:       []Send{{ToTrack: 3, LevelDB: -8.0}},
		},
		{
			ID:          3,
			Name:        "Reverb Bus",
			Type:        "bus",
			HasClips:    false,
			Volume:      1.0,
			Pan:         0.0,
			Monitor:     "off",
			InputSelect: "None",
			FX:          []*FX{verb},
		},
	}
}
