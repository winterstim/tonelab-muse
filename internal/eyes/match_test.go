//go:build darwin

package eyes

import "testing"

func TestMatchByTitleOrOwner(t *testing.T) {
	ws := []Window{
		{ID: 1, Owner: "Live", Name: "", W: 20, H: 20}, // a tooltip
		{ID: 2, Owner: "Other", Name: "FX: Track 1 ", W: 800, H: 600},
		{ID: 3, Owner: "Live", Name: "Song", W: 1400, H: 900},
		{ID: 4, Owner: "REAPER", Name: "FX: Track 1 EQ", W: 600, H: 400},
	}
	title := func(int) string { return "FX: Track 1 " }
	if got := match(ws, "REAPER", title, 1); got != 4 {
		t.Errorf("title match = %d, want 4", got)
	}
	if got := match(ws, "Live", nil, 1); got != 3 {
		t.Errorf("owner-only match = %d, want 3 (the real window, not the tooltip)", got)
	}
	if got := match(ws, "", nil, 1); got != 0 {
		t.Errorf("unknown app matched window %d", got)
	}
}

func TestQuoted(t *testing.T) {
	if got := quoted(`"Live" ASN:0x0-0x1a01a: ` + "\n bundleID=[ NULL ]"); got != "Live" {
		t.Errorf("quoted = %q", got)
	}
}
