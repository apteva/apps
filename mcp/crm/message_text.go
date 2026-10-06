package main

import (
	"strings"

	"golang.org/x/net/html"
)

func inboundMessageText(body inboundPayload) string {
	if strings.TrimSpace(body.BodyText) != "" {
		return body.BodyText
	}
	return plainTextFromHTML(body.BodyHTML)
}

func inboundActivityBody(body inboundPayload) string {
	text := inboundMessageText(body)
	if body.Channel == channelEmail && body.Subject != "" {
		return body.Subject + "\n\n" + text
	}
	return text
}

// Never render inbound HTML. Extract human-readable text while excluding
// executable, hidden metadata and stylesheet content.
func plainTextFromHTML(raw string) string {
	return normalizeHTMLMessageText(legacyPlainTextFromHTML(raw))
}

// Collapse layout-only blank lines, not message text or indentation. Email
// tables often nest many empty div/tr elements, which are not paragraphs.
func normalizeHTMLMessageText(raw string) string {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	var lines []string
	blank := false
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(lines) > 0 && !blank {
				lines = append(lines, "")
			}
			blank = true
			continue
		}
		lines = append(lines, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Retain the exact former extraction solely to prove that a stored body was
// produced by CRM before explicitly repairing its formatting. Never use a
// heuristic to replace complete or operator-edited content.
func legacyPlainTextFromHTML(raw string) string {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "template":
				return
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "br", "p", "div", "li", "tr":
				out.WriteByte('\n')
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "li", "tr":
				out.WriteByte('\n')
			}
		}
	}
	walk(doc)
	return strings.TrimSpace(out.String())
}
