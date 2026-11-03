package oslayer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type mockBackend struct {
	mu           sync.Mutex
	trusted      bool
	windows      []WindowInfo
	cmdSCount    int
	lastCmdSPID  int
	activatedPID int
	onCmdS       func(pid int)
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		trusted: true,
		windows: []WindowInfo{},
	}
}

func (m *mockBackend) CheckAccessibility() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.trusted {
		return ErrAccessibilityPermission
	}
	return nil
}

func (m *mockBackend) ListWindows() ([]WindowInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]WindowInfo, len(m.windows))
	copy(out, m.windows)
	return out, nil
}

func (m *mockBackend) SendCmdS(pid int) error {
	m.mu.Lock()
	m.cmdSCount++
	m.lastCmdSPID = pid
	fn := m.onCmdS
	m.mu.Unlock()

	if fn != nil {
		fn(pid)
	}
	return nil
}

func (m *mockBackend) GetWindowTitle(pid int, windowID int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.windows {
		if (windowID > 0 && w.ID == windowID) || (pid > 0 && w.PID == pid) {
			return w.Title, nil
		}
	}
	return "", nil
}

func (m *mockBackend) Activate(pid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activatedPID = pid
	return nil
}

func (m *mockBackend) setWindowTitle(windowID int, title string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.windows {
		if m.windows[i].ID == windowID {
			m.windows[i].Title = title
			return
		}
	}
}

func TestSaveProject_ConsentRequired(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "RockSong.rpp - REAPER"},
	}

	svc := New(WithBackend(mock), WithConsent(false))
	err := svc.SaveProject(context.Background())
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("expected ErrConsentRequired, got: %v", err)
	}
	if mock.cmdSCount != 0 {
		t.Fatalf("Cmd+S should not have been sent without consent, got %d", mock.cmdSCount)
	}
}

func TestSaveProject_ConsentFlag(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "RockSong.rpp - REAPER"},
	}

	svc := New(WithBackend(mock), WithConsent(true))
	err := svc.SaveProject(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.cmdSCount != 1 {
		t.Fatalf("expected 1 Cmd+S call, got %d", mock.cmdSCount)
	}
	if mock.lastCmdSPID != 100 {
		t.Fatalf("expected PID 100, got %d", mock.lastCmdSPID)
	}
}

func TestSaveProject_ConsentPromptGranted(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 101, Owner: "Live", Title: "Groove - Ableton Live"},
	}

	promptCalled := false
	svc := New(
		WithBackend(mock),
		WithConsent(false),
		WithPrompt(func() (bool, error) {
			promptCalled = true
			return true, nil
		}),
	)

	err := svc.SaveProject(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !promptCalled {
		t.Fatal("expected consent prompt to be called")
	}
	if mock.cmdSCount != 1 {
		t.Fatalf("expected 1 Cmd+S call, got %d", mock.cmdSCount)
	}
}

func TestSaveProject_ConsentPromptDenied(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 101, Owner: "Live", Title: "Groove - Ableton Live"},
	}

	svc := New(
		WithBackend(mock),
		WithConsent(false),
		WithPrompt(func() (bool, error) {
			return false, nil
		}),
	)

	err := svc.SaveProject(context.Background())
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("expected ErrConsentRequired, got: %v", err)
	}
	if mock.cmdSCount != 0 {
		t.Fatalf("expected 0 Cmd+S calls, got %d", mock.cmdSCount)
	}
}

func TestSaveProject_MissingAccessibility(t *testing.T) {
	mock := newMockBackend()
	mock.trusted = false
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "RockSong.rpp - REAPER"},
	}

	svc := New(WithBackend(mock), WithConsent(true))
	err := svc.SaveProject(context.Background())
	if !errors.Is(err, ErrAccessibilityPermission) {
		t.Fatalf("expected ErrAccessibilityPermission, got: %v", err)
	}
	if mock.cmdSCount != 0 {
		t.Fatalf("expected 0 Cmd+S calls when accessibility is missing, got %d", mock.cmdSCount)
	}
}

func TestSaveProject_NoDAWFound(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 200, Owner: "Finder", Title: "Desktop"},
	}

	svc := New(WithBackend(mock), WithConsent(true))
	err := svc.SaveProject(context.Background())
	if !errors.Is(err, ErrNoDAWFound) {
		t.Fatalf("expected ErrNoDAWFound, got: %v", err)
	}
}

func TestSaveProject_VerificationSucceeds(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "*RockSong.rpp [modified] - REAPER"},
	}

	// On Cmd+S, DAW clears the dirty marker
	mock.onCmdS = func(pid int) {
		mock.setWindowTitle(1, "RockSong.rpp - REAPER")
	}

	svc := New(
		WithBackend(mock),
		WithConsent(true),
		WithPollParams(10*time.Millisecond, 200*time.Millisecond),
	)

	err := svc.SaveProject(context.Background())
	if err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	if mock.cmdSCount != 1 {
		t.Fatalf("expected 1 Cmd+S call, got %d", mock.cmdSCount)
	}
}

func TestSaveProject_VerificationFailsWhenMarkerRemains(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "*RockSong.rpp - REAPER"},
	}

	// Window stays dirty after Cmd+S
	mock.onCmdS = func(pid int) {
		// Does not clear asterisk
	}

	svc := New(
		WithBackend(mock),
		WithConsent(true),
		WithPollParams(5*time.Millisecond, 30*time.Millisecond),
	)

	err := svc.SaveProject(context.Background())
	if err == nil {
		t.Fatal("expected save verification error, got nil")
	}
	if !errors.Is(err, ErrSaveVerificationFailed) {
		t.Fatalf("expected ErrSaveVerificationFailed, got: %v", err)
	}
}

