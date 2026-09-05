package main

import (
	"golang.org/x/net/html"
	"strings"
)

// Never render inbound HTML. Extract human-readable text while excluding
// executable, hidden metadata and stylesheet content.
func plainTextFromHTML(raw string) string {
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
