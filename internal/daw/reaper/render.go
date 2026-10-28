package reaper

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Renderer implements core.Renderer through the bridge: REAPER renders the
// first seconds of the project itself, faster than real time, on any OS.
// Each render gets its own file, so before/after never read one that was
// overwritten.
type Renderer struct {
	B     Bridge
	Dir   string
	n     int
	ready bool // REAPER answered once: the script is loaded for this session
}

func (rn *Renderer) Render(ctx context.Context, track int, seconds float64) (string, error) {
	return rn.render(ctx, track, 0, seconds, false)
}

func (rn *Renderer) RenderRange(ctx context.Context, track int, start, seconds float64) (string, error) {
	if math.IsNaN(start) || math.IsInf(start, 0) || start < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 122 {
		return "", fmt.Errorf("invalid recording render range")
	}
	return rn.render(ctx, track, start, seconds, true)
}

func (rn *Renderer) render(ctx context.Context, track int, start, seconds float64, ranged bool) (string, error) {
	if err := os.MkdirAll(rn.Dir, 0o755); err != nil {
		return "", err
	}
	rn.n++
	path, err := filepath.Abs(filepath.Join(rn.Dir, fmt.Sprintf("render-%s-%03d.wav", time.Now().Format("20060102-150405"), rn.n)))
	if err != nil {
		return "", err
	}
	op, arg := "render", path
	if ranged {
		op = "render_range"
		arg = strconv.FormatFloat(start, 'g', -1, 64) + "|" + path
	}
	if _, _, err := rn.B.call(ctx, op, track, arg, int(seconds*1000)); err != nil {
		return "", err
	}
	return path, nil
}

// Ready reports whether REAPER has loaded the bridge and answers it.
func (rn *Renderer) Ready(ctx context.Context) bool {
	if !rn.ready {
		_, err := rn.B.Ping(ctx)
		rn.ready = err == nil
	}
	return rn.ready
}
