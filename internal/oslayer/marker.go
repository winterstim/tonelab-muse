package oslayer

import "strings"

// HasUnsavedMarker checks if a window title contains indicators of unsaved state.
// Common DAW markers:
// - Asterisk "*": REAPER ("*Project.rpp", "Project.rpp*"), Ableton Live ("Project*")
// - Bullet "●" (\u25cf): macOS dirty window proxy indicator
// - "[modified]", "(modified)", "[unsaved]", "(unsaved)"
func HasUnsavedMarker(title string) bool {
	t := strings.TrimSpace(title)
	if strings.Contains(t, "*") {
		return true
	}
	if strings.Contains(t, "●") || strings.Contains(t, "\u25cf") {
		return true
	}
	lower := strings.ToLower(t)
	if strings.Contains(lower, "[modified]") || strings.Contains(lower, "(modified)") {
		return true
	}
	if strings.Contains(lower, "[unsaved]") || strings.Contains(lower, "(unsaved)") {
		return true
	}
	return false
}
