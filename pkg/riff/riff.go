// Package riff plays an original guitar riff with a plucked-string model
// (Karplus–Strong), as a clean DI signal: something to dial tone on before
// the user has recorded anything, and test audio nobody else owns.
package riff

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
)

// note is one event: strings (Hz) struck together at beat, held for len
// beats; muted is a palm mute.
type note struct {
	beat, len float64
	hz        []float64
	muted     bool
}

const e2, g2, a2, b2, d3, e3 = 82.41, 98.00, 110.00, 123.47, 146.83, 164.81

// five is a power chord: root, fifth, octave.
func five(root float64) []float64 { return []float64{root, root * 1.4983, root * 2} }

// Two bars in E: chugs on the low string, a power-chord walk and a lick.
var bars = []note{
	{0, .5, []float64{e2}, true}, {.5, .5, []float64{e2}, true}, {1, 1, five(g2), false},
	{2, .5, []float64{e2}, true}, {2.5, .5, []float64{e2}, true}, {3, 1, five(a2), false},
	{4, .5, []float64{e2}, true}, {4.5, .5, []float64{e2}, true}, {5, .5, five(b2), false}, {5.5, .5, five(a2), false},
	{6, 1, five(g2), false}, {7, .25, []float64{e3}, false}, {7.25, .25, []float64{d3}, false}, {7.5, .5, []float64{b2}, false},
}

// Generate returns seconds of mono audio at rate: the riff repeated at
// 120 bpm, peaking at -6 dBFS like a DI from an interface.
func Generate(rate int, seconds float64) []float64 {
	out := make([]float64, int(seconds*float64(rate)))
	const beat = 0.5 // seconds at 120 bpm
	rng := rand.New(rand.NewSource(7))
	for start := 0.0; start < seconds; start += 8 * beat {
		for _, n := range bars {
			at := int((start + n.beat*beat) * float64(rate))
			hold := int(n.len * beat * float64(rate))
			for _, hz := range n.hz {
				pluck(out, at, hold, hz, n.muted, rate, rng)
			}
		}
	}
	// a pickup and cable roll the top off
	y := 0.0
	for i, v := range out {
		y = 0.45*v + 0.55*y
		out[i] = y
	}
	peak := 0.0
	for _, v := range out {
		peak = math.Max(peak, math.Abs(v))
	}
	for i := range out {
		out[i] *= 0.5 / peak
	}
	return out
}

// pluck adds one string: a noise burst circulating in a delay line one
// period long, averaged on each pass so the highs die first.
func pluck(out []float64, at, hold int, hz float64, muted bool, rate int, rng *rand.Rand) {
	period := int(float64(rate) / hz)
	line := make([]float64, period)
	soft := 0.0
	for i := range line { // the pick is soft: smoothed noise, not white
		soft = 0.25*(rng.Float64()*2-1) + 0.75*soft
		line[i] = soft
	}
	decay := 0.996
	if muted {
		decay = 0.97 // the palm damps the string
	}
	ring := hold + rate/20 // a little past the next note
	for i, p := 0, 0; i < ring && at+i < len(out); i++ {
		next := (p + 1) % period
		v := line[p]
		line[p] = decay * 0.5 * (line[p] + line[next])
		p = next
		if i > hold { // let go: fade out fast
			v *= math.Exp(-float64(i-hold) / float64(rate/200))
		}
		out[at+i] += v
	}
}

// WriteWAV writes mono 16-bit PCM.
func WriteWAV(path string, x []float64, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n := uint32(len(x) * 2)
	h := []any{[4]byte{'R', 'I', 'F', 'F'}, 36 + n, [4]byte{'W', 'A', 'V', 'E'}, [4]byte{'f', 'm', 't', ' '},
		uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16), [4]byte{'d', 'a', 't', 'a'}, n}
	for _, v := range h {
		if err := binary.Write(f, binary.LittleEndian, v); err != nil {
			return err
		}
	}
	pcm := make([]int16, len(x))
	for i, v := range x {
		pcm[i] = int16(math.Max(-1, math.Min(1, v)) * 32767)
	}
	return binary.Write(f, binary.LittleEndian, pcm)
}
