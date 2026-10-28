package agent

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// curveDAW shows fx2.p1 through a display curve and counts the reads.
type curveDAW struct {
	*memDAW
	show  func(v float64) string
	reads int
}

func (d *curveDAW) ParamText(_ context.Context, _ int, id core.ParamID) (string, error) {
	d.reads++
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.show(d.vals[id]), nil
}

func TestSearchIsFewReads(t *testing.T) {
	for _, c := range []struct {
		name, target string
		show         func(float64) string
		want         float64
		maxReads     int
	}{
		{"log Hz", "400 Hz", func(v float64) string { return fmt.Sprintf("%.1f Hz", 20*math.Pow(1000, v)) }, 400, 9},
		{"linear dB", "-3 dB", func(v float64) string { return fmt.Sprintf("%.1f dB", -24+48*v) }, -3, 4},
	} {
		d := &curveDAW{memDAW: newMemDAW(), show: c.show}
		a := New(Deps{DAW: d}, Config{})
		a.Allow(kindProbe)
		v, err := a.findValue(context.Background(), 1, "fx2.p1", c.target)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := readoutOf(c.show(v))
		if math.Abs(got-c.want) > 0.006*math.Abs(c.want) {
			t.Errorf("%s: landed on %v (%s)", c.name, v, c.show(v))
		}
		t.Logf("%s: %d reads", c.name, d.reads)
		if d.reads > c.maxReads {
			t.Errorf("%s: %d reads, want at most %d", c.name, d.reads, c.maxReads)
		}
	}
}

func readoutOf(s string) float64 { f, _ := readout(s); return f }

func TestSearchSteppedDisplay(t *testing.T) {
	d := &curveDAW{memDAW: newMemDAW(), show: func(v float64) string { return fmt.Sprint(int(v * 10)) }}
	a := New(Deps{DAW: d}, Config{})
	a.Allow(kindProbe)
	v, err := a.findValue(context.Background(), 1, "fx2.p1", "7")
	if err != nil || v < 0.69 || v > 0.72 {
		t.Fatalf("landed on %v (%v), want the start of the 7 step", v, err)
	}
	t.Logf("%d reads", d.reads)
}
