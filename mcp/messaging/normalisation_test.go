package main

import "testing"

func TestNormaliseURI_Mailto(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"foo@bar.com", "mailto:foo@bar.com", false},
		{"FOO@bar.COM", "mailto:foo@bar.com", false},
		{"mailto:Foo@Bar.com", "mailto:foo@bar.com", false},
		{"  mailto:hi@x.io  ", "mailto:hi@x.io", false},
		{"support+T-1234@acme.com", "mailto:support+t-1234@acme.com", false},
		{"", "", true},
		{"not-an-email", "", true},
		{"foo@", "", true},
		{"@bar.com", "", true},
		{"foo@bar", "", true},
		{"tel:+15551234", "", true},                    // reserved
		{"apteva://contact/42", "", true},              // reserved
	}
	for _, tc := range cases {
		got, err := normaliseURI(tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normaliseURI(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseURI(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormaliseURIList_DedupsAndCoerces(t *testing.T) {
	out, err := normaliseURIList([]any{"a@x.com", "A@X.com", "b@y.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 unique URIs, got %d: %v", len(out), out)
	}
	if out[0] != "mailto:a@x.com" || out[1] != "mailto:b@y.com" {
		t.Errorf("got %v", out)
	}
}

func TestExtractSubaddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mailto:support+T-1234@acme.com", "T-1234"},
		{"mailto:support@acme.com", ""},
		{"mailto:foo+bar+baz@x.com", "bar+baz"},
	}
	for _, tc := range cases {
		if got := extractSubaddress(tc.in); got != tc.want {
			t.Errorf("extractSubaddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPatternMatches(t *testing.T) {
	cases := []struct {
		pattern, addr string
		want          bool
		sub           string
	}{
		{"mailto:support@acme.com", "mailto:support@acme.com", true, ""},
		{"mailto:support@acme.com", "mailto:other@acme.com", false, ""},
		// v0.1 canonicalises addresses to lowercase, so subaddresses
		// come back lowercase too. Callers that care about ticket-ID
		// case should ToUpper on receipt or use lowercase IDs.
		{"mailto:support+*@acme.com", "mailto:support+t-1234@acme.com", true, "t-1234"},
		{"mailto:support+*@acme.com", "mailto:support@acme.com", false, ""},
		{"mailto:*@acme.com", "mailto:anything@acme.com", true, ""},
		{"mailto:*@acme.com", "mailto:anything@other.com", false, ""},
		{"mailto:support@acme.com", "mailto:support@ACME.com", true, ""}, // case-insensitive domain
	}
	for _, tc := range cases {
		got, sub := patternMatches(tc.pattern, tc.addr)
		if got != tc.want {
			t.Errorf("patternMatches(%q, %q) = %v, want %v", tc.pattern, tc.addr, got, tc.want)
		}
		if sub != tc.sub {
			t.Errorf("patternMatches(%q, %q) sub=%q, want %q", tc.pattern, tc.addr, sub, tc.sub)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	got := renderTemplate("Hi {{name}}, your order #{{ order }} is ready.",
		map[string]any{"name": "Alice", "order": 1234})
	want := "Hi Alice, your order #1234 is ready."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Missing var stays as placeholder so it's noticed.
	got = renderTemplate("Hi {{name}}", map[string]any{})
	if got != "Hi {{name}}" {
		t.Errorf("expected unrendered placeholder, got %q", got)
	}
}

func TestBoundaryFromContentType(t *testing.T) {
	cases := []struct{ in, want string }{
		{`multipart/alternative; boundary="abc"`, "abc"},
		{`multipart/alternative; boundary=abc`, "abc"},
		{`multipart/mixed; boundary="abc"; charset=utf-8`, "abc"},
		{`multipart/alternative`, ""},
	}
	for _, tc := range cases {
		if got := boundaryFromContentType(tc.in); got != tc.want {
			t.Errorf("boundaryFromContentType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
