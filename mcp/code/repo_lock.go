package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// repoLockSet serializes whole-tree operations such as Git checkout with the
// ordinary FileStore operations used by agents and the panel. A keyed lock is
// kept for the life of the app; repository counts are small and retaining the
// mutex avoids reference-count races during archive/delete.
type repoLockSet struct {
	mu        sync.Mutex
	locks     map[string]*sync.RWMutex
	revisions map[string]uint64
}

func newRepoLockSet() *repoLockSet {
	return &repoLockSet{locks: map[string]*sync.RWMutex{}}
}

func (s *repoLockSet) forRepo(slug string) *sync.RWMutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lock := s.locks[slug]; lock != nil {
		return lock
	}
	lock := &sync.RWMutex{}
	s.locks[slug] = lock
	return lock
}

func (s *repoLockSet) lock(slug string) func() {
	lock := s.forRepo(slug)
	lock.Lock()
	return func() {
		s.mu.Lock()
		if s.revisions == nil {
			s.revisions = map[string]uint64{}
		}
		s.revisions[slug]++
		s.mu.Unlock()
		lock.Unlock()
	}
}
func (s *repoLockSet) revision(slug string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisions[slug]
}

func (s *repoLockSet) rlock(slug string) func() {
	lock := s.forRepo(slug)
	lock.RLock()
	return lock.RUnlock
}

// lockedFileStore puts every existing Code file operation on the same lock
// used by Git. GitService receives the unwrapped local store so it can hold an
// exclusive lock across a complete multi-command operation without deadlocking
// when it prepares or removes a repository root.
type lockedFileStore struct {
	inner FileStore
	locks *repoLockSet
}

func (s *lockedFileStore) Read(slug, path string) ([]byte, error) {
	defer s.locks.rlock(slug)()
	return s.inner.Read(slug, path)
}

func (s *lockedFileStore) ReadPage(slug, path string, offset, limit int) (*ReadResult, error) {
	defer s.locks.rlock(slug)()
	if paged, ok := s.inner.(pagedFileReader); ok {
		return paged.ReadPage(slug, path, offset, limit)
	}
	body, sha, err := readWithSHA(s.inner, slug, path)
	if err != nil {
		return nil, err
	}
	return renderReadResult(path, body, sha, offset, limit), nil
}

func (s *lockedFileStore) Write(slug, path string, content []byte) (FileMeta, error) {
	defer s.locks.lock(slug)()
	return s.inner.Write(slug, path, content)
}

func (s *lockedFileStore) Delete(slug, path string) error {
	defer s.locks.lock(slug)()
	return s.inner.Delete(slug, path)
}

func (s *lockedFileStore) DeleteTree(slug, path string) error {
	defer s.locks.lock(slug)()
	return s.inner.DeleteTree(slug, path)
}

func (s *lockedFileStore) Move(slug, src, dst string) ([]string, error) {
	defer s.locks.lock(slug)()
	return s.inner.Move(slug, src, dst)
}

func (s *lockedFileStore) List(slug, prefix string, recursive bool) ([]FileMeta, error) {
	defer s.locks.rlock(slug)()
	return s.inner.List(slug, prefix, recursive)
}

func (s *lockedFileStore) ListSource(slug, prefix string, recursive, includeGenerated bool) ([]FileMeta, error) {
	defer s.locks.rlock(slug)()
	if source, ok := s.inner.(sourceFileLister); ok {
		return source.ListSource(slug, prefix, recursive, includeGenerated)
	}
	files, err := s.inner.List(slug, prefix, recursive)
	if err != nil {
		return nil, err
	}
	return filterGenerated(files, includeGenerated), nil
}

func (s *lockedFileStore) Stat(slug, path string) (FileMeta, error) {
	defer s.locks.rlock(slug)()
	return s.inner.Stat(slug, path)
}

func (s *lockedFileStore) CreateRepo(slug string) error {
	defer s.locks.lock(slug)()
	return s.inner.CreateRepo(slug)
}

