package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	checksumScanBatch   = 100
	checksumJobsPerTick = 2
)

func fileObjectKey(f *File) string {
	if f != nil && strings.TrimSpace(f.BackendKey) != "" {
		return f.BackendKey
	}
	if f == nil {
		return ""
	}
	return objectKey(f.SHA256, f.StorageKey)
}

func enqueueChecksumScan(app *sdk.AppCtx, limit int) {
	if app == nil || app.AppDB() == nil || limit <= 0 {
		return
	}
	// A process restart can leave a worker marked running. OnMount is called
	// once per install process, so every running claim belongs to the process
	// that just exited and can safely resume.
	_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='pending',available_at=0,locked_at=0,updated_at=? WHERE status='running'`, time.Now().Unix())
	_, _ = app.AppDB().Exec(`INSERT OR IGNORE INTO checksum_jobs(file_id,project_id)
SELECT id,project_id FROM files
 WHERE deleted_at IS NULL AND (sha256 IS NULL OR sha256='' OR checksum_status IN ('pending','failed'))
 ORDER BY id LIMIT ?`, limit)
}

func queueChecksumJob(app *sdk.AppCtx, f *File) error {
	if app == nil || f == nil || f.ChecksumStatus == "verified" {
		return nil
	}
	_, err := app.AppDB().Exec(`INSERT INTO checksum_jobs(file_id,project_id,status,available_at,updated_at)
VALUES(?,?, 'pending', 0, ?) ON CONFLICT(file_id) DO UPDATE SET
 project_id=excluded.project_id,
 status=CASE WHEN checksum_jobs.status='running' THEN checksum_jobs.status ELSE 'pending' END,
 available_at=CASE WHEN checksum_jobs.status='running' THEN checksum_jobs.available_at ELSE 0 END,
 last_error=CASE WHEN checksum_jobs.status='running' THEN checksum_jobs.last_error ELSE '' END,
 updated_at=excluded.updated_at`, f.ID, f.ProjectID, time.Now().Unix())
	return err
}

func (a *App) toolEnsureChecksumCtx(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := a.requireFileAccess(ctx, app, args, "files.read")
	if err != nil {
		return nil, err
	}
	if f == nil {
		return map[string]any{"found": false, "file": nil}, nil
	}
	if f.SHA256 != "" && f.ChecksumStatus == "verified" {
		return map[string]any{"found": true, "file": f, "checksum_status": f.ChecksumStatus, "sha256": f.SHA256, "revision": f.Revision}, nil
	}
	if err = queueChecksumJob(app, f); err != nil {
		return nil, err
	}
	// Read back the durable state so concurrent callers all observe the same
	// status and no caller creates an independent repair operation.
	f, err = dbGetByID(app.AppDB(), f.ProjectID, f.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"found": true, "file": f, "checksum_status": f.ChecksumStatus, "sha256": f.SHA256, "revision": f.Revision}, nil
}

type checksumIdentity struct {
	size     int64
	version  string
	etag     string
	modified string
}

func objectIdentity(m ObjectMetadata) checksumIdentity {
	return checksumIdentity{size: m.Size, version: m.VersionID, etag: strings.Trim(m.ETag, `"`), modified: m.LastModified.UTC().Format(time.RFC3339Nano)}
}

func sameObject(a, b ObjectMetadata) bool {
	ia, ib := objectIdentity(a), objectIdentity(b)
	if ia.size != ib.size || ia.version != "" && ib.version != "" && ia.version != ib.version {
		return false
	}
	if ia.etag != "" && ib.etag != "" && ia.etag != ib.etag {
		return false
	}
	if ia.modified != "" && ib.modified != "" && ia.modified != ib.modified {
		return false
	}
	return true
}

