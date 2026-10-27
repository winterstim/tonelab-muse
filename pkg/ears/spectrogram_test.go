package ears

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestHeatEnds(t *testing.T) {
	if c := heat(floorDB); c.R != 0 || c.G != 0 || c.B != 0 {
		t.Errorf("floor = %v, want black", c)
	}
	if c := heat(0); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("0 dBFS = %v, want white", c)
	}
}

// A full-scale 1 kHz sine must light the 1 kHz row near 0 dBFS and leave
// a far row dark.
func TestSpectrogramSine(t *testing.T) {
	rate := 24000
	a := Audio{Rate: rate, Samples: make([]float64, rate)}
	for i := range a.Samples {
		a.Samples[i] = math.Sin(2 * math.Pi * 1000 * float64(i) / float64(rate))
	}
	img := Spectrogram(a, "sine")
	row := func(hz float64) int { return padT + plotH - 1 - int(math.Log(hz/minHz)/math.Log(maxHz/minHz)*plotH) }
	x := padL + plotW/2
	if c := img.RGBAAt(x, row(1000)); c.R < 250 || c.G < 200 {
		t.Errorf("1 kHz row = %v, want near white", c)
	}
	if c := img.RGBAAt(x, row(60)); c.R > 60 {
		t.Errorf("60 Hz row = %v, want dark", c)
	}
}

func TestSpectrogramWrites(t *testing.T) {
	a, err := ReadWAV("testdata/mudd.wav")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ReadWAV("testdata/clean.wav")
	p := filepath.Join(t.TempDir(), "s.png")
	if err := WritePNG(p, Stack(Spectrogram(a, "A"), Spectrogram(b, "B"))); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Size() < 10000 {
		t.Fatalf("png: %v %v", st, err)
	}
}
