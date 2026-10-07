package main

import (
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"time"
)

func recoverStorageState(app *sdk.AppCtx) error {
	// Repair the old protocol's committed-file/pending-session split before any
	// expired upload is reclaimed. The reference remains authoritative.
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT OR IGNORE INTO completed_uploads(upload_id,project_id,folder,file_id,was_existing,completed_at)
 SELECT p.upload_id,p.project_id,p.folder,f.id,0,? FROM pending_uploads p JOIN files f ON f.storage_key=p.storage_key AND f.sha256=p.declared_sha256 AND f.project_id=p.project_id`, time.Now().Unix())
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM pending_uploads WHERE upload_id IN(SELECT upload_id FROM completed_uploads)`)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	entries, err := os.ReadDir(uploadsDir(app))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || !validUploadID(e.Name()) {
			continue
		}
		meta, err := loadUploadMeta(uploadSessionDir(app, e.Name()))
		if err != nil {
			continue
		}
		// Leftover scratch after completion/abort must not recreate its quota.
		_, err = app.AppDB().Exec(`INSERT OR IGNORE INTO upload_reservations(upload_id,project_id,size_bytes)
 SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM completed_uploads WHERE upload_id=?)
 AND NOT EXISTS(SELECT 1 FROM upload_reservation_cleanup WHERE upload_id=?)`, e.Name(), meta.ProjectID, meta.DeclaredSize, e.Name(), e.Name())
		if err != nil {
			return err
		}
	}
	return reconcileUploadReservations(app)
}

func pruneCompletedUploadReceipts(app *sdk.AppCtx) {
	// Keep completion evidence while cleanup is pending or scratch remains;
	// otherwise a later startup could recreate a completed reservation.
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	rows, err := app.AppDB().Query(`SELECT upload_id FROM completed_uploads WHERE completed_at<?
 AND NOT EXISTS(SELECT 1 FROM upload_reservations r WHERE r.upload_id=completed_uploads.upload_id)
 ORDER BY completed_at LIMIT 1000`, cutoff)
	if err != nil {
		app.Logger().Warn("completion receipt pruning failed", "error", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		app.Logger().Warn("completion receipt pruning failed", "error", err)
		return
	}
	for _, id := range ids {
		if _, err := os.Stat(uploadSessionDir(app, id)); !os.IsNotExist(err) {
			continue
		}
		if _, err := app.AppDB().Exec(`DELETE FROM completed_uploads WHERE upload_id=? AND completed_at<?
 AND NOT EXISTS(SELECT 1 FROM upload_reservations r WHERE r.upload_id=completed_uploads.upload_id)`, id, cutoff); err != nil {
			app.Logger().Warn("completion receipt pruning failed", "upload_id", id, "error", err)
		}
	}
}

func sweepScratchFiles(app *sdk.AppCtx) {
	entries, err := os.ReadDir(uploadsDir(app))
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < 24*time.Hour {
			continue
		}
		if !e.IsDir() && len(e.Name()) > 7 && e.Name()[:7] == "stream-" {
			_ = os.Remove(filepath.Join(uploadsDir(app), e.Name()))
		}
		// Init can crash between mkdir and meta.json. Reclaim these too.
		if e.IsDir() && validUploadID(e.Name()) {
			if _, err := loadUploadMeta(uploadSessionDir(app, e.Name())); os.IsNotExist(err) {
				_ = os.RemoveAll(uploadSessionDir(app, e.Name()))
				releaseUploadReservation(app, e.Name())
			}
		}
	}
	rows, err := app.AppDB().Query(`SELECT upload_id FROM upload_reservations WHERE expires_at=0 AND created_at < datetime('now','-1 day')`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := os.Stat(uploadSessionDir(app, id)); os.IsNotExist(err) {
			releaseUploadReservation(app, id)
		}
	}
}
