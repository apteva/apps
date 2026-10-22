package main

import (
	"encoding/json"
	"strings"
	"testing"

	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

func TestFiveMBExtractionAcceptsFormerlyOversizedHTML(t *testing.T) {
	html := `<body><div>` + strings.Repeat("x", 1_500_000) + `</div><span id="tail">verified tail</span></body>`
	fake := &fakeComp{url: "https://example.com/large", extractResult: backends.ExtractResult{URL: "https://example.com/large", HTML: html, Rendered: true}}
	app := appWithSession("br_large", fake, "local")
	w := internalExtractResponse(t, app, "br_large", `{"formats":["html"],"max_chars":5000000,"readability":false}`, "actors")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["truncated"] == true || out["html"] != html || fake.extractOptions.MaxChars != 5_000_000 {
		t.Fatal("complete large HTML did not survive the internal extraction route")
	}
}

func TestFiveMBExtractionStillCapsSerializedResponse(t *testing.T) {
	fake := &fakeComp{url: "https://example.com/huge", extractResult: backends.ExtractResult{HTML: strings.Repeat(`<div title="&">é</div>`, 300_000), Rendered: true}}
	app := appWithSession("br_huge", fake, "local")
	w := internalExtractResponse(t, app, "br_huge", `{"formats":["html"],"max_chars":99999999}`, "actors")
	if w.Code != 200 || w.Body.Len() > 5_000_000 || fake.extractOptions.MaxChars != 5_000_000 {
		t.Fatal("response or requested budget exceeded cap", w.Code, w.Body.Len(), fake.extractOptions.MaxChars)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["truncated"] != true {
		t.Fatal("oversized serialized HTML was not marked incomplete")
	}
}
