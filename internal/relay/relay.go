// SPDX-License-Identifier: Apache-2.0

// Package relay orchestrates the sync: for each configured job it scrapes the
// channel, publishes new posts (oldest first, backdated to the original post
// time), advances the cursor, and applies retention.
package relay

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"

	"github.com/lin-snow/Ech0-Relay/internal/config"
	"github.com/lin-snow/Ech0-Relay/internal/ech0"
	"github.com/lin-snow/Ech0-Relay/internal/retention"
	"github.com/lin-snow/Ech0-Relay/internal/state"
	"github.com/lin-snow/Ech0-Relay/internal/telegram"
)

// Scraper is the Telegram source (interface for testability).
type Scraper interface {
	FetchLatest(ctx context.Context, channel string) ([]telegram.Post, error)
	FetchSince(ctx context.Context, channel string, sinceID int64) ([]telegram.Post, error)
	DownloadImage(ctx context.Context, url string) ([]byte, error)
}

// EchoClient is everything the relay needs from an Ech0 instance: posting,
// image re-hosting, plus the retention read/delete surface.
type EchoClient interface {
	PostEcho(ctx context.Context, req ech0.EchoRequest) error
	UploadImage(ctx context.Context, filename string, data []byte) (ech0.FileDto, error)
	DeleteFile(ctx context.Context, id string) error
	retention.EchoAPI
}

// Deps are the injected collaborators.
type Deps struct {
	Scraper   Scraper
	NewClient func(inst config.Instance, token string) EchoClient
	Logger    *slog.Logger
}

// Options tune a run.
type Options struct {
	DryRun   bool
	OnlySync string // if set, run only the sync with this name
}

// SyncResult reports one sync's outcome.
type SyncResult struct {
	Name      string
	Channel   string
	Found     int // new postable posts discovered (before max_per_run cap)
	Posted    int
	Failed    int
	Deleted   int
	Seeded    bool // first run: cursor seeded without posting history
	OldCursor int64
	NewCursor int64
	Err       error
}

// Summary aggregates all sync results.
type Summary struct {
	Results   []SyncResult
	HardError bool // any sync errored or had a failed post => process exits non-zero
}

// Run executes all configured syncs (or just Options.OnlySync). It mutates st
// in place; the caller persists it. Run never returns an error itself — each
// sync's failure is captured in its SyncResult and reflected in HardError.
func Run(ctx context.Context, cfg *config.Config, st *state.State, deps Deps, opts Options) Summary {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	var sum Summary
	for _, s := range cfg.Syncs {
		if opts.OnlySync != "" && s.Name != opts.OnlySync {
			continue
		}
		res := runSync(ctx, s, cfg, st, deps, opts)
		if res.Err != nil || res.Failed > 0 {
			sum.HardError = true
		}
		sum.Results = append(sum.Results, res)
	}
	return sum
}

func runSync(ctx context.Context, s config.Sync, cfg *config.Config, st *state.State, deps Deps, opts Options) SyncResult {
	log := deps.Logger.With("module", "relay", "sync", s.Name, "channel", s.Channel)
	res := SyncResult{Name: s.Name, Channel: s.Channel}

	inst := cfg.Instances[s.Instance] // existence guaranteed by config.Validate
	token := inst.Token()
	if token == "" && !opts.DryRun {
		// Dry-run only scrapes and renders, so a token is not required there.
		res.Err = fmt.Errorf("missing access token: env %s is empty", inst.TokenEnv)
		log.Error("missing token", "token_env", inst.TokenEnv)
		return res
	}
	client := deps.NewClient(inst, token)

	oldCursor, hasCursor := st.Get(s.Name)
	res.OldCursor = oldCursor
	finalCursor := oldCursor

	toPublish, seedCursor, err := deps.gatherPosts(ctx, s, oldCursor, hasCursor, log)
	if err != nil {
		res.Err = err
		return res
	}
	if seedCursor > 0 {
		res.Seeded = true
		finalCursor = seedCursor
		if !opts.DryRun {
			st.Set(s.Name, seedCursor)
		}
	}
	res.Found = len(toPublish)

	// Cap at max_per_run, keeping the oldest (posts are ascending). The backlog
	// drains across successive runs as the cursor advances.
	if s.MaxPerRun > 0 && len(toPublish) > s.MaxPerRun {
		toPublish = toPublish[:s.MaxPerRun]
	}

	for _, p := range toPublish {
		if opts.DryRun {
			content := buildContent(s, p, p.ImageURLs)
			if s.UploadImages && len(p.ImageURLs) > 0 {
				log.Info("dry-run: would re-host images", "id", p.ID, "count", len(p.ImageURLs))
			}
			log.Info("dry-run: would post", "id", p.ID, "chars", len(content))
			res.Posted++
			finalCursor = p.ID
			continue
		}

		// With upload_images on, images that re-host successfully attach as echo
		// files; the rest stay as CDN links in the content so no image is lost.
		linkImages := p.ImageURLs
		var files []ech0.EchoFileRef
		if s.UploadImages && len(p.ImageURLs) > 0 {
			files, linkImages = uploadImages(ctx, s, p, deps.Scraper, client, log)
		}
		req := buildRequest(s, p, buildContent(s, p, linkImages))
		if len(files) > 0 {
			req.EchoFiles = files
			req.Layout = "waterfall"
		}

		if err := client.PostEcho(ctx, req); err != nil {
			// Stop at the first failure to preserve order and avoid gaps; the
			// failed post and everything after it retry next run. Uploads whose
			// echo never landed are deleted best-effort (Ech0 would GC them
			// after 24h anyway).
			for _, f := range files {
				if derr := client.DeleteFile(ctx, f.FileID); derr != nil {
					log.Warn("cleanup of uploaded file failed", "file_id", f.FileID, "err", derr)
				}
			}
			res.Failed++
			res.Err = err
			log.Error("post failed", "id", p.ID, "err", err)
			break
		}
		res.Posted++
		finalCursor = p.ID
		st.Set(s.Name, p.ID)
		log.Info("posted", "id", p.ID, "images_rehosted", len(files))
	}
	res.NewCursor = finalCursor

	// Retention only when the publish phase was clean, so a posting outage does
	// not race deletions.
	if s.Keep > 0 && res.Err == nil {
		rsum, rerr := retention.Apply(ctx, client, retention.Config{
			Tag:             s.Tag,
			Keep:            s.Keep,
			MaxDeletePerRun: s.MaxDeletePerRun,
		}, opts.DryRun)
		if rerr != nil {
			res.Err = rerr
			log.Error("retention failed", "err", rerr)
		} else {
			res.Deleted = rsum.Deleted
			if rsum.Deleted > 0 {
				log.Info("retention pruned", "deleted", rsum.Deleted, "total", rsum.Total, "keep", s.Keep, "dry_run", opts.DryRun)
			}
		}
	}
	return res
}

