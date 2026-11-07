package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func fleetBackupSpace(path string, additional int64) error {
	free, err := fleetDiskAvailable(path)
	if err != nil {
		return err
	}
	if additional < 0 || free < 512<<20 || additional > free-(512<<20) {
		return fmt.Errorf("insufficient space for backup: need %d bytes plus 512 MiB reserve; %d available", additional, free)
	}
	return nil
}
func fleetBackupPause(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Incremental backups avoid VACUUM's full rebuild and release the source lock
// between steps in rollback mode. WAL readers pin a consistent view so active
// writers cannot continuously restart the copy. Never use a pooled write
// connection for that read transaction (_txlock=immediate would reserve writes).
func fleetOnlineBackupDB(ctx context.Context, db *sql.DB, dst string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			os.Remove(dst)
			os.Remove(dst + "-wal")
			os.Remove(dst + "-shm")
		}
	}()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var seq int
	var schema, sourcePath string
	if err = conn.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &schema, &sourcePath); err != nil {
		return err
	}
	if sourcePath != "" {
		if err = fleetBackupSpace(filepath.Dir(sourcePath), 0); err != nil {
			return err
		}
	}
	var mode string
	var pages, pageSize int64
	if err = conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode == "wal" {
		if _, err = conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
			return err
		}
		defer conn.ExecContext(context.Background(), "ROLLBACK")
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}
	if pages < 0 || pageSize <= 0 || pages > maxExtractedTenantBytes/pageSize {
		return fmt.Errorf("database exceeds recovery file limit")
	}
	if err = fleetBackupSpace(filepath.Dir(dst), pages*pageSize); err != nil {
		return err
	}
	err = conn.Raw(func(raw any) (result error) {
		source, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		b, e := source.NewBackup((&url.URL{Scheme: "file", Path: dst}).String())
		if e != nil {
			return e
		}
		defer func() {
			if e := b.Finish(); result == nil {
				result = e
			}
		}()
		for {
			if e = ctx.Err(); e != nil {
				return e
			}
			if sourcePath != "" {
				if e = fleetBackupSpace(filepath.Dir(sourcePath), 0); e != nil {
					return e
				}
			}
			if e = fleetBackupSpace(filepath.Dir(dst), 2<<20); e != nil {
				return e
			}
			more, e := b.Step(int32(max(1, (1<<20)/pageSize))) // 1 MiB at the usual 4 KiB page size.
			if e != nil {
				var se *sqlite.Error
				if !errors.As(e, &se) || (se.Code()&255 != 5 && se.Code()&255 != 6) {
					return e
				}
				if e = fleetBackupPause(ctx, 10*time.Millisecond); e != nil {
					return e
				}
				continue
			}
			if !more {
				return nil
			}
			if e = fleetBackupPause(ctx, time.Millisecond); e != nil {
				return e
			}
		}
	})
	completed = err == nil
	return err
}

func cloneSQLiteDBContext(ctx context.Context, src, dst string) error {
	u := &url.URL{Scheme: "file", Path: src, RawQuery: "mode=ro&_pragma=busy_timeout(100)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	return fleetOnlineBackupDB(ctx, db, dst)
}

type fleetBackupReader struct {
	ctx context.Context
	in  io.Reader
}

func (r fleetBackupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 128<<10 {
		p = p[:128<<10]
	}
	return r.in.Read(p)
}
func fleetSQLiteFile(path string) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	var h [16]byte
	_, e = io.ReadFull(f, h[:])
	return e == nil && string(h[:]) == "SQLite format 3\x00"
}

// filepath.WalkDir reads/sorts a whole directory before visiting its files.
// Stream names in batches so an over-limit directory cannot allocate memory
// proportional to millions of entries before our inventory check can reject it.
func walkFleetBackupTree(ctx context.Context, root string, visit fs.WalkDirFunc) error {
	info, err := os.Lstat(root)
	if err != nil {
		return visit(root, nil, err)
	}
	var walk func(string, fs.DirEntry) error
	walk = func(path string, entry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(path, entry, nil); err != nil {
			if err == filepath.SkipDir {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			entries, err := dir.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, child := range entries {
				if err := walk(filepath.Join(path, child.Name()), child); err != nil {
					return err
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	return walk(root, fs.FileInfoToDirEntry(info))
}
