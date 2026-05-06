package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileStore is the substrate for repository file content. Every file
// op the editing engine performs goes through this interface — the
// editing engine never touches the OS or HTTP directly. v0.1 ships
// LocalFileStore (disk under /data/repos/<slug>/files/); v0.2 will
// ship StorageAppFileStore that delegates to the Storage app once
// cross-app RPC lands. The engine is unchanged either way.
type FileStore interface {
	// Read returns the raw bytes of a file. Returns os.ErrNotExist if
	// the file is missing.
	Read(repoSlug, relPath string) ([]byte, error)

	// Write replaces (or creates) a file with the given bytes. Parent
	// directories are created as needed. The implementation must be
	// atomic within one repo (no partially-written file on crash).
	Write(repoSlug, relPath string, content []byte) (FileMeta, error)

	// Delete removes a single file. Returns nil if the file is already
	// absent (delete-is-idempotent).
	Delete(repoSlug, relPath string) error

	// DeleteTree removes a folder and everything under it. Used by
	// repos_archive --force and code_delete_file when the path is a
	// directory.
	DeleteTree(repoSlug, relPrefix string) error

	// Move renames a file or directory. Both src and dst are repo-
	// relative and already normalised. Returns the list of moved file
	// paths (post-move) so callers can update caches.
	Move(repoSlug, src, dst string) ([]string, error)

	// List returns metadata for every file under relPrefix, recursively
	// when recursive is true, top-level only otherwise. Sorted by path.
	List(repoSlug, relPrefix string, recursive bool) ([]FileMeta, error)

	// Stat returns metadata for a single file, or os.ErrNotExist.
	Stat(repoSlug, relPath string) (FileMeta, error)

	// CreateRepo prepares any backend bookkeeping for a brand-new repo.
	// LocalFileStore creates the on-disk root; future stores might
	// register the repo with Storage. Idempotent.
	CreateRepo(repoSlug string) error

	// DropRepo removes all files for a repo. Used by hard-delete.
	DropRepo(repoSlug string) error

	// TotalSize returns the cumulative size of all files in a repo —
	// used to enforce the per-repo soft cap on writes.
	TotalSize(repoSlug string) (int64, error)
}

// FileMeta is the minimum a Read/List caller needs to decide what to
// do with a file. ContentType is left empty by LocalFileStore; the
// editing engine sniffs when needed.
type FileMeta struct {
	Path    string `json:"path"`     // repo-relative, forward slashes
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
	ModTime int64  `json:"mod_time"` // unix seconds
	IsDir   bool   `json:"is_dir,omitempty"`
}

// ─── LocalFileStore ─────────────────────────────────────────────────
//
// Disk layout:
//
//   <root>/<slug>/files/<relPath>
//
// One subtree per repo, no special bookkeeping. Atomic writes via
// rename-from-tempfile-in-same-dir.

type LocalFileStore struct {
	root string // e.g. /data/repos
}

func NewLocalFileStore(root string) *LocalFileStore {
	return &LocalFileStore{root: root}
}

func (s *LocalFileStore) repoRoot(slug string) string {
	return filepath.Join(s.root, slug, "files")
}

// RepoPath returns the absolute on-disk path of a repo's storage root.
// Implements FileStoreLocalPath so the dev runtime can spawn child
// processes with cwd set to the live source — no copy, no sync. When
// storage moves to the Storage app (v0.6+) the new backend won't
// satisfy this interface and dev mode degrades with a clean error.
func (s *LocalFileStore) RepoPath(slug string) string {
	return s.repoRoot(slug)
}

// FileStoreLocalPath is the optional capability dev runtime relies on.
// LocalFileStore implements it; future remote-storage backends won't.
// The dev_runtime layer type-asserts and surfaces a clear error rather
// than silently breaking when storage isn't local.
type FileStoreLocalPath interface {
	RepoPath(slug string) string
}

// resolve joins the repo root with the (already-normalised) relPath
// and verifies the result is still inside the repo root. This is the
// last line of defence against path traversal — even if
// normalisePath missed something, the prefix check catches it.
func (s *LocalFileStore) resolve(slug, relPath string) (string, error) {
	root := s.repoRoot(slug)
	full := filepath.Join(root, filepath.FromSlash(relPath))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if rootAbs != fullAbs && !strings.HasPrefix(fullAbs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository: %q", relPath)
	}
	return full, nil
}

