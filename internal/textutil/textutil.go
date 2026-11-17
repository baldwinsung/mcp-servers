// Package textutil cleans upstream text for tool output.
package textutil

import (
	"html"
	"regexp"
	"strings"
)

var (
	tags   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces = regexp.MustCompile(`[ \t]+`)
	blank  = regexp.MustCompile(`\n{3,}`)
)

// Plain strips HTML tags and entities and collapses whitespace.
func Plain(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n", "</div>", "\n", "</li>", "\n").Replace(s)
	s = html.UnescapeString(tags.ReplaceAllString(s, " "))
	s = strings.ReplaceAll(s, " ", " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// Trim shortens s to n runes with an ellipsis.
func Trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
