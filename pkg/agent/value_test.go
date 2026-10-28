package agent

import "testing"

func TestReadout(t *testing.T) {
	for in, want := range map[string]float64{"-3.0 dB": -3, "400.0": 400, "1.5 kHz": 1500, "+4.5dB": 4.5, "7": 7, "-inf": -1e9, ".5": 0.5, "20 ms": 0.02, "1.50 s": 1.5, "250ms": 0.25, "64 samples": 64, "inf : 1": 1e9, "3.00 : 1": 3} {
		if got, ok := readout(in); !ok || got != want {
			t.Errorf("readout(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := readout("Off"); ok {
		t.Error("readout(Off) should fail")
	}
}

func TestTimedAndRaw(t *testing.T) {
	if !timed("100 ms") || !timed("1.50 s") || timed("89.0") || timed("64 samples") {
		t.Fatal("timed")
	}
	if raw("100 ms") != 100 || raw("-3.5 dB") != -3.5 {
		t.Fatal("raw")
	}
}
