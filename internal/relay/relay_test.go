// SPDX-License-Identifier: Apache-2.0

package relay

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lin-snow/Ech0-Relay/internal/config"
	"github.com/lin-snow/Ech0-Relay/internal/ech0"
	"github.com/lin-snow/Ech0-Relay/internal/state"
	"github.com/lin-snow/Ech0-Relay/internal/telegram"
)

type fakeScraper struct {
	latest    []telegram.Post
	since     []telegram.Post
	latestErr error
}

func (f *fakeScraper) FetchLatest(context.Context, string) ([]telegram.Post, error) {
	return f.latest, f.latestErr
}
func (f *fakeScraper) FetchSince(context.Context, string, int64) ([]telegram.Post, error) {
	return f.since, nil
}

type fakeClient struct {
	posted  []ech0.EchoRequest
	failOn  int // 1-based index of a post to fail; 0 = never
	tags    []ech0.Tag
	total   int64
	oldest  []ech0.EchoItem
	deleted []string
}

func (f *fakeClient) PostEcho(_ context.Context, req ech0.EchoRequest) error {
	if f.failOn > 0 && len(f.posted)+1 == f.failOn {
		return errors.New("boom")
	}
	f.posted = append(f.posted, req)
	return nil
}
func (f *fakeClient) ListTags(context.Context) ([]ech0.Tag, error) { return f.tags, nil }
func (f *fakeClient) QueryEchos(_ context.Context, _ []string, _, _ string, _, pageSize int) (int64, []ech0.EchoItem, error) {
	if pageSize <= 1 {
		return f.total, nil, nil
	}
	its := f.oldest
	if len(its) > pageSize {
		its = its[:pageSize]
	}
	return f.total, its, nil
}
func (f *fakeClient) DeleteEcho(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func posts(ids ...int64) []telegram.Post {
	out := make([]telegram.Post, len(ids))
	for i, id := range ids {
		out[i] = telegram.Post{ID: id, TextMD: "post", TimeUnix: 1_700_000_000 + id}
	}
	return out
}

// harness builds a one-sync config + state + deps wired to the given fakes.
func harness(t *testing.T, sync config.Sync, scr *fakeScraper, cli *fakeClient) (*config.Config, *state.State, Deps) {
	t.Helper()
	t.Setenv("TOK", "token")
	if sync.MaxPerRun == 0 {
		sync.MaxPerRun = 10
	}
	if sync.MaxDeletePerRun == 0 {
		sync.MaxDeletePerRun = 50
	}
	cfg := &config.Config{
		Instances: map[string]config.Instance{"i": {BaseURL: "http://x", TokenEnv: "TOK"}},
		Syncs:     []config.Sync{sync},
	}
	st, _ := state.Load(filepath.Join(t.TempDir(), "state.json"))
	deps := Deps{
		Scraper:   scr,
		NewClient: func(config.Instance, string) EchoClient { return cli },
	}
	return cfg, st, deps
}

func TestRun_FirstRunSeedsWithoutPosting(t *testing.T) {
	scr := &fakeScraper{latest: posts(10, 11, 12)}
	cli := &fakeClient{}
	cfg, st, deps := harness(t, config.Sync{Name: "i/c", Channel: "c", Instance: "i"}, scr, cli)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	r := sum.Results[0]
	if len(cli.posted) != 0 {
		t.Errorf("seed run must not post, posted %d", len(cli.posted))
	}
	if !r.Seeded || r.NewCursor != 12 {
		t.Errorf("expected seed to cursor 12, got seeded=%v cursor=%d", r.Seeded, r.NewCursor)
	}
	if id, _ := st.Get("i/c"); id != 12 {
		t.Errorf("state cursor = %d, want 12", id)
	}
}

func TestRun_FirstRunBackfillPostsOldest(t *testing.T) {
	scr := &fakeScraper{latest: posts(10, 11, 12)}
	cli := &fakeClient{}
	cfg, st, deps := harness(t, config.Sync{
		Name: "i/c", Channel: "c", Instance: "i",
		BackfillOnFirstRun: true, BackfillLimit: 2,
	}, scr, cli)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	if len(cli.posted) != 2 {
		t.Fatalf("backfill posted %d, want 2 (oldest)", len(cli.posted))
	}
	if sum.Results[0].NewCursor != 11 {
		t.Errorf("cursor = %d, want 11", sum.Results[0].NewCursor)
	}
}

func TestRun_IncrementalCapsMaxPerRun(t *testing.T) {
	scr := &fakeScraper{since: posts(101, 102, 103, 104, 105)}
	cli := &fakeClient{}
	cfg, st, deps := harness(t, config.Sync{Name: "i/c", Channel: "c", Instance: "i", MaxPerRun: 3}, scr, cli)
	st.Set("i/c", 100)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	r := sum.Results[0]
	if r.Found != 5 {
		t.Errorf("Found = %d, want 5", r.Found)
	}
	if len(cli.posted) != 3 || r.NewCursor != 103 {
		t.Errorf("posted %d cursor %d, want 3 posts to cursor 103", len(cli.posted), r.NewCursor)
	}
}

func TestRun_FailureStopsAndPreservesOrder(t *testing.T) {
	scr := &fakeScraper{since: posts(101, 102, 103)}
	cli := &fakeClient{failOn: 2} // second post fails
	cfg, st, deps := harness(t, config.Sync{Name: "i/c", Channel: "c", Instance: "i"}, scr, cli)
	st.Set("i/c", 100)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	r := sum.Results[0]
	if !sum.HardError {
		t.Error("expected HardError")
	}
	if len(cli.posted) != 1 || cli.posted[0].Content == "" {
		t.Errorf("expected only first post to land, got %d", len(cli.posted))
	}
	if r.NewCursor != 101 {
		t.Errorf("cursor = %d, want 101 (stop at failure)", r.NewCursor)
	}
	if id, _ := st.Get("i/c"); id != 101 {
		t.Errorf("state cursor = %d, want 101", id)
	}
}

func TestRun_BackfillsCreatedAt(t *testing.T) {
	scr := &fakeScraper{since: posts(101)}
	cli := &fakeClient{}
	cfg, st, deps := harness(t, config.Sync{Name: "i/c", Channel: "c", Instance: "i"}, scr, cli)
	st.Set("i/c", 100)

	Run(context.Background(), cfg, st, deps, Options{})
	if len(cli.posted) != 1 {
		t.Fatalf("posted %d", len(cli.posted))
	}
	if cli.posted[0].CreatedAt == nil || *cli.posted[0].CreatedAt != 1_700_000_101 {
		t.Errorf("created_at not backfilled from TG time: %v", cli.posted[0].CreatedAt)
	}
}

func TestRun_RetentionRunsAfterCleanPublish(t *testing.T) {
	scr := &fakeScraper{since: posts(101)}
	cli := &fakeClient{
		tags:   []ech0.Tag{{ID: "t1", Name: "src"}},
		total:  5,
		oldest: []ech0.EchoItem{{ID: "old1"}, {ID: "old2"}},
	}
	cfg, st, deps := harness(t, config.Sync{
		Name: "i/c", Channel: "c", Instance: "i", Tag: "src", Keep: 3,
	}, scr, cli)
	st.Set("i/c", 100)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	if sum.Results[0].Deleted != 2 {
		t.Errorf("Deleted = %d, want 2", sum.Results[0].Deleted)
	}
	if len(cli.deleted) != 2 {
		t.Errorf("DeleteEcho calls = %d, want 2", len(cli.deleted))
	}
}

func TestRun_DryRunTouchesNothing(t *testing.T) {
	scr := &fakeScraper{since: posts(101, 102)}
	cli := &fakeClient{tags: []ech0.Tag{{ID: "t1", Name: "src"}}, total: 100, oldest: []ech0.EchoItem{{ID: "o"}}}
	cfg, st, deps := harness(t, config.Sync{
		Name: "i/c", Channel: "c", Instance: "i", Tag: "src", Keep: 1,
	}, scr, cli)
	st.Set("i/c", 100)

	sum := Run(context.Background(), cfg, st, deps, Options{DryRun: true})
	if len(cli.posted) != 0 || len(cli.deleted) != 0 {
		t.Errorf("dry-run posted %d deleted %d, want 0/0", len(cli.posted), len(cli.deleted))
	}
	if sum.Results[0].Posted != 2 {
		t.Errorf("dry-run would-post = %d, want 2", sum.Results[0].Posted)
	}
	// state cursor must remain at 100 in dry-run
	if id, _ := st.Get("i/c"); id != 100 {
		t.Errorf("dry-run advanced cursor to %d", id)
	}
}

func TestRun_MissingTokenIsHardError(t *testing.T) {
	scr := &fakeScraper{since: posts(101)}
	cli := &fakeClient{}
	cfg, st, deps := harness(t, config.Sync{Name: "i/c", Channel: "c", Instance: "i"}, scr, cli)
	// Override the token env to empty.
	t.Setenv("TOK", "")
	st.Set("i/c", 100)

	sum := Run(context.Background(), cfg, st, deps, Options{})
	if !sum.HardError || sum.Results[0].Err == nil {
		t.Error("expected hard error for missing token")
	}
}

func TestBuildContent(t *testing.T) {
	s := config.Sync{Channel: "chan", WithSourceLink: true}
	got := buildContent(s, telegram.Post{ID: 7, TextMD: "hello", ImageURLs: []string{"http://a.jpg", "http://b.jpg"}})
	want := "hello\n\n![](http://a.jpg)\n![](http://b.jpg)\n\n🔗 https://t.me/chan/7"
	if got != want {
		t.Errorf("buildContent:\n got  %q\n want %q", got, want)
	}

	// Media-only (no text).
	got = buildContent(config.Sync{Channel: "chan"}, telegram.Post{ID: 8, ImageURLs: []string{"http://a.jpg"}})
	if got != "![](http://a.jpg)" {
		t.Errorf("media-only content = %q", got)
	}
}
