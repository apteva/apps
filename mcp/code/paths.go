package main

import (
	"errors"
	"path"
	"strings"
)

// normalisePath validates and canonicalises a relative path inside a
// repository. The result has no leading slash, no "..", no absolute
// component, and uses forward slashes only. This is the single
// chokepoint every file op routes through — get it right here, every
// caller is safe.
//
// Returns "" + error for paths the caller must reject.
func normalisePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path required")
	}
	// Reject Windows-style separators outright; we only speak posix.
	if strings.ContainsRune(p, '\\') {
		return "", errors.New("path must use forward slashes")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains NUL byte")
	}
	// Strip leading "./" and any leading slash; agents and humans both
	// write paths like "/app/page.tsx" and "app/page.tsx" — accept both
	// and store one canonical form.
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", errors.New("path required")
	}
	// path.Clean collapses doubled slashes and "." segments. After
	// cleaning, any remaining ".." means the path tried to escape —
	// reject it. (path.Clean turns "a/../b" into "b" and "../a" into
	// "../a", so the .. check needs to happen post-clean.)
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "" {
		return "", errors.New("path required")
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path escapes repository")
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == ".." {
			return "", errors.New("path escapes repository")
		}
	}
	return cleaned, nil
}

// splitDirBase returns the directory and basename of a normalised
// repo path. Root files have dir = "".
func splitDirBase(p string) (dir, base string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// isLikelyText is a coarse content sniff used to decide whether grep
// can read a file. Pure UTF-8 text is rare to misclassify; binary
// blobs typically have NUL bytes early. Cheap, good enough.
func isLikelyText(b []byte) bool {
	const sample = 4096
	if len(b) > sample {
		b = b[:sample]
	}
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}
