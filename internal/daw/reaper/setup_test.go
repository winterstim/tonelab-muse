package reaper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableOSC(t *testing.T) {
	running = func() bool { return false }
	defer func() { running = Running }()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
	ini := filepath.Join(resourceDir(), "reaper.ini")
	os.MkdirAll(filepath.Dir(ini), 0o755)
	os.WriteFile(ini, []byte("[REAPER]\nafxcfg=0\n"), 0o644)
	changed, err := EnableOSC("127.0.0.1:8000", "127.0.0.1:9000")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ := os.ReadFile(ini)
	if !strings.Contains(string(b), `csurf_0=OSC "" 3 8000 "127.0.0.1" 9000 1024 10 ""`) || !strings.Contains(string(b), "csurf_cnt=1") {
		t.Fatalf("%s", b)
	}
	if changed, _ := EnableOSC("127.0.0.1:8000", "127.0.0.1:9000"); changed {
		t.Fatal("added a second surface")
	}
}
