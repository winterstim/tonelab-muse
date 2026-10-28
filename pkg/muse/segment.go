package muse

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"regexp"
	"strconv"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Segmenter implements core.Segmenter with SAM 3.1 on the Responses API:
// a text prompt and an image in, boxes (and masks) out as tags in the text,
// e.g. <|box;x1=150;y1=455;x2=395;y2=750;w=2240;h=1120|>.
type Segmenter struct {
	*Client
	Model string // "" = sam-3.1
}

var boxTag = regexp.MustCompile(`<\|box;x1=(\d+);y1=(\d+);x2=(\d+);y2=(\d+);w=(\d+);h=(\d+)\|>`)

func (s Segmenter) Segment(ctx context.Context, path, prompt string) ([]core.Box, error) {
	model := s.Model
	if model == "" {
		model = "sam-3.1"
	}
	img, err := dataURL(path)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(map[string]any{"model": model, "input": []any{map[string]any{"role": "user", "content": []any{
		map[string]string{"type": "input_text", "text": prompt},
		map[string]string{"type": "input_image", "image_url": img},
	}}}})
	raw, err := s.responses(ctx, b)
	if err != nil {
		return nil, err
	}
	var r struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("sam: bad JSON: %w", err)
	}
	w, h := imageSize(path)
	var out []core.Box
	for _, o := range r.Output {
		for _, c := range o.Content {
			for _, m := range boxTag.FindAllStringSubmatch(c.Text, -1) {
				n := make([]int, 6)
				for i := range n {
					n[i], _ = strconv.Atoi(m[i+1])
				}
				sx, sy := 1.0, 1.0 // boxes are in the w×h the model saw
				if n[4] > 0 && n[5] > 0 && w > 0 {
					sx, sy = float64(w)/float64(n[4]), float64(h)/float64(n[5])
				}
				out = append(out, core.Box{X1: int(float64(n[0]) * sx), Y1: int(float64(n[1]) * sy), X2: int(float64(n[2]) * sx), Y2: int(float64(n[3]) * sy)})
			}
		}
	}
	return out, nil
}

func imageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return c.Width, c.Height
}
