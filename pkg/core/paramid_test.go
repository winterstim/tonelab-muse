package core

import (
	"errors"
	"testing"
)

func TestParamID(t *testing.T) {
	if got := MakeParamID(2, 14); got != "fx2.p14" {
		t.Fatalf("MakeParamID = %q", got)
	}
	fx, p, err := ParamID("fx2.p14").Parse()
	if err != nil || fx != 2 || p != 14 {
		t.Fatalf("Parse = %d %d %v", fx, p, err)
	}
	for _, bad := range []ParamID{"", "fx2", "fx.p1", "fx0.p1", "fx1.p0", "f1.p1", "fx1.q1", "fxa.p1"} {
		if _, _, err := bad.Parse(); !errors.Is(err, ErrUnknownParam) {
			t.Errorf("%q: want ErrUnknownParam, got %v", bad, err)
		}
	}
}
