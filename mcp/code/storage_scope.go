package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// repoStoreKey is intentionally based on the immutable database id rather
// than the user-visible slug. Slugs are unique only within a project, while a
// global Code install stores repositories from multiple projects together.
func repoStoreKey(repo *Repo) string {
	return filepath.ToSlash(filepath.Join("by-id", fmt.Sprintf("%d", repo.ID)))
}

func repoStorageRoot(repoID int64) string {
	return fmt.Sprintf("/repos/by-id/%d/", repoID)
}

// boundFileStore preserves the existing FileStore API while binding every
// operation to one repository's internal storage key. The slug argument is
// deliberately ignored; it remains present so the editing engine and public
// MCP surface stay backwards compatible.
type boundFileStore struct {
	base FileStore
	key  string
}

func bindRepoStore(base FileStore, repo *Repo) FileStore {
	bound := boundFileStore{base: base, key: repoStoreKey(repo)}
	if _, ok := base.(FileStoreLocalPath); ok {
		return boundLocalFileStore{boundFileStore: bound}
	}
	return bound
}

func (s boundFileStore) Read(_ string, p string) ([]byte, error) { return s.base.Read(s.key, p) }
func (s boundFileStore) ReadPage(_ string, p string, offset, limit int) (*ReadResult, error) {
	if paged, ok := s.base.(pagedFileReader); ok {
		return paged.ReadPage(s.key, p, offset, limit)
	}
	body, sha, err := readWithSHA(s.base, s.key, p)
	if err != nil {
		return nil, err
	}
	return renderReadResult(p, body, sha, offset, limit), nil
}
func (s boundFileStore) Write(_ string, p string, b []byte) (FileMeta, error) {
	return s.base.Write(s.key, p, b)
}
func (s boundFileStore) Delete(_ string, p string) error { return s.base.Delete(s.key, p) }
func (s boundFileStore) DeleteTree(_ string, p string) error {
	return s.base.DeleteTree(s.key, p)
}
func (s boundFileStore) Move(_ string, from string, to string) ([]string, error) {
	return s.base.Move(s.key, from, to)
}
func (s boundFileStore) List(_ string, p string, recursive bool) ([]FileMeta, error) {
	return s.base.List(s.key, p, recursive)
}
func (s boundFileStore) ListSource(_ string, p string, recursive, includeGenerated bool) ([]FileMeta, error) {
	if source, ok := s.base.(sourceFileLister); ok {
		return source.ListSource(s.key, p, recursive, includeGenerated)
	}
	files, err := s.base.List(s.key, p, recursive)
	if err != nil {
		return nil, err
	}
	return filterGenerated(files, includeGenerated), nil
}
func (s boundFileStore) Stat(_ string, p string) (FileMeta, error) {
	return s.base.Stat(s.key, p)
}
func (s boundFileStore) CreateRepo(_ string) error { return s.base.CreateRepo(s.key) }
func (s boundFileStore) DropRepo(_ string) error   { return s.base.DropRepo(s.key) }
func (s boundFileStore) TotalSize(_ string) (int64, error) {
	return s.base.TotalSize(s.key)
}

type boundLocalFileStore struct{ boundFileStore }

func (s boundLocalFileStore) RepoPath(_ string) string {
	if local, ok := s.base.(FileStoreLocalPath); ok {
		return local.RepoPath(s.key)
	}
	return ""
}

func (a *App) storeFor(repo *Repo) FileStore { return bindRepoStore(a.store, repo) }

// migrateLegacyRepoStorage upgrades <root>/<slug>/files to
// <root>/by-id/<repo-id>/files. It is idempotent and copy-first: if an old
// global install already had duplicate slugs, every database row receives a
// private copy of the shared legacy tree. Unique trees move atomically.
func migrateLegacyRepoStorage(db *sql.DB, store *LocalFileStore) error {
	rows, err := db.Query(`SELECT id, slug, storage_root FROM repositories ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type legacyRepo struct {
		id          int64
		slug        string
		storageRoot string
	}
	var repos []legacyRepo
	for rows.Next() {
		var repo legacyRepo
		if err := rows.Scan(&repo.id, &repo.slug, &repo.storageRoot); err != nil {
			return err
		}
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	slugCounts := map[string]int{}
	for _, repo := range repos {
		slugCounts[repo.slug]++
	}
	legacyRoots := map[string]struct{}{}
	for _, repo := range repos {
		key := filepath.ToSlash(filepath.Join("by-id", fmt.Sprintf("%d", repo.id)))
		target := store.repoRoot(key)
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			legacy := store.repoRoot(repo.slug)
			if _, statErr := os.Stat(legacy); statErr == nil {
				migrateErr := error(nil)
				if slugCounts[repo.slug] == 1 {
					if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
						return err
					}
					migrateErr = os.Rename(legacy, target)
				}
				if slugCounts[repo.slug] > 1 || migrateErr != nil {
					migrateErr = copyTreeAtomic(legacy, target)
				}
				if migrateErr != nil {
					return fmt.Errorf("migrate repository %s: %w", repo.slug, migrateErr)
				}
				legacyRoots[legacy] = struct{}{}
			} else if errors.Is(statErr, os.ErrNotExist) {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			} else {
				return statErr
			}
		} else if err != nil {
			return err
		}
		wantRoot := repoStorageRoot(repo.id)
		if repo.storageRoot != wantRoot {
			if _, err := db.Exec(`UPDATE repositories SET storage_root = ? WHERE id = ?`, wantRoot, repo.id); err != nil {
				return err
			}
		}
	}
	for legacy := range legacyRoots {
		if strings.Contains(filepath.ToSlash(legacy), "/by-id/") {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(legacy)); err != nil {
			return err
		}
	}
	return nil
}

func copyTreeAtomic(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".repo-migrate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			mode := fs.FileMode(0o755)
			if info, infoErr := d.Info(); infoErr == nil {
				mode = info.Mode().Perm()
			}
			return os.MkdirAll(target, mode)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil || filepath.IsAbs(link) {
				return nil
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil
			}
			srcAbs, _ := filepath.Abs(src)
			resolvedAbs, _ := filepath.Abs(resolved)
			if resolvedAbs != srcAbs && !strings.HasPrefix(resolvedAbs, srcAbs+string(filepath.Separator)) {
				return nil
			}
			return os.Symlink(link, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inErr != nil {
			return inErr
		}
		return closeErr
	})
}
