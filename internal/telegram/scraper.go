// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// ErrNoMessages means the page returned HTTP 200 but contained no message
// bubbles at all — typically a channel with the web preview disabled, a
// non-existent channel, or a rate-limited/blocked response. It is distinct from
// "the channel has messages but none newer than the cursor".
var ErrNoMessages = errors.New("telegram: no message bubbles on page (preview disabled, blocked, or empty channel)")

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Scraper fetches and parses public channel pages from t.me/s.
type Scraper struct {
	HTTP     *http.Client
	BaseURL  string        // default "https://t.me/s"
	UA       string        // default a desktop Chrome UA
	PageGap  time.Duration // politeness delay between page fetches
	MaxPages int           // safety cap on backward pagination
	Retries  int           // network/5xx/429 retry attempts
	Logger   *slog.Logger
}

// NewScraper returns a Scraper with sane defaults.
func NewScraper() *Scraper {
	return &Scraper{
		HTTP:     &http.Client{Timeout: 20 * time.Second},
		BaseURL:  "https://t.me/s",
		UA:       defaultUA,
		PageGap:  700 * time.Millisecond,
		MaxPages: 12,
		Retries:  3,
		Logger:   slog.Default(),
	}
}

// FetchLatest returns the newest page of postable posts (ascending, no paging).
// Used on first run to seed the cursor / decide backfill. Returns ErrNoMessages
// when the page has no message bubbles.
func (s *Scraper) FetchLatest(ctx context.Context, channel string) ([]Post, error) {
	posts, _, rawCount, err := s.fetchPage(ctx, channel, 0)
	if err != nil {
		return nil, err
	}
	if rawCount == 0 {
		return nil, ErrNoMessages
	}
	return posts, nil
}

// FetchSince returns all postable posts with ID > sinceID, ascending, paging
// backward up to MaxPages. Used for incremental runs. An empty result is normal
// (no new posts); it returns ErrNoMessages only when the first page has no
// bubbles at all.
func (s *Scraper) FetchSince(ctx context.Context, channel string, sinceID int64) ([]Post, error) {
	seen := make(map[int64]bool)
	var collected []Post
	var before int64

	for page := 0; page < s.MaxPages; page++ {
		posts, dataBefore, rawCount, err := s.fetchPage(ctx, channel, before)
		if err != nil {
			return nil, err
		}
		if page == 0 && rawCount == 0 {
			return nil, ErrNoMessages
		}

		reachedOld := false
		var minID int64
		for _, p := range posts {
			if minID == 0 || p.ID < minID {
				minID = p.ID
			}
			if p.ID <= sinceID {
				reachedOld = true
				continue
			}
			if !seen[p.ID] {
				seen[p.ID] = true
				collected = append(collected, p)
			}
		}

		if reachedOld || len(posts) == 0 || dataBefore == 0 {
			break
		}
		// Advance to the older page. Prefer the smaller of the page's data-before
		// and the smallest id we saw, and guard against making no progress.
		next := dataBefore
		if minID > 0 && minID < next {
			next = minID
		}
		if next == before || next == 0 {
			break
		}
		before = next

		if s.PageGap > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(s.PageGap):
			}
		}
	}

	sort.Slice(collected, func(i, j int) bool { return collected[i].ID < collected[j].ID })
	return collected, nil
}

// fetchPage GETs one page and parses it.
func (s *Scraper) fetchPage(ctx context.Context, channel string, before int64) ([]Post, int64, int, error) {
	url := s.BaseURL + "/" + channel
	if before > 0 {
		url += "?before=" + strconv.FormatInt(before, 10)
	}
	body, err := s.get(ctx, url)
	if err != nil {
		return nil, 0, 0, err
	}
	defer body.Close()
	return ParsePage(body)
}

// get performs an HTTP GET with retry/backoff on network errors, 429 and 5xx.
func (s *Scraper) get(ctx context.Context, url string) (io.ReadCloser, error) {
	var lastErr error
	attempts := s.Retries
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err // non-retryable
		}
		req.Header.Set("User-Agent", s.UA)
		req.Header.Set("Accept-Language", "en,zh-CN;q=0.9")

		resp, err := s.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp.Body, nil
		}
		// Drain and close before deciding.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("telegram: GET %s: status %d", url, resp.StatusCode)
			continue
		}
		return nil, fmt.Errorf("telegram: GET %s: status %d", url, resp.StatusCode) // non-retryable
	}
	return nil, fmt.Errorf("telegram: GET %s failed after %d attempts: %w", url, attempts, lastErr)
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second // 1s, 2s, 4s...
	if d > 15*time.Second {
		d = 15 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
