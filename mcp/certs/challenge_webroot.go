package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HTTP-01 challenge file management.
//
// The certs app writes the keyAuth body to a file the operator's
// reverse proxy (Caddy / nginx / traefik / whatever sits on :80)
// serves at /.well-known/acme-challenge/<token>. We never touch
// the proxy's config — the only shared surface is the directory.

// writeChallenge persists keyAuth so the reverse proxy can serve it
// at /.well-known/acme-challenge/<token>. Idempotent on the same
// token (LE may retry; same content is fine).
//
// The token is validated against the ACME base64url alphabet before
// joining the path — Let's Encrypt's tokens always match it, but a
// belt-and-braces check rules out directory traversal in case a
// future spec change (or a buggy/malicious ACME server) emits a
// token containing "/" or "..".
func writeChallenge(webroot, token, keyAuth string) error {
	if webroot == "" {
		return errors.New("webroot_path not configured")
	}
	if !validToken(token) {
		return fmt.Errorf("invalid challenge token %q", token)
	}
	dir := filepath.Join(webroot, ".well-known", "acme-challenge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir webroot dir: %w", err)
	}
	full := filepath.Join(dir, token)
	// 0644: world-readable so the reverse proxy's user can serve it
	// regardless of which user the certs sidecar runs as.
	if err := os.WriteFile(full, []byte(keyAuth), 0o644); err != nil {
		return fmt.Errorf("write challenge file: %w", err)
	}
	return nil
}

// deleteChallenge removes the file. Best-effort; missing file is fine
// (cleanup may run twice if both prepare-on-error and the loop's
// defer fire, and renewals retry the same token harmlessly).
func deleteChallenge(webroot, token string) error {
	if webroot == "" || !validToken(token) {
		return nil
	}
	full := filepath.Join(webroot, ".well-known", "acme-challenge", token)
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// validToken accepts the ACME base64url alphabet plus length bounds.
// Rejects empty, paths with "/", "..", and any byte outside the safe
// set. ACME tokens in practice are ~43 chars; cap at 256 as a sanity
// upper bound that won't bite a future protocol revision.
func validToken(t string) bool {
	if t == "" || len(t) > 256 {
		return false
	}
	for _, c := range t {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
