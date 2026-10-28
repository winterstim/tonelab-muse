//go:build darwin

package eyes

import "testing"

func TestWindowsLists(t *testing.T) {
	ws := Windows()
	for _, w := range ws {
		if w.Owner == "REAPER" {
			t.Logf("%d %q %dx%d", w.ID, w.Name, w.W, w.H)
		}
	}
}
