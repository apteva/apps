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

	"github.com/bmatcuk/doublestar/v4"
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
	Path     string `json:"path"`
	Status   string `json:"status"`
	Editable bool   `json:"editable"`
}

func buildSourceSnapshot(root string) (*sourceSnapshot, error) {
	return buildScopedSourceSnapshot(root, nil, nil)
}

func buildScopedSourceSnapshot(root string, workspacePaths, supportPaths []string) (*sourceSnapshot, error) {
	return buildSourceSnapshotMode(root, workspacePaths, supportPaths, true)
}

func buildSourceSnapshotMode(root string, workspacePaths, supportPaths []string, archive bool) (*sourceSnapshot, error) {
	workspacePaths, supportPaths, err := normalizeWorkspaceScope(workspacePaths, supportPaths)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]sourceEntry)
	var total int64
	err = filepath.WalkDir(root, func(full string, d fs.DirEntry, walkErr error) error {
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
		if !d.IsDir() && !workspaceScopeMatches(rel, workspacePaths, supportPaths) {
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
	return finishSourceSnapshot(entries, archive)
}

func normalizeWorkspaceScope(workspacePaths, supportPaths []string) ([]string, []string, error) {
	if len(workspacePaths)+len(supportPaths) > 128 {
		return nil, nil, errors.New("workspace scope supports at most 128 path patterns")
	}
	normalize := func(values []string) ([]string, error) {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(values))
		for _, value := range values {
			value = filepath.ToSlash(strings.TrimSpace(value))
			value = strings.TrimPrefix(value, "./")
			if value == "" || len(value) > 256 || filepath.IsAbs(value) || strings.IndexByte(value, 0) >= 0 {
				return nil, fmt.Errorf("invalid workspace path pattern %q", value)
			}
			for _, segment := range strings.Split(value, "/") {
				if segment == ".." || segment == "." || segment == "" {
					return nil, fmt.Errorf("invalid workspace path pattern %q", value)
				}
			}
			if !doublestar.ValidatePattern(value) {
				return nil, fmt.Errorf("invalid workspace path pattern %q", value)
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
		sort.Strings(out)
		return out, nil
	}
	editable, err := normalize(workspacePaths)
	if err != nil {
		return nil, nil, err
	}
	support, err := normalize(supportPaths)
	if err != nil {
		return nil, nil, err
	}
	return editable, support, nil
}

func workspaceScopeMatches(path string, workspacePaths, supportPaths []string) bool {
	if len(workspacePaths) == 0 && len(supportPaths) == 0 {
		return true
	}
	return matchesWorkspacePatterns(path, workspacePaths) || matchesWorkspacePatterns(path, supportPaths)
}

func matchesWorkspacePatterns(path string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, _ := doublestar.Match(pattern, filepath.ToSlash(path))
		if matched {
			return true
		}
	}
	return false
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
	managed := map[string]bool{}
	for _, p := range previousPaths {
		managed[p] = true
	}
	for _, p := range snapshot.Paths {
		managed[p] = true
	}
	paths := make([]string, 0, len(managed))
	for p := range managed {
		clean, e := normalizeWorkspaceSourcePath(p)
		if e != nil || clean != p || clean == "" {
			return fmt.Errorf("unsafe source path %q", p)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Validate the proposed tree and budgets before any mutation.
	if _, e := finishSourceSnapshot(snapshot.Entries, false); e != nil {
		return e
	}
	for _, p := range snapshot.Paths {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if _, ok := snapshot.Entries[parent]; ok {
				return fmt.Errorf("source path %s has a non-directory parent", p)
			}
		}
	}
	var total int64
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, full)
		if managed[filepath.ToSlash(rel)] {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	for _, entry := range snapshot.Entries {
		if int64(len(entry.Data)) > maxFileBytes() {
			return errors.New("file exceeds configured size limit")
		}
		total += int64(len(entry.Data))
	}
	if total > maxRepoBytes() {
		return errors.New("repository exceeds configured size limit")
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), ".workspace-apply-backup-")
	if err != nil {
		return err
	}
	originals := []string{}
	directories := []string{}
	// Back up every affected existing entry, without following symlink parents.
	for _, p := range paths {
		dst := filepath.Join(root, filepath.FromSlash(p))
		blocked := false
		for parent := filepath.Dir(p); parent != "."; parent = filepath.Dir(parent) {
			info, e := os.Lstat(filepath.Join(root, parent))
			if e == nil && !info.IsDir() {
				if managed[filepath.ToSlash(parent)] {
					blocked = true
					break
				}
				os.RemoveAll(backup)
				return fmt.Errorf("non-directory source parent: %s", parent)
			}
		}
		if blocked {
			continue
		}
		info, e := os.Lstat(dst)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			os.RemoveAll(backup)
			return e
		}
		if info.IsDir() {
			directories = append(directories, p)
			continue
		}
		if e = copySourceEntry(dst, filepath.Join(backup, filepath.FromSlash(p))); e != nil {
			os.RemoveAll(backup)
			return e
		}
		originals = append(originals, p)
	}
	remove := append([]string(nil), paths...)
	deepestFirst(remove)
	// Include empty parents left by removed paths (never recursively remove a directory).
	prune := func(p string) {
		for dir := filepath.Dir(p); dir != "."; dir = filepath.Dir(dir) {
			if e := os.Remove(filepath.Join(root, dir)); e != nil {
				break
			}
		}
	}
	rollback := func() error {
		var errs error
		for _, p := range remove {
			dst := filepath.Join(root, filepath.FromSlash(p))
			if e := removeSourcePath(root, p); e != nil && !errors.Is(e, os.ErrNotExist) {
				if info, se := os.Lstat(dst); se != nil || !info.IsDir() {
					errs = errors.Join(errs, e)
				}
			}
			prune(p)
		}
		for _, p := range directories {
			errs = errors.Join(errs, os.MkdirAll(filepath.Join(root, p), 0755))
		}
		for _, p := range originals {
			errs = errors.Join(errs, restoreSourceEntry(filepath.Join(backup, p), filepath.Join(root, p)))
		}
		return errs
	}
	defer func() {
		if returnErr != nil {
			if e := rollback(); e != nil {
				returnErr = fmt.Errorf("%w; rollback failed: %v; backup retained at %s", returnErr, e, backup)
				return
			}
		}
		_ = os.RemoveAll(backup)
	}()
	for _, p := range remove {
		if e := removeSourcePath(root, p); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		prune(p)
	}
	for _, p := range snapshot.Paths {
		dst, e := safeJoinSource(root, p)
		if e != nil {
			return e
		}
		if e = writeSourceEntry(dst, snapshot.Entries[p]); e != nil {
			return e
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

func removeSourcePath(root, path string) error {
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		if info, err := os.Lstat(filepath.Join(root, parent)); err == nil && !info.IsDir() {
			return nil
		}
	}
	return os.Remove(filepath.Join(root, filepath.FromSlash(path)))
}
