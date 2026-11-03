package oslayer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

// KnownDAWNames contains standard DAW process/owner names on macOS.
var KnownDAWNames = []string{"REAPER", "Live", "Ableton Live"}

// Service provides OS-level DAW integration including Accessibility-based project saving
// and open plugin window discovery.
type Service struct {
	backend      Backend
	targetDAW    string
	consent      bool
	prompt       func() (bool, error)
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// Option configures Service instances.
type Option func(*Service)

// WithBackend overrides the low-level OS backend.
func WithBackend(b Backend) Option {
	return func(s *Service) { s.backend = b }
}

// WithDAW sets the default target DAW application name.
func WithDAW(daw string) Option {
	return func(s *Service) { s.targetDAW = daw }
}

// WithConsent sets whether user consent is pre-granted.
func WithConsent(consent bool) Option {
	return func(s *Service) { s.consent = consent }
}

// WithPrompt provides an interactive prompt callback for user consent.
func WithPrompt(prompt func() (bool, error)) Option {
	return func(s *Service) { s.prompt = prompt }
}

// WithPollParams configures verification polling interval and timeout.
func WithPollParams(interval, timeout time.Duration) Option {
	return func(s *Service) {
		s.pollInterval = interval
		s.pollTimeout = timeout
	}
}

// New creates a new oslayer Service.
func New(opts ...Option) *Service {
	s := &Service{
		backend:      defaultBackend(),
		pollInterval: 50 * time.Millisecond,
		pollTimeout:  2 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// SaveOptions configures an individual SaveProject invocation.
type SaveOptions struct {
	Consent bool
	DAW     string
}

// SaveOption modifies SaveOptions.
type SaveOption func(*SaveOptions)

// WithSaveConsent sets the consent flag for SaveProject.
func WithSaveConsent(consent bool) SaveOption {
	return func(o *SaveOptions) { o.Consent = consent }
}

// WithSaveDAW sets the target DAW for SaveProject.
func WithSaveDAW(daw string) SaveOption {
	return func(o *SaveOptions) { o.DAW = daw }
}

// SaveProject sends Cmd+S to the DAW window via macOS Accessibility after user consent,
// and verifies that the window title loses its unsaved marker.
func (s *Service) SaveProject(ctx context.Context, opts ...SaveOption) error {
	so := SaveOptions{
		Consent: s.consent,
		DAW:     s.targetDAW,
	}
	for _, opt := range opts {
		opt(&so)
	}

	// 1. Check user consent
	hasConsent := so.Consent
	if !hasConsent && s.prompt != nil {
		granted, err := s.prompt()
		if err != nil {
			return fmt.Errorf("consent prompt failed: %w", err)
		}
		hasConsent = granted
	}
	if !hasConsent {
		return ErrConsentRequired
	}

	// 2. Verify Accessibility permission
	if err := s.backend.CheckAccessibility(); err != nil {
		return err
	}

	// 3. Find DAW main project window
	win, err := s.findDAWMainWindow(so.DAW)
	if err != nil {
		return err
	}

	// Check initial title state
	initialTitle := win.Title
	if initialTitle == "" {
		if t, err := s.backend.GetWindowTitle(win.PID, win.ID); err == nil && t != "" {
			initialTitle = t
		}
	}
	originallyUnsaved := HasUnsavedMarker(initialTitle)

	// 4. Send Cmd+S via Accessibility
	if err := s.backend.Activate(win.PID); err != nil {
		// Non-fatal, proceed with sending key event
	}
	if err := s.backend.SendCmdS(win.PID); err != nil {
		return fmt.Errorf("failed to send Cmd+S: %w", err)
	}

	// 5. Verify window title loses its unsaved marker
	timeout := s.pollTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}

	start := time.Now()
	var lastTitle string
	for {
		title, err := s.backend.GetWindowTitle(win.PID, win.ID)
		if err == nil && title != "" {
			lastTitle = title
			if !HasUnsavedMarker(title) {
				return nil
			}
		}

		if time.Since(start) >= timeout {
			break
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if originallyUnsaved && HasUnsavedMarker(lastTitle) {
		return fmt.Errorf("%w: title %q still contains unsaved marker", ErrSaveVerificationFailed, lastTitle)
	}
	return nil
}

// ListPluginWindows lists open plugin windows in the DAW, returning their title and owning app.
func (s *Service) ListPluginWindows(ctx context.Context, dawApp ...string) ([]PluginWindow, error) {
	target := s.targetDAW
	if len(dawApp) > 0 && dawApp[0] != "" {
		target = dawApp[0]
	}

	windows, err := s.backend.ListWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to list windows: %w", err)
	}

	var results []PluginWindow
	seen := make(map[string]bool)
	for _, w := range windows {
		if s.isPluginWindow(w, target) {
			key := w.Owner + "::" + w.Title
			if seen[key] {
				continue
			}
			seen[key] = true
			results = append(results, PluginWindow{
				Title:     w.Title,
				OwningApp: w.Owner,
			})
		}
	}
	return results, nil
}

// Execute executes an operation defined by the contract.
func (s *Service) Execute(ctx context.Context, op string, args any) (core.ContractResult, error) {
	normOp := strings.ToLower(strings.TrimSpace(op))
	m := toMap(args)

	switch normOp {
	case core.OpSaveProject:
		var opts []SaveOption
		if c, ok := m["consent"].(bool); ok {
			opts = append(opts, WithSaveConsent(c))
		}
		if daw, ok := m["daw"].(string); ok && daw != "" {
			opts = append(opts, WithSaveDAW(daw))
		}
		if err := s.SaveProject(ctx, opts...); err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "save_project", "consent": true},
			Readback: map[string]any{"saved": true},
		}, nil

	case core.OpPluginWindows, core.OpListPluginWindows, "list_plugins_windows":
		var daw string
		if d, ok := m["daw"].(string); ok {
			daw = d
		}
		wins, err := s.ListPluginWindows(ctx, daw)
		if err != nil {
			return core.ContractResult{}, err
		}
		return core.ContractResult{
			Applied:  map[string]any{"action": "list_plugin_windows"},
			Readback: wins,
		}, nil

	default:
		return core.ContractResult{}, fmt.Errorf("unsupported oslayer operation: %s", op)
	}
}

func (s *Service) findDAWMainWindow(target string) (WindowInfo, error) {
	windows, err := s.backend.ListWindows()
	if err != nil {
		return WindowInfo{}, fmt.Errorf("failed to list windows: %w", err)
	}

	for _, w := range windows {
		if s.isTargetDAW(w.Owner, target) {
			if s.isMainWindow(w) {
				return w, nil
			}
		}
	}
	return WindowInfo{}, ErrNoDAWFound
}

func (s *Service) isTargetDAW(owner string, target string) bool {
	if target != "" {
		return strings.EqualFold(owner, target)
	}
	for _, k := range KnownDAWNames {
		if strings.EqualFold(owner, k) {
			return true
		}
	}
	return false
}

func (s *Service) isMainWindow(w WindowInfo) bool {
	title := strings.TrimSpace(w.Title)
	owner := strings.ToLower(w.Owner)

	if strings.Contains(owner, "reaper") {
		// REAPER main window title has .rpp or "REAPER" and does not start with "FX: "
		if strings.HasPrefix(title, "FX: ") {
			return false
		}
		return strings.Contains(title, "REAPER") || strings.Contains(title, ".rpp") || title != ""
	}

	if strings.Contains(owner, "live") {
		// Ableton Live main window title ends with "- Ableton Live"
		if strings.Contains(title, "Ableton Live") {
			return true
		}
		// Skip known dialogs/utility windows
		if title == "" || title == "Preferences" || title == "Live Help" || title == "Help" {
			return false
		}
		return true
	}

	return title != ""
}

func (s *Service) isPluginWindow(w WindowInfo, target string) bool {
	title := strings.TrimSpace(w.Title)
	owner := strings.TrimSpace(w.Owner)
	if title == "" {
		return false
	}

	ownerLower := strings.ToLower(owner)

	// External plugin host processes associated with DAWs
	if strings.Contains(ownerLower, "auhosting") || strings.Contains(ownerLower, "plugin-host") || strings.Contains(ownerLower, "pluginhost") {
		return true
	}

	if !s.isTargetDAW(owner, target) {
		return false
	}

	if strings.Contains(ownerLower, "reaper") {
		// REAPER plugin windows start with "FX: " or contain plugin descriptors
		if strings.HasPrefix(title, "FX: ") {
			return true
		}
		if strings.Contains(title, "VST") || strings.Contains(title, "AU:") || strings.Contains(title, "JS:") || strings.Contains(title, "CLAP:") {
			return true
		}
		return false
	}

	if strings.Contains(ownerLower, "live") {
		// In Ableton Live, the main project window contains "- Ableton Live"
		if strings.Contains(title, "- Ableton Live") {
			return false
		}
		if title == "Live" || title == "Preferences" || title == "Live Help" || title == "Help" || title == "Audio" || title == "MIDI" {
			return false
		}
		// Floating plugin windows are identified by their device/plugin title
		return true
	}

	return false
}

func toMap(args any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	if m, ok := args.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
