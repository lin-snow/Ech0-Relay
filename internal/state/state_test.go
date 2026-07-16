// SPDX-License-Identifier: Apache-2.0

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Syncs) != 0 {
		t.Errorf("expected empty syncs, got %v", s.Syncs)
	}
	if _, ok := s.Get("any"); ok {
		t.Error("expected no cursor for unknown key")
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	s, _ := Load(path)
	s.Set("myblog/chan", 100)

	if !s.Dirty() {
		t.Error("expected dirty after Set")
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s.Dirty() {
		t.Error("expected clean after Save")
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	id, ok := got.Get("myblog/chan")
	if !ok || id != 100 {
		t.Errorf("reloaded cursor = %d,%v want 100,true", id, ok)
	}

	// File ends with a newline for clean diffs.
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Error("state file should end with newline")
	}
}

func TestSet_NeverGoesBackwards(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "s.json"))
	s.Set("k", 200)
	s.Set("k", 150) // stale/older; must be ignored
	if id, _ := s.Get("k"); id != 200 {
		t.Errorf("cursor = %d, want 200 (no backwards)", id)
	}
	s.Set("k", 250)
	if id, _ := s.Get("k"); id != 250 {
		t.Errorf("cursor = %d, want 250", id)
	}
}

func TestSet_NoChangeStaysClean(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "s.json"))
	s.Set("k", 10)
	_ = s.Save(filepath.Join(t.TempDir(), "s.json"))
	// Setting an equal/older id should not re-dirty.
	s.Set("k", 10)
	if s.Dirty() {
		t.Error("equal Set must not mark dirty")
	}
}
