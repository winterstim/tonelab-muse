package reaper

import (
	"regexp"
	"strconv"
	"strings"
)

// REAPER action ids the agent may trigger.
const (
	ActionUndo = 40029 // Edit: Undo
)

var allowedActions = map[int]bool{ActionUndo: true, ActionGoToStart: true}

var allowed = []*regexp.Regexp{
	regexp.MustCompile(`^/track/\d+/(volume|pan|mute|solo)$`),
	regexp.MustCompile(`^/track/\d+/fx/\d+/fxparam/\d+/value$`),
	regexp.MustCompile(`^/track/\d+/fx/\d+/bypass$`),
	regexp.MustCompile(`^/device/[a-z/]+$`),
	regexp.MustCompile(`^/fx/\d+/openui$`),
	regexp.MustCompile(`^/(play|stop)$`), // transport, for listening
}

// Allowed reports whether addr may be sent to REAPER. It is checked at the
// socket, below every tool, so nothing above can widen it.
func Allowed(addr string) bool {
	if id, ok := strings.CutPrefix(addr, "/action/"); ok {
		n, err := strconv.Atoi(id)
		return err == nil && allowedActions[n]
	}
	for _, re := range allowed {
		if re.MatchString(addr) {
			return true
		}
	}
	return false
}
