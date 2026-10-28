// Package reaper drives REAPER through its OSC control surface with the
// "Default" pattern file. Everything goes through the device track: we
// select a track, REAPER reports that track's FX and parameters, and every
// value we set is confirmed by REAPER's own feedback.
package reaper

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Timings. REAPER only announces changes, so reads wait for feedback to go
// quiet; a set waits a bounded time for the echo of its own address.
const (
	quiet      = 150 * time.Millisecond
	stepMax    = 3 * time.Second
	confirmMax = 400 * time.Millisecond
	textMax    = 50 * time.Millisecond // readout lag after a confirmed set
	paramCount = 128                   // parameters REAPER reports per FX, set once per session
	maxTracks  = 64
)

// REAPER implements core.DAW.
type REAPER struct {
	c          *Conn
	Bridge     *Bridge
	PresetDirs []string // custom directories to scan for plugin presets/models
	cleanup    func() error
	mu         sync.Mutex // one device-track conversation at a time
	// selected is the device track; 0 = unknown.
	selected int
	setup    bool
	texts    map[string]string // last readout REAPER echoed per value address
}

// Close closes the underlying OSC connection and any associated server.
func (r *REAPER) Close() error {
	var err error
	if r.cleanup != nil {
		err = r.cleanup()
	}
	if r.c != nil {
		if cErr := r.c.Close(); cErr != nil && err == nil {
			err = cErr
		}
	}
	return err
}

func (r *REAPER) bridge() Bridge {
	if r.Bridge != nil {
		b := *r.Bridge
		if len(b.PresetDirs) == 0 && len(r.PresetDirs) > 0 {
			b.PresetDirs = r.PresetDirs
		}
		return b
	}
	return Bridge{PresetDirs: r.PresetDirs}
}

func New(c *Conn) *REAPER { return &REAPER{c: c} }

var _ core.ParamReader = (*REAPER)(nil)
var _ core.Bypasser = (*REAPER)(nil)
var _ core.ValueFinder = (*REAPER)(nil)
var _ core.Presetter = (*REAPER)(nil)
var _ core.ChainMover = (*REAPER)(nil)
var _ core.SessionController = (*REAPER)(nil)
var _ core.ModelSwapper = (*REAPER)(nil)

// Tracks walks the device track over 1, 2, … until REAPER stops naming one.
func (r *REAPER) Tracks(ctx context.Context) ([]core.Track, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []core.Track
	for i := 1; i <= maxTracks; i++ {
		msgs, err := r.selectTrack(ctx, i, false)
		if err != nil {
			return nil, err
		}
		name, num := "", ""
		for _, m := range msgs {
			switch m.Addr {
			case "/track/name":
				if s, _ := m.String(0); s != "" {
					name = s
				}
			case "/track/number/str":
				if s, _ := m.String(0); s != "" {
					num = s
				}
			}
		}
		if num != strconv.Itoa(i) {
			break
		}
		out = append(out, core.Track{Index: i, Name: name})
	}
	if len(out) == 0 {
		return nil, core.ErrDAWGone
	}
	return out, nil
}

// FXChain reports every FX on track with all its parameters.
func (r *REAPER) FXChain(ctx context.Context, track int) ([]core.FX, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.selectTrack(ctx, track, true)
	if err != nil {
		return nil, err
	}
	r.texts = nil // a full read is fresher than any echo
	chain := parseChain(msgs)
	b := r.bridge()
	for i := range chain {
		fx := &chain[i]
		unnamedCount := 0
		for _, p := range fx.Params {
			if p.Unnamed {
				unnamedCount++
			}
		}
		mostlyUnnamed := len(fx.Params) == 0 || (float64(unnamedCount)/float64(len(fx.Params)) >= 0.5)
		if mostlyUnnamed {
			if rawChunk, err := b.GetChunk(ctx, track, fx.Index); err == nil && rawChunk != "" {
				if projected, err := ParseChunk(fx.Index, rawChunk); err == nil && len(projected) > 0 {
					allDead := len(fx.Params) <= 16 && unnamedCount == len(fx.Params)
					if allDead {
						fx.Params = projected
					} else {
						fx.Params = append(fx.Params, projected...)
					}
				}
			}
		}
	}
	return chain, nil
}

