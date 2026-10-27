//go:build ignore

// gen writes the four test clips from the built-in riff:
//
//	go run pkg/ears/testdata/gen.go
package main

import (
	"math"

	"github.com/winterstim/tonelab-muse/pkg/riff"
)

// one-pole filters and a waveshaper: enough to make four distinct tones.
func lowpass(x []float64, hz float64) []float64 {
	a := math.Exp(-2 * math.Pi * hz / 48000)
	out, y := make([]float64, len(x)), 0.0
	for i, v := range x {
		y = (1-a)*v + a*y
		out[i] = y
	}
	return out
}

func drive(x []float64, g float64) []float64 {
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = math.Tanh(g*v) * 0.5
	}
	return out
}

func mix(a []float64, ga float64, b []float64, gb float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = ga*a[i] + gb*b[i]
	}
	return out
}

func main() {
	di := riff.Generate(48000, 10)
	dir := "pkg/ears/testdata/"
	riff.WriteWAV(dir+"clean.wav", di, 48000)
	riff.WriteWAV(dir+"crunch.wav", lowpass(drive(di, 4), 5000), 48000)
	riff.WriteWAV(dir+"distortion.wav", drive(mix(di, 1, lowpass(di, 400), -0.8), 40), 48000)
	riff.WriteWAV(dir+"mudd.wav", mix(di, 0.4, lowpass(di, 300), 2.2), 48000)
}
