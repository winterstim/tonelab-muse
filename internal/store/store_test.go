package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

func TestProfilesAndSessions(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if p, err := s.Profile("Amp 5 (Vendor)"); p != nil || err != nil {
		t.Fatalf("missing profile = %v, %v", p, err)
	}
	in := core.Profile{Plugin: "Amp 5 (Vendor)", Map: map[int]core.ProfileEntry{3: {Label: "Bass"}}}
	if err := s.SaveProfile(in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Profile("Amp 5 (Vendor)")
	if err != nil || got.Map[3].Label != "Bass" {
		t.Fatalf("profile = %+v, %v", got, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Append("s1", core.Event{At: time.Now(), Kind: "text", Data: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "sessions", "s1.jsonl"))
	if strings.Count(string(b), "\n") != 2 {
		t.Fatalf("session = %q", b)
	}
}
