//go:build darwin

package eyes

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

typedef struct { int id, pid; char owner[128]; char name[256]; int x, y, w, h; } win;

static int str(CFDictionaryRef d, CFStringRef key, char *buf, int n) {
	CFStringRef s = CFDictionaryGetValue(d, key);
	buf[0] = 0;
	if (!s) return 0;
	return CFStringGetCString(s, buf, n, kCFStringEncodingUTF8);
}

static int num(CFDictionaryRef d, CFStringRef key) {
	CFNumberRef n = CFDictionaryGetValue(d, key);
	int v = 0;
	if (n) CFNumberGetValue(n, kCFNumberIntType, &v);
	return v;
}

// list fills out with on-screen windows and returns how many.
static int list(win *out, int max) {
	CFArrayRef a = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
	if (!a) return 0;
	int n = 0;
	for (CFIndex i = 0; i < CFArrayGetCount(a) && n < max; i++) {
		CFDictionaryRef d = CFArrayGetValueAtIndex(a, i);
		win *w = &out[n];
		w->id = num(d, kCGWindowNumber);
		w->pid = num(d, kCGWindowOwnerPID);
		str(d, kCGWindowOwnerName, w->owner, sizeof w->owner);
		str(d, kCGWindowName, w->name, sizeof w->name);
		CFDictionaryRef b = CFDictionaryGetValue(d, kCGWindowBounds);
		CGRect r = CGRectZero;
		if (b) CGRectMakeWithDictionaryRepresentation(b, &r);
		w->x = (int)r.origin.x; w->y = (int)r.origin.y; w->w = (int)r.size.width; w->h = (int)r.size.height;
		n++;
	}
	CFRelease(a);
	return n;
}
*/
import "C"

// Windows lists on-screen windows, front to back. Window names are empty
// unless the process has Screen Recording permission.
func Windows() []Window {
	buf := make([]C.win, 512)
	n := int(C.list(&buf[0], C.int(len(buf))))
	out := make([]Window, n)
	for i := 0; i < n; i++ {
		w := buf[i]
		out[i] = Window{ID: int(w.id), Pid: int(w.pid), Owner: C.GoString(&w.owner[0]), Name: C.GoString(&w.name[0]), X: int(w.x), Y: int(w.y), W: int(w.w), H: int(w.h)}
	}
	return out
}
