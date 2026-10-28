package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Models cannot invert a plugin's curve ("-3 dB" is not 0.4375 by any rule
// they know), so a named number is found the way an engineer would: turn
// the knob and read the display. Bisection over the DAW's own readout,
// then the knob goes back where it was; the plan applies the value later.

// A unit that scales the number: kHz, and times, which plugins show in ms
// low on the knob and in s high on it. Times are read in seconds.
var number = regexp.MustCompile(`[-+]?(\d+\.?\d*|\.\d+)\s*(k|ms\b|s\b|sec)?`)

// readout pulls the number out of a display string; "-inf" counts as very
// low and "inf" (a ratio's "inf : 1") as very high.
func readout(s string) (float64, bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(t, "-inf") {
		return -1e9, true
	}
	if strings.HasPrefix(t, "inf") || strings.HasPrefix(t, "+inf") {
		return 1e9, true
	}
	m := number.FindStringSubmatch(t)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimRight(m[0], " kmsec"), 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "k":
		f *= 1000
	case "ms":
		f /= 1000
	}
	return f, true
}

// stepOf is the smallest change a display can show: 1 in its last digit
// ("7" → 1, "-3.0 dB" → 0.1).
func stepOf(text string) float64 {
	m := number.FindString(strings.ToLower(text))
	if i := strings.IndexByte(m, '.'); i >= 0 {
		digits := 0
		for _, c := range m[i+1:] {
			if c < '0' || c > '9' {
				break
			}
			digits++
		}
		return math.Pow(10, -float64(digits))
	}
	return 1
}

// timed reports whether a display or target names a time unit.
func timed(s string) bool {
	m := number.FindStringSubmatch(strings.ToLower(s))
	return m != nil && (m[2] == "ms" || m[2] == "s" || m[2] == "sec")
}

// raw is the number as written, unit ignored ("100 ms" → 100).
func raw(s string) float64 {
	m := number.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.TrimRight(m[0], " kmsec"), 64)
	return f
}

const findSteps = 12 // 1/4096 of the range: finer than any knob

func (a *Agent) findValue(ctx context.Context, track int, id core.ParamID, target string) (float64, error) {
	ch, err := a.chain(ctx, track, false)
	if err != nil {
		return 0, err
	}
	p, ok := find(ch, id)
	if !ok {
		return 0, fmt.Errorf("%w: %s", core.ErrUnknownParam, id)
	}
	want, ok := readout(target)
	if !ok || p.Kind != "" {
		return a.findChoice(ctx, track, p, target)
	}
	if a.currentMode() == Preview {
		return 0, fmt.Errorf("review mode cannot search an unlearned numeric readout for %s", p.Name)
	}
	if err := a.probePolicy(); err != nil {
		return 0, err
	}
	if vf, ok := a.d.DAW.(core.ValueFinder); ok {
		if v, err := vf.FindValue(ctx, track, id, target); err == nil {
			return v, nil
		}
	}
	_, _ = a.d.DAW.SetTrack(ctx, track, "mute", 1)
	defer a.d.DAW.SetTrack(context.WithoutCancel(ctx), track, "mute", 0)
	var shown []string // display texts seen
	at := func(v float64) (float64, error) {
		if _, err := a.d.DAW.SetParam(ctx, track, id, v); err != nil {
			return 0, err
		}
		text, err := a.text(ctx, track, id)
		if err != nil {
			return 0, err
		}
		shown = append(shown, text)
		f, ok := readout(text)
		if !ok {
			return 0, fmt.Errorf("unreadable display %q", text)
		}
		return f, nil
	}
	defer a.d.DAW.SetParam(ctx, track, id, p.Value)
	lo, hi := 0.0, 1.0
	flo, err := at(lo)
	if err != nil {
		return 0, err
	}
	fhi, err := at(hi)
	if err != nil {
		return 0, err
	}
	if timed(target) && !timed(shown[0]) && !timed(shown[1]) {
		want = raw(target) // a bare-number time display reads in the units the person wrote
	}
	if math.Min(flo, fhi) > want || math.Max(flo, fhi) < want {
		return 0, fmt.Errorf("out of range: %s goes from %g to %g", p.Name, flo, fhi)
	}
	// Illinois false position on display(x) - want: a linear knob lands in
	// two or three reads, a log one in a handful; it stops as soon as the
	// display shows the target (within half a percent).
	tol := math.Max(0.005*math.Abs(want), 1e-4*math.Abs(fhi-flo))
	// a positive range spanning decades (frequency, time) is searched on a
	// log scale, where such knobs are close to linear
	scale := func(f float64) float64 { return f }
	if math.Min(flo, fhi) > 0 && want > 0 && math.Max(flo, fhi)/math.Min(flo, fhi) > 10 && math.Max(flo, fhi) < 1e8 {
		scale = math.Log
	}
	glo, ghi := scale(flo)-scale(want), scale(fhi)-scale(want)
	best, bestErr := lo, math.Abs(flo-want)
	if math.Abs(fhi-want) < bestErr {
		best, bestErr = hi, math.Abs(fhi-want)
	}
	side := 0
	for i := 0; i < findSteps && bestErr > tol; i++ {
		x := (lo + hi) / 2
		if math.Abs(glo) < 1e8 && math.Abs(ghi) < 1e8 && glo != ghi { // -inf/inf ends: bisect
			x = lo - glo*(hi-lo)/(ghi-glo)
			m := 0.01 * (hi - lo)
			x = math.Min(math.Max(x, lo+m), hi-m)
		}
		f, err := at(x)
		if err != nil {
			return 0, err
		}
		if math.Abs(f-want) < bestErr {
			best, bestErr = x, math.Abs(f-want)
		}
		g := f - want
		if math.Min(flo, fhi) > 0 {
			g = scale(math.Max(f, 1e-12)) - scale(want)
		}
		if (g < 0) == (glo < 0) {
			lo, glo = x, g
			if side == -1 {
				ghi /= 2
			}
			side = -1
		} else {
			hi, ghi = x, g
			if side == 1 {
				glo /= 2
			}
			side = 1
		}
	}
	// a stepped display ("7" from 0.70 to 0.80) shows the target exactly
	// over a whole stretch: walk to its edge, where the knob reads it first
	if bestErr == 0 && stepOf(shown[len(shown)-1]) > 0.005*math.Abs(fhi-flo) {
		edge := lo
		if glo >= 0 {
			edge = hi
		}
		for k := 0; k < 3 && math.Abs(best-edge) > 0.01; k++ {
			m := (edge + best) / 2
			f, err := at(m)
			if err != nil {
				return 0, err
			}
			if f == want {
				best = m
			} else {
				edge = m
			}
		}
	}
	// the knob goes back where it was (deferred above), so the cached chain
	// still holds
	return math.Round(best*10000) / 10000, nil
}