// SetParam sets one parameter and returns what REAPER confirmed.
func (r *REAPER) SetParam(ctx context.Context, track int, id core.ParamID, v float64) (core.Applied, error) {
	fx, p, err := id.Parse()
	if err != nil {
		return core.Applied{}, err
	}
	if p >= 1000 {
		b := r.bridge()
		rawChunk, err := b.GetChunk(ctx, track, fx)
		if err != nil {
			return core.Applied{}, fmt.Errorf("getting chunk for %s: %w", id, err)
		}
		updatedChunk, confirmedNorm, text, err := PatchChunk(rawChunk, p-1000, v)
		if err != nil {
			return core.Applied{}, fmt.Errorf("patching chunk for %s: %w", id, err)
		}
		if err := b.keepWrite(ctx, track, fx, updatedChunk, func(back string) error {
			return checkKept(fx, rawChunk, updatedChunk, back)
		}); err != nil {
			return core.Applied{}, fmt.Errorf("setting %s: %w", id, err)
		}
		return core.Applied{
			Param:     id,
			Sent:      v,
			Confirmed: confirmedNorm,
			Text:      text,
			Status:    core.StatusConfirmed,
		}, nil
	}
	// REAPER echoes the absolute address with the value it accepted, and
	// the readout text at .../value/str; keep the text for ParamText.
	addr := fmt.Sprintf("/track/%d/fx/%d/fxparam/%d/value", track, fx, p)
	ch, cancel := r.c.Subscribe(addr)
	defer cancel()
	if err := r.c.Send(addr, float32(v)); err != nil {
		return core.Applied{}, err
	}
	a := core.Applied{Param: id, Sent: v, Status: core.StatusUnconfirmed}
	text, got := "", false
	var after <-chan time.Time // once confirmed, wait briefly for the text
	limit := time.After(confirmMax)
	for {
		select {
		case m := <-ch:
			switch m.Addr {
			case addr:
				f, _ := m.Float(0)
				a.Confirmed, a.Status = f, core.StatusConfirmed
			case addr + "/str":
				text, got = m.String(0)
			}
			if a.Status == core.StatusConfirmed && got {
				r.remember(addr, text)
				return a, nil
			}
			if a.Status == core.StatusConfirmed && after == nil {
				after = time.After(textMax)
			}
		case <-after:
			r.remember(addr, "")
			return a, nil
		case <-limit:
			if a.Status == core.StatusConfirmed {
				return a, nil
			}
			return a, fmt.Errorf("%w: %s", core.ErrUnconfirmed, id)
		case <-ctx.Done():
			return a, ctx.Err()
		}
	}
}

// SetParams applies a slice of parameter changes, batching any chunk-based parameters
// (p >= 1000) by FX so each plugin's chunk is fetched, patched, and written in a single
// round-trip. Standard parameters (p < 1000) are set via OSC.
func (r *REAPER) SetParams(ctx context.Context, track int, changes []core.Change) ([]core.Applied, error) {
	if len(changes) == 0 {
		return nil, nil
	}

	out := make([]core.Applied, len(changes))

	type chunkItem struct {
		origIndex   int
		paramOffset int
		change      core.Change
	}

	// Group chunk changes by FX
	chunkChanges := map[int][]chunkItem{}
	var standardIndices []int

	for i, ch := range changes {
		fx, p, err := ch.Param.Parse()
		if err != nil {
			return nil, err
		}
		if p >= 1000 {
			chunkChanges[fx] = append(chunkChanges[fx], chunkItem{
				origIndex:   i,
				paramOffset: p - 1000,
				change:      ch,
			})
		} else {
			standardIndices = append(standardIndices, i)
		}
	}

	// 1. Process batch chunk changes per FX
	b := r.bridge()
	for fx, items := range chunkChanges {
		rawChunk, err := b.GetChunk(ctx, track, fx)
		if err != nil {
			return nil, fmt.Errorf("getting chunk for track %d fx %d: %w", track, fx, err)
		}

		edits := make(map[int]float64, len(items))
		for _, item := range items {
			val := item.change.Value
			if item.change.Target != "" {
				if foundVal, err := FindChunkValue(rawChunk, item.paramOffset, item.change.Target); err == nil {
					val = foundVal
				}
			}
			edits[item.paramOffset] = val
		}

		updatedChunk, appMap, err := PatchChunkBatch(rawChunk, edits)
		if err != nil {
			return nil, fmt.Errorf("patching batch chunk for track %d fx %d: %w", track, fx, err)
		}

		if err := b.keepWrite(ctx, track, fx, updatedChunk, func(back string) error {
			return checkKept(fx, rawChunk, updatedChunk, back)
		}); err != nil {
			return nil, fmt.Errorf("track %d: %w", track, err)
		}

		for _, item := range items {
			if app, ok := appMap[item.paramOffset]; ok {
				out[item.origIndex] = app
			} else {
				out[item.origIndex] = core.Applied{
					Param:  item.change.Param,
					Sent:   item.change.Value,
					Status: core.StatusConfirmed,
				}
			}
		}
	}

	// 2. Process standard OSC parameter changes
	for _, idx := range standardIndices {
		ch := changes[idx]
		val := ch.Value
		if ch.Target != "" {
			if foundVal, err := r.FindValue(ctx, track, ch.Param, ch.Target); err == nil {
				val = foundVal
			}
		}
		app, err := r.SetParam(ctx, track, ch.Param, val)
		if err != nil {
			return nil, err
		}
		out[idx] = app
	}

	return out, nil
}

