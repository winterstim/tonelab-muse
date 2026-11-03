//go:build live && darwin

package oslayer

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLiveDAWOSLayer executes live checks only against REAPER and Ableton Live open on this Mac.
func TestLiveDAWOSLayer(t *testing.T) {
	backend := defaultBackend()

	// 1. Accessibility permission check
	if err := backend.CheckAccessibility(); err != nil {
		t.Fatalf("Live accessibility check failed: %v", err)
	}

	// 2. Discover running DAWs
	windows, err := backend.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows failed: %v", err)
	}

	dawsFound := make(map[string]bool)
	for _, w := range windows {
		owner := strings.ToLower(w.Owner)
		if strings.Contains(owner, "reaper") {
			dawsFound["REAPER"] = true
		}
		if strings.Contains(owner, "live") {
			dawsFound["Live"] = true
		}
	}

	if len(dawsFound) == 0 {
		t.Skip("Live checks only run against REAPER and Ableton Live open on this Mac (neither found)")
	}

	for daw := range dawsFound {
		t.Run(daw, func(t *testing.T) {
			svc := New(WithBackend(backend), WithDAW(daw))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// Test listing plugin windows
			pluginWins, err := svc.ListPluginWindows(ctx)
			if err != nil {
				t.Fatalf("ListPluginWindows failed for %s: %v", daw, err)
			}
			t.Logf("Discovered %d plugin windows open in %s: %+v", len(pluginWins), daw, pluginWins)

			// Test save project with consent
			saveErr := svc.SaveProject(ctx, WithSaveConsent(true))
			if saveErr != nil {
				t.Fatalf("Live SaveProject failed for %s: %v", daw, saveErr)
			}
			t.Logf("Successfully sent Cmd+S and verified save for %s", daw)
		})
	}
}
