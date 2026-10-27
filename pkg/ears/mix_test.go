package ears

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeTone(t *testing.T, name string, freqs ...float64) string {
	t.Helper()
	x := make([]float64, 48000*2)
	for i := range x {
		for _, f := range freqs {
			x[i] += 0.2 * math.Sin(2*math.Pi*f*float64(i)/48000)
		}
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, encodeWAV(x, 48000), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClashesFindTheSharedBand(t *testing.T) {
	// guitar at 250 Hz and 2 kHz, bass at 250 Hz and 80 Hz: only 250 clashes
	g := writeTone(t, "g.wav", 250, 2000)
	b := writeTone(t, "b.wav", 250, 80)
	cs, err := Meter{}.Clashes(g, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 || cs[0].Lo > 250 || cs[0].Hi < 250 {
		t.Fatalf("clashes = %+v; want the band holding 250 Hz first", cs)
	}
	for _, c := range cs {
		if c.Lo <= 2000 && c.Hi >= 2000 || c.Lo <= 80 && c.Hi >= 80 {
			t.Fatalf("a band only one signal uses was reported: %+v", c)
		}
	}
}

// encodeWAV writes mono 16-bit PCM.
func encodeWAV(x []float64, rate int) []byte {
	b := make([]byte, 44+2*len(x))
	le := func(off int, v uint32, n int) {
		for i := 0; i < n; i++ {
			b[off+i] = byte(v >> (8 * i))
		}
	}
	copy(b, "RIFF")
	le(4, uint32(36+2*len(x)), 4)
	copy(b[8:], "WAVEfmt ")
	le(16, 16, 4)
	le(20, 1, 2)
	le(22, 1, 2)
	le(24, uint32(rate), 4)
	le(28, uint32(rate*2), 4)
	le(32, 2, 2)
	le(34, 16, 2)
	copy(b[36:], "data")
	le(40, uint32(2*len(x)), 4)
	for i, v := range x {
		le(44+2*i, uint32(uint16(int16(math.Max(-1, math.Min(1, v))*32767))), 2)
	}
	return b
}
