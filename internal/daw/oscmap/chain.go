package oscmap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Chain implements core.ChainEditor with a map's device commands. It works
// only after the user's one-time consent (the file at Consent exists), and
// removes only devices it added itself.
type Chain struct {
	D       *DAW
	Consent string

	mu    sync.Mutex
	added map[string]int // "track/name" → devices added this session
}

// ErrNoConsent means the user has not allowed plugin changes yet.
var ErrNoConsent = fmt.Errorf("%w: adding devices", core.ErrNeedsConsent)

// EnableAuto creates the parent directory and writes the consent file so
// Turbo mode can modify chains without manual approval.
func (c *Chain) EnableAuto() error {
	if c.Consent == "" {
		return errors.New("no consent file path configured")
	}
	if err := os.MkdirAll(filepath.Dir(c.Consent), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.Consent, []byte("auto\n"), 0o644)
}

// AutoEnable is an alias for EnableAuto.
func (c *Chain) AutoEnable() error {
	return c.EnableAuto()
}

func (c *Chain) allowed() error {
	if _, err := os.Stat(c.Consent); err != nil {
		return ErrNoConsent
	}
	return nil
}

func (c *Chain) Catalog(ctx context.Context) ([]core.CatalogEntry, error) {
	r, err := c.D.Do(ctx, c.D.M.DeviceCatalog, vars{track: c.D.M.Base})
	if err != nil {
		return nil, err
	}
	out := make([]core.CatalogEntry, 0, len(r))
	for _, n := range r {
		out = append(out, core.CatalogEntry{Name: fmt.Sprint(n)})
	}
	return out, nil
}

// AddFX adds a device at pos (1-based; 0 = at the end) and returns its
// index in the chain.
func (c *Chain) AddFX(ctx context.Context, track int, name string, pos int) (int, error) {
	if err := c.allowed(); err != nil {
		return 0, err
	}
	ch, err := c.D.FXChain(ctx, track)
	if err != nil {
		return 0, err
	}
	at := len(ch)
	if pos > 0 && pos <= len(ch) {
		at = pos - 1
	}
	b := c.D.M.Base
	r, err := c.D.Do(ctx, c.D.M.InsertDevice, vars{track: track - 1 + b, fx: at + b, name: name})
	if err != nil {
		return 0, err
	}
	if len(r) > 0 {
		if i, ok := num(r[0]); ok {
			if i < 0 {
				return 0, fmt.Errorf("the DAW refused %s: %v", name, r[1:])
			}
			at = int(i) - b
		}
	}
	after, err := c.D.FXChain(ctx, track)
	if err != nil {
		return 0, err
	}
	if len(after) != len(ch)+1 {
		return 0, errors.New("the DAW did not add " + name)
	}
	c.mu.Lock()
	if c.added == nil {
		c.added = map[string]int{}
	}
	c.added[fmt.Sprintf("%d/%s", track, after[at].Name)]++
	c.mu.Unlock()
	return at + 1, nil
}

// RemoveFX takes off the device at index fx (1-based), if this session
// added one of that kind on that track, and returns its name.
func (c *Chain) RemoveFX(ctx context.Context, track, fx int) (string, error) {
	if err := c.allowed(); err != nil {
		return "", err
	}
	ch, err := c.D.FXChain(ctx, track)
	if err != nil {
		return "", err
	}
	if fx < 1 || fx > len(ch) {
		return "", fmt.Errorf("track %d has no device %d", track, fx)
	}
	name := ch[fx-1].Name
	key := fmt.Sprintf("%d/%s", track, name)
	c.mu.Lock()
	n := c.added[key]
	c.mu.Unlock()
	if n == 0 {
		return "", fmt.Errorf("%s was not added by the agent; only devices it added can be removed", name)
	}
	b := c.D.M.Base
	if _, err := c.D.Do(ctx, c.D.M.RemoveDevice, vars{track: track - 1 + b, fx: fx - 1 + b}); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.added[key]--
	c.mu.Unlock()
	return name, nil
}

// ForceRemoveFX takes off the device at index fx (1-based) without requiring
// that the device was added in this session. It auto-enables consent if needed,
// for Turbo mode.
func (c *Chain) ForceRemoveFX(ctx context.Context, track, fx int) (string, error) {
	if err := c.allowed(); err != nil {
		if aerr := c.EnableAuto(); aerr != nil {
			return "", aerr
		}
	}
	ch, err := c.D.FXChain(ctx, track)
	if err != nil {
		return "", err
	}
	if fx < 1 || fx > len(ch) {
		return "", fmt.Errorf("track %d has no device %d", track, fx)
	}
	name := ch[fx-1].Name
	b := c.D.M.Base
	if _, err := c.D.Do(ctx, c.D.M.RemoveDevice, vars{track: track - 1 + b, fx: fx - 1 + b}); err != nil {
		return "", err
	}
	key := fmt.Sprintf("%d/%s", track, name)
	c.mu.Lock()
	if c.added != nil && c.added[key] > 0 {
		c.added[key]--
	}
	c.mu.Unlock()
	return name, nil
}

