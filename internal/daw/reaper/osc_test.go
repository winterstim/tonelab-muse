package reaper

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, m := range []Msg{
		{Addr: "/track/1/volume", Args: []any{float32(0.5)}},
		{Addr: "/device/track/select", Args: []any{int32(3)}},
		{Addr: "/fx/name", Args: []any{"ReaEQ (Cockos)"}},
		{Addr: "/abc", Args: []any{"abc", int32(-7), float32(1)}}, // "abc" needs exactly one pad byte
		{Addr: "/x"},
	} {
		b, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		if len(b)%4 != 0 {
			t.Errorf("%s: length %d not padded", m.Addr, len(b))
		}
		got, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		if m.Args == nil {
			m.Args = nil
		}
		if len(got) != 1 || got[0].Addr != m.Addr || !reflect.DeepEqual(got[0].Args, m.Args) {
			t.Errorf("round trip %+v → %+v", m, got)
		}
	}
}

func TestBundle(t *testing.T) {
	a, _ := Encode(Msg{Addr: "/a", Args: []any{int32(1)}})
	b, _ := Encode(Msg{Addr: "/b", Args: []any{"x"}})
	p := append([]byte("#bundle\x00"), make([]byte, 8)...) // timetag
	for _, m := range [][]byte{a, b} {
		p = binary.BigEndian.AppendUint32(p, uint32(len(m)))
		p = append(p, m...)
	}
	got, err := Decode(p)
	if err != nil || len(got) != 2 || got[0].Addr != "/a" || got[1].Addr != "/b" {
		t.Fatalf("bundle → %+v, %v", got, err)
	}
}

func TestDecodeShort(t *testing.T) {
	good, _ := Encode(Msg{Addr: "/a", Args: []any{int32(1)}})
	for _, p := range [][]byte{{'/', 'a'}, good[:len(good)-2]} {
		if _, err := Decode(p); err == nil {
			t.Errorf("%q: want error", p)
		}
	}
}

func TestEncodeUnsupported(t *testing.T) {
	if _, err := Encode(Msg{Addr: "/a", Args: []any{true}}); err == nil {
		t.Fatal("want error for bool arg")
	}
}
