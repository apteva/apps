package main

// Telegram supports a small, strict HTML subset rather than arbitrary
// CommonMark. Conversation messages stay stored as their original Markdown so
// the dashboard can render them normally; this adapter converts only the
// Telegram delivery copy. Every source fragment is HTML-escaped before tags
// are introduced, so model text can never inject Telegram markup.

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	telegramMarkdownLink   = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
	telegramMarkdownCode   = regexp.MustCompile("`([^`\\n]+)`")
	telegramMarkdownBold   = regexp.MustCompile(`\*\*([^\n]+?)\*\*`)
	telegramMarkdownBoldUS = regexp.MustCompile(`__([^\n]+?)__`)
	telegramMarkdownStrike = regexp.MustCompile(`~~([^\n]+?)~~`)
	telegramMarkdownItalic = regexp.MustCompile(`\*([^*\n]+)\*`)
)

func telegramMarkdownToHTML(source string) string {
	source = strings.ReplaceAll(strings.TrimSpace(source), "\r\n", "\n")
	if source == "" {
		return ""
	}
	lines := strings.Split(source, "\n")
	var out strings.Builder
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				out.WriteString("</code></pre>\n")
				inFence = false
			} else {
				out.WriteString("<pre><code>")
				inFence = true
			}
			continue
		}
		if inFence {
			out.WriteString(html.EscapeString(line))
			out.WriteByte('\n')
			continue
		}

		switch {
		case telegramMarkdownHeading(trimmed):
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			out.WriteString("<b>" + telegramMarkdownInlineHTML(heading) + "</b>")
		case strings.HasPrefix(trimmed, "> "):
			out.WriteString("<blockquote>" + telegramMarkdownInlineHTML(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))) + "</blockquote>")
		case telegramMarkdownBullet(trimmed):
			out.WriteString("• " + telegramMarkdownInlineHTML(strings.TrimSpace(trimmed[2:])))
		default:
			out.WriteString(telegramMarkdownInlineHTML(line))
		}
		out.WriteByte('\n')
	}
	if inFence {
		out.WriteString("</code></pre>\n")
	}
	return strings.TrimSpace(out.String())
}

func telegramMarkdownHeading(line string) bool {
	for i := 1; i <= 6; i++ {
		if strings.HasPrefix(line, strings.Repeat("#", i)+" ") {
			return true
		}
	}
	return false
}

func telegramMarkdownBullet(line string) bool {
	return len(line) >= 2 && line[1] == ' ' && (line[0] == '-' || line[0] == '*' || line[0] == '+')
}

func telegramMarkdownInlineHTML(source string) string {
	// Private-use delimiters make placeholders inert while the remaining
	// source is escaped. Replace them defensively if a model supplied them.
	source = strings.NewReplacer("\ue000", "�", "\ue001", "�").Replace(source)
	fragments := make([]string, 0, 8)
	protect := func(input string, pattern *regexp.Regexp, render func([]string) string) string {
		return pattern.ReplaceAllStringFunc(input, func(match string) string {
			parts := pattern.FindStringSubmatch(match)
			fragment := render(parts)
			index := len(fragments)
			fragments = append(fragments, fragment)
			return fmt.Sprintf("\ue000%d\ue001", index)
		})
	}

	var render func(string) string
	render = func(source string) string {
		source = protect(source, telegramMarkdownCode, func(parts []string) string {
			return "<code>" + html.EscapeString(parts[1]) + "</code>"
		})
		source = protect(source, telegramMarkdownLink, func(parts []string) string {
			return `<a href="` + html.EscapeString(parts[2]) + `">` + render(parts[1]) + `</a>`
		})
		source = protect(source, telegramMarkdownBold, func(parts []string) string {
			return "<b>" + render(parts[1]) + "</b>"
		})
		source = protect(source, telegramMarkdownBoldUS, func(parts []string) string {
			return "<b>" + render(parts[1]) + "</b>"
		})
		source = protect(source, telegramMarkdownStrike, func(parts []string) string {
			return "<s>" + render(parts[1]) + "</s>"
		})
		source = protect(source, telegramMarkdownItalic, func(parts []string) string {
			return "<i>" + render(parts[1]) + "</i>"
		})

		return html.EscapeString(source)
	}

	escaped := render(source)
	for index := len(fragments) - 1; index >= 0; index-- {
		fragment := fragments[index]
		escaped = strings.ReplaceAll(escaped, fmt.Sprintf("\ue000%d\ue001", index), fragment)
	}
	return escaped
}
