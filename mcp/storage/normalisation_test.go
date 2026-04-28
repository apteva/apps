package main

import "testing"

func TestNormaliseFolder(t *testing.T) {
	cases := map[string]string{
		"":                "/",
		"/":               "/",
		"/reports/":       "/reports/",
		"reports":         "/reports/",
		"/reports":        "/reports/",
		"/reports//2026/": "/reports/2026/",
		"reports/2026/":   "/reports/2026/",
		"/../etc/":        "/", // path-traversal segments scrubbed
		"/./x/":           "/", // same for "."
	}
	for in, want := range cases {
		if got := normaliseFolder(in); got != want {
			t.Errorf("normaliseFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormaliseFilename(t *testing.T) {
	cases := map[string]string{
		"summary.pdf":           "summary.pdf",
		"  trim me ":            "trim me",
		"path/to/file.txt":      "path_to_file.txt",
		`path\to\file.txt`:      "path_to_file.txt",
		"":                      "untitled",
	}
	for in, want := range cases {
		if got := normaliseFilename(in); got != want {
			t.Errorf("normaliseFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVisibilityOrDefault(t *testing.T) {
	for in, want := range map[string]string{
		"":         "private",
		"private":  "private",
		"PUBLIC":   "public",
		"  signed": "signed",
		"weird":    "private",
	} {
		if got := visibilityOrDefault(in); got != want {
			t.Errorf("visibilityOrDefault(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignatureRoundTrip(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-secret")
	exp := int64(99999999999)
	sig := signFile(42, exp)
	if !verifySignature(42, exp, sig) {
		t.Errorf("valid sig should verify")
	}
	if verifySignature(42, exp, "deadbeef") {
		t.Errorf("bogus sig must not verify")
	}
	if verifySignature(99, exp, sig) {
		t.Errorf("wrong id must not verify")
	}
	// Expired.
	if verifySignature(42, 1, sig) {
		t.Errorf("expired sig must not verify")
	}
}
