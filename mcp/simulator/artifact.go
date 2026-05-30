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
	"archive/zip"
	"bytes"
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
const maxSourceArchiveBytes = 512 << 20

// extractSourceTarGz decodes a base64 gzip tarball into destDir.
// Returns the number of files written. Hardened against path-traversal
// ("../" escapes) and decompression bombs (per-file size ceiling).
func extractSourceTarGz(b64 string, destDir string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return 0, fmt.Errorf("decode base64 source: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
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

// extractSourceUpload extracts a browser-uploaded source archive into
// destDir. It accepts zip and tar.gz/tgz archives, then returns the
// directory that should be used as the build root. If the archive has a
// single top-level folder, we build from that folder.
func extractSourceUpload(raw []byte, filename, destDir string) (string, int, error) {
	name := strings.ToLower(strings.TrimSpace(filename))
	var (
		count int
		err   error
	)
	switch {
	case bytes.HasPrefix(raw, []byte("PK\x03\x04")) || strings.HasSuffix(name, ".zip"):
		count, err = extractSourceZip(raw, destDir)
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz") || bytes.HasPrefix(raw, []byte{0x1f, 0x8b}):
		b64 := base64.StdEncoding.EncodeToString(raw)
		count, err = extractSourceTarGz(b64, destDir)
	default:
		return "", 0, fmt.Errorf("unsupported source archive %q; upload .zip, .tar.gz, or .tgz", filename)
	}
	if err != nil {
		return "", count, err
	}
	root, err := sourceBuildRoot(destDir)
	if err != nil {
		return "", count, err
	}
	return root, count, nil
}

func extractSourceZip(raw []byte, destDir string) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return 0, fmt.Errorf("read zip source: %w", err)
	}
	count := 0
	for _, f := range zr.File {
		clean, err := safeJoin(destDir, f.Name)
		if err != nil {
			return count, err
		}
		info := f.FileInfo()
		mode := info.Mode()
		if info.IsDir() {
			if err := os.MkdirAll(clean, 0o755); err != nil {
				return count, err
			}
			continue
		}
		if mode&os.ModeSymlink != 0 || mode&os.ModeType != 0 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			_ = rc.Close()
			return count, err
		}
		err = writeLimited(clean, rc, mode.Perm())
		_ = rc.Close()
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func sourceBuildRoot(destDir string) (string, error) {
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return "", err
	}
	filtered := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if name == ".DS_Store" || name == "__MACOSX" {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 1 && filtered[0].IsDir() {
		return filepath.Join(destDir, filtered[0].Name()), nil
	}
	return destDir, nil
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