func TestListPluginWindows(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		// REAPER main window
		{ID: 1, PID: 100, Owner: "REAPER", Title: "Album.rpp - REAPER v7.11"},
		// REAPER plugin windows
		{ID: 2, PID: 100, Owner: "REAPER", Title: "FX: Track 1 [AmpliTube 5]"},
		{ID: 3, PID: 100, Owner: "REAPER", Title: "FX: Track 2 [FabFilter Pro-Q 3]"},
		// Live main window
		{ID: 4, PID: 200, Owner: "Live", Title: "SynthTrack - Ableton Live"},
		// Live plugin window
		{ID: 5, PID: 200, Owner: "Live", Title: "Wavetable (Track 1)"},
		// Dialog window (should be excluded)
		{ID: 6, PID: 200, Owner: "Live", Title: "Preferences"},
		// Out-of-process plugin host
		{ID: 7, PID: 300, Owner: "AUHostingCompatibilityService", Title: "Serum AU"},
		// Unrelated app
		{ID: 8, PID: 400, Owner: "Safari", Title: "Google"},
	}

	svc := New(WithBackend(mock))

	// List for REAPER
	reaperPlugins, err := svc.ListPluginWindows(context.Background(), "REAPER")
	if err != nil {
		t.Fatalf("ListPluginWindows REAPER: %v", err)
	}
	if len(reaperPlugins) != 3 { // 2 REAPER FX + 1 AUHosting
		t.Fatalf("expected 3 plugin windows for REAPER, got %d: %+v", len(reaperPlugins), reaperPlugins)
	}
	if reaperPlugins[0].Title != "FX: Track 1 [AmpliTube 5]" || reaperPlugins[0].OwningApp != "REAPER" {
		t.Fatalf("unexpected plugin entry: %+v", reaperPlugins[0])
	}

	// List for Live
	livePlugins, err := svc.ListPluginWindows(context.Background(), "Live")
	if err != nil {
		t.Fatalf("ListPluginWindows Live: %v", err)
	}
	if len(livePlugins) != 2 { // 1 Live device + 1 AUHosting
		t.Fatalf("expected 2 plugin windows for Live, got %d: %+v", len(livePlugins), livePlugins)
	}
	if livePlugins[0].Title != "Wavetable (Track 1)" || livePlugins[0].OwningApp != "Live" {
		t.Fatalf("unexpected plugin entry: %+v", livePlugins[0])
	}
}

func TestContractOperations(t *testing.T) {
	mock := newMockBackend()
	mock.windows = []WindowInfo{
		{ID: 1, PID: 100, Owner: "REAPER", Title: "Mix.rpp - REAPER"},
		{ID: 2, PID: 100, Owner: "REAPER", Title: "FX: Track 3 [Distortion]"},
	}

	svc := New(WithBackend(mock))

	// 1. Contract save_project with consent
	saveRes, err := svc.Execute(context.Background(), core.OpSaveProject, map[string]any{"consent": true, "daw": "REAPER"})
	if err != nil {
		t.Fatalf("Execute OpSaveProject: %v", err)
	}
	readbackMap, ok := saveRes.Readback.(map[string]any)
	if !ok || readbackMap["saved"] != true {
		t.Fatalf("expected saved=true in readback, got: %v", saveRes.Readback)
	}

	// 2. Contract save_project without consent fails
	_, err = svc.Execute(context.Background(), core.OpSaveProject, map[string]any{"consent": false})
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("expected ErrConsentRequired, got: %v", err)
	}

	// 3. Contract list_plugin_windows
	plugRes, err := svc.Execute(context.Background(), core.OpListPluginWindows, map[string]any{"daw": "REAPER"})
	if err != nil {
		t.Fatalf("Execute OpListPluginWindows: %v", err)
	}
	wins, ok := plugRes.Readback.([]PluginWindow)
	if !ok || len(wins) != 1 {
		t.Fatalf("expected 1 PluginWindow in readback, got: %v", plugRes.Readback)
	}
	if wins[0].Title != "FX: Track 3 [Distortion]" || wins[0].OwningApp != "REAPER" {
		t.Fatalf("unexpected readback item: %+v", wins[0])
	}

	// 4. Unknown operation fails
	_, err = svc.Execute(context.Background(), "unknown_operation", nil)
	if err == nil {
		t.Fatal("expected error for unknown op, got nil")
	}
}

func TestHasUnsavedMarker(t *testing.T) {
	cases := []struct {
		title string
		dirty bool
	}{
		{"*Project.rpp - REAPER", true},
		{"Project.rpp* - REAPER", true},
		{"Project.rpp [modified] - REAPER", true},
		{"Project.rpp (modified) - REAPER", true},
		{"● Project - Ableton Live", true},
		{"Project* - Ableton Live", true},
		{"CleanProject.rpp - REAPER", false},
		{"CleanProject - Ableton Live", false},
		{"", false},
	}

	for _, tc := range cases {
		got := HasUnsavedMarker(tc.title)
		if got != tc.dirty {
			t.Errorf("HasUnsavedMarker(%q) = %v; want %v", tc.title, got, tc.dirty)
		}
	}
}