// remember keeps the readout for addr; "" forgets it.
func (r *REAPER) remember(addr, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if text == "" {
		delete(r.texts, addr)
		return
	}
	if r.texts == nil {
		r.texts = map[string]string{}
	}
	r.texts[addr] = text
}

// ParamText reads one parameter's readout (core.ParamReader): the text
// REAPER echoed to the last SetParam, else one read of the track.
func (r *REAPER) ParamText(ctx context.Context, track int, id core.ParamID) (string, error) {
	fx, p, err := id.Parse()
	if err != nil {
		return "", err
	}
	if p >= 1000 {
		ch, err := r.FXChain(ctx, track)
		if err != nil {
			return "", err
		}
		if fx < 1 || fx > len(ch) {
			return "", fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
		}
		for _, q := range ch[fx-1].Params {
			if q.ID == id {
				return q.Text, nil
			}
		}
		return "", fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
	}
	addr := fmt.Sprintf("/track/%d/fx/%d/fxparam/%d/value", track, fx, p)
	r.mu.Lock()
	text, ok := r.texts[addr]
	r.mu.Unlock()
	if ok {
		return text, nil
	}
	ch, err := r.FXChain(ctx, track)
	if err != nil {
		return "", err
	}
	if fx < 1 || fx > len(ch) {
		return "", fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
	}
	for _, q := range ch[fx-1].Params {
		if q.ID == id {
			return q.Text, nil
		}
	}
	return "", fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
}

// FindValue performs the Illinois false-position search for target directly in REAPER
// via the bridge script and returns the found normalized value.
func (r *REAPER) FindValue(ctx context.Context, track int, id core.ParamID, target string) (float64, error) {
	return r.bridge().FindValue(ctx, track, id, target)
}

// MoveFX reorders an FX on track using the bridge script.
func (r *REAPER) MoveFX(ctx context.Context, track, fromFX, toFX int) error {
	return r.bridge().MoveFX(ctx, track, fromFX, toFX)
}

// Presets lists available presets on an FX using the bridge script.
func (r *REAPER) Presets(ctx context.Context, track, fx int) (string, []string, error) {
	return r.bridge().Presets(ctx, track, fx)
}

// SetPreset sets a preset by name on an FX using the bridge script.
func (r *REAPER) SetPreset(ctx context.Context, track, fx int, preset string) error {
	return r.bridge().SetPreset(ctx, track, fx, preset)
}

// PluginModels queries available sub-models and current model for a plugin slot.
func (r *REAPER) PluginModels(ctx context.Context, track, fx int, slotQuery string) ([]core.PluginSlot, error) {
	return r.bridge().PluginModels(ctx, track, fx, slotQuery)
}

// SetPluginModel swaps an internal sub-model in a plugin slot and returns updated parameters.
func (r *REAPER) SetPluginModel(ctx context.Context, track, fx int, slot, model string) ([]core.Param, error) {
	return r.bridge().SetPluginModel(ctx, track, fx, slot, model)
}

