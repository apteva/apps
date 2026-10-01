package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyTreeAllRelocatesBunSourceLinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dist := filepath.Join(root, "dist")
	cache := filepath.Join(src, "node_modules", ".bun-cache")
	packageDir := filepath.Join(cache, "example@1")
	link := filepath.Join(cache, "example", "1")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "index.js"), []byte("export default 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(packageDir, link); err != nil {
		t.Fatal(err)
	}
	if err := copyTreeAll(src, dist); err != nil {
		t.Fatal(err)
	}
	stagedLink := filepath.Join(dist, "node_modules", ".bun-cache", "example", "1")
	stagedTarget, err := os.Readlink(stagedLink)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(stagedTarget) {
		t.Fatalf("staged link still points to temporary source: %q", stagedTarget)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(stagedLink, "index.js"))
	if err != nil || string(content) != "export default 1" {
		t.Fatalf("staged link is unusable after source cleanup: %q, %v", content, err)
	}
	digest, err := artifactTreeDigest(dist)
	if err != nil {
		t.Fatal(err)
	}
	uploadCopy := filepath.Join(root, "upload")
	if err := copyTreeAll(dist, uploadCopy); err != nil {
		t.Fatal(err)
	}
	copyDigest, err := artifactTreeDigest(uploadCopy)
	if err != nil || copyDigest != digest {
		t.Fatalf("publisher copy changed artifact digest: %q vs %q, %v", digest, copyDigest, err)
	}
}

func TestCopyTreeAllRejectsExternalSourceLink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "bad-link")); err != nil {
		t.Fatal(err)
	}
	if err := copyTreeAll(src, filepath.Join(root, "dist")); err == nil || !strings.Contains(err.Error(), "bad-link") {
		t.Fatalf("external source link was not identified: %v", err)
	}
}

func TestPipelinePathNamesEscapingLink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "bad-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := pipelinePath(root, "bad-link"); err == nil || !strings.Contains(err.Error(), "bad-link") {
		t.Fatalf("escaping link path missing from error: %v", err)
	}
}
