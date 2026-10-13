package e3

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRE        = regexp.MustCompile(`<[^>]+>`)
	blockBreakRE = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|h[1-6]|tr)>`)
	spaceRunRE   = regexp.MustCompile(`[ \t]+`)
	blankRunRE   = regexp.MustCompile(`\n{3,}`)
)

// stripHTML drops tags and entities and trims the result into one run of text.
func stripHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

// htmlToText keeps line breaks from block tags so a post reads as paragraphs,
// not one run-on line.
func htmlToText(s string) string {
	s = blockBreakRE.ReplaceAllString(s, "\n")
	s = html.UnescapeString(tagRE.ReplaceAllString(s, ""))
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(spaceRunRE.ReplaceAllString(line, " "))
	}
	return strings.TrimSpace(blankRunRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