func (s *LocalFileStore) Read(slug, relPath string) ([]byte, error) {
	full, err := s.resolve(slug, relPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

func (s *LocalFileStore) Stat(slug, relPath string) (FileMeta, error) {
	full, err := s.resolve(slug, relPath)
	if err != nil {
		return FileMeta{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return FileMeta{}, err
	}
	meta := FileMeta{
		Path:    relPath,
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
		IsDir:   info.IsDir(),
	}
	if !info.IsDir() {
		// Stat is also called by code_read_file's caller chain to get
		// sha256 for the read-receipt check; compute it here so the
		// caller doesn't need a second read.
		b, err := os.ReadFile(full)
		if err == nil {
			h := sha256.Sum256(b)
			meta.SHA256 = hex.EncodeToString(h[:])
		}
	}
	return meta, nil
}

func (s *LocalFileStore) Write(slug, relPath string, content []byte) (FileMeta, error) {
	full, err := s.resolve(slug, relPath)
	if err != nil {
		return FileMeta{}, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return FileMeta{}, err
	}
	// Atomic write: temp file + rename, both in the same dir so it's
	// a same-filesystem rename (always atomic on posix).
	tmp, err := os.CreateTemp(filepath.Dir(full), ".write-*")
	if err != nil {
		return FileMeta{}, err
	}
	tmpPath := tmp.Name()
	defer func() {
		// If we didn't reach the rename, tidy up.
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return FileMeta{}, err
	}
	if err := tmp.Close(); err != nil {
		return FileMeta{}, err
	}
	if err := os.Rename(tmpPath, full); err != nil {
		return FileMeta{}, err
	}
	h := sha256.Sum256(content)
	info, _ := os.Stat(full)
	mod := int64(0)
	if info != nil {
		mod = info.ModTime().Unix()
	}
	return FileMeta{
		Path:    relPath,
		Size:    int64(len(content)),
		SHA256:  hex.EncodeToString(h[:]),
		ModTime: mod,
	}, nil
}

func (s *LocalFileStore) Delete(slug, relPath string) error {
	full, err := s.resolve(slug, relPath)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalFileStore) DeleteTree(slug, relPrefix string) error {
	full, err := s.resolve(slug, relPrefix)
	if err != nil {
		return err
	}
	err = os.RemoveAll(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalFileStore) Move(slug, src, dst string) ([]string, error) {
	srcFull, err := s.resolve(slug, src)
	if err != nil {
		return nil, err
	}
	dstFull, err := s.resolve(slug, dst)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(srcFull)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dstFull), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(srcFull, dstFull); err != nil {
		return nil, err
	}
	moved := []string{}
	if info.IsDir() {
		// Walk the destination to enumerate moved files. We could
		// pre-walk src instead, but post-walk gives the new paths
		// which is what callers want.
		filepath.WalkDir(dstFull, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, e := filepath.Rel(s.repoRoot(slug), p)
			if e == nil {
				moved = append(moved, filepath.ToSlash(rel))
			}
			return nil
		})
	} else {
		moved = append(moved, dst)
	}
	return moved, nil
}

func (s *LocalFileStore) List(slug, relPrefix string, recursive bool) ([]FileMeta, error) {
	root := s.repoRoot(slug)
	var base string
	if relPrefix == "" {
		base = root
	} else {
		f, err := s.resolve(slug, relPrefix)
		if err != nil {
			return nil, err
		}
		base = f
	}
	if _, err := os.Stat(base); errors.Is(err, os.ErrNotExist) {
		return []FileMeta{}, nil
	}
	out := []FileMeta{}
	if recursive {
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return nil
			}
			out = append(out, FileMeta{
				Path:    filepath.ToSlash(rel),
				Size:    info.Size(),
				ModTime: info.ModTime().Unix(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		entries, err := os.ReadDir(base)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			rel := e.Name()
			if relPrefix != "" {
				rel = relPrefix + "/" + e.Name()
			}
			out = append(out, FileMeta{
				Path:    rel,
				Size:    info.Size(),
				ModTime: info.ModTime().Unix(),
				IsDir:   e.IsDir(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *LocalFileStore) CreateRepo(slug string) error {
	return os.MkdirAll(s.repoRoot(slug), 0o755)
}

func (s *LocalFileStore) DropRepo(slug string) error {
	return os.RemoveAll(filepath.Join(s.root, slug))
}

func (s *LocalFileStore) TotalSize(slug string) (int64, error) {
	root := s.repoRoot(slug)
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	var total int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// shaOf hashes a byte slice — used by callers that already have the
// content in memory (avoids re-reading from disk just for the hash).
func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readWithSHA reads a file and returns content + sha in one go.
func readWithSHA(s FileStore, slug, p string) ([]byte, string, error) {
	b, err := s.Read(slug, p)
	if err != nil {
		return nil, "", err
	}
	return b, shaOf(b), nil
}

// _ keeps io imported for v0.2 (StorageAppFileStore will stream).
var _ = io.Discard
