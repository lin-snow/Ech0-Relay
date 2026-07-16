// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// render wraps an HTML fragment as message text and converts it to markdown.
func render(t *testing.T, fragment string) string {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(
		`<div class="tgme_widget_message_text js-message_text">` + fragment + `</div>`))
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	return nodeToMarkdown(doc.Find(".js-message_text").First())
}

func TestNodeToMarkdown(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
		want     string
	}{
		{"plain", "hello world", "hello world"},
		{"br", "line1<br/>line2", "line1\nline2"},
		{"bold", "<b>bold</b> text", "**bold** text"},
		{"italic", "<i>it</i> text", "*it* text"},
		{"strike", "<s>gone</s>", "~~gone~~"},
		{"code", "<code>x</code>", "`x`"},
		{"link", `<a href="http://x.ai/" target="_blank">x.ai</a>`, "[x.ai](http://x.ai/)"},
		{
			"hashtag link keeps text only",
			`<a href="?q=%23news" onclick="return false">#news</a>`,
			"#news",
		},
		{
			"mention link keeps text only",
			`<a href="https://t.me/someone">@someone</a>`,
			"@someone",
		},
		{
			"emoji inner bold not treated as markdown",
			`<tg-emoji emoji-id="1"><i class="emoji" style="background-image:url('x.png')"><b>🤖</b></i></tg-emoji> hi`,
			"🤖 hi",
		},
		{
			"bare emoji i tag",
			`<i class="emoji" style="background-image:url('x.png')"><b>🎗</b></i> tag`,
			"🎗 tag",
		},
		{
			"multi blank lines collapsed",
			"a<br/><br/><br/><br/>b",
			"a\n\nb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(t, tc.fragment); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