func (s *lockedFileStore) DropRepo(slug string) error {
	defer s.locks.lock(slug)()
	return s.inner.DropRepo(slug)
}

func (s *lockedFileStore) TotalSize(slug string) (int64, error) {
	defer s.locks.rlock(slug)()
	return s.inner.TotalSize(slug)
}

func (s *lockedFileStore) RepoPath(slug string) string {
	if local, ok := s.inner.(FileStoreLocalPath); ok {
		return local.RepoPath(slug)
	}
	return ""
}

// hardDeleteRepo removes the database row, working tree, and external Git
// metadata under one repository lock. Using the raw local store here avoids
// recursively acquiring the lockedFileStore mutex.
// Quarantine filesystem state before deleting the database row. Failures keep
// the row/link and restore the source so deletion can be retried safely.
func (a *App) hardDeleteRepo(db *sql.DB, projectID, slug string) (err error) {
	repo, err := dbGetRepoBySlug(db, projectID, slug)
	if err != nil || repo == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	finish, err := a.commands.beginDelete(ctx, repo.ID)
	if err != nil {
		return err
	}
	success := false
	defer func() { finish(success) }()
	if a.dev != nil && globalCtx != nil {
		if err := a.dev.stopDevRun(globalCtx, projectID, repo.ID); err != nil {
			return err
		}
	}
	link, err := dbGetRepoWorkspace(db, projectID, repo.ID)
	if err != nil {
		return err
	}
	if link != nil {
		if globalCtx == nil || globalCtx.PlatformAPI() == nil {
			return errors.New("workspace cleanup requires the platform; repository was preserved")
		}
		var out map[string]any
		if err := globalCtx.PlatformAPI().CallAppResult(workspacesAppName, "workspace_destroy", map[string]any{"workspace_id": link.WorkspaceID, "confirm": true}, &out); err != nil {
			return fmt.Errorf("workspace cleanup failed; repository was preserved: %w", err)
		}
		if err := dbDeleteRepoWorkspace(db, projectID, repo.ID); err != nil {
			return err
		}
	}
	key := repoStoreKey(repo)
	if a.locks != nil {
		defer a.locks.lock(key)()
	}
	raw := a.store
	if locked, ok := raw.(*lockedFileStore); ok {
		raw = locked.inner
	}
	local, ok := raw.(FileStoreLocalPath)
	if !ok {
		return errors.New("transactional repository deletion requires local storage")
	}
	paths := []string{local.RepoPath(key)}
	if a.git != nil {
		paths = append(paths, a.git.gitDir(repo.ID))
	}
	moved := map[string]string{}
	defer func() {
		if err != nil {
			for original, temp := range moved {
				if e := os.Rename(temp, original); e != nil {
					err = errors.Join(err, fmt.Errorf("restore %s from %s: %w", original, temp, e))
				}
			}
		}
	}()
	for _, original := range paths {
		if _, e := os.Lstat(original); errors.Is(e, os.ErrNotExist) {
			continue
		} else if e != nil {
			return e
		}
		dir, e := os.MkdirTemp(filepath.Dir(original), ".code-delete-")
		if e != nil {
			return e
		}
		temp := filepath.Join(dir, "source")
		if e = os.Rename(original, temp); e != nil {
			_ = os.Remove(dir)
			return e
		}
		moved[original] = temp
	}
	if err = dbHardDeleteRepo(db, projectID, slug); err != nil {
		return err
	}
	success = true
	// Database deletion committed. Quarantine is no longer live source; retain
	// its path in a cleanup error rather than resurrecting a deleted repository.
	for original, temp := range moved {
		delete(moved, original)
		if e := os.RemoveAll(filepath.Dir(temp)); e != nil {
			err = errors.Join(err, fmt.Errorf("repository deleted; remove quarantine %s: %w", filepath.Dir(temp), e))
		}
	}
	return err
}
