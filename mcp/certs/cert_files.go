package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PEM file output for reverse-proxy consumption.
//
// When cert_output_dir is configured, every issued / renewed cert is
// also written to disk as standard PEM files alongside the DB row:
//
//   <cert_output_dir>/<fqdn>/fullchain.pem   (0644)
//   <cert_output_dir>/<fqdn>/privkey.pem     (0644)
//
// A reverse proxy (driven by the routes app, or wired by hand) points
// its TLS config at these. The certs app stays proxy-agnostic — it
// just drops standard files; it doesn't know or care what reads them.
//
// privkey is 0644 on purpose: the reverse proxy usually runs as a
// different user (caddy / www-data) than this sidecar, and a
// world-readable key is what lets it work with zero coordination.
// On a dedicated single-purpose host that's an acceptable trade for
// "works out of the box"; lock it down by pointing cert_output_dir
// somewhere group-restricted if your threat model needs it.

// safeFQDNComponent rejects anything that can't be a single path
// component. fqdns come from our own DB, but a stray "/" or ".." must
// never let a write escape cert_output_dir.
func safeFQDNComponent(fqdn string) bool {
	if fqdn == "" || fqdn == "." || fqdn == ".." || len(fqdn) > 253 {
		return false
	}
	return !strings.ContainsAny(fqdn, "/\\") && !strings.Contains(fqdn, "..")
}

// writeCertFiles writes the cert chain + key for fqdn under dir.
// Atomic per file (temp + rename) so a proxy never reads a half-
// written file. Best-effort caller: the cert is already in the DB,
// so a write failure is a logged warning, not an issuance failure.
func writeCertFiles(dir, fqdn string, certPEM, keyPEM []byte) error {
	if dir == "" {
		return nil
	}
	if !safeFQDNComponent(fqdn) {
		return fmt.Errorf("unsafe fqdn for cert path: %q", fqdn)
	}
	d := filepath.Join(dir, fqdn)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("mkdir cert dir: %w", err)
	}
	if err := atomicWrite(filepath.Join(d, "fullchain.pem"), certPEM, 0o644); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(d, "privkey.pem"), keyPEM, 0o644); err != nil {
		return err
	}
	return nil
}

// removeCertFiles deletes a cert's PEM directory — called on revoke so
// a proxy stops being able to serve a revoked cert. Missing dir is OK.
func removeCertFiles(dir, fqdn string) error {
	if dir == "" || !safeFQDNComponent(fqdn) {
		return nil
	}
	if err := os.RemoveAll(filepath.Join(dir, fqdn)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// atomicWrite writes data to a sibling temp file then renames it over
// path, so readers see either the old contents or the new — never a
// partial write.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
