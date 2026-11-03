package oscmap

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/internal/daw/reaper"
	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Browser preset discovery walks Live's browser tree and can take several
// seconds on a cold cache. Keep ordinary OSC requests snappy, but allow that
// one operation enough time to return its complete catalog.
const replyWait = 3 * time.Second

// DAW implements core.DAW from a Map. One request at a time: request/reply
// APIs answer on one port, and a reply must not be taken for another's.
type DAW struct {
	M  Map
	pc net.PacketConn
	to net.Addr

	mu        sync.Mutex
	groupMu   sync.Mutex
	groupName string
	ranges    map[string][2]float64 // "track/fx/param" → min, max
}

// Open binds the reply port on localhost and targets the DAW's port.
func Open(m Map) (*DAW, error) {
	if err := m.Check(); err != nil {
		return nil, err
	}
	pc, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", m.ReplyPort))
	if err != nil {
		return nil, err
	}
	to, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", m.Port))
	if err != nil {
		pc.Close()
		return nil, err
	}
	return &DAW{M: m, pc: pc, to: to, ranges: map[string][2]float64{}}, nil
}

func (d *DAW) Close() error { return d.pc.Close() }

// Do sends one command; the chain may have changed, so cached parameter
// ranges are dropped.
func (d *DAW) Do(ctx context.Context, c Command, v vars) ([]any, error) {
	out, err := d.ask(ctx, c, v)
	d.mu.Lock()
	d.ranges = map[string][2]float64{}
	d.mu.Unlock()
	return out, err
}

// ask sends c and returns the reply's args after Skip; no reply wanted
// returns nil, nil.
func (d *DAW) ask(ctx context.Context, c Command, v vars) ([]any, error) {
	if len(c.Send) == 0 {
		return nil, errors.New("not supported by this DAW")
	}
	addr, args, err := c.build(v)
	if err != nil {
		return nil, err
	}
	b, err := reaper.Encode(reaper.Msg{Addr: addr, Args: args})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drain()
	if _, err := d.pc.WriteTo(b, d.to); err != nil {
		return nil, err
	}
	if c.Reply == "" {
		return nil, nil
	}
	wait := replyWait
	if strings.Contains(addr, "/preset/list") {
		wait = 10 * time.Second
	}
	deadline := time.Now().Add(wait)
	buf := make([]byte, 65536)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		d.pc.SetReadDeadline(deadline)
		n, _, err := d.pc.ReadFrom(buf)
		if err != nil {
			break
		}
		ms, err := reaper.Decode(buf[:n])
		if err != nil {
			continue
		}
		for _, m := range ms {
			if m.Addr == c.Reply {
				if len(m.Args) < c.Skip {
					return nil, fmt.Errorf("%s: reply has %d args, expected more than %d", addr, len(m.Args), c.Skip)
				}
				return m.Args[c.Skip:], nil
			}
		}
	}
	return nil, fmt.Errorf("%s: no reply on %s", addr, c.Reply)
}

// drain drops stale replies (a late answer to an earlier request).
func (d *DAW) drain() {
	buf := make([]byte, 65536)
	for {
		d.pc.SetReadDeadline(time.Now().Add(time.Millisecond))
		if _, _, err := d.pc.ReadFrom(buf); err != nil {
			return
		}
	}
}

