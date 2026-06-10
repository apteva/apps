package main

import (
	"context"
	"database/sql"
	"errors"
)

var errNotFound = errors.New("not found")

func insertProfile(ctx context.Context, db *sql.DB, p storedProfile) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
INSERT INTO source_profiles
  (id, project_id, name, provider, auth_type, encrypted_payload, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		p.ID, p.ProjectID, p.Name, p.Provider, p.AuthType, p.EncryptedPayload, ts, ts)
	return err
}

func listProfiles(ctx context.Context, db *sql.DB, projectID string) ([]sourceProfile, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, project_id, name, provider, auth_type, status, COALESCE(last_validated_at,''), COALESCE(last_error,''), created_at, updated_at
FROM source_profiles
WHERE project_id = ? AND status != 'deleted'
ORDER BY updated_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sourceProfile
	for rows.Next() {
		var p sourceProfile
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Provider, &p.AuthType, &p.Status, &p.LastValidatedAt, &p.LastError, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func getProfile(ctx context.Context, db *sql.DB, projectID, id string) (storedProfile, error) {
	var p storedProfile
	err := db.QueryRowContext(ctx, `
SELECT id, project_id, name, provider, auth_type, encrypted_payload, status, COALESCE(last_validated_at,''), COALESCE(last_error,''), created_at, updated_at
FROM source_profiles
WHERE id = ? AND project_id = ? AND status != 'deleted'`, id, projectID).
		Scan(&p.ID, &p.ProjectID, &p.Name, &p.Provider, &p.AuthType, &p.EncryptedPayload, &p.Status, &p.LastValidatedAt, &p.LastError, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound
	}
	return p, err
}

func markProfileValidated(ctx context.Context, db *sql.DB, id, projectID, lastErr string) error {
	_, err := db.ExecContext(ctx, `
UPDATE source_profiles
SET last_validated_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND project_id = ?`, nowRFC3339(), lastErr, nowRFC3339(), id, projectID)
	return err
}

func deleteProfile(ctx context.Context, db *sql.DB, id, projectID string) error {
	res, err := db.ExecContext(ctx, `
UPDATE source_profiles
SET status = 'deleted', updated_at = ?
WHERE id = ? AND project_id = ? AND status != 'deleted'`, nowRFC3339(), id, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

func insertDownload(ctx context.Context, db *sql.DB, j downloadJob) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
INSERT INTO downloads
  (id, project_id, url, status, progress, mode, quality, format_id, source_profile_id, storage_folder, storage_visibility, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.ProjectID, j.URL, j.Status, j.Mode, j.Quality, j.FormatID, j.SourceProfileID, j.StorageFolder, j.StorageVisibility, ts, ts)
	return err
}

func getDownload(ctx context.Context, db *sql.DB, projectID, id string) (downloadJob, error) {
	var j downloadJob
	err := db.QueryRowContext(ctx, `
SELECT id, project_id, url, status, progress, COALESCE(title,''), COALESCE(extractor,''), mode, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
FROM downloads
WHERE id = ? AND project_id = ?`, id, projectID).
		Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, errNotFound
	}
	return j, err
}

func listDownloads(ctx context.Context, db *sql.DB, projectID string, limit int) ([]downloadJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, project_id, url, status, progress, COALESCE(title,''), COALESCE(extractor,''), mode, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
FROM downloads
WHERE project_id = ?
ORDER BY updated_at DESC
LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []downloadJob
	for rows.Next() {
		var j downloadJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func setDownloadRunning(ctx context.Context, db *sql.DB, id string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `UPDATE downloads SET status = ?, started_at = ?, updated_at = ? WHERE id = ?`, statusRunning, ts, ts, id)
	return err
}

func updateDownloadProgress(ctx context.Context, db *sql.DB, id string, progress float64) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := db.ExecContext(ctx, `UPDATE downloads SET progress = ?, updated_at = ? WHERE id = ?`, progress, nowRFC3339(), id)
	return err
}

func setDownloadProbe(ctx context.Context, db *sql.DB, id, title, extractor string) error {
	_, err := db.ExecContext(ctx, `UPDATE downloads SET title = ?, extractor = ?, updated_at = ? WHERE id = ?`, title, extractor, nowRFC3339(), id)
	return err
}

func completeDownload(ctx context.Context, db *sql.DB, id, outputName string, outputBytes, storageFileID int64, storageURL string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
UPDATE downloads
SET status = ?, progress = 100, output_name = ?, output_bytes = ?, storage_file_id = ?, storage_url = ?, completed_at = ?, updated_at = ?, error = ''
WHERE id = ?`, statusCompleted, outputName, outputBytes, storageFileID, storageURL, ts, ts, id)
	return err
}

func failDownload(ctx context.Context, db *sql.DB, id, status, message string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
UPDATE downloads
SET status = ?, error = ?, completed_at = ?, updated_at = ?
WHERE id = ?`, status, message, ts, ts, id)
	return err
}

func appendLog(ctx context.Context, db *sql.DB, id, level, message string) {
	message = trimLogLine(message)
	if message == "" {
		return
	}
	_, _ = db.ExecContext(ctx, `INSERT INTO download_logs(download_id, level, message, created_at) VALUES (?, ?, ?, ?)`, id, level, message, nowRFC3339())
}

func interruptActiveDownloads(ctx context.Context, db *sql.DB, reason string) ([]downloadJob, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, project_id, url, status, progress, COALESCE(title,''), COALESCE(extractor,''), mode, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
FROM downloads
WHERE status IN (?, ?)`, statusQueued, statusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []downloadJob
	for rows.Next() {
		var j downloadJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		j.Status = statusFailed
		j.Error = reason
		j.CompletedAt = nowRFC3339()
		j.UpdatedAt = j.CompletedAt
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, job := range jobs {
		appendLog(ctx, db, job.ID, "error", reason)
		if err := failDownload(ctx, db, job.ID, statusFailed, reason); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func recentLogs(ctx context.Context, db *sql.DB, id string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := db.QueryContext(ctx, `
SELECT level, message, created_at
FROM download_logs
WHERE download_id = ?
ORDER BY id DESC
LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var level, message, createdAt string
		if err := rows.Scan(&level, &message, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"level": level, "message": message, "created_at": createdAt})
	}
	return out, rows.Err()
}
