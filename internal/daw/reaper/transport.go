package reaper

import (
	"context"
	"fmt"
)

// Bundle is REAPER's macOS bundle id, the app whose audio output listening
// records.
const Bundle = "com.cockos.reaper"

// ActionGoToStart is "Transport: Go to start of project".
const ActionGoToStart = 40042

// Transport plays from the start of the project or stops, so every capture
// covers the same bars whatever the user's stop preferences are.
func (r *REAPER) Transport(ctx context.Context, play bool) error {
	if !play {
		return r.c.Send("/stop")
	}
	if err := r.c.Send(fmt.Sprintf("/action/%d", ActionGoToStart)); err != nil {
		return err
	}
	return r.c.Send("/play")
}
