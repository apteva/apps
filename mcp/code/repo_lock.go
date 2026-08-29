package main

import (
	"database/sql"
	"os"
	"sync"
)

// repoLockSet serializes whole-tree operations such as Git checkout with the
// ordinary FileStore operations used by agents and the panel. A keyed lock is
// kept for the life of the app; repository counts are small and retaining the
// mutex avoids reference-count races during archive/delete.
type repoLockSet struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
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
	return lock.Unlock
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
func (a *App) hardDeleteRepo(db *sql.DB, projectID, slug string) error {
	repo, err := dbGetRepoBySlug(db, projectID, slug)
	if err != nil {
		return err
	}
	if a.locks == nil || a.git == nil || repo == nil {
		if err := dbHardDeleteRepo(db, projectID, slug); err != nil {
			return err
		}
		if repo == nil {
			return nil
		}
		return a.storeFor(repo).DropRepo(slug)
	}
	key := repoStoreKey(repo)
	defer a.locks.lock(key)()
	if err := dbHardDeleteRepo(db, projectID, slug); err != nil {
		return err
	}
	if err := a.git.store.DropRepo(key); err != nil {
		return err
	}
	return os.RemoveAll(a.git.gitDir(repo.ID))
}
