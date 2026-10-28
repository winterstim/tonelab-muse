//go:build windows

package eyes

import (
	"context"
	"testing"
)

func TestWindowsAudioRecorderImplementsOS(t *testing.T) {
	var o OS = WindowsAudioRecorder{}
	if o == nil {
		t.Fatal("expected non-nil OS implementation")
	}

	var wRec OS = WindowsRecorder{}
	if wRec == nil {
		t.Fatal("expected non-nil WindowsRecorder")
	}

	// Windows() should return unsupported error
	if _, err := o.Windows(); err == nil {
		t.Fatal("expected error from Windows(), got nil")
	}

	// Capture() should return unsupported error
	if err := o.Capture(context.Background(), 1, "out.png"); err == nil {
		t.Fatal("expected error from Capture(), got nil")
	}

	// Record() with invalid duration should return validation error
	if err := o.Record(context.Background(), "ableton.exe", 0, "out.wav"); err == nil {
		t.Fatal("expected error for 0 seconds, got nil")
	}
	if err := o.Record(context.Background(), "ableton.exe", -1, "out.wav"); err == nil {
		t.Fatal("expected error for -1 seconds, got nil")
	}
}

func TestDefaultOSOnWindows(t *testing.T) {
	o := defaultOS()
	if o == nil {
		t.Fatal("expected non-nil defaultOS()")
	}
	if _, ok := o.(WindowsAudioRecorder); !ok {
		t.Fatalf("expected WindowsAudioRecorder, got %T", o)
	}
}
