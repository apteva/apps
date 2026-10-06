package main

import (
	"strings"

	"golang.org/x/net/html"
)

// HTML-only email is valid MIME. Supply readable text to every downstream
// consumer while retaining the original HTML and any provider-supplied text.
func inboundEmailText(text, rawHTML string) string {
	if strings.TrimSpace(text) != "" {
		return text
	}
	doc, err := html.Parse(strings.NewReader(rawHTML))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "head", "script", "style", "template":
				return
			case "br", "p", "div", "li", "tr":
				out.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
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
