// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

var multiBlankLine = regexp.MustCompile(`\n{3,}`)

// nodeToMarkdown converts a Telegram message-text element into markdown.
//
// Telegram wraps the message body in nested inline elements: <br> line breaks,
// <a> links, <b>/<i>/<s>/<code> formatting, and emoji as
// <tg-emoji><i class="emoji"><b>😀</b></i></tg-emoji> where the emoji char is
// the text content. The emoji's inner <b> must NOT become bold markdown, so
// emoji elements are special-cased to emit their plain text only.
func nodeToMarkdown(sel *goquery.Selection) string {
	if sel.Length() == 0 {
		return ""
	}
	var sb strings.Builder
	renderChildren(sel.Nodes[0], &sb)
	return cleanupMarkdown(sb.String())
}

func cleanupMarkdown(s string) string {
	s = strings.ReplaceAll(s, " ", " ") // nbsp -> space
	s = multiBlankLine.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func renderChildren(n *html.Node, sb *strings.Builder) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		renderNode(c, sb)
	}
}

func renderNode(n *html.Node, sb *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		sb.WriteString(n.Data)
	case html.ElementNode:
		renderElement(n, sb)
	default:
		renderChildren(n, sb)
	}
}

func renderElement(n *html.Node, sb *strings.Builder) {
	switch n.Data {
	case "br":
		sb.WriteByte('\n')
	case "a":
		renderLink(n, sb)
	case "tg-emoji":
		sb.WriteString(textContent(n))
	case "b", "strong":
		wrapInline(n, sb, "**")
	case "i", "em":
		if hasClass(n, "emoji") {
			sb.WriteString(textContent(n))
			return
		}
		wrapInline(n, sb, "*")
	case "s", "del", "strike":
		wrapInline(n, sb, "~~")
	case "code":
		wrapInline(n, sb, "`")
	case "pre":
		sb.WriteString("\n```\n")
		renderChildren(n, sb)
		sb.WriteString("\n```\n")
	case "blockquote":
		var inner strings.Builder
		renderChildren(n, &inner)
		for _, line := range strings.Split(strings.TrimRight(inner.String(), "\n"), "\n") {
			sb.WriteString("> ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	default:
		renderChildren(n, sb)
	}
}

// wrapInline renders children and wraps non-empty output in a markdown marker.
func wrapInline(n *html.Node, sb *strings.Builder, marker string) {
	var inner strings.Builder
	renderChildren(n, &inner)
	s := inner.String()
	if strings.TrimSpace(s) == "" {
		sb.WriteString(s)
		return
	}
	sb.WriteString(marker)
	sb.WriteString(s)
	sb.WriteString(marker)
}

func renderLink(n *html.Node, sb *strings.Builder) {
	text := textContent(n)
	trimmed := strings.TrimSpace(text)
	// Hashtag / mention links are noise as markdown links — keep the text only.
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "@") {
		sb.WriteString(text)
		return
	}
	href := attr(n, "href")
	if href == "" || trimmed == "" {
		sb.WriteString(text)
		return
	}
	sb.WriteString("[")
	sb.WriteString(text)
	sb.WriteString("](")
	sb.WriteString(href)
	sb.WriteString(")")
}

// textContent returns the concatenated text of all descendant text nodes.
func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			sb.WriteString(nd.Data)
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}