func hashStoredObject(ctx context.Context, f *File) (string, error) {
	key := fileObjectKey(f)
	if key == "" {
		return "", errors.New("file has no backend object key")
	}
	before, err := backend().HeadObject(ctx, key)
	if err != nil {
		return "", err
	}
	if before.Size != f.SizeBytes {
		return "", fmt.Errorf("object size changed: backend=%d row=%d", before.Size, f.SizeBytes)
	}
	read, err := backend().OpenObject(ctx, key, ObjectReadOptions{})
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, read.Body)
	closeErr := read.Body.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != before.Size || n != f.SizeBytes {
		return "", fmt.Errorf("hashed byte count %d does not match object/row size %d/%d", n, before.Size, f.SizeBytes)
	}
	after, err := backend().HeadObject(ctx, key)
	if err != nil {
		return "", err
	}
	if !sameObject(before, after) {
		return "", errors.New("object changed during checksum read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checksumBackoff(attempt int64) int64 {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return int64(1<<(attempt-1)) * 15
}

func processChecksumJobs(ctx context.Context, app *sdk.AppCtx, limit int) {
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return
		}
		var fileID int64
		var projectID string
		var attempts int64
		err := app.AppDB().QueryRow(`SELECT file_id,project_id,attempts FROM checksum_jobs
 WHERE status IN ('pending','failed') AND available_at<=? ORDER BY file_id LIMIT 1`, time.Now().Unix()).Scan(&fileID, &projectID, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			app.Logger().Warn("checksum job lookup failed", "error", err)
			return
		}
		res, err := app.AppDB().Exec(`UPDATE checksum_jobs SET status='running',attempts=attempts+1,locked_at=?,updated_at=?
 WHERE file_id=? AND status IN ('pending','failed') AND available_at<=?`, time.Now().Unix(), time.Now().Unix(), fileID, time.Now().Unix())
		if err != nil {
			return
		}
		changed, _ := res.RowsAffected()
		if changed != 1 {
			continue
		}
		f, err := dbGetByID(app.AppDB(), projectID, fileID)
		if err != nil {
			msg := err.Error()
			_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='failed',available_at=?,last_error=?,updated_at=? WHERE file_id=?`, time.Now().Unix()+checksumBackoff(attempts+1), msg, time.Now().Unix(), fileID)
			continue
		}
		if f == nil {
			_, _ = app.AppDB().Exec(`DELETE FROM checksum_jobs WHERE file_id=?`, fileID)
			continue
		}
		if f.ChecksumStatus == "verified" && f.SHA256 != "" {
			_, _ = app.AppDB().Exec(`DELETE FROM checksum_jobs WHERE file_id=?`, fileID)
			continue
		}
		_, _ = app.AppDB().Exec(`UPDATE files SET checksum_status='running',checksum_error='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, fileID, projectID)
		sha, hashErr := hashStoredObject(ctx, f)
		if hashErr != nil {
			attempt := attempts + 1
			msg := hashErr.Error()
			_, _ = app.AppDB().Exec(`UPDATE files SET checksum_status='failed',checksum_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, msg, fileID, projectID)
			_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='failed',available_at=?,last_error=?,updated_at=? WHERE file_id=?`, time.Now().Unix()+checksumBackoff(attempt), msg, time.Now().Unix(), fileID)
			app.Logger().Warn("checksum verification failed", "file_id", fileID, "attempt", attempt, "error", msg)
			continue
		}
		if f.SHA256 != "" {
			if !looksLikeSHA256Hex(f.SHA256) || !strings.EqualFold(f.SHA256, sha) {
				msg := "verified checksum disagrees with existing checksum"
				_, _ = app.AppDB().Exec(`UPDATE files SET checksum_status='failed',checksum_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, msg, fileID, projectID)
				_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='failed',available_at=?,last_error=?,updated_at=? WHERE file_id=?`, time.Now().Unix()+checksumBackoff(attempts+1), msg, time.Now().Unix(), fileID)
				continue
			}
			res, err = app.AppDB().Exec(`UPDATE files SET checksum_status='verified',checksum_error='',revision=revision+1,updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND project_id=? AND size_bytes=? AND object_key=?`, fileID, projectID, f.SizeBytes, fileObjectKey(f))
		} else {
			res, err = app.AppDB().Exec(`UPDATE files SET sha256=?,checksum_status='verified',checksum_error='',revision=revision+1,updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND project_id=? AND COALESCE(sha256,'')='' AND size_bytes=? AND object_key=?`, sha, fileID, projectID, f.SizeBytes, fileObjectKey(f))
		}
		if err != nil {
			msg := err.Error()
			_, _ = app.AppDB().Exec(`UPDATE files SET checksum_status='failed',checksum_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, msg, fileID, projectID)
			_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='failed',available_at=?,last_error=?,updated_at=? WHERE file_id=?`, time.Now().Unix()+checksumBackoff(attempts+1), msg, time.Now().Unix(), fileID)
			continue
		}
		changed, _ = res.RowsAffected()
		if changed != 1 {
			var existing string
			_ = app.AppDB().QueryRow(`SELECT COALESCE(sha256,'') FROM files WHERE id=? AND project_id=?`, fileID, projectID).Scan(&existing)
			msg := "file changed while checksum was being verified"
			if existing != "" && !strings.EqualFold(existing, sha) {
				msg = "verified checksum disagrees with existing checksum"
			}
			_, _ = app.AppDB().Exec(`UPDATE files SET checksum_status='failed',checksum_error=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND checksum_status='running'`, msg, fileID, projectID)
			_, _ = app.AppDB().Exec(`UPDATE checksum_jobs SET status='failed',available_at=?,last_error=?,updated_at=? WHERE file_id=?`, time.Now().Unix()+checksumBackoff(attempts+1), msg, time.Now().Unix(), fileID)
			continue
		}
		_, _ = app.AppDB().Exec(`DELETE FROM checksum_jobs WHERE file_id=?`, fileID)
		fresh, getErr := dbGetByID(app.AppDB(), projectID, fileID)
		if getErr == nil && fresh != nil {
			emitChecksumReady(app, fresh)
		}
	}
}

func emitChecksumReady(app *sdk.AppCtx, f *File) {
	if app == nil || f == nil || f.SHA256 == "" {
		return
	}
	installID, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID")), 10, 64)
	app.EmitWithProject("file.checksum.ready", f.ProjectID, map[string]any{
		"project_id": f.ProjectID, "install_id": installID, "file_id": f.ID,
		"sha256": f.SHA256, "file_revision": f.Revision,
	})
}
