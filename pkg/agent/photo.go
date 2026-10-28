package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// A photo of the person's real amp or pedal: SAM finds every knob and
// switch, each is cut out with its printed label and scale, and Muse reads
// label and position from the close-ups. The planner then dials the same
// settings into the plugins on the track.

const maxControls = 16

const photoReader = `You read the controls on a photo of guitar gear (an amp, a pedal).
You get the whole photo, then close-ups numbered 1..N, one per control, each with its printed label and scale around it.
For each close-up: the label as printed, and where the pointer points on its printed scale. Knobs: read the number the pointer line or dot aims at, interpolating between marks (e.g. 6.5); if the scale is unmarked, give the position as a clock time ("2 o'clock") and as a fraction 0..1 of the sweep from fully left (7 o'clock) to fully right (5 o'clock). Switches: the printed position it is in.
Answer JSON only:
{"gear": "<what this gear is, make and model if printed or recognisable, else a description>",
 "controls": [{"n": 1, "label": "GAIN", "kind": "knob|switch", "value": 6.5, "min": 0, "max": 10, "fraction": 0.65, "position": "<switch position or clock time>", "sure": true}]}
Set "sure": false when the pointer or label is hard to see. Never invent a control that is not in a close-up.`

// SetPhoto makes a photo of the person's gear the one read_photo reads.
func (a *Agent) SetPhoto(path string) {
	a.mu.Lock()
	a.photo = path
	a.mu.Unlock()
	a.emit("photo_added", map[string]any{"photo": filepath.Base(path)})
}

type reading struct {
	Gear     string `json:"gear"`
	Controls []struct {
		N        int     `json:"n"`
		Label    string  `json:"label"`
		Kind     string  `json:"kind"`
		Value    float64 `json:"value"`
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
		Fraction float64 `json:"fraction"`
		Position string  `json:"position,omitempty"`
		Sure     bool    `json:"sure"`
		Crop     string  `json:"crop,omitempty"`
	} `json:"controls"`
}

func (a *Agent) readPhoto(ctx context.Context) (any, []core.Part, error) {
	a.mu.Lock()
	photo := a.photo
	a.mu.Unlock()
	if photo == "" {
		return nil, nil, errors.New("no photo yet: the person adds one with the camera button")
	}
	if a.d.Segmenter == nil {
		return nil, nil, errors.New("reading photos is not set up")
	}
	var boxes []core.Box
	for _, prompt := range []string{"knob", "toggle switch"} {
		bs, err := a.d.Segmenter.Segment(ctx, photo, prompt)
		if err != nil {
			return nil, nil, fmt.Errorf("finding the controls: %w", err)
		}
		boxes = append(boxes, bs...)
	}
	boxes = dedupe(boxes)
	if len(boxes) == 0 {
		return map[string]any{"controls": []any{}, "note": "No knobs or switches found in the photo. Ask for a closer, straight-on photo."}, nil, nil
	}
	sort.Slice(boxes, func(i, j int) bool { // reading order: rows top to bottom, then left to right
		bi, bj := boxes[i], boxes[j]
		if abs(bi.Y1-bj.Y1) > (bi.Y2-bi.Y1)/2 {
			return bi.Y1 < bj.Y1
		}
		return bi.X1 < bj.X1
	})
	if len(boxes) > maxControls {
		boxes = boxes[:maxControls]
	}
	src, err := decodeImage(photo)
	if err != nil {
		return nil, nil, err
	}
	whole, err := imagePartScaled(src, 1024)
	if err != nil {
		return nil, nil, err
	}
	parts := []core.Part{{Type: "text", Text: "The whole photo:"}, whole}
	var crops []string
	for i, b := range boxes {
		path := strings.TrimSuffix(photo, filepath.Ext(photo)) + fmt.Sprintf("-control-%02d.png", i+1)
		if err := saveCrop(src, around(b, src.Bounds()), path); err != nil {
			return nil, nil, err
		}
		crops = append(crops, path)
		p, err := imagePart(path)
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, core.Part{Type: "text", Text: fmt.Sprintf("Close-up %d:", i+1)}, p)
	}
	llm, model := a.eyes()
	r, err := llm.Chat(ctx, model, []core.Msg{core.Text("system", photoReader), {Role: "user", Parts: parts}}, nil)
	if err != nil {
		return nil, nil, err
	}
	a.emit("usage", r.Usage)
	var rd reading
	if err := json.Unmarshal([]byte(jsonObject(r.Text)), &rd); err != nil {
		return nil, nil, fmt.Errorf("unreadable answer: %q", r.Text)
	}
	for i := range rd.Controls {
		if n := rd.Controls[i].N; n >= 1 && n <= len(crops) {
			rd.Controls[i].Crop = crops[n-1]
		}
	}
	out := map[string]any{"photo": photo, "gear": rd.Gear, "controls": rd.Controls,
		"note": "Dial these into the track's plugins in one propose_tone: match each control by meaning (GAIN/DRIVE to the drive or preamp gain, BASS/MIDDLE/TREBLE/PRESENCE to the amp's or EQ's, MASTER/VOLUME to output only if asked). If the plugin shows the same 0-10 scale, use the value as the target; otherwise use the fraction as the 0..1 value. Leave out controls marked not sure or with no counterpart, and say which."}
	a.emit("photo", out)
	return out, nil, nil
}

// around grows a control's box to take in its label above and the scale
// printed around it.
func around(b core.Box, r image.Rectangle) image.Rectangle {
	w, h := b.X2-b.X1, b.Y2-b.Y1
	out := image.Rect(b.X1-w*45/100, b.Y1-h*75/100, b.X2+w*45/100, b.Y2+h*75/100) // labels sit above on amps, below on pedals
	return out.Intersect(r)
}

// dedupe drops boxes that mostly cover another (a knob found as both a
// knob and a switch).
func dedupe(bs []core.Box) []core.Box {
	var out []core.Box
	for _, b := range bs {
		dup := false
		for _, o := range out {
			if overlap(b, o) > 0.6 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}

func overlap(a, b core.Box) float64 {
	ix := max(0, min(a.X2, b.X2)-max(a.X1, b.X1))
	iy := max(0, min(a.Y2, b.Y2)-max(a.Y1, b.Y1))
	small := min((a.X2-a.X1)*(a.Y2-a.Y1), (b.X2-b.X1)*(b.Y2-b.Y1))
	if small <= 0 {
		return 0
	}
	return float64(ix*iy) / float64(small)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func saveCrop(src image.Image, r image.Rectangle, path string) error {
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, dst)
}

// imagePartScaled sends a large photo at most width pixels wide.
func imagePartScaled(src image.Image, width int) (core.Part, error) {
	b := src.Bounds()
	if b.Dx() > width {
		h := b.Dy() * width / b.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, width, h))
		for y := 0; y < h; y++ {
			for x := 0; x < width; x++ {
				dst.Set(x, y, src.At(b.Min.X+x*b.Dx()/width, b.Min.Y+y*b.Dy()/h))
			}
		}
		src = dst
	}
	f, err := os.CreateTemp("", "photo-*.png")
	if err != nil {
		return core.Part{}, err
	}
	defer os.Remove(f.Name())
	if err := png.Encode(f, src); err != nil {
		f.Close()
		return core.Part{}, err
	}
	f.Close()
	return imagePart(f.Name())
}
