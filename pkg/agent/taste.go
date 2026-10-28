package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// The agent learns the user's taste from what they do after it, not from
// what they say: an Undo says "not that direction", and moving a control
// right after a plan says "a bit more this way". Only the control, the
// direction and a count are kept.

const (
	tweakMin   = 0.03 // smaller moves are noise or rounding
	maxLeaning = 5    // how many go into the prompt
	habitMin   = 2    // one undo is a moment, two are a habit
	habitAge   = 30 * 24 * time.Hour
)

// lean adds one observation.
func (a *Agent) lean(control string, dir int) {
	if a.d.Taste == nil || dir == 0 {
		return
	}
	t, err := a.d.Taste.Taste()
	if err != nil {
		return
	}
	found := false
	for i := range t.Leanings {
		l := &t.Leanings[i]
		if l.Control == control && l.Dir == dir {
			l.Count++
			l.Last, found = time.Now(), true
		}
	}
	if !found {
		t.Leanings = append(t.Leanings, core.Leaning{Control: control, Dir: dir, Count: 1, Last: time.Now()})
	}
	a.d.Taste.SaveTaste(t)
	a.emit("taste", map[string]any{"control": control, "dir": dir})
}

// tasteFromUndo: the user undid a plan, so each control it moved leans
// the other way.
func (a *Agent) tasteFromUndo(h applied, names map[core.ParamID]string) {
	for _, c := range h.Plan.Changes {
		prev, ok := h.Previous[c.Param]
		if !ok || math.Abs(c.Value-prev) < tweakMin {
			continue
		}
		a.lean(names[c.Param], -sign(c.Value-prev))
	}
}

// tasteFromTweaks runs at the start of a request: controls of the last
// plan that are no longer where the agent left them were moved by the user.
func (a *Agent) tasteFromTweaks(ctx context.Context) {
	a.mu.Lock()
	if len(a.history) == 0 || a.d.Taste == nil {
		a.mu.Unlock()
		return
	}
	h := a.history[len(a.history)-1]
	checked := h.checked
	a.history[len(a.history)-1].checked = true
	a.mu.Unlock()
	if checked {
		return
	}
	ch, err := a.d.DAW.FXChain(ctx, h.Track)
	if err != nil {
		return
	}
	for _, c := range h.Plan.Changes {
		for _, fx := range ch {
			for _, p := range fx.Params {
				if p.ID == c.Param && math.Abs(p.Value-c.Value) >= tweakMin {
					a.lean(fx.Name+" / "+p.Name, sign(p.Value-c.Value))
				}
			}
		}
	}
}

// tastePrompt is the leanings worth telling the model, strongest first.
func (a *Agent) tastePrompt() string {
	if a.d.Taste == nil {
		return ""
	}
	t, err := a.d.Taste.Taste()
	if err != nil || len(t.Leanings) == 0 {
		return ""
	}
	var ls []core.Leaning
	for _, l := range t.Leanings {
		if l.Count >= habitMin && time.Since(l.Last) < habitAge {
			ls = append(ls, l)
		}
	}
	if len(ls) == 0 {
		return ""
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Count != ls[j].Count {
			return ls[i].Count > ls[j].Count
		}
		return ls[i].Last.After(ls[j].Last)
	})
	var lines []string
	for _, l := range ls {
		if len(lines) == maxLeaning {
			break
		}
		way := "up"
		if l.Dir < 0 {
			way = "down"
		}
		lines = append(lines, fmt.Sprintf("- after your plans the user moved %s %s (%d×)", l.Control, way, l.Count))
	}
	return "\n\nThis user's habits after your plans (learned from their undos and tweaks):\n" + strings.Join(lines, "\n") + `
How to use them: sound design comes first and the request always wins. Use a habit only to choose between options that are equally right for the request, and as a small nudge (about 1-2 dB, or 5% of a control's range) on top of the chosen sound. Never follow a habit into something that hurts the sound (mud piling up around 200-400 Hz, harsh 2-5 kHz, drive into fizz, clipping); if a habit and good practice disagree, follow good practice and say so in one short sentence.`
}

func sign(x float64) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}
