package eyes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Transport starts and stops a DAW's playback.
type Transport interface {
	Transport(ctx context.Context, play bool) error
}

// AppRenderer implements core.Renderer for any DAW that can play and stop:
// it plays from the start and records the DAW's own audio output. So
// listening needs no export API, and nothing else on the Mac is heard.
type AppRenderer struct {
	T      Transport
	OS     OS     // nil = the helper app
	Bundle string // the DAW's bundle id, e.g. com.ableton.live
	Dir    string
	n      int
}

func (r *AppRenderer) Render(ctx context.Context, track int, seconds float64) (string, error) {
	o := r.OS
	if o == nil {
		o = defaultOS()
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return "", err
	}
	r.n++
	path := filepath.Join(r.Dir, fmt.Sprintf("capture-%s-%03d.wav", time.Now().Format("20060102-150405"), r.n))
	// stopping twice returns to the start in most DAWs, so every capture
	// covers the same bars and before/after compare like for like
	r.T.Transport(ctx, false)
	r.T.Transport(ctx, false)
	if err := r.T.Transport(ctx, true); err != nil {
		return "", fmt.Errorf("play: %w", err)
	}
	time.Sleep(150 * time.Millisecond) // past the first buffer
	err := o.Record(ctx, r.Bundle, seconds, path)
	r.T.Transport(context.WithoutCancel(ctx), false)
	if err != nil {
		return "", err
	}
	return path, nil
}
