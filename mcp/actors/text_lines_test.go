package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

func TestPreservedEditorText(t *testing.T) {
	for _, tc := range []struct{ name, markup, want string }{
		{"paragraphs", "<p>First paragraph.</p><p>Second paragraph.</p>", "First paragraph.\nSecond paragraph."},
		{"blank paragraph", "<p>First</p><p><br></p><p>Last</p>", "First\n\nLast"},
		{"Slate placeholder", "<p>First</p><p><span data-slate-zero-width='n'>\ufeff<br></span></p><p>Last</p>", "First\n\nLast"},
		{"inline formatting", "<p>A <strong>bold</strong> word and a<strong>ttached</strong> word.</p>", "A bold word and attached word."},
		{"soft break", "<p>First<span><br></span>Second</p>", "First\nSecond"},
		{"spaces", "<p>  Keep  these spaces  </p>", "  Keep  these spaces  "},
		{"trailing newline", "<p>First</p><p><br></p>", "First\n"},
		{"list", "<ul><li><p>One</p></li><li>Two</li></ul>", "One\nTwo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := html.Parse(strings.NewReader("<div id='editor'>" + tc.markup + "</div>"))
			if err != nil {
				t.Fatal(err)
			}
			node := cascadia.Query(root, cascadia.MustCompile("#editor"))
			fields := map[string]actorField{"body": {Type: "text", Required: true, PreserveLineBreaks: true, Pattern: "^" + regexp.QuoteMeta(tc.want) + "$"}}
			got, err := extractNodeItem(node, fields, "https://example.com")
			if err != nil || got["body"] != tc.want {
				t.Fatalf("got=%#v err=%v want=%q", got, err, tc.want)
			}
			if strings.Contains(tc.want, "\n") {
				fields["body"] = actorField{Type: "text", Required: true, PreserveLineBreaks: true, Pattern: "^" + regexp.QuoteMeta(strings.ReplaceAll(tc.want, "\n", " ")) + "$"}
				if _, err := extractNodeItem(node, fields, "https://example.com"); err == nil {
					t.Fatal("incorrect paragraph structure passed exact-copy verification")
				}
			}
		})
	}
}

func TestDefaultTextStillNormalizesWhitespace(t *testing.T) {
	root, _ := html.Parse(strings.NewReader("<div id='editor'><p>First  paragraph.</p><p>Second paragraph.</p></div>"))
	node := cascadia.Query(root, cascadia.MustCompile("#editor"))
	got, err := extractNodeItem(node, map[string]actorField{"body": {Type: "text", Required: true}}, "https://example.com")
	if err != nil || got["body"] != "First paragraph. Second paragraph." {
		t.Fatalf("got=%#v err=%v", got, err)
	}
}

func TestAllEditorParagraphsExcludesControlsAndPreservesCopy(t *testing.T) {
	root, _ := html.Parse(strings.NewReader(`<div id="editor"><div contenteditable="false">Paid access starts here</div><p>First <strong>bold</strong> paragraph.</p><p><br></p><p>Last  paragraph.</p></div>`))
	fields := map[string]actorField{"body": {Selector: "#editor p", Type: "text", Required: true, All: true, PreserveLineBreaks: true, Pattern: `^\QFirst bold paragraph.

Last  paragraph.\E$`}}
	got, err := extractNodeItem(root, fields, "https://example.com")
	if err != nil || got["body"] != "First bold paragraph.\n\nLast  paragraph." {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	fields["body"] = actorField{Selector: "#missing p", Type: "text", Required: true, All: true, PreserveLineBreaks: true}
	if _, err := extractNodeItem(root, fields, "https://example.com"); err == nil {
		t.Fatal("missing paragraphs passed required check")
	}
}

func TestExplicitParagraphSeparatorRetainsSoftBreaks(t *testing.T) {
	root, _ := html.Parse(strings.NewReader(`<div id="editor"><p>First paragraph.</p><p>Second paragraph.<br>Soft break.</p></div>`))
	want := "First paragraph.\n\nSecond paragraph.\nSoft break."
	fields := map[string]actorField{"body": {Selector: "#editor p", Type: "text", Required: true, All: true, PreserveLineBreaks: true, Separator: "\n\n", Pattern: "^" + regexp.QuoteMeta(want) + "$"}}
	got, err := extractNodeItem(root, fields, "https://example.com")
	if err != nil || got["body"] != want {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	fields["body"] = actorField{Selector: "#editor p", Type: "text", Required: true, All: true, PreserveLineBreaks: true, Separator: "\n\n", Pattern: "^" + regexp.QuoteMeta(strings.ReplaceAll(want, "\n\n", "\n")) + "$"}
	if _, err := extractNodeItem(root, fields, "https://example.com"); err == nil {
		t.Fatal("missing paragraph boundary passed exact-copy check")
	}
}
