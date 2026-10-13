package main

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestTrimActorLineEdgesPreservesApprovedCopy(t *testing.T) {
	input := "\n\t Hello, \r\n\u00a0 Our rate is £80. \r\n \r\n\tCase, punctuation!  Two spaces stay. \r\n John\t\n"
	want := "Hello,\nOur rate is £80.\n\nCase, punctuation!  Two spaces stay.\nJohn"
	if got := trimActorLineEdges(input); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, changed := range []string{"hello,", "£800", "Two spaces go.", "John!"} {
		if trimActorLineEdges(changed) == want {
			t.Fatal("changed copy accepted")
		}
	}
}

func TestLineEdgeExtractionIsOptInAndExact(t *testing.T) {
	root, err := html.Parse(strings.NewReader(`<div class="body"><p> Hello, <br> Our rate is £80. <br> John </p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	field := actorField{Selector: ".body", Type: "text", PreserveLineBreaks: true, Required: true}
	raw, err := extractNodeItem(root, map[string]actorField{"body": field}, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	field.TrimLineEdges = true
	item, err := extractNodeItem(root, map[string]actorField{"body": field}, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := "Hello,\nOur rate is £80.\nJohn"
	if item["body"] != want || raw["body"] == want {
		t.Fatalf("opt-in normalization: raw=%q normalized=%q", raw["body"], item["body"])
	}
	e := &actorExecution{lastValues: item}
	if err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"body": {EqualsExact: want}}}); err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []string{"hello,\nOur rate is £80.\nJohn", "Hello,\nOur rate is £800.\nJohn", "Hello, Our rate is £80. John", "Hello,\nOur rate is £80.\nJohn!"} {
		if err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"body": {EqualsExact: wrong}}}); err == nil {
			t.Fatalf("accepted changed copy %q", wrong)
		}
	}
}

func TestTrimLineEdgesRejectsNonTextOrNonMultilineFields(t *testing.T) {
	for _, field := range []actorField{
		{TrimLineEdges: true, Type: "text"},
		{TrimLineEdges: true, PreserveLineBreaks: true, Attribute: "href", Type: "text"},
		{TrimLineEdges: true, PreserveLineBreaks: true, Type: "url"},
		{TrimLineEdges: true, PreserveLineBreaks: true, OwnText: true, Type: "text"},
	} {
		if err := validateTrimLineEdges(field); err == nil {
			t.Fatalf("accepted invalid configuration %+v", field)
		}
	}
}
