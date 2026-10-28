package reaper

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// REAPER keeps its control surfaces in reaper.ini as csurf_N lines, e.g.
//
//	csurf_0=OSC "" 3 8000 "127.0.0.1" 9000 1024 10 ""
//
// (mode 3 = send and receive, then the port it listens on, where it
// sends, the port it sends to). It rewrites the file when it quits, so the
// file is only edited while REAPER is closed.
var csurf = regexp.MustCompile(`(?m)^csurf_(\d+)=(.*)$`)

var running = Running // tests stand in for it

// Running reports whether REAPER is open.
func Running() bool {
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq reaper.exe", "/NH").Output()
		return strings.Contains(strings.ToLower(string(out)), "reaper.exe")
	}
	return exec.Command("pgrep", "-x", "REAPER").Run() == nil
}

// EnableOSC adds an OSC control surface that listens on in and sends to
// out, unless one already does. REAPER must be closed. It returns whether
// it changed anything.
func EnableOSC(in, out string) (bool, error) {
	if running() {
		return false, fmt.Errorf("REAPER is open")
	}
	_, inPort, err := splitAddr(in)
	if err != nil {
		return false, err
	}
	outHost, outPort, err := splitAddr(out)
	if err != nil {
		return false, err
	}
	path := filepath.Join(resourceDir(), "reaper.ini")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, fmt.Errorf("start REAPER once first, so it creates its settings")
	}
	if err != nil {
		return false, err
	}
	ini := strings.ReplaceAll(string(raw), "\r\n", "\n")
	n := 0
	for _, m := range csurf.FindAllStringSubmatch(ini, -1) {
		i, _ := strconv.Atoi(m[1])
		n = max(n, i+1)
		f := strings.Fields(m[2])
		if len(f) >= 6 && f[0] == "OSC" && f[3] == inPort && f[5] == outPort {
			return false, nil
		}
	}
	line := fmt.Sprintf(`csurf_%d=OSC "" 3 %s "%s" %s 1024 10 ""`, n, inPort, outHost, outPort)
	cnt := regexp.MustCompile(`(?m)^csurf_cnt=\d+$`)
	sec := regexp.MustCompile(`(?m)^\[REAPER\]$`)
	switch {
	case cnt.MatchString(ini):
		ini = cnt.ReplaceAllString(ini, line+"\ncsurf_cnt="+strconv.Itoa(n+1))
	case sec.MatchString(ini):
		ini = sec.ReplaceAllString(ini, "[REAPER]\n"+line+"\ncsurf_cnt="+strconv.Itoa(n+1))
	default:
		return false, fmt.Errorf("unexpected reaper.ini")
	}
	if runtime.GOOS == "windows" {
		ini = strings.ReplaceAll(ini, "\n", "\r\n")
	}
	os.WriteFile(path+".before-agent", raw, 0o644) // the user's own copy
	return true, os.WriteFile(path, []byte(ini), 0o644)
}

// Launch opens REAPER and waits until it answers on OSC.
func Launch(ctx context.Context, alive func(context.Context) error) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command(filepath.Join(os.Getenv("ProgramFiles"), "REAPER (x64)", "reaper.exe"))
	} else {
		cmd = exec.Command("open", "-a", "REAPER")
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start REAPER: %w", err)
	}
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if alive(ctx) == nil {
			return nil
		}
	}
	return fmt.Errorf("REAPER started but does not answer on OSC")
}

func splitAddr(a string) (string, string, error) {
	i := strings.LastIndex(a, ":")
	if i < 0 {
		return "", "", fmt.Errorf("bad address %q", a)
	}
	return a[:i], a[i+1:], nil
}
