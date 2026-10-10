package main

import (
	"net/url"
	"testing"
)

func TestURLAssertionVerifiesEveryWrappedSource(t *testing.T) {
	expected := "https://video.example/embed/library/asset"
	wrapper := "https://wrapper.example/player"
	a := actorURLAssertion{URL: expected, AllowExtraQuery: []string{"autoplay"}, Wrappers: []actorURLWrapper{{URL: wrapper, Parameters: []string{"url", "src"}}}}
	wrapped := func(source, display string) string {
		return wrapper + "?src=" + url.QueryEscape(source) + "&url=" + url.QueryEscape(display)
	}
	tests := []struct {
		name, actual string
		ok           bool
	}{
		{"direct", expected, true},
		{"player query", expected + "?autoplay=true", true},
		{"wrapped", wrapped(expected+"?autoplay=true", expected), true},
		{"single wrapped source", wrapper + "?src=" + url.QueryEscape(expected), true},
		{"wrong asset", wrapped("https://video.example/embed/library/other", expected), false},
		{"misleading display", wrapped(expected, "https://video.example/embed/library/other"), false},
		{"wrong host", wrapped("https://video.example.attacker/embed/library/asset", expected), false},
		{"wrong wrapper host", "https://wrapper.example.attacker/player?url=" + url.QueryEscape(expected), false},
		{"wrong wrapper path", "https://wrapper.example/other?url=" + url.QueryEscape(expected), false},
		{"wrong scheme", "http://video.example/embed/library/asset", false},
		{"credentials", "https://user@video.example/embed/library/asset", false},
		{"fragment", expected + "#different", false},
		{"unapproved query", expected + "?asset=other", false},
		{"invalid encoding", expected + "?bad=%XX", false},
		{"duplicate source", wrapper + "?url=" + url.QueryEscape(expected) + "&url=" + url.QueryEscape(expected), false},
		{"empty source", wrapper + "?url=", false},
		{"no source", wrapper + "?title=asset", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := a.compare(tc.actual)
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v error=%v", tc.ok, err)
			}
		})
	}
	// Expected query values are preserved, including duplicate values, even when
	// optional player controls can be appended by the embedding provider.
	a.URL = expected + "?token=approved"
	if err := a.compare(expected + "?token=other&autoplay=true"); err == nil {
		t.Fatal("changed expected query accepted")
	}
	if err := a.compare(expected + "?token=approved&autoplay=true"); err != nil {
		t.Fatal(err)
	}
	e := &actorExecution{lastValues: map[string]any{"media_iframe_src": wrapped(expected, "https://video.example/embed/library/other")}}
	if err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"media_iframe_src": {EqualsURL: &a}}}); err == nil {
		t.Fatal("conflicting media accepted through assert_values")
	}
}
func TestURLAssertionRejectsInvalidPolicy(t *testing.T) {
	for _, a := range []actorURLAssertion{
		{}, {URL: "javascript:alert(1)"},
		{URL: "https://video.example/a", Wrappers: []actorURLWrapper{{URL: "https://wrapper.example/player?src=a", Parameters: []string{"src"}}}},
		{URL: "https://video.example/a", Wrappers: []actorURLWrapper{{URL: "https://wrapper.example/player"}}},
	} {
		if err := a.validate(); err == nil {
			t.Fatal("invalid URL assertion policy accepted")
		}
	}
	if err := (actorURLAssertion{URL: "{{video_url}}"}).validate(); err != nil {
		t.Fatal(err)
	}
}
