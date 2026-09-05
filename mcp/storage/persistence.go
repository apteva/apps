package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

// Catalog writers serialize only metadata decisions, never backend transfers.
var catalogMu sync.Mutex

func saveStream(c context.Context, app *sdk.AppCtx, pid string, in uploadInput, r io.Reader) (*File, bool, error) {
	var err error
	in.Name, err = validateFilename(in.Name)
	if err != nil {
		return nil, false, err
	}
	in.Folder, err = validateFolder(in.Folder)
	if err != nil {
		return nil, false, err
	}
	if err = validateVisibility(in.Visibility); err != nil {
		return nil, false, err
	}
	in.Visibility = effectiveVisibility(app, in.Visibility)
	in.ContentType = safeResponseContentType(in.ContentType)
	in.Tags = cleanTags(in.Tags)
	if err = os.MkdirAll(uploadsDir(app), 0700); err != nil {
		return nil, false, err
	}
	f, err := os.CreateTemp(uploadsDir(app), "stream-*")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(&contextReader{c, r}, maxUploadBytes(app)+1))
	if err != nil {
		return nil, false, err
	}
	if size > maxUploadBytes(app) {
		return nil, false, errors.New("upload exceeds max_upload_size_mb")
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	if in.ExpectedSHA != "" && !strings.EqualFold(sha, in.ExpectedSHA) {
		return nil, false, errors.New("sha256 mismatch: bytes corrupted")
	}
	if in.ExpectedSize > 0 && size != in.ExpectedSize {
		return nil, false, errors.New("size mismatch")
	}

	if existing, err := dbFindExact(app.AppDB(), pid, sha, in.Folder, in.Name); err != nil {
		return nil, false, err
	} else if existing != nil {
		if _, _, err = compatibleExisting(existing, in); err != nil {
			return nil, false, err
		}
		if in.UploadID != "" {
			if err = recordCompletion(app, in.UploadID, pid, in.Folder, existing.ID, true, in.UserID); err != nil {
				return nil, false, err
			}
		}
		return existing, true, nil
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}
	sk := uuid.NewString() + extOf(in.Name, in.ContentType)
	key := objectKey(sha, sk)
	// Persist cleanup intent before creating bytes. A crash after Put but before
	// commit leaves a durable, delayed cleanup job instead of an untracked blob.
	if err = queueBlobCleanup(app, key, time.Now().Add(24*time.Hour)); err != nil {
		return nil, false, err
	}
	if disk, ok := backend().(*diskBackend); ok {
		if err = f.Sync(); err == nil {
			err = disk.CommitTemp(f.Name(), key)
		}
	} else {
		err = backend().Put(c, key, in.ContentType, f, size)
	}
	if err != nil {
		cleanupBlobNow(app, key)
		return nil, false, err
	}
	row, existed, err := publishFile(c, app, pid, in, sha, sk, size, in.UploadID)
	if err != nil || existed {
		cleanupBlobNow(app, key)
	}
	return row, existed, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func compatibleExisting(f *File, in uploadInput) (*File, bool, error) {
	a, b := cleanTags(f.Tags), cleanTags(in.Tags)
	sort.Strings(a)
	sort.Strings(b)
	if f.Visibility != in.Visibility || strings.Join(a, "\x00") != strings.Join(b, "\x00") || safeResponseContentType(f.ContentType) != safeResponseContentType(in.ContentType) {
		return nil, false, errors.New("identical file already exists with different metadata; update that file explicitly")
	}
	return f, true, nil
}

// publishFile atomically binds bytes to a row, disarms orphan cleanup, and
// records completion for retries. Only the winning upload owns the object.
func publishFile(c context.Context, app *sdk.AppCtx, pid string, in uploadInput, sha, sk string, size int64, uploadID string) (*File, bool, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	existing, err := dbFindExact(app.AppDB(), pid, sha, in.Folder, in.Name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if _, _, err = compatibleExisting(existing, in); err != nil {
			return nil, false, err
		}
		if uploadID != "" {
			if err = recordCompletion(app, uploadID, pid, in.Folder, existing.ID, true, in.UserID); err != nil {
				return nil, false, err
			}
		}
		return existing, true, nil
	}
	tx, err := app.AppDB().BeginTx(c, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	tags, _ := json.Marshal(cleanTags(in.Tags))
	res, err := tx.Exec(`INSERT INTO files(project_id,name,folder,storage_key,content_type,size_bytes,sha256,uploaded_by,source,tags,visibility) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, pid, in.Name, in.Folder, sk, in.ContentType, size, sha, actorFrom(c), in.Source, string(tags), effectiveVisibility(app, in.Visibility))
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(`DELETE FROM blob_cleanup WHERE object_key=?`, objectKey(sha, sk)); err != nil {
		return nil, false, err
	}
	if uploadID != "" {
		if _, err = tx.Exec(`INSERT INTO completed_uploads(upload_id,project_id,folder,file_id,was_existing,completed_at,user_id) VALUES(?,?,?,?,0,?,?)`, uploadID, pid, in.Folder, id, time.Now().Unix(), in.UserID); err != nil {
			return nil, false, err
		}
		if _, err = tx.Exec(`DELETE FROM pending_uploads WHERE upload_id=?`, uploadID); err != nil {
			return nil, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	f, err := dbGetByID(app.AppDB(), pid, id)
	return f, false, err
}

func recordCompletion(app *sdk.AppCtx, id, pid, folder string, fileID int64, existed bool, userID int64) error {
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO completed_uploads(upload_id,project_id,folder,file_id,was_existing,completed_at,user_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(upload_id) DO NOTHING`, id, pid, folder, fileID, existed, time.Now().Unix(), userID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM pending_uploads WHERE upload_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func completedUpload(app *sdk.AppCtx, id, pid string) (*File, bool, error) {
	var fid int64
	var existed bool
	err := app.AppDB().QueryRow(`SELECT file_id,was_existing FROM completed_uploads WHERE upload_id=? AND project_id=?`, id, pid).Scan(&fid, &existed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f, err := dbGetByID(app.AppDB(), pid, fid)
	if err == nil && f == nil {
		err = errors.New("completed upload's file has been deleted")
	}
	return f, existed, err
}

func queueBlobCleanup(app *sdk.AppCtx, key string, when time.Time) error {
	_, err := app.AppDB().Exec(`INSERT INTO blob_cleanup(object_key,not_before) VALUES(?,?) ON CONFLICT(object_key) DO UPDATE SET not_before=excluded.not_before`, key, when.Unix())
	return err
}
func cleanupBlobNow(app *sdk.AppCtx, key string) {
	_ = queueBlobCleanup(app, key, time.Now())
	cleanupBlob(app, key)
}
func cleanupBlob(app *sdk.AppCtx, key string) bool {
	catalogMu.Lock()
	var n int
	// A leftover job must never remove a referenced object, including tombstones.
	err := app.AppDB().QueryRow(`SELECT count(*) FROM files WHERE (CASE WHEN length(sha256)>=2 THEN substr(sha256,1,2) ELSE '00' END)||'/'||storage_key=?`, key).Scan(&n)
	catalogMu.Unlock()
	if n > 0 {
		_, _ = app.AppDB().Exec(`DELETE FROM blob_cleanup WHERE object_key=?`, key)
	}
	if err != nil || n > 0 {
		return false
	}
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = backend().Delete(c, key); err != nil {
		app.Logger().Warn("blob cleanup pending", "key", key, "error", err)
		return false
	}
	_, err = app.AppDB().Exec(`DELETE FROM blob_cleanup WHERE object_key=?`, key)
	return err == nil
}
func sweepBlobCleanup(app *sdk.AppCtx) {
	sweepScratchFiles(app)
	_, _ = app.AppDB().Exec(`DELETE FROM upload_reservations WHERE expires_at>0 AND expires_at<?`, time.Now().Add(-time.Minute).Unix())
	rows, err := app.AppDB().Query(`SELECT object_key FROM blob_cleanup WHERE not_before<=? LIMIT 100`, time.Now().Unix())
	if err != nil {
		return
	}
	var keys []string
	for rows.Next() {
		var key string
		if rows.Scan(&key) == nil {
			keys = append(keys, key)
		}
	}
	rows.Close()
	for _, key := range keys {
		cleanupBlob(app, key)
	}
	_, _ = app.AppDB().Exec(`DELETE FROM completed_uploads WHERE completed_at<?`, time.Now().Add(-7*24*time.Hour).Unix())
}

func verifyBackendIdentity(app *sdk.AppCtx, be Backend) error {
	identity := be.Kind()
	if s, ok := be.(*s3Backend); ok {
		identity = fmt.Sprintf("s3:%s:%s", s.client.EndpointURL().String(), s.bucket)
	}
	var prior string
	err := app.AppDB().QueryRow(`SELECT value FROM storage_state WHERE key='backend'`).Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if os.Getenv("STORAGE_VERIFY_BACKEND_MIGRATION") == "1" {
		return verifyMigratedBackend(app, be, identity)
	}
	if prior != "" && prior != identity {
		return errors.New("backend change requires migrating existing files and upload sessions before switching storage")
	}
	if prior == "" {
		// Before first pin, legacy local bytes must not be silently orphaned.
		var sha, sk string
		e := app.AppDB().QueryRow(`SELECT sha256,storage_key FROM files LIMIT 1`).Scan(&sha, &sk)
		if e == nil {
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, statErr := be.Stat(c, objectKey(sha, sk))
			cancel()
			if statErr != nil {
				return fmt.Errorf("selected backend cannot read existing objects: %w", statErr)
			}
			_, localErr := os.Stat(blobPath(app, sha, sk))
			if (be.Kind() == "s3" && localErr == nil) || (be.Kind() == "disk" && os.IsNotExist(localErr)) {
				return errors.New("existing files do not match selected backend; explicit migration required")
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	_, err = app.AppDB().Exec(`INSERT INTO storage_state(key,value) VALUES('backend',?) ON CONFLICT(key) DO NOTHING`, identity)
	return err
}
