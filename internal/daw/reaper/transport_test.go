package reaper_test

import (
	"slices"
	"testing"
	"time"
)

func TestTransport(t *testing.T) {
	r, srv := studio(t)
	if err := r.Transport(ctx(t), true); err != nil {
		t.Fatal(err)
	}
	if err := r.Transport(ctx(t), false); err != nil {
		t.Fatal(err)
	}
	want := []string{"/action/40042", "/play", "/stop"}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := srv.Received()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
