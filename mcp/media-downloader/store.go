package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	status := "valid"
	if lastErr != "" {
		status = "invalid"
	}
	_, err := db.ExecContext(ctx, `
UPDATE source_profiles
SET status = ?, last_validated_at = ?, last_error = ?, updated_at = ?
WHERE id = ? AND project_id = ?`, status, nowRFC3339(), lastErr, nowRFC3339(), id, projectID)
	return err
}

func deleteProfile(ctx context.Context, db *sql.DB, id, projectID string) error {
	res, err := db.ExecContext(ctx, `
UPDATE source_profiles
SET status = 'deleted', encrypted_payload = '', last_error = '', updated_at = ?
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
  (id, project_id, url, status, stage, progress, mode, ingest, quality, format_id, source_profile_id, storage_folder, storage_visibility, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.ProjectID, j.URL, j.Status, j.Stage, j.Mode, j.Ingest, j.Quality, j.FormatID, j.SourceProfileID, j.StorageFolder, j.StorageVisibility, ts, ts)
	return err
}

func getDownload(ctx context.Context, db *sql.DB, projectID, id string) (downloadJob, error) {
	var j downloadJob
	var metadataJSON, warningsJSON string
	err := db.QueryRowContext(ctx, `
SELECT id, project_id, url, status, stage, progress, COALESCE(title,''), COALESCE(extractor,''), mode, ingest, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(metadata_json,''), COALESCE(warnings_json,'[]'), COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
FROM downloads
WHERE id = ? AND project_id = ?`, id, projectID).
		Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Stage, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Ingest, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &metadataJSON, &warningsJSON, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, errNotFound
	}
	if err != nil {
		return j, err
	}
	decodeDownloadDetails(&j, metadataJSON, warningsJSON)
	j.Artifacts, err = listDownloadArtifacts(ctx, db, j.ID)
	j.StorageFileIDs = artifactStorageFileIDs(j.Artifacts)
	return j, err
}

func listDownloads(ctx context.Context, db *sql.DB, projectID string, limit int) ([]downloadJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := db.QueryContext(ctx, `
SELECT id, project_id, url, status, stage, progress, COALESCE(title,''), COALESCE(extractor,''), mode, ingest, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(metadata_json,''), COALESCE(warnings_json,'[]'), COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
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
		var metadataJSON, warningsJSON string
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Stage, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Ingest, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &metadataJSON, &warningsJSON, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		decodeDownloadDetails(&j, metadataJSON, warningsJSON)
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Artifacts, err = listDownloadArtifacts(ctx, db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].StorageFileIDs = artifactStorageFileIDs(out[i].Artifacts)
	}
	return out, nil
}

func setDownloadRunning(ctx context.Context, db *sql.DB, id string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `UPDATE downloads SET status = ?, stage = ?, progress = 0, started_at = ?, updated_at = ? WHERE id = ?`, statusRunning, stageDownloading, ts, ts, id)
	return err
}

func updateDownloadStage(ctx context.Context, db *sql.DB, id, stage string, progress float64) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := db.ExecContext(ctx, `UPDATE downloads SET stage = ?, progress = ?, updated_at = ? WHERE id = ?`, stage, progress, nowRFC3339(), id)
	return err
}

func setDownloadProbe(ctx context.Context, db *sql.DB, id, title, extractor string) error {
	_, err := db.ExecContext(ctx, `UPDATE downloads SET title = ?, extractor = ?, updated_at = ? WHERE id = ?`, title, extractor, nowRFC3339(), id)
	return err
}

