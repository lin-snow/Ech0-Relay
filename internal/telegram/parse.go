// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Post is a single parsed Telegram channel message.
type Post struct {
	// ID is the numeric message id (the part after "/" in data-post). It is
	// monotonically increasing per channel and is the dedup / cursor key.
	ID int64
	// TextMD is the message text converted to markdown ("" for media-only posts).
	TextMD string
	// TimeUnix is the post time in Unix seconds (0 if the timestamp was absent).
	TimeUnix int64
	// ImageURLs are the photo CDN URLs, in album order.
	ImageURLs []string
}

var bgImageURL = regexp.MustCompile(`background-image\s*:\s*url\((['"]?)(.*?)(['"]?)\)`)

// ParsePage extracts postable messages from one t.me/s HTML page.
//
// It returns posts sorted ascending by ID (oldest first); dataBefore — the id
// cursor for loading the previous (older) page (0 when there is no older page);
// and rawCount — the number of message bubbles found on the page regardless of
// whether they were postable. rawCount == 0 signals an empty/blocked page
// (preview disabled or rate-limited), which callers treat differently from
// "bubbles present but no new posts". Messages with neither text nor images
// (polls, service rows, video-only) are dropped; malformed bubbles are skipped.
func ParsePage(r io.Reader) (posts []Post, dataBefore int64, rawCount int, err error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, 0, 0, err
	}

	bubbles := doc.Find(".tgme_widget_message[data-post]")
	rawCount = bubbles.Length()

	seen := make(map[int64]bool)
	bubbles.Each(func(_ int, s *goquery.Selection) {
		dp, _ := s.Attr("data-post")
		id := parsePostID(dp)
		if id == 0 || seen[id] {
			// id 0 => unparseable; seen => pinned duplicate bubble.
			return
		}

		var textMD string
		if t := s.Find(".tgme_widget_message_text.js-message_text").First(); t.Length() > 0 {
			textMD = nodeToMarkdown(t)
		}

		var timeUnix int64
		if dt, ok := s.Find(".tgme_widget_message_date time[datetime]").First().Attr("datetime"); ok {
			if parsed, perr := time.Parse(time.RFC3339, dt); perr == nil {
				timeUnix = parsed.Unix()
			}
		}

		var images []string
		s.Find(".tgme_widget_message_photo_wrap").Each(func(_ int, ps *goquery.Selection) {
			if style, ok := ps.Attr("style"); ok {
				if u := extractBgURL(style); u != "" {
					images = append(images, u)
				}
			}
		})

		if textMD == "" && len(images) == 0 {
			return // nothing postable
		}
		seen[id] = true
		posts = append(posts, Post{ID: id, TextMD: textMD, TimeUnix: timeUnix, ImageURLs: images})
	})

	sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })

	// The "load older" link carries data-before = oldest id on this page.
	if v, ok := doc.Find(".tme_messages_more[data-before]").First().Attr("data-before"); ok {
		dataBefore, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	}
	return posts, dataBefore, rawCount, nil
}

// parsePostID pulls the numeric id from a data-post value like "channel/42600".
// The channel segment is ignored on purpose: a renamed channel reports a
// different internal name than the requested slug, but the id stays stable.
func parsePostID(dataPost string) int64 {
	slash := strings.LastIndex(dataPost, "/")
	if slash < 0 {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(dataPost[slash+1:]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func extractBgURL(style string) string {
	m := bgImageURL.FindStringSubmatch(style)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[2])
}
