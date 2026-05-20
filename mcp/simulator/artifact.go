package main

// Shared artifact + source-archive helpers used by both build backends.
//
// Source flow: callers (Code's dev_remote, the standalone panel's
// upload) hand us a base64-encoded gzip tarball of the repo source.
// We extract it to a fresh temp build dir, run the platform builder,
// then copy the produced artifact into the per-install artifacts dir
// keyed by content hash so identical builds dedupe.
//
//   android → /data/artifacts/<sha>.apk      (single file)
//   ios     → /data/artifacts/<sha>.app/      (bundle directory)

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxArtifactBytes = 512 << 20 // 512 MiB ceiling on any extracted file

// extractSourceTarGz decodes a base64 gzip tarball into destDir.
// Returns the number of files written. Hardened against path-traversal
// ("../" escapes) and decompression bombs (per-file size ceiling).
func extractSourceTarGz(b64 string, destDir string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return 0, fmt.Errorf("decode base64 source: %w", err)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("gunzip source: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return count, fmt.Errorf("read tar: %w", err)
		}
		clean, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return count, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(clean, 0o755); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
				return count, err
			}
			if err := writeLimited(clean, tr, os.FileMode(hdr.Mode)); err != nil {
				return count, err
			}
			count++
		default:
			// Skip symlinks, devices, fifos — a source tree shouldn't
			// contain them, and honoring symlinks is a traversal risk.
		}
	}
	return count, nil
}

func writeLimited(path string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, maxArtifactBytes+1))
	if err != nil {
		return err
	}
	if n > maxArtifactBytes {
		return fmt.Errorf("file %s exceeds %d byte ceiling", path, maxArtifactBytes)
	}
	return nil
}

// safeJoin joins base + name, rejecting any result that escapes base.
// Mirrors the guard in code's zip importer.
func safeJoin(base, name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	clean := filepath.Join(base, name)
	rel, err := filepath.Rel(base, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry %q escapes build dir", name)
	}
	return clean, nil
}

// hashFile returns the hex sha256 of a file's contents — used to key
// single-file artifacts (APKs).
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashDir returns a hex sha256 over a directory tree's relative paths
// + contents — used to key bundle-directory artifacts (.app). Walks in
// lexical order so the hash is stable across runs.
func hashDir(root string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\n", rel)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies src → dst, creating parent dirs.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// copyTree recursively copies a directory tree src → dst. Used to
// stash a built .app bundle into the artifacts dir.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

// findFirst walks dir and returns the first path whose base name
// matches one of suffixes (case-sensitive). Used to locate build
// outputs (app-debug.apk, *.app) without hardcoding the full gradle /
// DerivedData path layout.
func findFirst(dir string, match func(path string, info os.FileInfo) bool) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees rather than aborting
		}
		if found != "" {
			return filepath.SkipDir
		}
		if match(path, info) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("no matching build output found")
	}
	return found, nil
}
