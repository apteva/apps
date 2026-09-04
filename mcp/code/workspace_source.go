package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxWorkspaceSourceFiles           = 20000
	maxWorkspaceSourceBytes           = 128 << 20
	maxWorkspaceSourceCompressedBytes = 8 << 20
)

type sourceEntry struct {
	Path       string
	Mode       fs.FileMode
	Data       []byte
	LinkTarget string
}

type sourceSnapshot struct {
	Entries map[string]sourceEntry
	Paths   []string
	Digest  string
	Archive string
}

type SourceChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

func buildSourceSnapshot(root string) (*sourceSnapshot, error) {
	entries := make(map[string]sourceEntry)
	var total int64
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, full)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipGenerated(rel) || firstPathSegment(rel) == ".home" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(entries) >= maxWorkspaceSourceFiles {
			return errors.New("workspace source exceeds 20000 files")
		}
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		entry := sourceEntry{Path: rel, Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			total += info.Size()
			if total > maxWorkspaceSourceBytes {
				return errors.New("workspace source exceeds 128 MiB expanded")
			}
			entry.Data, err = os.ReadFile(full)
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			entry.LinkTarget, err = os.Readlink(full)
			if err != nil {
				return err
			}
			if err := validateSourceSymlink(rel, entry.LinkTarget); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported source file type: %s", rel)
		}
		entries[rel] = entry
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finishSourceSnapshot(entries, true)
}

func finishSourceSnapshot(entries map[string]sourceEntry, makeArchive bool) (*sourceSnapshot, error) {
	paths := make([]string, 0, len(entries))
	for path := range entries {
		for parent := filepath.ToSlash(filepath.Dir(path)); parent != "." && parent != ""; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if entry, ok := entries[parent]; ok && entry.Mode&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("source entry %q has a symlink parent %q", path, parent)
			}
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		entry := entries[path]
		kind := "file"
		payload := entry.Data
		if entry.Mode&os.ModeSymlink != 0 {
			kind = "symlink"
			payload = []byte(entry.LinkTarget)
		}
		contentHash := sha256.Sum256(payload)
		fmt.Fprintf(h, "%s\x00%s\x00%o\x00%s\n", path, kind, entry.Mode.Perm(), hex.EncodeToString(contentHash[:]))
	}
	snapshot := &sourceSnapshot{Entries: entries, Paths: paths, Digest: hex.EncodeToString(h.Sum(nil))}
	if !makeArchive {
		return snapshot, nil
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tw := tar.NewWriter(gz)
	for _, path := range paths {
		entry := entries[path]
		header := &tar.Header{Name: path, Mode: int64(entry.Mode.Perm()), ModTime: epochTime, Uid: 1000, Gid: 1000}
		if entry.Mode&os.ModeSymlink != 0 {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.LinkTarget
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.Data))
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write(entry.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > maxWorkspaceSourceCompressedBytes {
		return nil, errors.New("workspace source exceeds 8 MiB compressed")
	}
	snapshot.Archive = base64.StdEncoding.EncodeToString(compressed.Bytes())
	return snapshot, nil
}

func parseSourceArchive(encoded string) (*sourceSnapshot, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("decode workspace source: %w", err)
	}
	if len(raw) > maxWorkspaceSourceCompressedBytes {
		return nil, errors.New("workspace source exceeds 8 MiB compressed")
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open workspace source: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := make(map[string]sourceEntry)
	var total int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read workspace source: %w", err)
		}
		path, err := normalizeWorkspaceSourcePath(header.Name)
		if err != nil {
			return nil, err
		}
		if path == "" || header.Typeflag == tar.TypeDir {
			continue
		}
		if shouldSkipGenerated(path) || firstPathSegment(path) == ".home" {
			continue
		}
		if len(entries) >= maxWorkspaceSourceFiles {
			return nil, errors.New("workspace source exceeds 20000 files")
		}
		entry := sourceEntry{Path: path, Mode: fs.FileMode(header.Mode & 0o777)}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return nil, fmt.Errorf("invalid source file size: %s", path)
			}
			total += header.Size
			if total > maxWorkspaceSourceBytes {
				return nil, errors.New("workspace source exceeds 128 MiB expanded")
			}
			entry.Data, err = io.ReadAll(io.LimitReader(tr, maxWorkspaceSourceBytes+1))
			if err != nil {
				return nil, err
			}
		case tar.TypeSymlink:
			entry.Mode |= os.ModeSymlink
			entry.LinkTarget = header.Linkname
			if err := validateSourceSymlink(path, entry.LinkTarget); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported workspace source entry: %s", path)
		}
		entries[path] = entry
	}
	return finishSourceSnapshot(entries, false)
}

func normalizeWorkspaceSourcePath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	if value == "" || value == "." {
		return "", nil
	}
	clean, err := normalisePath(value)
	if err != nil || clean == "" || filepath.IsAbs(value) {
		return "", fmt.Errorf("unsafe workspace source path %q", value)
	}
	return clean, nil
}

