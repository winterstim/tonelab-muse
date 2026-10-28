package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// A reference is a recording whose tone the user wants. Band shares are
// relative to total power, so a quieter or louder reference compares
// fairly; what is left is the tonal gap the plan has to close.

// SetReference makes a WAV the session's reference.
func (a *Agent) SetReference(path string) (any, error) {
	if a.d.Measurer == nil {
		return nil, errors.New("measuring is not set up")
	}
	m, err := a.d.Measurer.Measure(path)
	if err != nil {
		return nil, fmt.Errorf("reference: %w", err)
	}
	a.mu.Lock()
	a.reference, a.refMeasure = path, m
	a.mu.Unlock()
	out := map[string]any{"reference": filepath.Base(path), "measured": m}
	a.emit("reference", out)
	return out, nil
}

// matchReference renders the track and reports how far each band is from
// the reference, with both spectrograms for Muse to look at.
func (a *Agent) matchReference(ctx context.Context, track int) (any, []core.Part, error) {
	a.mu.Lock()
	ref, rm := a.reference, a.refMeasure
	a.mu.Unlock()
	if ref == "" {
		return nil, nil, errors.New("no reference yet: the user adds one with the paperclip in the composer")
	}
	if a.d.Renderer == nil || a.d.Visualizer == nil {
		return nil, nil, errors.New("rendering is not set up")
	}
	wav, err := a.renderAlone(ctx, track)
	if err != nil {
		return nil, nil, fmt.Errorf("render: %w", err)
	}
	m, err := a.d.Measurer.Measure(wav)
	if errors.Is(err, core.ErrSilent) {
		out, err := a.diagnoseSilence(ctx, track)
		return out, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	if a.lastWAV == "" {
		a.lastWAV = wav // also the baseline for the first listen
	}
	a.mu.Unlock()
	gap := diffBands(m.Bands, rm.Bands) // + means the reference has more
	png, err := a.d.Visualizer.Spectrogram([]string{ref, wav}, []string{"Reference", "Now"})
	if err != nil {
		return nil, nil, err
	}
	dist := rmsBands(m.Bands, rm.Bands)
	a.mu.Lock()
	a.rounds = append(a.rounds, map[string]any{
		"round":       len(a.rounds) + 1,
		"distance_db": dist,
		"label":       fmt.Sprintf("Round %d", len(a.rounds)+1),
	})
	roundsCopy := make([]map[string]any, len(a.rounds))
	copy(roundsCopy, a.rounds)
	a.mu.Unlock()
	out := map[string]any{
		"gap_db":                gap,
		"distance_db":           dist,
		"convergence":           roundsCopy,
		"biggest":               biggestGap(gap),
		"centroid_now_hz":       math.Round(m.Centroid),
		"centroid_reference_hz": math.Round(rm.Centroid),
		"spectrogram":           png,
		"render":                wav,
	}
	a.emit("reference_gap", out)
	a.emit("reference", out)
	img, err := imagePart(png)
	if err != nil {
		return nil, nil, err
	}
	note := core.Part{Type: "text", Text: "Spectrograms from match_reference, top to bottom: Reference, Now. gap_db is reference minus now per band (tone only, loudness removed): positive = add, negative = cut."}
	return out, []core.Part{note, img}, nil
}

// biggestGap names the bands furthest off, largest first, for the model
// and the UI to lead with.
func biggestGap(g map[string]float64) string {
	type kv struct {
		k string
		v float64
	}
	var xs []kv
	for k, v := range g {
		if math.Abs(v) >= 1 {
			xs = append(xs, kv{k, v})
		}
	}
	for i := range xs { // tiny insertion sort by |gap|
		for j := i; j > 0 && math.Abs(xs[j].v) > math.Abs(xs[j-1].v); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	if len(xs) == 0 {
		return "already within 1 dB in every band"
	}
	var parts []string
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%s %+.1f dB", x.k, x.v))
	}
	return strings.Join(parts, ", ")
}

// MatchReference is match_reference for callers outside the tool loop.
func (a *Agent) MatchReference(ctx context.Context, track int) (any, error) {
	out, _, err := a.matchReference(ctx, track)
	return out, err
}
