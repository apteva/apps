package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteChallenge_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	const tok, body = "tok-123_ABC", "challenge.body"
	if err := writeChallenge(dir, tok, body); err != nil {
		t.Fatalf("writeChallenge: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".well-known", "acme-challenge", tok))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestWriteChallenge_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	const tok = "tok"
	if err := writeChallenge(dir, tok, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := writeChallenge(dir, tok, "v1"); err != nil {
		t.Fatalf("second write should be ok: %v", err)
	}
}

func TestWriteChallenge_RejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, webroot, token string
	}{
		{"empty webroot", "", "tok"},
		{"empty token", dir, ""},
		{"slash in token", dir, "a/b"},
		{"dotdot token", dir, ".."},
		{"absolute traversal", dir, "/etc/passwd"},
		{"unicode", dir, "toké"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := writeChallenge(c.webroot, c.token, "x"); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestDeleteChallenge_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	const tok = "to-delete"
	if err := writeChallenge(dir, tok, "x"); err != nil {
		t.Fatal(err)
	}
	if err := deleteChallenge(dir, tok); err != nil {
		t.Fatalf("deleteChallenge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".well-known", "acme-challenge", tok)); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err = %v", err)
	}
}

func TestDeleteChallenge_MissingIsOK(t *testing.T) {
	dir := t.TempDir()
	if err := deleteChallenge(dir, "never-existed"); err != nil {
		t.Errorf("delete of missing file should be ok, got %v", err)
	}
}

func TestDeleteChallenge_IgnoresBadToken(t *testing.T) {
	// Bad token: silently no-op rather than error, so a doubled cleanup
	// from a fast-failing prepare can't break things.
	if err := deleteChallenge(t.TempDir(), "../etc/passwd"); err != nil {
		t.Errorf("delete of bad token should be ok (no-op), got %v", err)
	}
}

func TestValidToken(t *testing.T) {
	ok := []string{"a", "Z", "0", "-", "_", "tok-123_ABC", "x" + string(make([]byte, 250))}
	for _, s := range ok {
		// fill any zero bytes from make() with 'a' so the test reflects valid chars only
		buf := []byte(s)
		for i, c := range buf {
			if c == 0 {
				buf[i] = 'a'
			}
		}
		s = string(buf)
		if !validToken(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	bad := []string{"", "..", "a/b", "a.b", "a b", "tok\n", "toké", string(make([]byte, 257))}
	for _, s := range bad {
		if validToken(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
