package main

import "testing"

func TestNormaliseAddress_Email(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"foo@bar.com", "foo@bar.com", false},
		{"FOO@bar.COM", "foo@bar.com", false},
		{"mailto:Foo@Bar.com", "foo@bar.com", false}, // tolerates legacy URI
		{"  hi@x.io  ", "hi@x.io", false},
		{"support+T-1234@acme.com", "support+t-1234@acme.com", false},
		{"", "", true},
		{"not-an-email", "", true},
		{"foo@", "", true},
		{"@bar.com", "", true},
		{"foo@bar", "", true},
		{"+15551234", "", true}, // not an email
	}
	for _, tc := range cases {
		got, err := normaliseAddress(channelEmail, tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normaliseAddress(email, %q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseAddress(email, %q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseAddress(email, %q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormaliseAddress_Phone(t *testing.T) {
	cases := []struct {
		channel, in, want string
		err               bool
	}{
		{channelSMS, "+15551234567", "+15551234567", false},
		{channelSMS, "tel:+15551234567", "+15551234567", false}, // tolerates legacy URI
		{channelWhatsApp, "+15551234567", "+15551234567", false},
		{channelWhatsApp, "whatsapp:+15551234567", "+15551234567", false},
		{channelSMS, "+1", "", true},                   // too short
		{channelSMS, "15551234567", "", true},          // missing +
		{channelSMS, "+0551234567", "", true},          // leading zero
		{channelSMS, "alice@x.com", "", true},          // not a phone
		{channelSMS, "+1555-123-4567", "", true},       // dashes not allowed
	}
	for _, tc := range cases {
		got, err := normaliseAddress(tc.channel, tc.in)
		if tc.err {
			if err == nil {
				t.Errorf("normaliseAddress(%s, %q) expected error, got %q", tc.channel, tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normaliseAddress(%s, %q) error: %v", tc.channel, tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normaliseAddress(%s, %q) = %q, want %q", tc.channel, tc.in, got, tc.want)
		}
	}
}

func TestNormaliseAddressList_DedupsCaseInsensitive(t *testing.T) {
	out, err := normaliseAddressList(channelEmail, []any{"a@x.com", "A@X.com", "b@y.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0] != "a@x.com" || out[1] != "b@y.com" {
		t.Errorf("got %v", out)
	}
}

func TestExtractSubaddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"support+T-1234@acme.com", "T-1234"},
		{"support@acme.com", ""},
		{"foo+bar+baz@x.com", "bar+baz"},
		// Legacy URI form still works (we don't pass URIs internally
		// any more, but the helper is forgiving).
		{"mailto:support+T-1234@acme.com", "T-1234"},
	}
	for _, tc := range cases {
		if got := extractSubaddress(tc.in); got != tc.want {
			t.Errorf("extractSubaddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPatternMatches_Email(t *testing.T) {
	cases := []struct {
		pattern, addr string
		want          bool
		sub           string
	}{
		{"support@acme.com", "support@acme.com", true, ""},
		{"support@acme.com", "other@acme.com", false, ""},
		// addresses lowercase by canonicalisation, subaddresses come
		// back lowercase as a result.
		{"support+*@acme.com", "support+t-1234@acme.com", true, "t-1234"},
		{"support+*@acme.com", "support@acme.com", false, ""},
		{"*@acme.com", "anything@acme.com", true, ""},
		{"*@acme.com", "anything@other.com", false, ""},
		{"support@acme.com", "support@ACME.com", true, ""}, // case-insensitive
	}
	for _, tc := range cases {
		got, sub := patternMatches(channelEmail, tc.pattern, tc.addr)
		if got != tc.want {
			t.Errorf("patternMatches(email, %q, %q) = %v, want %v", tc.pattern, tc.addr, got, tc.want)
		}
		if sub != tc.sub {
			t.Errorf("patternMatches(email, %q, %q) sub=%q, want %q", tc.pattern, tc.addr, sub, tc.sub)
		}
	}
}

func TestPatternMatches_Phone(t *testing.T) {
	cases := []struct {
		channel, pattern, addr string
		want                   bool
	}{
		{channelSMS, "+15551234567", "+15551234567", true},
		{channelSMS, "+15551234567", "+15559876543", false},
		{channelSMS, "*", "+15551234567", true},
		{channelWhatsApp, "+15551234567", "+15551234567", true},
		{channelWhatsApp, "*", "+15559876543", true},
	}
	for _, tc := range cases {
		got, _ := patternMatches(tc.channel, tc.pattern, tc.addr)
		if got != tc.want {
			t.Errorf("patternMatches(%s, %q, %q) = %v, want %v", tc.channel, tc.pattern, tc.addr, got, tc.want)
		}
	}
}

func TestGuessChannelFromAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice@x.com", channelEmail},
		{"+15551234567", channelSMS},
		{"mailto:alice@x.com", channelEmail},
		{"tel:+15551234567", channelSMS},
		{"not-anything", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := guessChannelFromAddress(tc.in); got != tc.want {
			t.Errorf("guessChannelFromAddress(%q) = %q, want %q", tc.in, got, tc.want)
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
