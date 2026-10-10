package main

import (
	"golang.org/x/net/html"
	"strings"
	"testing"
)

func TestOwnTextExcludesNestedBadgesWithoutStrippingNames(t *testing.T) {
	root, err := html.Parse(strings.NewReader(`<html><body><h1> Studio Pro <span id="online"></span><a>Premium Plus</a></h1><h1> Second <a>Pro</a></h1></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	values, err := extractNodeItem(root, map[string]actorField{
		"own":    {Selector: "h1", Type: "text", OwnText: true, Required: true},
		"legacy": {Selector: "h1", Type: "text", Required: true},
		"all":    {Selector: "h1", Type: "text", OwnText: true, All: true, Separator: " / "},
		"many":   {Selector: "h1", Type: "text", OwnText: true, Many: true},
	}, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if values["own"] != "Studio Pro" || values["legacy"] != "Studio Pro Premium Plus" || values["all"] != "Studio Pro / Second" {
		t.Fatalf("values=%#v", values)
	}
	many := values["many"].([]any)
	if len(many) != 2 || many[0] != "Studio Pro" || many[1] != "Second" {
		t.Fatalf("many=%#v", many)
	}
	for _, field := range []actorField{{Type: "text", OwnText: true, Attribute: "id"}, {Type: "attr:id", OwnText: true}, {Type: "text", OwnText: true, PreserveLineBreaks: true}} {
		if _, err := extractNodeItem(root, map[string]actorField{"bad": field}, "https://example.com"); err == nil {
			t.Fatal("ambiguous own_text policy accepted")
		}
	}
}

func TestLiteralTypingBoundAndReadOnlyPolicy(t *testing.T) {
	def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "goto", URL: "https://example.com"}, {Action: "type", Text: " "}}}
	if err := validateActorDefinition(def); err != nil {
		t.Fatal(err)
	}
	def.Steps[1].Text = strings.Repeat("x", 257)
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("unbounded literal typing accepted")
	}
	def.Steps[1].Text = " "
	def.ReadOnly = true
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("read-only keyboard input accepted")
	}
}
