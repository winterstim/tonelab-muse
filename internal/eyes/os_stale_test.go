package eyes

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A helper that quit leaves its socket file behind with nothing listening on
// it. The call must remove that file and start the helper, not wait on the
// dead file.
func TestStaleSocketIsRemovedAndHelperStarted(t *testing.T) {
	dir, err := os.MkdirTemp("", "hs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "hands.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // the file stays; nothing listens on it now
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("setup: stale socket file missing: %v", err)
	}

	// no helper app is installed here, so start fails right after the cleanup
	r := Remote{Sock: sock, App: filepath.Join(dir, "missing.app")}
	_, err = r.call(msg{Op: "windows"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("call error %v, want the helper-not-installed start error", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket file still there: %v", err)
	}
}
