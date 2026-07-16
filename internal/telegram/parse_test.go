// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"os"
	"strings"
	"testing"
)

func TestParsePage_Fixture(t *testing.T) {
	f, err := os.Open("testdata/testflightcn.html")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	posts, dataBefore, rawCount, err := ParsePage(f)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	// Anchors observed in the frozen fixture.
	if rawCount != 18 {
		t.Errorf("rawCount = %d, want 18", rawCount)
	}
	if dataBefore != 42595 {
		t.Errorf("dataBefore = %d, want 42595", dataBefore)
	}
	if len(posts) == 0 {
		t.Fatal("no postable posts parsed")
	}

	// Ascending, unique, all above the page's oldest cursor.
	var prev int64
	seen := make(map[int64]bool)
	hasImage := false
	for _, p := range posts {
		if p.ID <= prev {
			t.Errorf("posts not strictly ascending: %d after %d", p.ID, prev)
		}
		prev = p.ID
		if seen[p.ID] {
			t.Errorf("duplicate id %d", p.ID)
		}
		seen[p.ID] = true
		if p.ID < dataBefore {
			t.Errorf("post id %d below page cursor %d", p.ID, dataBefore)
		}
		if p.TextMD == "" && len(p.ImageURLs) == 0 {
			t.Errorf("post %d has neither text nor images", p.ID)
		}
		if p.TimeUnix <= 0 {
			t.Errorf("post %d missing timestamp", p.ID)
		}
		if len(p.ImageURLs) > 0 {
			hasImage = true
		}
	}
	if !hasImage {
		t.Error("expected at least one post with images in fixture")
	}
}

func TestParsePage_Empty(t *testing.T) {
	_, dataBefore, rawCount, err := ParsePage(strings.NewReader("<html><body><div class=\"tgme_channel_history\"></div></body></html>"))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if rawCount != 0 {
		t.Errorf("rawCount = %d, want 0", rawCount)
	}
	if dataBefore != 0 {
		t.Errorf("dataBefore = %d, want 0", dataBefore)
	}
}

func TestParsePostID(t *testing.T) {
	cases := map[string]int64{
		"zaihuapd/42600":  42600, // renamed channel: internal name differs, id stable
		"TestFlightCN/1":  1,
		"":                0,
		"noSlash":         0,
		"chan/notanumber": 0,
	}
	for in, want := range cases {
		if got := parsePostID(in); got != want {
			t.Errorf("parsePostID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestExtractBgURL(t *testing.T) {
	style := "left:0;background-image:url('https://cdn5.telesco.pe/file/abc.jpg');width:1px"
	if got := extractBgURL(style); got != "https://cdn5.telesco.pe/file/abc.jpg" {
		t.Errorf("extractBgURL = %q", got)
	}
	if got := extractBgURL("no image here"); got != "" {
		t.Errorf("extractBgURL(none) = %q, want empty", got)
	}
}