func num(a any) (float64, bool) {
	switch x := a.(type) {
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// asBool decodes any OSC answer value into a boolean.
// Accepts booleans (true/false), integers (0/1), and floats (0.0/1.0).
func asBool(a any) (bool, bool) {
	switch v := a.(type) {
	case bool:
		return v, true
	case int32:
		return v != 0, true
	case int64:
		return v != 0, true
	case int:
		return v != 0, true
	case float32:
		return v != 0, true
	case float64:
		return v != 0, true
	}
	return false, false
}

func str(a any) string {
	if s, ok := a.(string); ok {
		return s
	}
	return fmt.Sprint(a)
}

func (d *DAW) Alive(ctx context.Context) error {
	if _, err := d.ask(ctx, d.M.Ping, vars{}); err != nil {
		return core.ErrDAWGone
	}
	return nil
}

// ContractExecutor exposes the map's real DAW operations through the canonical
// contract. Operation-specific state reads and restorations are performed here.
func (d *DAW) Execute(ctx context.Context, op string, raw any) (core.ContractResult, error) {
	return (&contractExecutor{d: d}).Execute(ctx, op, raw)
}

func (d *DAW) Tracks(ctx context.Context) ([]core.Track, error) {
	r, err := d.ask(ctx, d.M.TrackCount, vars{})
	if err != nil || len(r) == 0 {
		return nil, fmt.Errorf("track count: %v", err)
	}
	n, _ := num(r[0])
	var out []core.Track
	for i := 0; i < int(n); i++ {
		name := fmt.Sprintf("Track %d", i+1)
		if r, err := d.ask(ctx, d.M.TrackName, vars{track: i + d.M.Base}); err == nil && len(r) > 0 {
			name = str(r[0])
		}
		t := core.Track{Index: i + 1, Name: name}
		if len(d.M.TrackAudio.Send) > 0 {
			if r, err := d.ask(ctx, d.M.TrackAudio, vars{track: i + d.M.Base}); err == nil && len(r) > 0 {
				if b, ok := asBool(r[0]); ok {
					t.Audio = b
				} else if n, ok := num(r[0]); ok {
					t.Audio = n != 0
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// stepsOf is how many equal steps a parameter moves in: its native range
// runs over whole numbers and it sits on one of them (an enum such as an
// amp type, or a stepped control). 0 for a continuous one.
func stepsOf(lo, hi, v float64) int {
	span := hi - lo
	if span < 1 || span > 127 || lo != math.Trunc(lo) || hi != math.Trunc(hi) || v != math.Trunc(v) {
		return 0
	}
	return int(span)
}

func (d *DAW) list(ctx context.Context, c Command, v vars, n int, def float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = def
	}
	if len(c.Send) == 0 {
		return out
	}
	r, err := d.ask(ctx, c, v)
	if err != nil {
		return out
	}
	for i := 0; i < n && i < len(r); i++ {
		if f, ok := num(r[i]); ok {
			out[i] = f
		}
	}
	return out
}

func key(track, fx, p int) string { return fmt.Sprintf("%d/%d/%d", track, fx, p) }

func (d *DAW) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	t := track - 1 + d.M.Base
	r, err := d.ask(ctx, d.M.DeviceNames, vars{track: t})
	if err != nil {
		return nil, err
	}
	var out []core.FX
	for i, dn := range r {
		f := i + d.M.Base
		v := vars{track: t, fx: f}
		names, err := d.ask(ctx, d.M.ParamNames, v)
		if err != nil {
			return nil, err
		}
		n := len(names)
		vals := d.list(ctx, d.M.ParamValues, v, n, 0)
		mins := d.list(ctx, d.M.ParamMins, v, n, 0)
		maxs := d.list(ctx, d.M.ParamMaxs, v, n, 1)
		texts := d.texts(ctx, t, f, n)
		fx := core.FX{Index: i + 1, Name: str(dn)}
		for p := 0; p < n; p++ {
			lo, hi := mins[p], maxs[p]
			norm := 0.0
			if hi > lo {
				norm = (vals[p] - lo) / (hi - lo)
			}
			text := texts[p]
			d.mu.Lock()
			d.ranges[key(track, i+1, p+1)] = [2]float64{lo, hi}
			d.mu.Unlock()
			fx.Params = append(fx.Params, core.Param{
				ID: core.MakeParamID(i+1, p+1), Name: str(names[p]), Kind: reaper.Kind(text),
				Text: text, Value: math.Round(norm*10000) / 10000,
				Steps: stepsOf(lo, hi, vals[p]),
			})
		}
		out = append(out, fx)
	}
	return out, nil
}

func (d *DAW) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	fx, p, err := id.Parse()
	if err != nil {
		return core.Applied{}, err
	}
	d.mu.Lock()
	rg, ok := d.ranges[key(track, fx, p)]
	d.mu.Unlock()
	if !ok {
		if _, err := d.FXChain(ctx, track); err != nil {
			return core.Applied{}, err
		}
		d.mu.Lock()
		rg, ok = d.ranges[key(track, fx, p)]
		d.mu.Unlock()
		if !ok {
			return core.Applied{}, fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
		}
	}
	a := core.Applied{Param: id, Sent: v, Status: core.StatusUnconfirmed}
	vv := vars{track: track - 1 + d.M.Base, fx: fx - 1 + d.M.Base, param: p - 1 + d.M.Base, value: rg[0] + v*(rg[1]-rg[0])}
	if _, err := d.ask(ctx, d.M.SetParam, vv); err != nil {
		return a, err
	}
	r, err := d.ask(ctx, d.M.GetParam, vv)
	if err != nil || len(r) == 0 {
		return a, fmt.Errorf("%w: %s", core.ErrUnconfirmed, id)
	}
	got, _ := num(r[0])
	if rg[1] > rg[0] {
		a.Confirmed = (got - rg[0]) / (rg[1] - rg[0])
	}
	a.Status = core.StatusConfirmed
	return a, nil
}

func (d *DAW) SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error) {
	var c Command
	val := v
	switch prop {
	case "volume":
		c = d.M.SetVolume
	case "pan":
		c = d.M.SetPan
	case "mute":
		c = d.M.SetMute
		if v != 0 {
			val = 1
		} else {
			val = 0
		}
	case "solo":
		c = d.M.SetSolo
		if v != 0 {
			val = 1
		} else {
			val = 0
		}
	default:
		return 0, fmt.Errorf("%s is not supported on %s yet", prop, d.M.Name)
	}
	if len(c.Send) == 0 {
		return 0, fmt.Errorf("%s is not supported on %s yet", prop, d.M.Name)
	}
	_, err := d.ask(ctx, c, vars{track: track - 1 + d.M.Base, value: val})
	if err != nil {
		return 0, err
	}
	if prop == "volume" || prop == "pan" || prop == "mute" || prop == "solo" {
		r, readErr := d.RunExtra(ctx, "track_state", track, 0, 0, 0, "")
		if readErr == nil && len(r) >= 9 {
			i := map[string]int{"volume": 6, "pan": 7, "mute": 3, "solo": 4}[prop]
			if got, ok := num(r[i]); ok {
				return got, nil
			}
		}
	}
	return val, nil
}

// SetBypass switches a device off and on (core.Bypasser).
func (d *DAW) SetBypass(ctx context.Context, track, fx int, bypassed bool) error {
	c := d.M.SetBypass
	if len(c.Send) == 0 {
		return fmt.Errorf("bypass is not supported on %s yet", d.M.Name)
	}
	val := 0.0
	addr := strings.ToLower(c.Send[0])
	if strings.Contains(addr, "active") || strings.Contains(addr, "enable") {
		if !bypassed {
			val = 1.0
		}
	} else {
		if bypassed {
			val = 1.0
		}
	}
	_, err := d.ask(ctx, c, vars{
		track: track - 1 + d.M.Base,
		fx:    fx - 1 + d.M.Base,
		value: val,
	})
	return err
}

// Presets lists available presets for a device.
func (d *DAW) Presets(ctx context.Context, track, fx int) (string, []string, error) {
	c := d.M.DevicePresets
	if len(c.Send) == 0 {
		return "", nil, fmt.Errorf("presets are not supported on %s yet", d.M.Name)
	}
	r, err := d.ask(ctx, c, vars{
		track: track - 1 + d.M.Base,
		fx:    fx - 1 + d.M.Base,
	})
	if err != nil {
		return "", nil, err
	}
	if len(r) > 0 && strings.HasPrefix(strings.ToLower(str(r[0])), "error:") {
		return "", nil, errors.New(str(r[0]))
	}
	list := make([]string, 0, len(r))
	for _, a := range r {
		list = append(list, str(a))
	}
	return "", list, nil
}

// SetPreset loads a preset on a device.
func (d *DAW) SetPreset(ctx context.Context, track, fx int, preset string) error {
	c := d.M.SetPreset
	if len(c.Send) == 0 {
		return fmt.Errorf("presets are not supported on %s yet", d.M.Name)
	}
	r, err := d.ask(ctx, c, vars{
		track:  track - 1 + d.M.Base,
		fx:     fx - 1 + d.M.Base,
		name:   preset,
		preset: preset,
	})
	if err != nil {
		return err
	}
	if len(r) > 0 && strings.HasPrefix(strings.ToLower(str(r[0])), "error:") {
		return errors.New(str(r[0]))
	}
	return nil
}

// SavePresetAs saves a device's current state under a named preset when the map supports it.
func (d *DAW) SavePresetAs(ctx context.Context, track, fx int, name string) error {
	c := d.M.SavePresetAs
	if len(c.Send) == 0 {
		return fmt.Errorf("saving presets is not supported on %s yet", d.M.Name)
	}
	_, err := d.ask(ctx, c, vars{
		track:  track - 1 + d.M.Base,
		fx:     fx - 1 + d.M.Base,
		name:   name,
		preset: name,
	})
	return err
}

// RunExtra executes a custom DAW-specific command from Map.Extra.
func (d *DAW) RunExtra(ctx context.Context, name string, track, fx, param int, value float64, strArg string) ([]any, error) {
	return d.runExtra(ctx, name, varsForExtra(d.M.Base, track, fx, param, value, strArg, 0))
}

// RunExtraSlot executes an extra command with an explicit clip-slot index.
func (d *DAW) RunExtraSlot(ctx context.Context, name string, track, slot int, value float64) ([]any, error) {
	v := varsForExtra(d.M.Base, track, 0, 0, value, "", slot)
	return d.runExtra(ctx, name, v)
}

func varsForExtra(base, track, fx, param int, value float64, strArg string, slot int) vars {
	tr, f, p := track, fx, param
	if tr > 0 {
		tr = tr - 1 + base
	}
	if f > 0 {
		f = f - 1 + base
	}
	if p > 0 {
		p = p - 1 + base
	}
	return vars{track: tr, fx: f, param: p, send: p, value: value, name: strArg, preset: strArg, slot: slot}
}

func (d *DAW) runExtra(ctx context.Context, name string, v vars) ([]any, error) {
	if d.M.Extra == nil {
		return nil, fmt.Errorf("extra command %q not found", name)
	}
	c, ok := d.M.Extra[name]
	if !ok || len(c.Send) == 0 {
		return nil, fmt.Errorf("extra command %q not found", name)
	}
	return d.Do(ctx, c, v)
}

func (d *DAW) Undo(ctx context.Context) error {
	_, err := d.ask(ctx, d.M.Undo, vars{})
	return err
}

// Transport plays or stops, for capture-based listening.
func (d *DAW) Transport(ctx context.Context, play bool) error {
	c := d.M.Stop
	if play {
		c = d.M.Play
	}
	_, err := d.ask(ctx, c, vars{})
	return err
}

func (d *DAW) SupportsUndoActionGrouping() bool {
	return d.M.BeginUndoAction != nil && len(d.M.BeginUndoAction.Send) > 0 && d.M.BeginUndoAction.Success != "" && d.M.EndUndoAction != nil && len(d.M.EndUndoAction.Send) > 0 && d.M.EndUndoAction.Success != "" && d.M.EndUndoAction.CancelSuccess != ""
}

func (d *DAW) BeginUndoAction(ctx context.Context, name string) error {
	if !d.SupportsUndoActionGrouping() {
		return fmt.Errorf("undo action grouping is unsupported by this map")
	}
	if !d.groupMu.TryLock() {
		return fmt.Errorf("another undo action is already active")
	}
	if d.groupName != "" {
		d.groupMu.Unlock()
		return fmt.Errorf("another undo action is already active")
	}
	d.groupName = name
	r, err := d.Do(ctx, *d.M.BeginUndoAction, vars{name: name})
	if err != nil {
		d.groupName = ""
		d.groupMu.Unlock()
		return err
	}
	if err := mapSuccess(r, d.M.BeginUndoAction.Success); err != nil {
		d.groupName = ""
		d.groupMu.Unlock()
		return fmt.Errorf("begin_undo_action: %w", err)
	}
	return nil
}

func (d *DAW) EndUndoAction(ctx context.Context, name string, commit bool) error {
	if !d.SupportsUndoActionGrouping() {
		return fmt.Errorf("undo action grouping is unsupported by this map")
	}
	if d.groupName != name {
		return fmt.Errorf("undo action name does not match the active map group")
	}
	value := 0.0
	if commit {
		value = 1
	}
	r, err := d.Do(ctx, *d.M.EndUndoAction, vars{name: name, value: value})
	if err != nil {
		d.groupName = ""
		d.groupMu.Unlock()
		return err
	}
	want := d.M.EndUndoAction.Success
	if !commit {
		want = d.M.EndUndoAction.CancelSuccess
	}
	if err := mapSuccess(r, want); err != nil {
		d.groupName = ""
		d.groupMu.Unlock()
		return fmt.Errorf("end_undo_action: %w", err)
	}
	d.groupName = ""
	d.groupMu.Unlock()
	return nil
}

func (d *DAW) UndoActionIsCurrent(ctx context.Context, name string) (bool, error) {
	c := d.M.UndoActionCurrent
	if c == nil || len(c.Send) == 0 {
		return false, nil
	}
	r, err := d.Do(ctx, *c, vars{name: name})
	if err != nil {
		return false, err
	}
	if len(r) == 0 {
		return false, fmt.Errorf("undo_action_current readback missing")
	}
	if got, ok := r[0].(string); ok {
		return got == name, nil
	}
	return false, fmt.Errorf("undo_action_current reply must be the current action name")
}

func (d *DAW) SupportsRequestSnapshot() bool {
	return d.M.CaptureSessionSnapshot != nil && len(d.M.CaptureSessionSnapshot.Send) > 0 && d.M.RestoreSessionSnapshot != nil && len(d.M.RestoreSessionSnapshot.Send) > 0 && d.M.RestoreSessionSnapshot.Success != ""
}

func mapSuccess(reply []any, expected string) error {
	if expected == "" {
		return fmt.Errorf("expected success reply is not configured")
	}
	if len(reply) == 0 {
		return fmt.Errorf("readback missing")
	}
	got, ok := reply[0].(string)
	if !ok {
		return fmt.Errorf("readback is not text")
	}
	if got != expected {
		return fmt.Errorf("readback %q does not match configured success %q", got, expected)
	}
	return nil
}

func (d *DAW) CaptureRequestSnapshot(ctx context.Context) ([]byte, error) {
	c := d.M.CaptureSessionSnapshot
	if c == nil || len(c.Send) == 0 {
		return nil, fmt.Errorf("exact session snapshots are unsupported by this map")
	}
	r, err := d.Do(ctx, *c, vars{})
	if err != nil {
		return nil, err
	}
	if len(r) != 1 {
		return nil, fmt.Errorf("session snapshot reply must be exactly one opaque string")
	}
	s, ok := r[0].(string)
	if !ok || s == "" {
		return nil, fmt.Errorf("session snapshot reply was empty or not text")
	}
	return []byte(s), nil
}

func (d *DAW) RestoreRequestSnapshot(ctx context.Context, state []byte) error {
	c := d.M.RestoreSessionSnapshot
	if c == nil || len(c.Send) == 0 {
		return fmt.Errorf("exact session restore is unsupported by this map")
	}
	if len(state) == 0 {
		return fmt.Errorf("session snapshot is empty")
	}
	if len(state) > 60_000 {
		return fmt.Errorf("session snapshot exceeds the OSC payload limit")
	}
	r, err := d.Do(ctx, *c, vars{name: string(state), preset: string(state)})
	if err != nil {
		return err
	}
	return mapSuccess(r, c.Success)
}

// AudioEngineRunning reports whether the DAW's audio engine is running and
// outputting audio.
func (d *DAW) AudioEngineRunning(ctx context.Context) (bool, error) {
	var c Command
	if len(d.M.AudioEngine.Send) > 0 {
		c = d.M.AudioEngine
	} else if extra, ok := d.M.Extra["audio_engine"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return false, errors.New("audio_engine not supported by this DAW")
	}
	r, err := d.ask(ctx, c, vars{})
	if err != nil {
		return false, err
	}
	if len(r) == 0 {
		return false, errors.New("no reply from audio_engine query")
	}
	if n, ok := num(r[0]); ok {
		return n != 0, nil
	}
	if b, ok := r[0].(bool); ok {
		return b, nil
	}
	s := strings.ToLower(str(r[0]))
	if s == "true" || s == "1" || s == "on" || s == "running" {
		return true, nil
	}
	if s == "false" || s == "0" || s == "off" || s == "stopped" {
		return false, nil
	}
	return false, fmt.Errorf("unexpected audio_engine reply: %v", r[0])
}

var _ core.DAW = (*DAW)(nil)
var _ core.Bypasser = (*DAW)(nil)
var _ core.AudioEngineChecker = (*DAW)(nil)
var _ core.SessionController = (*DAW)(nil)
var _ core.UndoActionGrouper = (*DAW)(nil)
var _ core.RequestSnapshotter = (*DAW)(nil)
var _ core.SessionTrackControls = (*DAW)(nil)
var _ core.SessionRouting = (*DAW)(nil)
var _ core.SessionMarkers = (*DAW)(nil)
var _ core.SessionProject = (*DAW)(nil)
var _ core.SessionMeter = (*DAW)(nil)

// CreateTrack creates a new track with name at index (1-based; index <= 0 = end).
func (d *DAW) CreateTrack(ctx context.Context, name string, index int) (int, error) {
	var c Command
	if d.M.CreateTrack != nil && len(d.M.CreateTrack.Send) > 0 {
		c = *d.M.CreateTrack
	} else if extra, ok := d.M.Extra["create_track"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return 0, fmt.Errorf("create_track is not supported on %s yet", d.M.Name)
	}

	t := index
	if t > 0 {
		t = t - 1 + d.M.Base
	} else {
		t = -1
	}
	r, err := d.Do(ctx, c, vars{track: t, name: name, value: float64(t)})
	if err != nil {
		return 0, err
	}
	if len(r) > 0 {
		if n, ok := num(r[0]); ok {
			return int(n) - d.M.Base + 1, nil
		}
	}
	if index > 0 {
		return index, nil
	}
	trs, err := d.Tracks(ctx)
	if err == nil && len(trs) > 0 {
		return len(trs), nil
	}
	return 1, nil
}

// DuplicateTrack duplicates track (1-based index).
func (d *DAW) DuplicateTrack(ctx context.Context, track int, name string) (int, error) {
	var c Command
	if d.M.DuplicateTrack != nil && len(d.M.DuplicateTrack.Send) > 0 {
		c = *d.M.DuplicateTrack
	} else if extra, ok := d.M.Extra["duplicate_track"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return 0, fmt.Errorf("duplicate_track is not supported on %s yet", d.M.Name)
	}

	r, err := d.Do(ctx, c, vars{track: track - 1 + d.M.Base, name: name})
	if err != nil {
		return 0, err
	}
	if len(r) > 0 {
		if n, ok := num(r[0]); ok {
			return int(n) - d.M.Base + 1, nil
		}
	}
	return track + 1, nil
}

// DeleteTrack deletes track (1-based index).
func (d *DAW) DeleteTrack(ctx context.Context, track int) error {
	var c Command
	if d.M.DeleteTrack != nil && len(d.M.DeleteTrack.Send) > 0 {
		c = *d.M.DeleteTrack
	} else if extra, ok := d.M.Extra["delete_track"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return fmt.Errorf("delete_track is not supported on %s yet", d.M.Name)
	}

	_, err := d.Do(ctx, c, vars{track: track - 1 + d.M.Base})
	return err
}

// RenameTrack renames track (1-based index).
func (d *DAW) RenameTrack(ctx context.Context, track int, name string) error {
	var c Command
	if d.M.RenameTrack != nil && len(d.M.RenameTrack.Send) > 0 {
		c = *d.M.RenameTrack
	} else if extra, ok := d.M.Extra["rename_track"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return fmt.Errorf("rename_track is not supported on %s yet", d.M.Name)
	}

	_, err := d.Do(ctx, c, vars{track: track - 1 + d.M.Base, name: name})
	return err
}

// InsertMedia puts an audio file on the track at positionSec.
func (d *DAW) InsertMedia(ctx context.Context, track int, path string, positionSec float64) error {
	var c Command
	if d.M.InsertMedia != nil && len(d.M.InsertMedia.Send) > 0 {
		c = *d.M.InsertMedia
	} else if extra, ok := d.M.Extra["insert_media"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return fmt.Errorf("insert_media is not supported on %s yet", d.M.Name)
	}

	r, err := d.Do(ctx, c, vars{track: track - 1 + d.M.Base, name: path, value: positionSec})
	if err != nil {
		return err
	}
	if len(r) > 1 {
		if n, ok := num(r[0]); ok && n < 0 {
			return fmt.Errorf("failed to insert media: %v", r[1:])
		}
	}
	return nil
}

// CreateSend creates an aux send / routing from fromTrack to toTrack.
func (d *DAW) CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error {
	var c Command
	if d.M.CreateSend != nil && len(d.M.CreateSend.Send) > 0 {
		c = *d.M.CreateSend
	} else if extra, ok := d.M.Extra["create_send"]; ok && len(extra.Send) > 0 {
		c = extra
	} else {
		return fmt.Errorf("create_send is not supported on %s yet", d.M.Name)
	}

	b := d.M.Base
	_, err := d.Do(ctx, c, vars{
		track: fromTrack - 1 + b,
		to:    toTrack - 1 + b,
		value: levelDB,
	})
	return err
}

// SetTransport controls transport actions: "play", "stop", "tempo", "loop", "cursor".
func (d *DAW) SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error {
	switch strings.ToLower(action) {
	case "play":
		return d.Transport(ctx, true)
	case "stop":
		return d.Transport(ctx, false)
	case "tempo":
		var c Command
		if d.M.SetTempo != nil && len(d.M.SetTempo.Send) > 0 {
			c = *d.M.SetTempo
		} else if extra, ok := d.M.Extra["set_tempo"]; ok && len(extra.Send) > 0 {
			c = extra
		} else {
			return fmt.Errorf("set_tempo is not supported on %s yet", d.M.Name)
		}
		_, err := d.Do(ctx, c, vars{value: bpm})
		return err
	case "loop":
		var c Command
		if d.M.SetLoop != nil && len(d.M.SetLoop.Send) > 0 {
			c = *d.M.SetLoop
		} else if extra, ok := d.M.Extra["set_loop"]; ok && len(extra.Send) > 0 {
			c = extra
		} else {
			return fmt.Errorf("set_loop is not supported on %s yet", d.M.Name)
		}
		_, err := d.Do(ctx, c, vars{value: loopStart, to: int(math.Round(loopEnd)), start: loopStart, end: loopEnd})
		return err
	case "cursor", "seek":
		if extra, ok := d.M.Extra["set_cursor"]; ok && len(extra.Send) > 0 {
			_, err := d.Do(ctx, extra, vars{value: posSec})
			return err
		}
		if extra, ok := d.M.Extra["seek"]; ok && len(extra.Send) > 0 {
			_, err := d.Do(ctx, extra, vars{value: posSec})
			return err
		}
		return fmt.Errorf("set_cursor is not supported on %s yet", d.M.Name)
	default:
		return fmt.Errorf("unknown transport action: %s", action)
	}
}

func (d *DAW) optional(name string, c *Command) (Command, error) {
	if c != nil && len(c.Send) > 0 {
		return *c, nil
	}
	if x, ok := d.M.Extra[name]; ok && len(x.Send) > 0 {
		return x, nil
	}
	return Command{}, fmt.Errorf("%s is not supported on %s yet", name, d.M.Name)
}

func (d *DAW) SetTrackControl(ctx context.Context, track int, control, value string) (string, error) {
	var c *Command
	role := "set_track_" + control
	switch control {
	case "arm":
		c = d.M.SetTrackArm
	case "monitor":
		c = d.M.SetTrackMonitor
	case "input":
		c = d.M.SetTrackInput
	default:
		return "", fmt.Errorf("unsupported track control %q", control)
	}
	x, err := d.optional(role, c)
	if err != nil {
		return "", err
	}
	r, err := d.Do(ctx, x, vars{track: track - 1 + d.M.Base, value: func() float64 { f, _ := strconv.ParseFloat(value, 64); return f }(), name: value})
	if err != nil {
		return "", err
	}
	if len(r) == 0 {
		return "", fmt.Errorf("%s readback missing", role)
	}
	return str(r[0]), nil
}

func (d *DAW) RemoveSend(ctx context.Context, fromTrack, send int) error {
	c, err := d.optional("remove_send", d.M.RemoveSend)
	if err != nil {
		return err
	}
	r, err := d.Do(ctx, c, vars{track: fromTrack - 1 + d.M.Base, marker: send - 1 + d.M.Base, send: send - 1 + d.M.Base, fx: send - 1 + d.M.Base})
	if err != nil {
		return err
	}
	return requireMutationSuccess("remove_send", c, r)
}

func (d *DAW) SetTimeSignature(ctx context.Context, numerator, denominator int) error {
	c, err := d.optional("set_time_signature", d.M.SetTimeSignature)
	if err != nil {
		return err
	}
	r, err := d.Do(ctx, c, vars{track: numerator, value: float64(denominator), to: denominator, numerator: numerator, denominator: denominator})
	if err != nil {
		return err
	}
	return requireMutationSuccess("set_time_signature", c, r)
}

func (d *DAW) SaveProject(ctx context.Context) error {
	c, err := d.optional("save_project", d.M.SaveProject)
	if err != nil {
		return err
	}
	r, err := d.Do(ctx, c, vars{})
	if err != nil {
		return err
	}
	return requireMutationSuccess("save_project", c, r)
}

func (d *DAW) CreateMarker(ctx context.Context, start, end float64, name string, region bool) (int, error) {
	c, err := d.optional("create_marker", d.M.CreateMarker)
	if err != nil {
		return 0, err
	}
	flag := 0
	if region {
		flag = 1
	}
	r, err := d.Do(ctx, c, vars{name: name, start: start, end: end, flag: flag})
	if err != nil {
		return 0, err
	}
	if len(r) != 1 {
		return 0, fmt.Errorf("marker creation readback missing")
	}
	n, ok := num(r[0])
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(d.M.Base) {
		return 0, fmt.Errorf("invalid marker readback")
	}
	return int(n) - d.M.Base + 1, nil // 1-based, like GoToMarker
}

func (d *DAW) GoToMarker(ctx context.Context, index int) error {
	c, err := d.optional("goto_marker", d.M.GoToMarker)
	if err != nil {
		return err
	}
	r, err := d.Do(ctx, c, vars{marker: index - 1 + d.M.Base, track: index - 1 + d.M.Base})
	if err != nil {
		return err
	}
	return requireMutationSuccess("goto_marker", c, r)
}

// DeleteMarker removes marker index (1-based, as Markers reports it).
func (d *DAW) DeleteMarker(ctx context.Context, index int) error {
	c, err := d.optional("delete_marker", d.M.DeleteMarker)
	if err != nil {
		return err
	}
	r, err := d.Do(ctx, c, vars{marker: index - 1 + d.M.Base})
	if err != nil {
		return err
	}
	return requireMutationSuccess("delete_marker", c, r)
}

func requireMutationSuccess(role string, c Command, reply []any) error {
	if c.Success == "" {
		return fmt.Errorf("%s has no configured success token", role)
	}
	if len(reply) != 1 || str(reply[0]) != c.Success {
		return fmt.Errorf("%s was not confirmed: expected exact status %q, got %v", role, c.Success, reply)
	}
	return nil
}

func (d *DAW) Markers(ctx context.Context) ([]core.Marker, error) {
	c, err := d.optional("list_markers", d.M.ListMarkers)
	if err != nil {
		return nil, err
	}
	r, err := d.Do(ctx, c, vars{})
	if err != nil {
		return nil, err
	}
	if len(r)%5 != 0 {
		return nil, fmt.Errorf("marker list reply must contain groups of index,name,start,end,region")
	}
	out := make([]core.Marker, 0, len(r)/5)
	for i := 0; i < len(r); i += 5 {
		idx, iok := num(r[i])
		start, sok := num(r[i+2])
		end, eok := num(r[i+3])
		flag, fok := num(r[i+4])
		name, nok := r[i+1].(string)
		if !iok || !sok || !eok || !fok || !nok || math.IsNaN(idx) || math.IsInf(idx, 0) || math.Trunc(idx) != idx || idx < float64(d.M.Base) || math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) || (flag != 0 && flag != 1) {
			return nil, fmt.Errorf("invalid marker list entry at offset %d", i)
		}
		out = append(out, core.Marker{Index: int(idx) - d.M.Base + 1, Name: name, PositionSec: start, EndSec: end, Region: flag == 1})
	}
	return out, nil
}

// ParamText reads one parameter's readout (core.ParamReader).
func (d *DAW) ParamText(ctx context.Context, track int, id core.ParamID) (string, error) {
	fx, p, err := id.Parse()
	if err != nil {
		return "", err
	}
	r, err := d.ask(ctx, d.M.ParamText, vars{track: track - 1 + d.M.Base, fx: fx - 1 + d.M.Base, param: p - 1 + d.M.Base})
	if err != nil || len(r) == 0 {
		return "", fmt.Errorf("no readout for %s: %v", id, err)
	}
	return str(r[0]), nil
}

// texts asks for every parameter's readout at once and sorts the replies
// by the parameter index the DAW echoes (the last echoed arg before the
// value); one round trip instead of n.
func (d *DAW) texts(ctx context.Context, t, f, n int) []string {
	out := make([]string, n)
	c := d.M.ParamText
	if len(c.Send) == 0 || c.Reply == "" || c.Skip < 1 {
		for p := 0; p < n; p++ { // no echo to sort by: one at a time
			if r, err := d.ask(ctx, c, vars{track: t, fx: f, param: p + d.M.Base}); err == nil && len(r) > 0 {
				out[p] = str(r[0])
			}
		}
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drain()
	for p := 0; p < n; p++ {
		addr, args, err := c.build(vars{track: t, fx: f, param: p + d.M.Base})
		if err != nil {
			return out
		}
		if b, err := reaper.Encode(reaper.Msg{Addr: addr, Args: args}); err == nil {
			d.pc.WriteTo(b, d.to)
		}
	}
	// count answers, not non-empty texts (a control may show nothing), and
	// stop once answers stop coming
	seen := make([]bool, n)
	got := 0
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 65536)
	for got < n && time.Now().Before(deadline) && ctx.Err() == nil {
		wait := deadline
		if got > 0 {
			wait = time.Now().Add(250 * time.Millisecond)
		}
		d.pc.SetReadDeadline(wait)
		k, _, err := d.pc.ReadFrom(buf)
		if err != nil {
			break
		}
		ms, err := reaper.Decode(buf[:k])
		if err != nil {
			continue
		}
		for _, m := range ms {
			if m.Addr != c.Reply || len(m.Args) <= c.Skip {
				continue
			}
			idx, ok := num(m.Args[c.Skip-1])
			if p := int(idx) - d.M.Base; ok && p >= 0 && p < n && !seen[p] {
				out[p], seen[p] = str(m.Args[c.Skip]), true
				got++
			}
		}
	}
	return out
}

// OpenUI shows device fx (1-based) of track on screen with the map's
// show_device. Hiding is left to the user: nothing to send.
func (d *DAW) OpenUI(ctx context.Context, track, fx int, open bool) error {
	if !open {
		return nil
	}
	if !d.M.Shows() {
		return errors.New("this DAW's map cannot show a device")
	}
	b := d.M.Base
	_, err := d.ask(ctx, d.M.ShowDevice, vars{track: track - 1 + b, fx: fx - 1 + b})
	return err
}
