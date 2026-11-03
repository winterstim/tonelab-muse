//go:build darwin

package oslayer

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static int isTrusted(void) {
	return AXIsProcessTrusted();
}

static int sendCmdS(int pid) {
	CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
	if (!src) return -1;
	// ANSI key code for 's' is 1
	CGEventRef keyDown = CGEventCreateKeyboardEvent(src, (CGKeyCode)1, true);
	CGEventRef keyUp = CGEventCreateKeyboardEvent(src, (CGKeyCode)1, false);
	if (!keyDown || !keyUp) {
		if (keyDown) CFRelease(keyDown);
		if (keyUp) CFRelease(keyUp);
		CFRelease(src);
		return -2;
	}
	CGEventSetFlags(keyDown, kCGEventFlagMaskCommand);
	CGEventSetFlags(keyUp, kCGEventFlagMaskCommand);

	if (pid > 0) {
		CGEventPostToPid((pid_t)pid, keyDown);
		CGEventPostToPid((pid_t)pid, keyUp);
	}
	CGEventPost(kCGSessionEventTap, keyDown);
	CGEventPost(kCGSessionEventTap, keyUp);

	CFRelease(keyDown);
	CFRelease(keyUp);
	CFRelease(src);
	return 0;
}

static int activatePID(int pid) {
	if (pid <= 0) return -1;
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	if (!app) return -1;
	AXError err = AXUIElementSetAttributeValue(app, kAXFrontmostAttribute, kCFBooleanTrue);
	CFRelease(app);
	return (err == kAXErrorSuccess) ? 0 : -1;
}

typedef struct {
	int id;
	int pid;
	char owner[128];
	char name[256];
} c_win;

static int listWindowsCG(c_win *out, int max) {
	CFArrayRef a = CGWindowListCopyWindowInfo(kCGWindowListOptionAll | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!a) return 0;
	int n = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(a) && n < max; i++) {
		CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(a, i);
		c_win *w = &out[n];
		w->id = 0;
		w->pid = 0;
		CFNumberRef idNum = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowNumber);
		CFNumberRef pidNum = (CFNumberRef)CFDictionaryGetValue(d, kCGWindowOwnerPID);
		if (idNum) CFNumberGetValue(idNum, kCFNumberIntType, &w->id);
		if (pidNum) CFNumberGetValue(pidNum, kCFNumberIntType, &w->pid);

		w->owner[0] = 0;
		CFStringRef owner = (CFStringRef)CFDictionaryGetValue(d, kCGWindowOwnerName);
		if (owner) CFStringGetCString(owner, w->owner, sizeof(w->owner), kCFStringEncodingUTF8);

		w->name[0] = 0;
		CFStringRef name = (CFStringRef)CFDictionaryGetValue(d, kCGWindowName);
		if (name) CFStringGetCString(name, w->name, sizeof(w->name), kCFStringEncodingUTF8);

		n++;
	}
	CFRelease(a);
	return n;
}

static int getTitleAX(int pid, char *outBuf, int maxLen) {
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	if (!app) return -1;
	AXUIElementRef win = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXMainWindowAttribute, (CFTypeRef*)&win) != kAXErrorSuccess || !win) {
		if (AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, (CFTypeRef*)&win) != kAXErrorSuccess || !win) {
			CFArrayRef wins = NULL;
			if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, (CFTypeRef*)&wins) == kAXErrorSuccess && wins) {
				if (CFArrayGetCount(wins) > 0) {
					win = (AXUIElementRef)CFRetain(CFArrayGetValueAtIndex(wins, 0));
				}
				CFRelease(wins);
			}
		}
	}
	if (!win) {
		CFRelease(app);
		return -2;
	}
	CFStringRef title = NULL;
	int res = -3;
	if (AXUIElementCopyAttributeValue(win, kAXTitleAttribute, (CFTypeRef*)&title) == kAXErrorSuccess && title) {
		if (CFStringGetCString(title, outBuf, maxLen, kCFStringEncodingUTF8)) {
			res = 0;
		}
		CFRelease(title);
	}
	CFRelease(win);
	CFRelease(app);
	return res;
}
*/
import "C"

import (
	"fmt"
	"strings"
)

type darwinBackend struct{}

func defaultBackend() Backend {
	return &darwinBackend{}
}

func (b *darwinBackend) CheckAccessibility() error {
	if C.isTrusted() == 0 {
		return ErrAccessibilityPermission
	}
	return nil
}

func (b *darwinBackend) ListWindows() ([]WindowInfo, error) {
	buf := make([]C.c_win, 256)
	count := int(C.listWindowsCG(&buf[0], C.int(len(buf))))
	out := make([]WindowInfo, 0, count)
	for i := 0; i < count; i++ {
		w := buf[i]
		owner := strings.TrimSpace(C.GoString(&w.owner[0]))
		name := strings.TrimSpace(C.GoString(&w.name[0]))
		pid := int(w.pid)
		id := int(w.id)

		// If name is empty (e.g. without Screen Recording permission), attempt to query title via Accessibility
		if name == "" && pid > 0 {
			var axBuf [512]C.char
			if C.getTitleAX(C.int(pid), &axBuf[0], 512) == 0 {
				name = strings.TrimSpace(C.GoString(&axBuf[0]))
			}
		}

		out = append(out, WindowInfo{
			ID:    id,
			PID:   pid,
			Owner: owner,
			Title: name,
		})
	}
	return out, nil
}

func (b *darwinBackend) SendCmdS(pid int) error {
	if C.sendCmdS(C.int(pid)) != 0 {
		return fmt.Errorf("failed to dispatch Cmd+S event to pid %d", pid)
	}
	return nil
}

func (b *darwinBackend) GetWindowTitle(pid int, windowID int) (string, error) {
	if pid > 0 {
		var axBuf [512]C.char
		if C.getTitleAX(C.int(pid), &axBuf[0], 512) == 0 {
			title := strings.TrimSpace(C.GoString(&axBuf[0]))
			if title != "" {
				return title, nil
			}
		}
	}

	// Fallback to checking CG window list
	wins, err := b.ListWindows()
	if err != nil {
		return "", err
	}
	for _, w := range wins {
		if (windowID > 0 && w.ID == windowID) || (pid > 0 && w.PID == pid && w.Title != "") {
			return w.Title, nil
		}
	}
	return "", nil
}

func (b *darwinBackend) Activate(pid int) error {
	if pid <= 0 {
		return nil
	}
	C.activatePID(C.int(pid))
	return nil
}
