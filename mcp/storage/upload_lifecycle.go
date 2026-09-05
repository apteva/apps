package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func canonicalUploadID(args map[string]any) (string, error) {
	id, alias := strings.TrimSpace(strArg(args, "upload_id")), strings.TrimSpace(strArg(args, "id"))
	if id != "" && alias != "" && id != alias {
		return "", errors.New("conflicting id and upload_id")
	}
	if id == "" {
		id = alias
	}
	if !validUploadID(id) {
		return "", errors.New("valid upload_id required")
	}
	return id, nil
}

func loadSessionOrCompletion(app *sdk.AppCtx, id, pid string) (*uploadMeta, error) {
	// A completion receipt grants no stale folder access after the file moves.
	meta := &uploadMeta{}
	err := app.AppDB().QueryRow(`SELECT c.project_id,f.folder,c.user_id FROM completed_uploads c JOIN files f ON f.id=c.file_id WHERE c.upload_id=? AND c.project_id=? AND f.deleted_at IS NULL`, id, pid).Scan(&meta.ProjectID, &meta.Folder, &meta.UserID)
	if err == nil {
		return meta, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	meta, err = loadUploadMeta(uploadSessionDir(app, id))
	if err != nil {
		return nil, errUploadSessionNotFound
	}
	return meta, nil
}
func authorizeHTTPSession(r *http.Request, meta *uploadMeta) error {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		return err
	}
	uid, _ := strconv.ParseInt(r.Header.Get("X-User-ID"), 10, 64)
	if pid != meta.ProjectID || uid != meta.UserID {
		return errAbortNotOwner
	}
	return nil
}

// Transfers take a shared lifecycle lock. Completion/abort takes it exclusively.
// Short reservations protect the aggregate budget while different parts stream
// concurrently. Cache entries are bounded and rebuilt once after a restart.
func writeUploadPart(c context.Context, app *sdk.AppCtx, id string, n int, r io.Reader, length int64, authorize func(*uploadMeta) error) (int64, error) {
	if !validUploadID(id) || n < 1 || n > maxPartNumber {
		return 0, errors.New("invalid upload part")
	}
	mu := sessionLock(id)
	mu.RLock()
	defer func() { mu.RUnlock(); releaseSessionLock(id) }()
	dir := uploadSessionDir(app, id)
	meta, err := loadUploadMeta(dir)
	if err != nil {
		return 0, errUploadSessionNotFound
	}
	if authorize != nil {
		if err = authorize(meta); err != nil {
			return 0, err
		}
	}
	var committed int
	if err = app.AppDB().QueryRow(`SELECT count(*) FROM completed_uploads WHERE upload_id=?`, id).Scan(&committed); err != nil {
		return 0, err
	}
	if committed > 0 {
		return 0, errors.New("upload already completed")
	}
	mu.budget.Lock()
	if mu.sizes == nil {
		parts, e := listParts(app, id)
		if e != nil {
			mu.budget.Unlock()
			return 0, e
		}
		mu.sizes = map[int]int64{}
		mu.writing = map[int]bool{}
		mu.total = 0
		for _, p := range parts {
			mu.sizes[p.N] = p.Size
			mu.total += p.Size
		}
	}
	if mu.writing[n] {
		mu.budget.Unlock()
		return 0, errors.New("part already being written; retry")
	}
	prior := mu.sizes[n]
	remaining := min(meta.DeclaredSize, maxUploadBytes(app)) - mu.total + prior
	allowance := min(remaining, int64(maxPartSize))
	if length >= 0 {
		allowance = length
	}
	if allowance <= 0 || allowance > remaining || allowance > maxPartSize {
		mu.budget.Unlock()
		return 0, errors.New("part exceeds remaining upload allowance")
	}
	mu.total += allowance - prior
	mu.sizes[n] = allowance
	mu.writing[n] = true
	mu.budget.Unlock()
	success := false
	var written int64
	defer func() {
		mu.budget.Lock()
		defer mu.budget.Unlock()
		delete(mu.writing, n)
		if success {
			mu.total += written - allowance
			mu.sizes[n] = written
		} else {
			mu.total += prior - allowance
			mu.sizes[n] = prior
		}
	}()
	tmp, err := os.CreateTemp(partsDir(app, id), "part-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	written, err = io.Copy(tmp, io.LimitReader(&contextReader{c, r}, allowance+1))
	closeErr := tmp.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if written == 0 {
		return 0, errors.New("empty part")
	}
	if written > allowance {
		return 0, errors.New("part exceeds remaining upload allowance")
	}
	if length >= 0 && written != length {
		return 0, errors.New("incomplete part body")
	}
	if err = os.Rename(tmp.Name(), partPath(app, id, n)); err != nil {
		return 0, err
	}
	success = true
	_ = os.Chtimes(dir, time.Now(), time.Now())
	return written, nil
}

func reserveUpload(app *sdk.AppCtx, id, pid string, size, expires int64) error {
	maxSessions := configIntClamped(app.Config().Get("max_upload_sessions"), 64, 1, 1024)
	maxBytes := int64(configIntClamped(app.Config().Get("max_pending_upload_mb"), 1024, 1, 102400)) * 1024 * 1024
	// One SQLite statement makes admission atomic across all session protocols.
	res, err := app.AppDB().Exec(`INSERT INTO upload_reservations(upload_id,project_id,size_bytes,expires_at)
 SELECT ?,?,?,? WHERE (SELECT count(*) FROM upload_reservations)< ? AND
 (SELECT COALESCE(sum(size_bytes),0) FROM upload_reservations)+? <= ?`, id, pid, size, expires, maxSessions, size, maxBytes)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("upload session or pending-byte quota exceeded; finish or abort existing uploads")
	}
	return nil
}
func releaseUploadReservation(app *sdk.AppCtx, id string) {
	_, _ = app.AppDB().Exec(`DELETE FROM upload_reservations WHERE upload_id=?`, id)
}
