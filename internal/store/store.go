// Package store keeps the agent's files under one directory:
// profiles/<plugin>.json (learned plugins) and sessions/<id>.jsonl (every
// event of a session, the evidence behind each change).
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/core"
)

type Store struct {
	Dir string
	mu  sync.Mutex
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func (s *Store) profilePath(plugin string) string {
	return filepath.Join(s.Dir, "profiles", unsafe.ReplaceAllString(plugin, "_")+".json")
}

func (s *Store) SaveProfile(p core.Profile) error {
	path := s.profilePath(p.Plugin)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Profile returns nil, nil when the plugin has not been learned.
func (s *Store) Profile(plugin string) (*core.Profile, error) {
	b, err := os.ReadFile(s.profilePath(plugin))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p core.Profile
	return &p, json.Unmarshal(b, &p)
}

func (s *Store) Append(session string, e core.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, session+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(e)
}

// Profiles lists every learned plugin.
func (s *Store) Profiles() ([]core.Profile, error) {
	files, err := filepath.Glob(filepath.Join(s.Dir, "profiles", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []core.Profile
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p core.Profile
		if json.Unmarshal(b, &p) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) snapshotPath(name string) string {
	return filepath.Join(s.Dir, "snapshots", unsafe.ReplaceAllString(name, "_")+".json")
}

func (s *Store) SaveSnapshot(sn core.Snapshot) error {
	path := s.snapshotPath(sn.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sn, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Snapshot returns nil, nil when there is none by that name.
func (s *Store) Snapshot(name string) (*core.Snapshot, error) {
	b, err := os.ReadFile(s.snapshotPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sn core.Snapshot
	return &sn, json.Unmarshal(b, &sn)
}

// Snapshots lists saved tones, newest first.
func (s *Store) Snapshots() ([]core.Snapshot, error) {
	files, _ := filepath.Glob(filepath.Join(s.Dir, "snapshots", "*.json"))
	var out []core.Snapshot
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var sn core.Snapshot
		if json.Unmarshal(b, &sn) == nil {
			out = append(out, sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Saved.After(out[j].Saved) })
	return out, nil
}

func (s *Store) Taste() (core.Taste, error) {
	var t core.Taste
	b, err := os.ReadFile(filepath.Join(s.Dir, "taste.json"))
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(b, &t)
}

func (s *Store) SaveTaste(t core.Taste) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "taste.json"), b, 0o644)
}
