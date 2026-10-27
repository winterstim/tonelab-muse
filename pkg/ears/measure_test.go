package ears

import (
	"math"
	"math/cmplx"
	"testing"
)

func TestFFTSine(t *testing.T) {
	n := 64
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(math.Sin(2*math.Pi*5*float64(i)/float64(n)), 0)
	}
	fft(x)
	for k := 0; k < n/2; k++ {
		mag := cmplx.Abs(x[k])
		if k == 5 && math.Abs(mag-float64(n)/2) > 1e-9 || k != 5 && mag > 1e-9 {
			t.Fatalf("bin %d = %g", k, mag)
		}
	}
}

// Reference values from scipy.signal.welch (nperseg 4096) on the same
// clips (made by testdata/gen.go): the Go port must agree within 0.5 dB.
func TestMeasureMatchesPrototype(t *testing.T) {
	want := map[string]struct {
		bands    [6]float64
		centroid float64
		crest    float64
	}{
		"clean":      {[6]float64{-15.0, -7.6, -5.4, -3.7, -11.5, -21.3}, 1347.9, 16.00},
		"crunch":     {[6]float64{-14.6, -7.3, -5.1, -3.8, -13.1, -25.3}, 1190.9, 10.75},
		"distortion": {[6]float64{-21.4, -11.8, -5.9, -3.1, -8.8, -15.4}, 1994.7, 1.70},
		"mudd":       {[6]float64{-9.0, -3.3, -5.8, -9.0, -19.5, -29.8}, 549.3, 14.11},
	}
	for name, w := range want {
		m, err := Meter{}.Measure("testdata/" + name + ".wav")
		if err != nil {
			t.Fatal(err)
		}
		b := m.Bands
		got := [6]float64{b.Low, b.Mud, b.Mid, b.Upper, b.Presence, b.Air}
		for i := range got {
			if math.Abs(got[i]-w.bands[i]) > 0.5 {
				t.Errorf("%s band %d: got %.2f want %.1f", name, i, got[i], w.bands[i])
			}
		}
		if math.Abs(m.Centroid-w.centroid) > 5 {
			t.Errorf("%s centroid: got %.1f want %.1f", name, m.Centroid, w.centroid)
		}
		if math.Abs(m.Crest-w.crest) > 0.1 {
			t.Errorf("%s crest: got %.2f want %.2f", name, m.Crest, w.crest)
		}
	}
}

func TestLevelIgnoresTone(t *testing.T) {
	// a sine at amplitude 0.5 has RMS 0.5/√2 → -9.0 dBFS; doubling it adds 6 dB
	// to the level and leaves every band share the same
	mk := func(amp float64) Audio {
		x := make([]float64, 48000)
		for i := range x {
			x[i] = amp * math.Sin(2*math.Pi*1000*float64(i)/48000)
		}
		return Audio{Rate: 48000, Samples: x}
	}
	a, err := Analyze(mk(0.25))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Analyze(mk(0.5))
	if d := b.Level - a.Level; math.Abs(d-6.0) > 0.1 {
		t.Errorf("level change %.2f dB, want 6.0", d)
	}
	if math.Abs(b.Bands.Mid-a.Bands.Mid) > 0.01 {
		t.Errorf("band share moved with loudness: %.3f → %.3f", a.Bands.Mid, b.Bands.Mid)
	}
}
