package main

import "testing"

func TestNormaliseChannel_Email(t *testing.T) {
	cases := map[string]string{
		"alice@example.com":     "alice@example.com",
		"ALICE@EXAMPLE.COM":     "alice@example.com",
		"  Alice@Example.com  ": "alice@example.com",
	}
	for in, want := range cases {
		if got := normaliseChannel("email", in); got != want {
			t.Errorf("normaliseChannel(email, %q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseChannel_Phone(t *testing.T) {
	cases := map[string]string{
		"+1 415-555-0100": "+14155550100",
		"(415) 555-0100":  "4155550100",
		"  +44 20 7946 ":  "+44207946",
		"abc":             "",
	}
	for in, want := range cases {
		if got := normaliseChannel("phone", in); got != want {
			t.Errorf("normaliseChannel(phone, %q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseChannel_OtherKindsPassThrough(t *testing.T) {
	in := "https://linkedin.com/in/alice"
	if got := normaliseChannel("linkedin", in); got != in {
		t.Errorf("expected passthrough, got %q", got)
	}
}
