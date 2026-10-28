//go:build darwin

package eyes

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

static int screenOK(void) { return CGPreflightScreenCaptureAccess(); }
static void askScreen(void) { CGRequestScreenCaptureAccess(); }
*/
import "C"

import (
	"context"
	"time"
)

// AskAccess asks for screen and audio recording, the one permission the
// helper needs. macOS shows its prompt only once per app, so after that it
// opens the right Settings page instead.
func AskAccess() {
	if C.screenOK() == 0 {
		C.askScreen()
		time.Sleep(3 * time.Second)
		if C.screenOK() == 0 {
			OpenSettings("Privacy_ScreenCapture")
		}
	}
}

// Local does everything in this process, with this process's permissions.
// Only the helper app uses it.
type Local struct{}

func (Local) Windows() ([]Window, error) { return Windows(), nil }
func (Local) Capture(ctx context.Context, id int, path string) error {
	return screencapture(ctx, id, path)
}
