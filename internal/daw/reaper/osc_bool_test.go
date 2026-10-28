package reaper

import "testing"

// AbletonOSC answers yes/no queries with the OSC booleans T and F, which
// carry no payload. They must decode as 1 and 0, not be dropped.
func TestDecodeBooleanTags(t *testing.T) {
	// OSC strings are NUL-terminated and padded to 4 bytes; then the int 7
	raw := []byte("/a\x00\x00" + ",TFi\x00\x00\x00\x00" + "\x00\x00\x00\x07")
	ms, err := Decode(raw)
	if err != nil || len(ms) != 1 {
		t.Fatalf("decode: %v %v", ms, err)
	}
	got := ms[0].Args
	if len(got) != 3 || got[0] != int32(1) || got[1] != int32(0) || got[2] != int32(7) {
		t.Fatalf("args %#v, want [1 0 7]", got)
	}
}
