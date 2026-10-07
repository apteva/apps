package main

import (
	"strings"
	"testing"
)

func TestHTMLMessageLayoutBlankLines(t *testing.T) {
	raw := `<html><head><style>hidden-css</style></head><body><p>First &amp; paragraph.</p>` + strings.Repeat("<div><table><tr><td>", 52) + `<p>Second paragraph.</p>` + strings.Repeat("</td></tr></table></div>", 52) + `<script>hidden-script</script></body></html>`
	text := plainTextFromHTML(raw)
	if text != "First & paragraph.\n\nSecond paragraph." {
		t.Fatalf("layout gaps retained: %q", text)
	}
	if len(legacyPlainTextFromHTML(raw)) <= len(text) {
		t.Fatal("legacy proof did not reproduce layout gap")
	}
}

func TestHTMLMessageWhitespacePreservesText(t *testing.T) {
	raw := "First\r\n \r\n\t\r\n\u00a0\r\n  indented second\r\nline\n\n\n"
	if got := normalizeHTMLMessageText(raw); got != "First\n\n  indented second\nline" {
		t.Fatalf("text or indentation changed: %q", got)
	}
	body := inboundPayload{BodyText: raw, BodyHTML: `<p>Other text</p>`}
	if got := inboundMessageText(body); got != raw {
		t.Fatal("original plain text changed")
	}
}
