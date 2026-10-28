package reaper

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type fakeParam struct {
	Name  string
	Value float64
	Text  func(v float64) string
	Dead  bool
}

type fakeFX struct {
	Name   string
	Params []*fakeParam
}

type fakeTrack struct {
	Name string
	FX   []*fakeFX
}

type fakeServer struct {
	pc     net.PacketConn
	client net.Addr
	mu     sync.Mutex
	tracks []*fakeTrack
	device int
	count  int
}

func newFakeServer(client string, tracks []*fakeTrack) (*fakeServer, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ca, err := net.ResolveUDPAddr("udp", client)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	s := &fakeServer{pc: pc, client: ca, tracks: tracks, count: 16}
	go s.serve()
	return s, nil
}

func (s *fakeServer) Addr() string { return s.pc.LocalAddr().String() }
func (s *fakeServer) Close() error { return s.pc.Close() }

func (s *fakeServer) serve() {
	buf := make([]byte, 65536)
	for {
		n, _, err := s.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		ms, err := Decode(buf[:n])
		if err != nil {
			continue
		}
		for _, m := range ms {
			s.handle(m)
		}
	}
}

func (s *fakeServer) handle(m Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		if pp := s.param(t, f, p); pp != nil {
			if !pp.Dead {
				pp.Value = math.Max(0, math.Min(1, v))
			}
			s.send(m.Addr, float32(pp.Value))
			if pp.Text != nil {
				s.send(m.Addr+"/str", pp.Text(pp.Value))
			}
		}
	case len(parts) == 3 && parts[0] == "track" && (parts[2] == "volume" || parts[2] == "pan" || parts[2] == "mute" || parts[2] == "solo"):
		v, _ := m.Float(0)
		s.send(m.Addr, float32(v))
	}
}

func (s *fakeServer) selectTrack(t int) {
	if t < 1 || t > len(s.tracks) || t == s.device {
		return
	}
	if s.device != 0 {
		s.send("/track/name", "")
		for f, fx := range s.tracks[s.device-1].FX {
			s.send(fmt.Sprintf("/fx/%d/name", f+1), "")
			for p := range fx.Params {
				s.send(fmt.Sprintf("/fx/%d/fxparam/%d/name", f+1, p+1), "")
			}
		}
	}
	s.device = t
	tr := s.tracks[t-1]
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
				continue
			}
			s.send(base+"value", float32(pp.Value))
			if pp.Text != nil {
				s.send(base+"value/str", pp.Text(pp.Value))
			}
		}
	}
}

func (s *fakeServer) param(t, f, p int) *fakeParam {
	if t < 1 || t > len(s.tracks) || f < 1 || f > len(s.tracks[t-1].FX) {
		return nil
	}
	ps := s.tracks[t-1].FX[f-1].Params
	if p < 1 || p > len(ps) {
		return nil
	}
	return ps[p-1]
}

func (s *fakeServer) send(addr string, args ...any) {
	b, err := Encode(Msg{Addr: addr, Args: args})
	if err == nil {
		_, _ = s.pc.WriteTo(b, s.client)
	}
}

func defaultFakeTracks() []*fakeTrack {
	db := func(v float64) string { return strconv.FormatFloat((v-0.5)*48, 'f', 1, 64) }
	hz := func(v float64) string { return strconv.FormatFloat(20*math.Pow(1000, v), 'f', 1, 64) }
	pct := func(v float64) string { return strconv.Itoa(int(v * 10)) }

	makeAmp := func() *fakeFX {
		amp := &fakeFX{Name: "Amp Sim (Test)"}
		for i := 1; i <= 4; i++ {
			amp.Params = append(amp.Params, &fakeParam{Name: fmt.Sprintf("Param %d", i), Dead: true})
		}
		for _, n := range []string{"Gain", "Bass", "Middle", "Treble", "Presence", "Master"} {
			amp.Params = append(amp.Params, &fakeParam{Name: n, Value: 0.5, Text: pct})
		}
		amp.Params = append(amp.Params, &fakeParam{Name: "Bright", Value: 0, Text: func(v float64) string {
			if v >= 0.5 {
				return "On"
			}
			return "Off"
		}})
		return amp
	}

	makeEQ := func() *fakeFX {
		return &fakeFX{Name: "ReaEQ (Cockos)", Params: []*fakeParam{
			{Name: "Freq-Low Shelf", Value: 0.14, Text: hz},
			{Name: "Gain-Low Shelf", Value: 0.5, Text: db},
			{Name: "Freq-Band 2", Value: 0.29, Text: hz},
			{Name: "Gain-Band 2", Value: 0.5, Text: db},
			{Name: "Freq-High Shelf 4", Value: 0.74, Text: hz},
			{Name: "Gain-High Shelf 4", Value: 0.5, Text: db},
		}}
	}

	verb := &fakeFX{Name: "ReaVerb (Cockos)", Params: []*fakeParam{
		{Name: "Wet", Value: 0.3, Text: db},
		{Name: "Dry", Value: 0.7, Text: db},
	}}

	return []*fakeTrack{
		{Name: "Rhythm Guitar", FX: []*fakeFX{makeAmp(), makeEQ()}},
		{Name: "Lead Guitar", FX: []*fakeFX{makeAmp(), makeEQ()}},
		{Name: "Reverb Bus", FX: []*fakeFX{verb}},
	}
}

// NewFake creates an in-process simulated REAPER DAW session.
// It sets up a multi-track demo studio, dials it via UDP loopback, wires up
// a sandbox Bridge directory, and returns the active *REAPER.
// Calling Close() on the returned REAPER cleanly shuts down the connection and server.
func NewFake() (*REAPER, error) {
	l, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ours := l.LocalAddr().String()
	_ = l.Close()

	srv, err := newFakeServer(ours, defaultFakeTracks())
	if err != nil {
		return nil, err
	}

	conn, err := Dial(ours, srv.Addr())
	if err != nil {
		_ = srv.Close()
		return nil, err
	}

	rp := New(conn)
	tmpBridge := filepath.Join(os.TempDir(), "tonelab-fake-bridge")
	_ = os.MkdirAll(tmpBridge, 0o755)
	rp.Bridge = &Bridge{Dir: tmpBridge}

	rp.cleanup = func() error {
		_ = conn.Close()
		return srv.Close()
	}
	return rp, nil
}
