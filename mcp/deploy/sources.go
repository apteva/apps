package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SourceFetcher unpacks a Deployment's source spec into a working
// directory the builder can operate on. Each kind plugs in here; the
// builder above never knows where the bytes came from.
type SourceFetcher interface {
	Kind() string
	Fetch(d *Deployment, destDir string) error
}

// fetchSource is the dispatch point. Adds new kinds here as they ship.
func fetchSource(d *Deployment, destDir string, cfg sourceConfig) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	switch d.SourceKind {
	case "code":
		return (&codeFetcher{baseURL: cfg.CodeAppURL}).Fetch(d, destDir)
	case "local":
		return (&localFetcher{}).Fetch(d, destDir)
	default:
		return fmt.Errorf("unsupported source_kind %q (v0.1 supports: code, local)", d.SourceKind)
	}
}

type sourceConfig struct {
	CodeAppURL string
	ProjectID  string
	InstallID  string
}

// ─── code source ──────────────────────────────────────────────────

type codeFetcher struct {
	baseURL string
}

func (f *codeFetcher) Kind() string { return "code" }

// Fetch hits the Code app's REST surface to download the repo zip
// and extract it into destDir. SourceRef is the repo slug.
func (f *codeFetcher) Fetch(d *Deployment, destDir string) error {
	if d.SourceRef == "" {
		return errors.New("source_ref (repo slug) required for kind=code")
	}
	if f.baseURL == "" {
		return errors.New("code app URL not configured (set deploy.code_app_url)")
	}
	exportURL, err := url.Parse(strings.TrimRight(f.baseURL, "/") + "/api/repos/" + d.SourceRef + "/export")
	if err != nil {
		return fmt.Errorf("bad code app URL: %w", err)
	}
	q := exportURL.Query()
	q.Set("project_id", d.ProjectID)
	exportURL.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, exportURL.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch code export: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("code export %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("not a valid zip: %w", err)
	}
	return unpackZip(zr, destDir)
}

// ─── local source ─────────────────────────────────────────────────

type localFetcher struct{}

func (f *localFetcher) Kind() string { return "local" }

// Fetch copies the on-disk directory at SourceRef into destDir. Used
// when Apteva is running on a developer machine and wants to build
// straight from a local checkout.
func (f *localFetcher) Fetch(d *Deployment, destDir string) error {
	src := strings.TrimSpace(d.SourceRef)
	if src == "" {
		return errors.New("source_ref (path) required for kind=local")
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("local source %q: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local source %q: not a directory", src)
	}
	return copyTree(src, destDir)
}

// ─── shared helpers ───────────────────────────────────────────────

// unpackZip extracts zr into destDir. Rejects path traversal and
// preserves file modes the zip carried.
func unpackZip(zr *zip.Reader, destDir string) error {
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("zip entry escapes root: %q", f.Name)
		}
		out := filepath.Join(destDir, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode == 0 {
			mode = 0o644
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			w.Close()
			return err
		}
		rc.Close()
		w.Close()
	}
	return nil
}

// copyTree mirrors src into dst. Plain file copy; symlinks become
// the file they point to (good enough for build inputs).
func copyTree(src, dst string) error {
	src = filepath.Clean(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode())
		}
		// Skip common build-output / VCS noise to keep the source
		// hash stable.
		if shouldSkipForBuild(rel) {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	})
}

func shouldSkipForBuild(rel string) bool {
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts {
		switch p {
		case ".git", "node_modules", ".next", "dist", "build", ".cache":
			return true
		}
	}
	return false
}

// hashTree returns a deterministic hex digest of the file contents
// + paths under root. Used to short-circuit redundant rebuilds.
func hashTree(root string) (string, error) {
	type entry struct {
		path string
		sum  []byte
	}
	var entries []entry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(body)
		entries = append(entries, entry{path: rel, sum: h[:]})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\n", e.path, hex.EncodeToString(e.sum))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