// CreateTrack inserts a new track at 1-based index (0 = at the end).
func (r *REAPER) CreateTrack(ctx context.Context, name string, index int) (int, error) {
	return r.bridge().CreateTrack(ctx, name, index)
}

// DuplicateTrack duplicates the track at index and names the duplicate.
func (r *REAPER) DuplicateTrack(ctx context.Context, track int, name string) (int, error) {
	return r.bridge().DuplicateTrack(ctx, track, name)
}

// DeleteTrack deletes the track at index.
func (r *REAPER) DeleteTrack(ctx context.Context, track int) error {
	return r.bridge().DeleteTrack(ctx, track)
}

// RenameTrack changes the name of the track at index.
func (r *REAPER) RenameTrack(ctx context.Context, track int, name string) error {
	return r.bridge().RenameTrack(ctx, track, name)
}

// InsertMedia adds the media item at path to the track at positionSec.
func (r *REAPER) InsertMedia(ctx context.Context, track int, path string, positionSec float64) error {
	return r.bridge().InsertMedia(ctx, track, path, positionSec)
}

// CreateSend routes fromTrack to toTrack with the given level in dB.
func (r *REAPER) CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error {
	return r.bridge().CreateSend(ctx, fromTrack, toTrack, levelDB)
}

// SetTransport controls playback, tempo, cursor, and loop range.
func (r *REAPER) SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error {
	return r.bridge().SetTransport(ctx, action, bpm, posSec, loopStart, loopEnd)
}