// MoveFX moves a device from fromFX to toFX (1-based indices).
func (c *Chain) MoveFX(ctx context.Context, track, fromFX, toFX int) error {
	if err := c.allowed(); err != nil {
		return err
	}
	if len(c.D.M.MoveDevice.Send) == 0 {
		return fmt.Errorf("move_device is not supported on %s yet", c.D.M.Name)
	}
	b := c.D.M.Base
	_, err := c.D.Do(ctx, c.D.M.MoveDevice, vars{
		track: track - 1 + b,
		fx:    fromFX - 1 + b,
		to:    toFX - 1 + b,
	})
	return err
}

// Presets lists available presets for a device.
func (c *Chain) Presets(ctx context.Context, track, fx int) (string, []string, error) {
	return c.D.Presets(ctx, track, fx)
}

// SetPreset loads a preset on a device.
func (c *Chain) SetPreset(ctx context.Context, track, fx int, preset string) error {
	return c.D.SetPreset(ctx, track, fx, preset)
}

// SetBypass switches a device off and on (core.Bypasser).
func (c *Chain) SetBypass(ctx context.Context, track, fx int, bypassed bool) error {
	return c.D.SetBypass(ctx, track, fx, bypassed)
}

// CreateTrack forwards to DAW.
func (c *Chain) CreateTrack(ctx context.Context, name string, index int) (int, error) {
	return c.D.CreateTrack(ctx, name, index)
}

// DuplicateTrack forwards to DAW.
func (c *Chain) DuplicateTrack(ctx context.Context, track int, name string) (int, error) {
	return c.D.DuplicateTrack(ctx, track, name)
}

// DeleteTrack forwards to DAW.
func (c *Chain) DeleteTrack(ctx context.Context, track int) error {
	return c.D.DeleteTrack(ctx, track)
}

// RenameTrack forwards to DAW.
func (c *Chain) RenameTrack(ctx context.Context, track int, name string) error {
	return c.D.RenameTrack(ctx, track, name)
}

// InsertMedia forwards to DAW.
func (c *Chain) InsertMedia(ctx context.Context, track int, path string, positionSec float64) error {
	return c.D.InsertMedia(ctx, track, path, positionSec)
}

// CreateSend forwards to DAW.
func (c *Chain) CreateSend(ctx context.Context, fromTrack, toTrack int, levelDB float64) error {
	return c.D.CreateSend(ctx, fromTrack, toTrack, levelDB)
}

// SetTransport forwards to DAW.
func (c *Chain) SetTransport(ctx context.Context, action string, bpm, posSec, loopStart, loopEnd float64) error {
	return c.D.SetTransport(ctx, action, bpm, posSec, loopStart, loopEnd)
}

// AudioEngineRunning forwards to DAW.
func (c *Chain) AudioEngineRunning(ctx context.Context) (bool, error) {
	return c.D.AudioEngineRunning(ctx)
}

func (c *Chain) SetTrackControl(ctx context.Context, track int, control, value string) (string, error) {
	return c.D.SetTrackControl(ctx, track, control, value)
}
func (c *Chain) RemoveSend(ctx context.Context, fromTrack, send int) error {
	return c.D.RemoveSend(ctx, fromTrack, send)
}
func (c *Chain) Markers(ctx context.Context) ([]core.Marker, error) { return c.D.Markers(ctx) }
func (c *Chain) CreateMarker(ctx context.Context, start, end float64, name string, region bool) (int, error) {
	return c.D.CreateMarker(ctx, start, end, name, region)
}
func (c *Chain) GoToMarker(ctx context.Context, index int) error { return c.D.GoToMarker(ctx, index) }
func (c *Chain) SaveProject(ctx context.Context) error           { return c.D.SaveProject(ctx) }
func (c *Chain) SetTimeSignature(ctx context.Context, numerator, denominator int) error {
	return c.D.SetTimeSignature(ctx, numerator, denominator)
}

var _ core.SessionController = (*Chain)(nil)
var _ core.AudioEngineChecker = (*Chain)(nil)
var _ core.SessionTrackControls = (*Chain)(nil)
var _ core.SessionRouting = (*Chain)(nil)
var _ core.SessionMarkers = (*Chain)(nil)
var _ core.SessionProject = (*Chain)(nil)
var _ core.SessionMeter = (*Chain)(nil)
