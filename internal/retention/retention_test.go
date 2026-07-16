// SPDX-License-Identifier: Apache-2.0

package retention

import (
	"context"
	"testing"

	"github.com/lin-snow/Ech0-Relay/internal/ech0"
)

// fakeAPI is a scripted EchoAPI for tests.
type fakeAPI struct {
	tags    []ech0.Tag
	total   int64
	oldest  []ech0.EchoItem // returned for the "fetch oldest" query
	deleted []string
	listErr error
}

func (f *fakeAPI) ListTags(_ context.Context) ([]ech0.Tag, error) {
	return f.tags, f.listErr
}

func (f *fakeAPI) QueryEchos(_ context.Context, _ []string, _, _ string, _, pageSize int) (int64, []ech0.EchoItem, error) {
	if pageSize <= 1 {
		return f.total, nil, nil // count-only query
	}
	items := f.oldest
	if len(items) > pageSize {
		items = items[:pageSize]
	}
	return f.total, items, nil
}

func (f *fakeAPI) DeleteEcho(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func items(ids ...string) []ech0.EchoItem {
	out := make([]ech0.EchoItem, len(ids))
	for i, id := range ids {
		out[i] = ech0.EchoItem{ID: id, CreatedAt: int64(i)}
	}
	return out
}

func TestApply_DeletesOldestBeyondKeep(t *testing.T) {
	api := &fakeAPI{
		tags:   []ech0.Tag{{ID: "t1", Name: "src"}},
		total:  5,
		oldest: items("a", "b"), // 5 total, keep 3 => delete 2 oldest
	}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 3, MaxDeletePerRun: 50}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", sum.Deleted)
	}
	if len(api.deleted) != 2 || api.deleted[0] != "a" || api.deleted[1] != "b" {
		t.Errorf("deleted = %v, want [a b]", api.deleted)
	}
}

func TestApply_MaxDeletePerRunCaps(t *testing.T) {
	api := &fakeAPI{
		tags:   []ech0.Tag{{ID: "t1", Name: "src"}},
		total:  100,
		oldest: items("a", "b", "c", "d", "e"),
	}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 10, MaxDeletePerRun: 3}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 3 {
		t.Errorf("Deleted = %d, want 3 (capped)", sum.Deleted)
	}
	if len(api.deleted) != 3 {
		t.Errorf("deleted count = %d, want 3", len(api.deleted))
	}
}

func TestApply_UnderCapDeletesNothing(t *testing.T) {
	api := &fakeAPI{tags: []ech0.Tag{{ID: "t1", Name: "src"}}, total: 2}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 10, MaxDeletePerRun: 50}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 0 || len(api.deleted) != 0 {
		t.Errorf("expected no deletions, got %d", sum.Deleted)
	}
}

func TestApply_DryRunDeletesNothing(t *testing.T) {
	api := &fakeAPI{
		tags:   []ech0.Tag{{ID: "t1", Name: "src"}},
		total:  5,
		oldest: items("a", "b"),
	}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 3, MaxDeletePerRun: 50}, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 2 {
		t.Errorf("dry-run Deleted (would-delete) = %d, want 2", sum.Deleted)
	}
	if len(api.deleted) != 0 {
		t.Errorf("dry-run must not call DeleteEcho, got %v", api.deleted)
	}
}

func TestApply_MissingTagDeletesNothing(t *testing.T) {
	api := &fakeAPI{tags: []ech0.Tag{{ID: "t1", Name: "other"}}, total: 999, oldest: items("a")}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 1, MaxDeletePerRun: 50}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 0 || len(api.deleted) != 0 {
		t.Error("unresolved tag must delete nothing")
	}
}

func TestApply_NoTagIsRefused(t *testing.T) {
	api := &fakeAPI{total: 100, oldest: items("a")}
	_, err := Apply(context.Background(), api, Config{Tag: "", Keep: 1, MaxDeletePerRun: 50}, false)
	if err == nil {
		t.Fatal("expected refusal when tag is empty")
	}
	if len(api.deleted) != 0 {
		t.Error("must not delete when refusing")
	}
}

func TestApply_DisabledWhenKeepZero(t *testing.T) {
	api := &fakeAPI{tags: []ech0.Tag{{ID: "t1", Name: "src"}}, total: 100, oldest: items("a")}
	sum, err := Apply(context.Background(), api, Config{Tag: "src", Keep: 0}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sum.Deleted != 0 || len(api.deleted) != 0 {
		t.Error("keep=0 must be a no-op")
	}
}
