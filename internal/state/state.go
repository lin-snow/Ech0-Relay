// SPDX-License-Identifier: Apache-2.0

// Package state persists per-sync dedup cursors to a JSON file that is
// committed back to the repository by the CI workflow. Each sync records the
// id of the last message successfully relayed; the next run only considers
// messages with a larger id.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const currentVersion = 1

// SyncState is one sync's cursor.
type SyncState struct {
	LastMessageID int64  `json:"last_message_id"`
	UpdatedAt     string `json:"updated_at"`
}

// State is the whole cursor file, keyed by sync name.
type State struct {
	Version int                  `json:"version"`
	Syncs   map[string]SyncState `json:"syncs"`

	dirty bool // in-memory; true when a cursor changed since load/save
}

// Load reads the state file. A missing file yields an empty State (not an error)
// so the first run starts clean.
func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{Version: currentVersion, Syncs: map[string]SyncState{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", path, err)
	}
	if s.Syncs == nil {
		s.Syncs = map[string]SyncState{}
	}
	if s.Version == 0 {
		s.Version = currentVersion
	}
	return &s, nil
}

// Get returns the last relayed id for key and whether a cursor exists.
func (s *State) Get(key string) (int64, bool) {
	v, ok := s.Syncs[key]
	return v.LastMessageID, ok
}

// Set records id as the cursor for key (stamping UpdatedAt) if it advances the
// cursor. It never moves a cursor backwards. Marks the state dirty on change.
func (s *State) Set(key string, id int64) {
	cur, ok := s.Syncs[key]
	if ok && id <= cur.LastMessageID {
		return
	}
	s.Syncs[key] = SyncState{
		LastMessageID: id,
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	s.dirty = true
}

// Dirty reports whether any cursor changed since load or the last Save.
func (s *State) Dirty() bool { return s.dirty }

// Save atomically writes the state to path (write temp + rename) with a stable,
// indented layout and trailing newline so committed diffs stay clean.
func (s *State) Save(path string) error {
	if s.Version == 0 {
		s.Version = currentVersion
	}
	buf, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	buf = append(buf, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("state: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename into place: %w", err)
	}
	s.dirty = false
	return nil
}
