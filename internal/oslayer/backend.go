package oslayer

import (
	"errors"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

var (
	// ErrAccessibilityPermission is returned when macOS Accessibility permissions are not granted.
	ErrAccessibilityPermission = errors.New("accessibility permission missing: please grant accessibility access in System Settings > Privacy & Security > Accessibility")

	// ErrConsentRequired is returned when saving a project is attempted without user consent.
	ErrConsentRequired = errors.New("user consent required to save project")

	// ErrNoDAWFound is returned when no matching DAW application or window is running.
	ErrNoDAWFound = errors.New("no running DAW window found")

	// ErrSaveVerificationFailed is returned when window title retains its unsaved marker after Cmd+S.
	ErrSaveVerificationFailed = errors.New("save verification failed: window title still marked unsaved")
)

// WindowInfo represents information about an on-screen window.
type WindowInfo struct {
	ID    int    `json:"id"`
	PID   int    `json:"pid"`
	Owner string `json:"owner"`
	Title string `json:"title"`
}

// PluginWindow aliases core.PluginWindow.
type PluginWindow = core.PluginWindow

// Backend defines the low-level OS/windowing operations needed by oslayer.
type Backend interface {
	CheckAccessibility() error
	ListWindows() ([]WindowInfo, error)
	SendCmdS(pid int) error
	GetWindowTitle(pid int, windowID int) (string, error)
	Activate(pid int) error
}
