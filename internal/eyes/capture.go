// Package eyes captures plugin windows so Muse can see them.
package eyes

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

// Opener shows a plugin's window (the DAW adapter's OpenUI).
type Opener interface {
	OpenUI(ctx context.Context, track, fx int, open bool) error
}

// Capturer implements core.Capturer on macOS: open the FX window, find it
// by title, screenshot that window only, and scale it down for the model.
type Capturer struct {
	App    string // the DAW's process name
	Bundle string // or its bundle id, resolved to the name when App is ""
	// Title is the start of a plugin window's title for a track, as the
	// DAW names it (REAPER: "FX: Track 3 "). nil = no known title: the
	// DAW's front window (a device shown inside the main window).
	Title func(track int) string
	Open  Opener
	Dir   string
	Width int           // output width in pixels; 0 = 1400
	Wait  time.Duration // for the plugin UI to draw; 0 = 700 ms
	OS    OS            // nil = the helper app (Remote)

	n int
}

func (c *Capturer) CaptureFX(ctx context.Context, track, fx int) (string, error) {
	if err := c.Open.OpenUI(ctx, track, fx, true); err != nil {
		return "", err
	}
	wait := c.Wait
	if wait == 0 {
		wait = 700 * time.Millisecond
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	o := c.OS
	if o == nil {
		o = Remote{}
	}
	app := named(&c.App, c.Bundle)
	id := 0
	// heavy plugin UIs take a moment to put their window on screen
	for tries := 0; id == 0 && tries < 12; tries++ {
		ws, err := o.Windows()
		if err != nil {
			return "", err
		}
		id = match(ws, app, c.Title, track)
		if id == 0 {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if id == 0 {
		return "", fmt.Errorf("no FX window for track %d on screen (is Muse Hands allowed to record the screen?)", track)
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return "", err
	}
	c.n++
	raw := filepath.Join(c.Dir, fmt.Sprintf("fx-%03d-raw.png", c.n))
	if err := o.Capture(ctx, id, raw); err != nil {
		return "", err
	}
	defer os.Remove(raw)
	dst := filepath.Join(c.Dir, fmt.Sprintf("fx-%d-%03d.png", time.Now().Unix(), c.n))
	return dst, shrink(raw, dst, c.Width)
}

// match finds the plugin window of track: by title prefix when the DAW
// names its plugin windows, else the DAW's frontmost real window.
func match(ws []Window, app string, title func(int) string, track int) int {
	if app == "" {
		return 0
	}
	if title == nil {
		if w, ok := frontOf(ws, app); ok {
			return w.ID
		}
		return 0
	}
	prefix := title(track)
	for _, w := range ws {
		if w.Owner == app && strings.HasPrefix(w.Name, prefix) {
			return w.ID
		}
	}
	return 0
}

// named returns *app, filling it from the bundle id once the DAW runs.
func named(app *string, bundle string) string {
	if *app == "" {
		*app = AppName(bundle)
	}
	return *app
}

func shrink(src, dst string, width int) error {
	if width == 0 {
		width = 1400
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		return err
	}
	b := img.Bounds()
	if b.Dx() > width {
		out := image.NewRGBA(image.Rect(0, 0, width, b.Dy()*width/b.Dx()))
		draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Src, nil)
		img = out
	}
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := png.Encode(w, img); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