// SetTrack sets volume, pan (0.5 = centre), mute or solo.
func (r *REAPER) SetTrack(ctx context.Context, track int, prop string, v float64) (float64, error) {
	switch prop {
	case "volume", "pan", "mute", "solo":
	default:
		return 0, fmt.Errorf("unknown track property %q", prop)
	}
	addr := fmt.Sprintf("/track/%d/%s", track, prop)
	ch, cancel := r.c.Subscribe(addr)
	defer cancel()
	var arg any = float32(v)
	if prop == "mute" || prop == "solo" {
		arg = int32(0)
		if v >= 0.5 {
			arg = int32(1)
		}
	}
	if err := r.c.Send(addr, arg); err != nil {
		return 0, err
	}
	select {
	case m := <-ch:
		f, _ := m.Float(0)
		return f, nil
	case <-time.After(confirmMax):
		return 0, fmt.Errorf("%w: %s", core.ErrUnconfirmed, addr)
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Undo runs REAPER's own undo, so the user's undo history stays true.
func (r *REAPER) Undo(ctx context.Context) error {
	return r.c.Send(fmt.Sprintf("/action/%d", ActionUndo))
}

// Alive asks for the device track and expects any answer: an idle REAPER
// is silent, so silence alone is not a disconnect.
func (r *REAPER) Alive(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, cancel := r.c.Subscribe("/")
	defer cancel()
	t := r.selected
	if t == 0 {
		t = 1
	}
	r.selected = 0
	// selecting the track already selected is silent, so step away and back
	for _, n := range []int{t + 1, t} {
		if err := r.c.Send("/device/track/select", int32(n)); err != nil {
			return err
		}
	}
	select {
	case <-ch:
		r.selected = t
		return nil
	case <-time.After(time.Second):
		return core.ErrDAWGone
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OpenUI shows or hides an FX window on the device track.
func (r *REAPER) OpenUI(ctx context.Context, track, fx int, open bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.selected != track {
		if _, err := r.selectTrack(ctx, track, false); err != nil {
			return err
		}
	}
	v := int32(0)
	if open {
		v = 1
	}
	return r.c.Send(fmt.Sprintf("/fx/%d/openui", fx), v)
}

// selectTrack makes track the device track and returns everything REAPER
// reported about it. REAPER announces only changes, so selecting the track
// already selected would report nothing: park on a neighbour first.
func (r *REAPER) selectTrack(ctx context.Context, track int, full bool) ([]Msg, error) {
	if !r.setup {
		if err := r.c.Send("/device/fxparam/count", int32(paramCount)); err != nil {
			return nil, err
		}
		r.setup = true
	}
	park := track + 1
	if track > 1 {
		park = track - 1
	}
	if err := r.c.Send("/device/track/select", int32(park)); err != nil {
		return nil, err
	}
	if err := r.c.WaitQuiet(ctx, quiet, stepMax); err != nil {
		return nil, err
	}
	ch, cancel := r.c.Subscribe("/")
	defer cancel()
	if err := r.c.Send("/device/track/select", int32(track)); err != nil {
		return nil, err
	}
	wait := quiet
	if full {
		wait = 2 * quiet // plugins with many parameters report in bursts
	}
	if err := r.c.WaitQuiet(ctx, wait, stepMax); err != nil {
		return nil, err
	}
	r.selected = track
	var out []Msg
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out, nil
		}
	}
}

// parseChain folds a device-track dump into FX and parameters. Later
// messages win: REAPER first clears the previous track's strings with "".
func parseChain(msgs []Msg) []core.FX {
	type param struct {
		name, text string
		value      float64
		hasValue   bool
	}
	names := map[int]string{}
	params := map[int]map[int]*param{}
	get := func(fx, p int) *param {
		if params[fx] == nil {
			params[fx] = map[int]*param{}
		}
		if params[fx][p] == nil {
			params[fx][p] = &param{}
		}
		return params[fx][p]
	}
	for _, m := range msgs {
		parts := strings.Split(strings.Trim(m.Addr, "/"), "/")
		if len(parts) < 3 || parts[0] != "fx" {
			continue
		}
		fx, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		if len(parts) == 3 && parts[2] == "name" {
			names[fx], _ = m.String(0)
			continue
		}
		if len(parts) < 5 || parts[2] != "fxparam" {
			continue
		}
		p, err := strconv.Atoi(parts[3])
		if err != nil {
			continue
		}
		switch strings.Join(parts[4:], "/") {
		case "name":
			s, _ := m.String(0)
			get(fx, p).name = s
		case "value":
			if f, ok := m.Float(0); ok {
				pp := get(fx, p)
				pp.value, pp.hasValue = f, true
			}
		case "value/str":
			s, _ := m.String(0)
			get(fx, p).text = s
		}
	}
	var out []core.FX
	for fx, name := range names {
		if name == "" {
			continue
		}
		f := core.FX{Index: fx, Name: name}
		for p, pp := range params[fx] {
			if pp.name == "" {
				continue // a slot beyond the plugin's real parameters
			}
			f.Params = append(f.Params, core.Param{
				ID:      core.MakeParamID(fx, p),
				Name:    pp.name,
				Kind:    Kind(pp.text),
				Text:    pp.text,
				Value:   pp.value,
				Unnamed: isGeneric(pp.name),
			})
		}
		sort.Slice(f.Params, func(i, j int) bool {
			_, a, _ := f.Params[i].ID.Parse()
			_, b, _ := f.Params[j].ID.Parse()
			return a < b
		})
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// Kind classifies a parameter from its readout so the agent never sends
// 0.73 to an on/off switch.
func Kind(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	switch t {
	case "on", "off", "yes", "no", "bypass", "bypassed", "active", "inactive", "enabled", "disabled", "normal":
		return "switch"
	}
	if t == "" {
		return ""
	}
	f := strings.Fields(t)[0]
	f = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(f, "+"), "%"), "db")
	if _, err := strconv.ParseFloat(f, 64); err == nil || strings.HasPrefix(t, "-inf") {
		return ""
	}
	return "discrete"
}

// isGeneric spots placeholder names like "Param 12" that say nothing.
func isGeneric(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range []string{"param ", "parameter ", "p"} {
		if rest, ok := strings.CutPrefix(n, p); ok {
			if _, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				return true
			}
		}
	}
	return false
}

// SetBypass turns an FX off (bypassed) or back on. REAPER's OSC "bypass"
// flag is 1 while the FX is active.
func (r *REAPER) SetBypass(ctx context.Context, track, fx int, bypassed bool) error {
	v := int32(1)
	if bypassed {
		v = 0
	}
	return r.c.Send(fmt.Sprintf("/track/%d/fx/%d/bypass", track, fx), v)
}

// App is REAPER's process name, as macOS lists its windows.
const App = "REAPER"

// WindowTitle is how REAPER titles a track's plugin windows.
func (r *REAPER) WindowTitle(track int) string { return fmt.Sprintf("FX: Track %d ", track) }