func validateSourceSymlink(linkPath, target string) error {
	if target == "" || filepath.IsAbs(target) || strings.IndexByte(target, 0) >= 0 {
		return fmt.Errorf("unsafe source symlink %s -> %s", linkPath, target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), target))
	if resolved == ".." || strings.HasPrefix(filepath.ToSlash(resolved), "../") {
		return fmt.Errorf("source symlink escapes repository: %s -> %s", linkPath, target)
	}
	return nil
}

func firstPathSegment(value string) string {
	value = filepath.ToSlash(value)
	if i := strings.IndexByte(value, '/'); i >= 0 {
		return value[:i]
	}
	return value
}

func diffSourceSnapshots(base, next *sourceSnapshot) []SourceChange {
	all := make(map[string]struct{}, len(base.Entries)+len(next.Entries))
	for path := range base.Entries {
		all[path] = struct{}{}
	}
	for path := range next.Entries {
		all[path] = struct{}{}
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]SourceChange, 0)
	for _, path := range paths {
		before, hadBefore := base.Entries[path]
		after, hasAfter := next.Entries[path]
		status := ""
		switch {
		case !hadBefore:
			status = "added"
		case !hasAfter:
			status = "deleted"
		case !sourceEntriesEqual(before, after):
			status = "modified"
		}
		if status != "" {
			changes = append(changes, SourceChange{Path: path, Status: status})
		}
	}
	return changes
}

func sourceEntriesEqual(a, b sourceEntry) bool {
	return a.Mode.Perm() == b.Mode.Perm() && a.Mode&os.ModeSymlink == b.Mode&os.ModeSymlink &&
		a.LinkTarget == b.LinkTarget && bytes.Equal(a.Data, b.Data)
}

func applySourceSnapshot(root string, previousPaths []string, snapshot *sourceSnapshot) (returnErr error) {
	managed := make(map[string]struct{}, len(previousPaths))
	for _, path := range previousPaths {
		managed[path] = struct{}{}
	}
	for _, path := range snapshot.Paths {
		managed[path] = struct{}{}
	}
	paths := make([]string, 0, len(managed))
	for path := range managed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	backup, err := os.MkdirTemp(filepath.Dir(root), ".workspace-apply-backup-")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr == nil {
			_ = os.RemoveAll(backup)
		} else {
			returnErr = fmt.Errorf("%w; original source backup retained at %s", returnErr, backup)
		}
	}()
	touched := make([]string, 0, len(paths))
	rollback := func() {
		for i := len(touched) - 1; i >= 0; i-- {
			path := touched[i]
			dst, dstErr := safeJoinSource(root, path)
			if dstErr != nil {
				continue
			}
			_ = os.Remove(dst)
			src := filepath.Join(backup, filepath.FromSlash(path))
			if _, statErr := os.Lstat(src); statErr == nil {
				_ = restoreSourceEntry(src, dst)
			}
		}
	}
	for _, path := range paths {
		dst, err := safeJoinSource(root, path)
		if err != nil {
			rollback()
			return err
		}
		if info, statErr := os.Lstat(dst); statErr == nil {
			if info.IsDir() {
				rollback()
				return fmt.Errorf("source path is an existing directory: %s", path)
			}
			backupPath := filepath.Join(backup, filepath.FromSlash(path))
			if err := copySourceEntry(dst, backupPath); err != nil {
				rollback()
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			rollback()
			return statErr
		}
		touched = append(touched, path)
		entry, exists := snapshot.Entries[path]
		if !exists {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollback()
				return err
			}
			continue
		}
		if err := writeSourceEntry(dst, entry); err != nil {
			rollback()
			return err
		}
	}
	return nil
}

func safeJoinSource(root, rel string) (string, error) {
	clean, err := normalizeWorkspaceSourcePath(rel)
	if err != nil || clean == "" {
		return "", fmt.Errorf("invalid source path %q", rel)
	}
	dst := filepath.Join(root, filepath.FromSlash(clean))
	rootAbs, _ := filepath.Abs(root)
	dstAbs, _ := filepath.Abs(dst)
	if !strings.HasPrefix(dstAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("source path escapes repository: %s", rel)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	ancestor := filepath.Dir(dstAbs)
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve source parent: %s", rel)
		}
		ancestor = parent
	}
	ancestorReal, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if ancestorReal != rootReal && !strings.HasPrefix(ancestorReal, rootReal+string(filepath.Separator)) {
		return "", fmt.Errorf("source path escapes repository through symlink: %s", rel)
	}
	return dstAbs, nil
}

func writeSourceEntry(dst string, entry sourceEntry) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if entry.Mode&os.ModeSymlink != 0 {
		return os.Symlink(entry.LinkTarget, dst)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".workspace-write-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(entry.Data); err == nil {
		err = tmp.Chmod(entry.Mode.Perm())
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func copySourceEntry(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	entry := sourceEntry{Mode: info.Mode()}
	if info.Mode()&os.ModeSymlink != 0 {
		entry.LinkTarget, err = os.Readlink(src)
	} else {
		entry.Data, err = os.ReadFile(src)
	}
	if err != nil {
		return err
	}
	return writeSourceEntry(dst, entry)
}

func restoreSourceEntry(src, dst string) error { return copySourceEntry(src, dst) }

var epochTime = func() (t time.Time) { return time.Unix(0, 0).UTC() }()
