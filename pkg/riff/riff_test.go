package riff

import (
	"math"
	"path/filepath"
	"testing"
)

func TestGenerate(t *testing.T) {
	x := Generate(48000, 4)
	if len(x) != 4*48000 {
		t.Fatalf("%d samples", len(x))
	}
	peak, energy := 0.0, 0.0
	for _, v := range x {
		peak = math.Max(peak, math.Abs(v))
		energy += v * v
	}
	if math.Abs(peak-0.5) > 1e-9 || energy == 0 {
		t.Fatalf("peak %g, energy %g", peak, energy)
	}
	if err := WriteWAV(filepath.Join(t.TempDir(), "r.wav"), x, 48000); err != nil {
		t.Fatal(err)
	}
}