// gatherPosts decides what to publish (ascending). On first run it returns
// either a seedCursor (> 0, seed the cursor without posting history — the
// default) or the oldest backfill window. State writes happen in the caller so
// dry-run stays side-effect free.
func (deps Deps) gatherPosts(ctx context.Context, s config.Sync, oldCursor int64, hasCursor bool, log *slog.Logger) (posts []telegram.Post, seedCursor int64, err error) {
	if hasCursor {
		p, err := deps.Scraper.FetchSince(ctx, s.Channel, oldCursor)
		return p, 0, err
	}

	latest, err := deps.Scraper.FetchLatest(ctx, s.Channel)
	if err != nil {
		return nil, 0, err
	}
	if len(latest) == 0 {
		log.Warn("first run: no postable posts to seed from; leaving cursor unset")
		return nil, 0, nil
	}
	if s.BackfillOnFirstRun {
		n := s.BackfillLimit
		if n > len(latest) {
			n = len(latest)
		}
		log.Info("first run: backfilling", "count", n)
		return latest[:n], 0, nil // ascending => oldest window
	}

	maxID := latest[len(latest)-1].ID
	log.Info("first run: seeding cursor without backfill", "cursor", maxID)
	return nil, maxID, nil
}

// uploadImages re-hosts a post's images on the instance: download from the
// Telegram CDN, upload via the file API. Failed images are returned as
// fallback URLs so the caller can keep them as CDN links in the content —
// a degraded post beats a wedged sync.
func uploadImages(ctx context.Context, s config.Sync, p telegram.Post, scr Scraper, client EchoClient, log *slog.Logger) (files []ech0.EchoFileRef, fallback []string) {
	for i, u := range p.ImageURLs {
		data, err := scr.DownloadImage(ctx, u)
		if err != nil {
			log.Warn("image download failed; falling back to CDN link", "id", p.ID, "url", u, "err", err)
			fallback = append(fallback, u)
			continue
		}
		dto, err := client.UploadImage(ctx, imageFilename(s.Channel, p.ID, i, u), data)
		if err != nil {
			log.Warn("image upload failed; falling back to CDN link", "id", p.ID, "url", u, "err", err)
			fallback = append(fallback, u)
			continue
		}
		files = append(files, ech0.EchoFileRef{FileID: dto.ID, SortOrder: i})
	}
	return files, fallback
}

// imageExts is Ech0's upload whitelist for images.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
}

// imageFilename derives an upload filename whose extension passes Ech0's
// validation, defaulting to .jpg (Telegram photo CDN serves JPEG).
func imageFilename(channel string, postID int64, idx int, url string) string {
	if q := strings.IndexByte(url, '?'); q >= 0 {
		url = url[:q]
	}
	ext := strings.ToLower(path.Ext(url))
	if !imageExts[ext] {
		ext = ".jpg"
	}
	return fmt.Sprintf("%s-%d-%d%s", channel, postID, idx, ext)
}

// buildContent renders the echo text. imageURLs are the images to embed as
// markdown links: all post images in link mode, only re-host failures in
// upload mode (successfully uploaded images attach as echo files instead).
func buildContent(s config.Sync, p telegram.Post, imageURLs []string) string {
	var parts []string
	if p.TextMD != "" {
		parts = append(parts, p.TextMD)
	}
	if len(imageURLs) > 0 {
		var imgs strings.Builder
		for i, u := range imageURLs {
			if i > 0 {
				imgs.WriteByte('\n')
			}
			imgs.WriteString("![](")
			imgs.WriteString(u)
			imgs.WriteString(")")
		}
		parts = append(parts, imgs.String())
	}
	if s.WithSourceLink {
		parts = append(parts, "🔗 https://t.me/"+s.Channel+"/"+strconv.FormatInt(p.ID, 10))
	}
	return strings.Join(parts, "\n\n")
}

func buildRequest(s config.Sync, p telegram.Post, content string) ech0.EchoRequest {
	req := ech0.EchoRequest{
		Content: content,
		Private: s.Private,
	}
	if s.Tag != "" {
		req.Tags = []ech0.TagRef{{Name: s.Tag}}
	}
	if p.TimeUnix > 0 {
		ts := p.TimeUnix
		req.CreatedAt = &ts
	}
	return req
}