// findChoice lands a switch or a list control on a named choice ("On",
// "Low Shelf", "Marshall JCM800"): switches by word, lists by stepping
// through and reading the display.
func (a *Agent) findChoice(ctx context.Context, track int, p core.Param, target string) (value float64, retErr error) {
	ch, err := a.chain(ctx, track, false)
	if err != nil {
		return 0, err
	}
	fx, ok := fxForParam(ch, p.ID)
	if !ok {
		return 0, fmt.Errorf("%w: %s", core.ErrUnknownParam, p.ID)
	}
	var profile *core.Profile
	if a.d.Store != nil {
		profile, _ = a.d.Store.Profile(fx.Name)
	}
	_, paramIndex, _ := p.ID.Parse()
	entry := core.ProfileEntry{}
	if profile != nil {
		entry = profile.Map[paramIndex]
	}
	match := func(s string) bool { return choiceMatch(s, target) }
	if a.currentMode() == Preview {
		for _, c := range entry.Choices {
			if match(c.Text) && c.Value >= 0 && c.Value <= 1 && !math.IsNaN(c.Value) && !math.IsInf(c.Value, 0) {
				return c.Value, nil
			}
		}
		return 0, fmt.Errorf("choice %q is not in the learned profile for %s; review mode does not probe", target, p.Name)
	}
	if err := a.probePolicy(); err != nil {
		return 0, err
	}
	original := p.Value
	for _, cached := range entry.Choices {
		if !match(cached.Text) || cached.Value < 0 || cached.Value > 1 || math.IsNaN(cached.Value) || math.IsInf(cached.Value, 0) {
			continue
		}
		applied, writeErr := a.d.DAW.SetParam(ctx, track, p.ID, cached.Value)
		if writeErr != nil {
			return 0, errors.Join(fmt.Errorf("cached choice probe for %s failed: %w", p.ID, writeErr), restoreProbeValue(ctx, a.d.DAW, track, p.ID, original))
		}
		if applied.Status != core.StatusConfirmed || math.IsNaN(applied.Confirmed) || math.IsInf(applied.Confirmed, 0) || applied.Confirmed < 0 || applied.Confirmed > 1 {
			return 0, errors.Join(fmt.Errorf("cached choice probe for %s was not confirmed (status %q)", p.ID, applied.Status), restoreProbeValue(ctx, a.d.DAW, track, p.ID, original))
		}
		shown, readErr := a.text(ctx, track, p.ID)
		if readErr != nil {
			return 0, errors.Join(fmt.Errorf("cached choice readback for %s failed: %w", p.ID, readErr), restoreProbeValue(ctx, a.d.DAW, track, p.ID, original))
		}
		restoreErr := restoreProbeValue(ctx, a.d.DAW, track, p.ID, original)
		if restoreErr != nil {
			return 0, fmt.Errorf("cached choice check could not restore %s: %w", p.ID, restoreErr)
		}
		if match(shown) {
			return applied.Confirmed, nil
		}
		break // a valid but stale mapping: relearn with a bounded sweep
	}
	const steps = 256
	restored := false
	restore := func() error {
		if restored {
			return nil
		}
		restored = true
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		got, restoreErr := a.d.DAW.SetParam(cleanup, track, p.ID, original)
		if restoreErr == nil && (got.Status != core.StatusConfirmed || math.IsNaN(got.Confirmed) || math.IsInf(got.Confirmed, 0) || math.Abs(got.Confirmed-original) > 0.0001) {
			restoreErr = fmt.Errorf("restore was not confirmed at original value %.4f (status %q, readback %.4f)", original, got.Status, got.Confirmed)
		}
		return restoreErr
	}
	defer func() {
		if err := restore(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("could not restore %s after choice probe: %w", p.ID, err))
		}
		a.mu.Lock()
		delete(a.chains, track)
		a.mu.Unlock()
	}()
	choices := make([]core.ParamChoice, 0, 8)
	seen := map[string]bool{}
	var matched *float64
	for i := 0; i <= steps; i++ {
		v := float64(i) / steps
		applied, err := a.d.DAW.SetParam(ctx, track, p.ID, v)
		if err != nil {
			return 0, err
		}
		if applied.Status != core.StatusConfirmed || math.IsNaN(applied.Confirmed) || math.IsInf(applied.Confirmed, 0) || applied.Confirmed < 0 || applied.Confirmed > 1 {
			return 0, fmt.Errorf("probe write for %s was not confirmed (status %q)", p.ID, applied.Status)
		}
		text, err := a.text(ctx, track, p.ID)
		if err != nil {
			return 0, err
		}
		key := strings.ToLower(strings.TrimSpace(text))
		if key == "" {
			continue
		}
		if !seen[key] {
			choices = append(choices, core.ParamChoice{Text: text, Value: applied.Confirmed})
			seen[key] = true
		}
		if matched == nil && match(text) {
			found := applied.Confirmed
			matched = &found
		}
	}
	if err := restore(); err != nil {
		return 0, fmt.Errorf("could not restore %s after choice probe: %w", p.ID, err)
	}
	if profile == nil {
		profile = &core.Profile{Plugin: fx.Name, Map: map[int]core.ProfileEntry{}}
	}
	if profile.Map == nil {
		profile.Map = map[int]core.ProfileEntry{}
	}
	entry.Choices = choices
	entry.ChoicesPartial = true // finite normalized sampling cannot prove narrower unseen plateaus do not exist
	profile.Map[paramIndex] = entry
	if a.d.Store != nil {
		_ = a.d.Store.SaveProfile(*profile)
	}
	if matched != nil {
		return *matched, nil
	}
	seenText := make([]string, 0, len(choices))
	for _, c := range choices {
		seenText = append(seenText, c.Text)
	}
	return 0, fmt.Errorf("no choice matching %q; the choices are: %s", target, strings.Join(seenText, ", "))
}

