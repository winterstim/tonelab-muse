package ears

import (
	"errors"
	"math"
	"sort"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// Third-octave-ish bands from 60 Hz to 10 kHz: fine enough to say "cut
// around 250 Hz", coarse enough to be stable on a few seconds of audio.
func mixBands() [][2]float64 {
	var out [][2]float64
	step := math.Pow(2, 1.0/3)
	for lo := 60.0; lo < 10000; lo *= step {
		out = append(out, [2]float64{lo, math.Min(lo*step, 10000)})
	}
	return out
}

// bandLevels returns absolute band levels in dBFS (not shares: here the
// two signals' real loudness decides who masks whom).
func bandLevels(a Audio, bands [][2]float64) []float64 {
	psd := welch(a.Samples)
	binHz := float64(a.Rate) / segment
	out := make([]float64, len(bands))
	for b, e := range bands {
		var s float64
		for i, p := range psd {
			if f := float64(i) * binHz; f >= e[0] && f < e[1] {
				s += p
			}
		}
		out[b] = 10 * math.Log10(s+1e-20)
	}
	return out
}

// Clashes implements core.MixAnalyzer. A band clashes when both signals are
// within 25 dB of their own loudest band (they both matter there) and
// within 10 dB of each other (neither clearly wins). The score grows as
// both get stronger and closer.
func (Meter) Clashes(trackWAV, restWAV string) ([]core.Clash, error) {
	t, err := ReadWAV(trackWAV)
	if err != nil {
		return nil, err
	}
	r, err := ReadWAV(restWAV)
	if err != nil {
		return nil, err
	}
	if len(t.Samples) < segment || len(r.Samples) < segment {
		return nil, errors.New("audio shorter than one analysis segment")
	}
	bands := mixBands()
	tl, rl := bandLevels(t, bands), bandLevels(r, bands)
	tmax, rmax := maxOf(tl), maxOf(rl)
	var out []core.Clash
	for i, e := range bands {
		tRel, rRel := tl[i]-tmax, rl[i]-rmax
		gap := math.Abs(tl[i] - rl[i])
		if tRel < -25 || rRel < -25 || gap > 10 {
			continue
		}
		score := (25 + math.Min(tRel, rRel)) * (10 - gap) / 25
		out = append(out, core.Clash{
			Lo: math.Round(e[0]), Hi: math.Round(e[1]), Center: math.Round(math.Sqrt(e[0] * e[1])),
			TrackDB: math.Round(tl[i]*10) / 10, RestDB: math.Round(rl[i]*10) / 10,
			Score: math.Round(score*10) / 10,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 4 {
		out = out[:4]
	}
	return out, nil
}

func maxOf(xs []float64) float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}
