package ears

import (
	"errors"
	"math"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

const segment = 4096 // Welch segment, 50 % overlap

// Band edges in Hz, in core.Bands field order.
var bandEdges = [6][2]float64{{60, 150}, {150, 400}, {400, 1000}, {1000, 3000}, {3000, 6000}, {6000, 10000}}

// Meter implements core.Measurer.
type Meter struct{}

func (Meter) Measure(wavPath string) (core.Measure, error) {
	a, err := ReadWAV(wavPath)
	if err != nil {
		return core.Measure{}, err
	}
	m, err := Analyze(a)
	m.WAV = wavPath
	return m, err
}

// Analyze measures loudness-matched band shares (dB of total power),
// spectral centroid and crest factor.
func Analyze(a Audio) (core.Measure, error) {
	x := a.Samples
	if len(x) < segment {
		return core.Measure{}, errors.New("audio shorter than one analysis segment")
	}
	var sq float64
	for _, v := range x {
		sq += v * v
	}
	rms := math.Sqrt(sq / float64(len(x)))
	var peak0 float64
	for _, v := range x {
		peak0 = math.Max(peak0, math.Abs(v))
	}
	if rms == 0 || 20*math.Log10(peak0) < -60 { // dither and denormals are not sound
		return core.Measure{}, core.ErrSilent
	}
	psd := welch(x)
	binHz := float64(a.Rate) / segment

	var total, weighted float64
	for i, p := range psd {
		total += p
		weighted += float64(i) * binHz * p
	}
	var bands [6]float64
	for b, e := range bandEdges {
		var s float64
		for i, p := range psd {
			if f := float64(i) * binHz; f >= e[0] && f < e[1] {
				s += p
			}
		}
		bands[b] = 10 * math.Log10(s/total+1e-20)
	}
	var peak float64
	for _, v := range x {
		peak = math.Max(peak, math.Abs(v))
	}
	return core.Measure{
		Bands:    core.Bands{Low: bands[0], Mud: bands[1], Mid: bands[2], Upper: bands[3], Presence: bands[4], Air: bands[5]},
		Centroid: weighted / total,
		Crest:    20 * math.Log10(peak/rms),
		Level:    math.Round(20*math.Log10(rms)*10) / 10,
	}, nil
}

// welch averages Hann-windowed periodograms over 50 %-overlapping segments,
// removing the mean per segment as scipy's default detrend does.
func welch(x []float64) []float64 {
	win := hann(segment)
	buf := make([]complex128, segment)
	frame := make([]float64, segment)
	acc := make([]float64, segment/2+1)
	n := 0
	for start := 0; start+segment <= len(x); start += segment / 2 {
		var m float64
		for _, v := range x[start : start+segment] {
			m += v
		}
		m /= segment
		for i := range frame {
			frame[i] = x[start+i] - m
		}
		for i, p := range power(frame, win, buf) {
			acc[i] += p
		}
		n++
	}
	for i := range acc {
		acc[i] /= float64(n)
		if i > 0 && i < segment/2 {
			acc[i] *= 2 // one-sided
		}
	}
	return acc
}