func restoreProbeValue(ctx context.Context, daw core.DAW, track int, id core.ParamID, original float64) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	got, err := daw.SetParam(cleanup, track, id, original)
	if err != nil {
		return fmt.Errorf("restore %s: %w", id, err)
	}
	if got.Status != core.StatusConfirmed || math.IsNaN(got.Confirmed) || math.IsInf(got.Confirmed, 0) || math.Abs(got.Confirmed-original) > 0.0001 {
		return fmt.Errorf("restore %s was not confirmed at original value %.4f (status %q, readback %.4f)", id, original, got.Status, got.Confirmed)
	}
	return nil
}

func choiceMatch(readout, target string) bool {
	r, t := strings.ToLower(strings.TrimSpace(readout)), strings.ToLower(strings.TrimSpace(target))
	if r == t || strings.Contains(r, t) {
		return true
	}
	trueWords := map[string]bool{"on": true, "yes": true, "enabled": true, "active": true, "true": true}
	falseWords := map[string]bool{"off": true, "no": true, "disabled": true, "bypassed": true, "false": true}
	if trueWords[t] && trueWords[r] {
		return true
	}
	return falseWords[t] && falseWords[r]
}

func targetReadoutMatches(shown, target string) bool {
	want, wok := readout(target)
	got, gok := readout(shown)
	if wok && gok {
		if timed(target) && !timed(shown) {
			want = raw(target)
		}
		if timed(shown) && !timed(target) {
			got = raw(shown)
		}
		tol := math.Max(0.02*math.Abs(want), 0.05)
		return math.Abs(got-want) <= tol
	}
	return choiceMatch(shown, target)
}

func fxForParam(ch []core.FX, id core.ParamID) (core.FX, bool) {
	fx, _, err := id.Parse()
	if err != nil {
		return core.FX{}, false
	}
	for _, f := range ch {
		if f.Index == fx {
			return f, true
		}
	}
	return core.FX{}, false
}

// text reads one control's display, cheaply when the DAW allows.
func (a *Agent) text(ctx context.Context, track int, id core.ParamID) (string, error) {
	if pr, ok := a.d.DAW.(core.ParamReader); ok {
		return pr.ParamText(ctx, track, id)
	}
	ch, err := a.d.DAW.FXChain(ctx, track)
	if err != nil {
		return "", err
	}
	q, _ := find(ch, id)
	return q.Text, nil
}