func setDownloadMetadata(ctx context.Context, db *sql.DB, id string, metadata sourceMetadata) error {
	body, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE downloads SET title = ?, extractor = ?, metadata_json = ?, updated_at = ? WHERE id = ?`, metadata.Title, metadata.Extractor, string(body), nowRFC3339(), id)
	return err
}

func addDownloadWarning(ctx context.Context, db *sql.DB, id, warning string) error {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(warnings_json,'[]') FROM downloads WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	warnings := []string{}
	_ = json.Unmarshal([]byte(raw), &warnings)
	warnings = append(warnings, warning)
	body, _ := json.Marshal(warnings)
	_, err := db.ExecContext(ctx, `UPDATE downloads SET warnings_json = ?, updated_at = ? WHERE id = ?`, string(body), nowRFC3339(), id)
	return err
}

func insertDownloadArtifact(ctx context.Context, db *sql.DB, downloadID string, artifact downloadArtifact) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO download_artifacts
  (download_id, kind, storage_file_id, storage_url, name, content_type, bytes, language, caption_source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		downloadID, artifact.Kind, artifact.StorageFileID, artifact.StorageURL, artifact.Name, artifact.ContentType, artifact.Bytes, artifact.Language, artifact.CaptionSource, nowRFC3339())
	return err
}

func listDownloadArtifacts(ctx context.Context, db *sql.DB, downloadID string) ([]downloadArtifact, error) {
	rows, err := db.QueryContext(ctx, `
SELECT kind, storage_file_id, COALESCE(storage_url,''), name, COALESCE(content_type,''), bytes, COALESCE(language,''), COALESCE(caption_source,'')
FROM download_artifacts WHERE download_id = ? ORDER BY id`, downloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]downloadArtifact, 0)
	for rows.Next() {
		var artifact downloadArtifact
		if err := rows.Scan(&artifact.Kind, &artifact.StorageFileID, &artifact.StorageURL, &artifact.Name, &artifact.ContentType, &artifact.Bytes, &artifact.Language, &artifact.CaptionSource); err != nil {
			return nil, err
		}
		out = append(out, artifact)
	}
	return out, rows.Err()
}

func artifactStorageFileIDs(artifacts []downloadArtifact) []int64 {
	ids := make([]int64, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.StorageFileID != 0 {
			ids = append(ids, artifact.StorageFileID)
		}
	}
	return ids
}

func decodeDownloadDetails(job *downloadJob, metadataJSON, warningsJSON string) {
	if metadataJSON != "" {
		var metadata sourceMetadata
		if json.Unmarshal([]byte(metadataJSON), &metadata) == nil {
			job.Metadata = &metadata
		}
	}
	_ = json.Unmarshal([]byte(warningsJSON), &job.Warnings)
}

func completeDownload(ctx context.Context, db *sql.DB, id, outputName string, outputBytes, storageFileID int64, storageURL string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
UPDATE downloads
SET status = ?, stage = ?, progress = 100, output_name = ?, output_bytes = ?, storage_file_id = ?, storage_url = ?, completed_at = ?, updated_at = ?, error = ''
WHERE id = ?`, statusCompleted, stageCompleted, outputName, outputBytes, storageFileID, storageURL, ts, ts, id)
	return err
}

func failDownload(ctx context.Context, db *sql.DB, id, status, message string) error {
	ts := nowRFC3339()
	_, err := db.ExecContext(ctx, `
UPDATE downloads
SET status = ?, stage = ?, error = ?, completed_at = ?, updated_at = ?
WHERE id = ?`, status, terminalStage(status), message, ts, ts, id)
	return err
}

func terminalStage(status string) string {
	if status == statusCanceled {
		return stageCanceled
	}
	return stageFailed
}

func appendLog(ctx context.Context, db *sql.DB, id, level, message string) {
	message = trimLogLine(message)
	if message == "" {
		return
	}
	_, _ = db.ExecContext(ctx, `INSERT INTO download_logs(download_id, level, message, created_at) VALUES (?, ?, ?, ?)`, id, level, message, nowRFC3339())
}

func pruneDownloadLogs(ctx context.Context, db *sql.DB, id string, keep int) {
	if keep <= 0 {
		keep = 200
	}
	_, _ = db.ExecContext(ctx, `
DELETE FROM download_logs
WHERE download_id = ?
  AND id NOT IN (
    SELECT id FROM download_logs
    WHERE download_id = ?
    ORDER BY id DESC
    LIMIT ?
  )`, id, id, keep)
}

func interruptActiveDownloads(ctx context.Context, db *sql.DB, reason string) ([]downloadJob, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, project_id, url, status, stage, progress, COALESCE(title,''), COALESCE(extractor,''), mode, ingest, quality, COALESCE(format_id,''), COALESCE(source_profile_id,''), storage_folder, storage_visibility,
       COALESCE(storage_file_id,0), COALESCE(storage_url,''), COALESCE(output_name,''), output_bytes, COALESCE(metadata_json,''), COALESCE(warnings_json,'[]'), COALESCE(error,''), created_at, COALESCE(started_at,''), COALESCE(completed_at,''), updated_at
FROM downloads
WHERE status IN (?, ?)`, statusQueued, statusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []downloadJob
	for rows.Next() {
		var j downloadJob
		var metadataJSON, warningsJSON string
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.URL, &j.Status, &j.Stage, &j.Progress, &j.Title, &j.Extractor, &j.Mode, &j.Ingest, &j.Quality, &j.FormatID, &j.SourceProfileID, &j.StorageFolder, &j.StorageVisibility, &j.StorageFileID, &j.StorageURL, &j.OutputName, &j.OutputBytes, &metadataJSON, &warningsJSON, &j.Error, &j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		decodeDownloadDetails(&j, metadataJSON, warningsJSON)
		j.Status = statusFailed
		j.Stage = stageFailed
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
