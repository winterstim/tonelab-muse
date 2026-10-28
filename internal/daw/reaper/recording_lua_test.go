package reaper

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Optional direct execution of the shipped script against a fake native API.
// No DAW process, UI, input device or helper is used.
func TestLuaInputRecording(t *testing.T) {
	lua := os.Getenv("MUSE_LUA_TEST_BIN")
	if lua == "" {
		t.Skip("set MUSE_LUA_TEST_BIN to a Lua 5.3+ interpreter for bridge fake tests")
	}
	script, err := filepath.Abs("bridge/startup.lua")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"watchdog", "exit", "other-armed-at-start", "other-armed-mid-take", "stop-failure", "disarm-failure", "undo-failure", "project-change", "range-render", "range-render-failure"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, ".tonelab-muse", "bridge"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(lua, "bridge/recording_test.lua", script, scenario)
			cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			} else {
				t.Log(string(out))
			}
		})
	}
}
