package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

var errRevisionConflict = errors.New("file revision conflict; reload and merge your changes")

// withRepoWrite keeps read/validate/write on the same repository lock as Git.
// The callback receives an unlocked store bound to the same storage identity.
func withRepoWrite[T any](store FileStore, slug string, fn func(FileStore) (T, error)) (T, error) {
	var value T
	if tx, ok := store.(interface {
		Transaction(string, func(FileStore) error) error
	}); ok {
		err := tx.Transaction(slug, func(raw FileStore) error { var err error; value, err = fn(raw); return err })
		return value, err
	}
	return fn(store)
}

func (s *lockedFileStore) Transaction(slug string, fn func(FileStore) error) error {
	defer s.locks.lock(slug)()
	return fn(s.inner)
}
func (s boundFileStore) Transaction(_ string, fn func(FileStore) error) error {
	if tx, ok := s.base.(interface {
		Transaction(string, func(FileStore) error) error
	}); ok {
		return tx.Transaction(s.key, func(raw FileStore) error { return fn(bindKey(raw, s.key)) })
	}
	return fn(s)
}
func bindKey(base FileStore, key string) FileStore {
	bound := boundFileStore{base: base, key: key}
	if _, ok := base.(FileStoreLocalPath); ok {
		return boundLocalFileStore{bound}
	}
	return bound
}

// Absence checks must treat dangling symlinks as occupied paths.
func sourcePathExists(store FileStore, slug, path string) (bool, error) {
	if local, ok := store.(FileStoreLocalPath); ok {
		full, err := safeJoinSource(local.RepoPath(slug), path)
		if err != nil {
			return false, err
		}
		_, err = os.Lstat(full)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	_, err := store.Stat(slug, path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func writeConditional(store FileStore, slug, path string, body []byte, expected string, absent bool) (FileMeta, error) {
	return withRepoWrite(store, slug, func(raw FileStore) (FileMeta, error) {
		if absent {
			occupied, err := sourcePathExists(raw, slug, path)
			if err != nil {
				return FileMeta{}, err
			}
			if occupied {
				return FileMeta{}, fmt.Errorf("%w: destination already exists", errRevisionConflict)
			}
		}
		current, err := raw.Stat(slug, path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return FileMeta{}, err
		}
		if expected != "" && (err != nil || current.SHA256 != expected) {
			return FileMeta{}, errRevisionConflict
		}
		return raw.Write(slug, path, body)
	})
}

type fileMutation struct {
	Path       string
	Body       []byte
	Delete     bool
	Mode       os.FileMode
	LinkTarget string
}

// applyFileMutations validates and applies a batch with rollback. Call under
// withRepoWrite. Local storage preserves modes and symlinks in the journal.
func applyFileMutations(store FileStore, slug string, changes []fileMutation) error {
	seen := map[string]bool{}
	for _, c := range changes {
		p, e := normalisePath(c.Path)
		if e != nil || p != c.Path {
			return fmt.Errorf("invalid mutation path %q", c.Path)
		}
		if seen[p] {
			return fmt.Errorf("duplicate mutation path %q", p)
		}
		seen[p] = true
		if !c.Delete && int64(len(c.Body)) > maxFileBytes() {
			return errors.New("file exceeds configured size limit")
		}
	}
	if local, ok := store.(FileStoreLocalPath); ok {
		root := local.RepoPath(slug)
		entries := map[string]sourceEntry{}
		paths := []string{}
		for _, c := range changes {
			paths = append(paths, c.Path)
			if c.Delete {
				continue
			}
			mode := c.Mode
			if mode == 0 {
				mode = 0644
				if info, e := os.Lstat(filepath.Join(root, filepath.FromSlash(c.Path))); e == nil && info.Mode().IsRegular() {
					mode = info.Mode().Perm()
				}
			}
			entries[c.Path] = sourceEntry{Path: c.Path, Mode: mode, Data: c.Body, LinkTarget: c.LinkTarget}
		}
		snapshot, err := finishSourceSnapshot(entries, false)
		if err != nil {
			return err
		}
		return applySourceSnapshot(root, paths, snapshot)
	}
	// Non-local backends must restore all successful writes on any failure.
	before := map[string][]byte{}
	existed := map[string]bool{}
	for _, c := range changes {
		b, e := store.Read(slug, c.Path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		before[c.Path] = b
		existed[c.Path] = e == nil
	}
	touched := []string{}
	for _, c := range changes {
		var err error
		if c.Delete {
			err = store.Delete(slug, c.Path)
		} else {
			_, err = store.Write(slug, c.Path, c.Body)
		}
		touched = append(touched, c.Path)
		if err != nil {
			for i := len(touched) - 1; i >= 0; i-- {
				p := touched[i]
				var e error
				if existed[p] {
					_, e = store.Write(slug, p, before[p])
				} else {
					e = store.Delete(slug, p)
				}
				err = errors.Join(err, e)
			}
			return err
		}
	}
	return nil
}

func maxFileBytes() int64 {
	return envLimit("CODE_MAX_FILE_BYTES", configuredBytes("max_file_size_mb", 10<<20))
}
func maxRepoBytes() int64 {
	return envLimit("CODE_MAX_REPO_BYTES", configuredBytes("max_repo_size_mb", 1024<<20))
}

// deepestFirst is used for removals and rollback of file/directory transitions.
func deepestFirst(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) > len(paths[j])
		}
		return paths[i] > paths[j]
	})
}

func configuredBytes(name string, fallback int64) int64 {
	if globalCtx != nil && globalCtx.Config() != nil {
		if n, err := strconv.ParseInt(globalCtx.Config().Get(name), 10, 64); err == nil && n > 0 && n <= 1<<20 {
			return n << 20
		}
	}
	return fallback
}
