package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errRecordingStorageUnbound = errors.New("Storage app is not bound")

type recordingSettings struct {
	ProjectID     string `json:"project_id"`
	DefaultMode   string `json:"default_mode"`
	Channels      string `json:"channels"`
	StorageMode   string `json:"storage_mode"`
	StorageFormat string `json:"storage_format"`
	RetentionDays int    `json:"retention_days"`
	MaxFileBytes  int64  `json:"max_file_bytes"`
	UpdatedAt     string `json:"updated_at"`
}

func defaultRecordingSettings(projectID string) recordingSettings {
	return recordingSettings{
		ProjectID: projectID, DefaultMode: recordingModeOff, Channels: "dual",
		StorageMode: recordingStorageCopy, StorageFormat: "mp3", RetentionDays: 0, MaxFileBytes: maxRecordingFileBytes,
	}
}

func (c *callsDB) recordingSettings(projectID string) (recordingSettings, error) {
	settings := defaultRecordingSettings(projectID)
	err := c.db.QueryRow(`SELECT default_mode, channels, storage_mode, retention_days, updated_at, max_file_bytes, storage_format
        FROM recording_settings WHERE project_id = ?`, projectID).
		Scan(&settings.DefaultMode, &settings.Channels, &settings.StorageMode, &settings.RetentionDays, &settings.UpdatedAt, &settings.MaxFileBytes, &settings.StorageFormat)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	return settings, err
}

func (c *callsDB) saveRecordingSettings(settings recordingSettings) error {
	if settings.StorageFormat == "" {
		settings.StorageFormat = "mp3"
	}
	if settings.MaxFileBytes == 0 {
		settings.MaxFileBytes = maxRecordingFileBytes
	}
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := c.db.Exec(`INSERT INTO recording_settings
        (project_id, default_mode, channels, storage_mode, retention_days, updated_at, max_file_bytes, storage_format)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(project_id) DO UPDATE SET default_mode = excluded.default_mode,
          channels = excluded.channels, storage_mode = excluded.storage_mode,
          retention_days = excluded.retention_days, updated_at = excluded.updated_at, max_file_bytes = excluded.max_file_bytes, storage_format = excluded.storage_format`,
		settings.ProjectID, settings.DefaultMode, settings.Channels, settings.StorageMode,
		settings.RetentionDays, settings.UpdatedAt, settings.MaxFileBytes, settings.StorageFormat)
	return err
}

func normalizeRouteRecordingMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != recordingModeInherit && mode != recordingModeOff && mode != recordingModeAlways {
		return "", errors.New("recording_mode must be inherit, off, or always")
	}
	return mode, nil
}

func validateRecordingSettings(settings recordingSettings) error {
	if settings.StorageFormat != "" && settings.StorageFormat != "mp3" && settings.StorageFormat != "wav" {
		return errors.New("storage_format must be mp3 or wav")
	}
	if settings.DefaultMode != recordingModeOff && settings.DefaultMode != recordingModeAlways {
		return errors.New("default_mode must be off or always")
	}
	if settings.Channels != "mono" && settings.Channels != "dual" {
		return errors.New("channels must be mono or dual")
	}
	if settings.StorageMode != recordingStorageCopy && settings.StorageMode != recordingStorageMove {
		return errors.New("storage_mode must be copy_to_storage or copy_then_delete_provider")
	}
	if settings.MaxFileBytes != 0 && (settings.MaxFileBytes < 1<<20 || settings.MaxFileBytes > maxRecordingFileBytes) {
		return errors.New("max_file_bytes must be between 1 MiB and 1 GiB; applies to import/playback, not capture")
	}
	if settings.RetentionDays < 0 || settings.RetentionDays > 3650 {
		return errors.New("retention_days must be between 0 and 3650")
	}
	return nil
}

func (a *App) toolRouteRecordingPolicy(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	route, err := a.routeForCaller(ctx, strArg(args, "route_id", ""), callerAgentID(callerCtx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	mode, err := normalizeRouteRecordingMode(strArg(args, "recording_mode", ""))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if mode == recordingModeAlways && route.InboundTransport != inboundTransportSIPDirect && !providerSupportsRecording(route.CarrierSlug) {
		return mcpError("call recording is not implemented for provider " + route.CarrierSlug), nil
	}
	if _, err := ctx.AppDB().Exec(`UPDATE inbound_routes SET recording_mode = ?, updated_at = ? WHERE id = ?`,
		mode, time.Now().UTC().Format(time.RFC3339), route.ID); err != nil {
		return mcpError("persist recording policy: " + err.Error()), nil
	}
	route.RecordingMode = mode
	return map[string]any{"ok": true, "route": routePublic(a, *route)}, nil
}

func (a *App) toolRecordingSettingsGet(_ context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	settings, err := a.db().recordingSettings(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return a.recordingSettingsPublic(ctx, settings), nil
}

func (a *App) toolRecordingSettingsSet(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, err := a.db().recordingSettings(currentProject(ctx))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	applyRecordingSettingsArgs(&settings, args)
	if err := validateRecordingSettings(settings); err != nil {
		return mcpError(err.Error()), nil
	}
	carrier, supported := recordingCarrierSupport(ctx)
	if settings.DefaultMode == recordingModeAlways && !supported {
		return mcpError("recording is not yet supported for the bound carrier " + carrier), nil
	}
	if err := a.db().saveRecordingSettings(settings); err != nil {
		return mcpError(err.Error()), nil
	}
	return a.recordingSettingsPublic(ctx, settings), nil
}

func applyRecordingSettingsArgs(settings *recordingSettings, args map[string]any) {
	if value, ok := args["storage_format"].(string); ok {
		settings.StorageFormat = strings.ToLower(strings.TrimSpace(value))
		if settings.StorageFormat == "" {
			settings.StorageFormat = "mp3"
		}
	}
	if value, ok := args["default_mode"].(string); ok {
		settings.DefaultMode = strings.ToLower(strings.TrimSpace(value))
	}
	if value, ok := args["channels"].(string); ok {
		settings.Channels = strings.ToLower(strings.TrimSpace(value))
	}
	if value, ok := args["storage_mode"].(string); ok {
		settings.StorageMode = strings.ToLower(strings.TrimSpace(value))
	}
	if _, ok := args["max_file_bytes"]; ok {
		settings.MaxFileBytes = recordingMaxFileArg(args["max_file_bytes"])
	}
	if _, ok := args["retention_days"]; ok {
		settings.RetentionDays = intArg(args, "retention_days", settings.RetentionDays)
	}
}

func recordingCarrierSupport(ctx *sdk.AppCtx) (string, bool) {
	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return "none", false
	}
	slug := strings.ToLower(strings.TrimSpace(bound.AppSlug))
	if creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID); err == nil && creds != nil {
		slug = strings.ToLower(firstNonEmpty(creds.Slug, slug))
	}
	return slug, providerSupportsRecording(slug) || slug == "didww"
}

func providerSupportsRecording(slug string) bool {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "twilio", "telnyx", "plivo", "bandwidth", localRecordingProvider:
		return true
	default:
		return false
	}
}

func (a *App) recordingSettingsPublic(ctx *sdk.AppCtx, settings recordingSettings) map[string]any {
	carrier, supported := recordingCarrierSupport(ctx)
	return map[string]any{
		"project_id": settings.ProjectID, "default_mode": settings.DefaultMode,
		"channels": settings.Channels, "storage_mode": settings.StorageMode, "storage_format": settings.StorageFormat,
		"retention_days": settings.RetentionDays, "updated_at": settings.UpdatedAt, "max_file_bytes": settings.MaxFileBytes,
		"carrier": carrier, "recording_supported": supported,
	}
}

type recordingRow struct {
	ID                  string
	CallID              string
	ProjectID           string
	Provider            string
	CarrierConnectionID int64
	ProviderRecordingID string
	ProviderStatus      string
	Channels            int
	Track               string
	Format              string
	StorageFormat       string
	SourceSizeBytes     int64
	DurationMS          int64
	SizeBytes           int64
	StorageFileID       int64
	StorageStatus       string
	ImportAttempts      int
	ImportStartedAt     string
	NextAttemptAt       string
	LastError           string
	ImportBlockedReason string
	ImportLimitBytes    int64
	ProviderDeletedAt   string
	RetentionExpiresAt  string
	CreatedAt           string
	CompletedAt         string
	StoredAt            string
	DeletedAt           string
}

const recordingSelectColumns = `id, call_id, project_id, provider, carrier_connection_id,
    provider_recording_id, provider_status, channels, track, format, duration_ms,
    size_bytes, storage_file_id, storage_status, import_attempts, import_started_at,
    next_attempt_at, last_error, provider_deleted_at, retention_expires_at,
    created_at, completed_at, stored_at, deleted_at, import_blocked_reason, import_limit_bytes, storage_format, source_size_bytes`

func scanRecording(row rowScanner) (*recordingRow, error) {
	var recording recordingRow
	err := row.Scan(&recording.ID, &recording.CallID, &recording.ProjectID, &recording.Provider,
		&recording.CarrierConnectionID, &recording.ProviderRecordingID, &recording.ProviderStatus,
		&recording.Channels, &recording.Track, &recording.Format, &recording.DurationMS,
		&recording.SizeBytes, &recording.StorageFileID, &recording.StorageStatus,
		&recording.ImportAttempts, &recording.ImportStartedAt, &recording.NextAttemptAt,
		&recording.LastError, &recording.ProviderDeletedAt, &recording.RetentionExpiresAt,
		&recording.CreatedAt, &recording.CompletedAt, &recording.StoredAt, &recording.DeletedAt, &recording.ImportBlockedReason, &recording.ImportLimitBytes, &recording.StorageFormat, &recording.SourceSizeBytes)
	return &recording, err
}

func (c *callsDB) findRecording(id string) (*recordingRow, error) {
	row, err := scanRecording(c.db.QueryRow(`SELECT `+recordingSelectColumns+` FROM recordings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func (c *callsDB) listRecordings(projectID, callID string, limit int) ([]recordingRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := c.db.Query(`SELECT `+recordingSelectColumns+` FROM recordings
        WHERE project_id = ? AND deleted_at = '' AND (? = '' OR call_id = ?)
        ORDER BY created_at DESC LIMIT ?`, projectID, callID, callID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recordingRow
	for rows.Next() {
		row, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row)
	}
	return out, rows.Err()
}

func (c *callsDB) attachRecordingSummaries(projectID string, calls []callRow) error {
	ids, args := callReadIDs(projectID, calls)
	if len(ids) == 0 {
		return nil
	}
	rows, err := c.db.Query(`SELECT call_id, COUNT(*),
		MAX(CASE storage_status WHEN 'stored' THEN 5 WHEN 'provider_only' THEN 4 WHEN 'importing' THEN 3
			WHEN 'pending' THEN 2 WHEN 'failed' THEN 1 ELSE 0 END)
        FROM recordings WHERE project_id = ? AND deleted_at = '' AND call_id IN (`+phonePlaceholders(len(ids))+`) GROUP BY call_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type summary struct{ count, state int }
	summaries := map[string]summary{}
	for rows.Next() {
		var callID string
		var item summary
		if err := rows.Scan(&callID, &item.count, &item.state); err != nil {
			return err
		}
		summaries[callID] = item
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range calls {
		item := summaries[calls[i].ID]
		calls[i].RecordingCount = item.count
		switch item.state {
		case 5:
			calls[i].RecordingStatus = "stored"
		case 4:
			calls[i].RecordingStatus = recordingStorageProvider
		case 3:
			calls[i].RecordingStatus = "importing"
		case 2:
			calls[i].RecordingStatus = "pending"
		case 1:
			calls[i].RecordingStatus = "failed"
		}
	}
	return nil
}

func (c *callsDB) upsertTwilioRecording(call *callRow, sid, status string, durationMS int64, channels int) (*recordingRow, error) {
	return c.upsertProviderRecording(call, "twilio", sid, status, "wav", durationMS, channels, "both")
}

func (c *callsDB) upsertProviderRecording(call *callRow, provider, recordingID, status, format string, durationMS int64, channels int, track string) (*recordingRow, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if call == nil || recordingID == "" || !providerSupportsRecording(provider) {
		return nil, errors.New("call, supported provider, and recording id are required")
	}
	if channels <= 0 {
		channels = 1
		if call.RecordingChannels == "dual" {
			channels = 2
		}
	}
	if format != "mp3" {
		format = "wav"
	}
	if track == "" {
		track = "both"
	}
	now := time.Now().UTC()
	storageStatus := "pending"
	completedAt := now.Format(time.RFC3339)
	if status == "absent" {
		storageStatus = "absent"
		completedAt = ""
	}
	retentionExpiry := ""
	if call.RecordingRetentionDays > 0 {
		retentionExpiry = now.Add(time.Duration(call.RecordingRetentionDays) * 24 * time.Hour).Format(time.RFC3339)
	}
	rowID := "rec-" + newCallID()
	_, err := c.db.Exec(`INSERT INTO recordings
        (id, call_id, project_id, provider, carrier_connection_id, provider_recording_id,
         provider_status, channels, track, format, duration_ms, storage_status,
         next_attempt_at, retention_expires_at, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(provider, carrier_connection_id, provider_recording_id) DO UPDATE SET
          provider_status = excluded.provider_status, channels = excluded.channels,
          duration_ms = excluded.duration_ms, completed_at = excluded.completed_at,
		  storage_status = CASE WHEN recordings.storage_status IN ('stored','deleted','provider_only','importing') OR recordings.import_blocked_reason<>''
								THEN recordings.storage_status ELSE excluded.storage_status END,
		  next_attempt_at = CASE WHEN recordings.storage_status IN ('stored','deleted','provider_only','importing') OR recordings.import_blocked_reason<>''
                                 THEN recordings.next_attempt_at ELSE excluded.next_attempt_at END`,
		rowID, call.ID, call.ProjectID, provider, call.CarrierConnectionID, recordingID, status, channels, track, format,
		durationMS, storageStatus, now.Format(time.RFC3339), retentionExpiry,
		now.Format(time.RFC3339), completedAt)
	if err != nil {
		return nil, err
	}
	return scanRecording(c.db.QueryRow(`SELECT `+recordingSelectColumns+` FROM recordings
		WHERE provider = ? AND carrier_connection_id = ? AND provider_recording_id = ?`, provider, call.CarrierConnectionID, recordingID))
}

func (a *App) handleTwilioRecordingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	callID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/webhook/recording/twilio/"), "/")
	if callID == "" || strings.Contains(callID, "/") {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	call, err := a.db().findCall(callID)
	if err != nil || call == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if call.CarrierSlug != "twilio" || a.authorizeCallRequest(r, call) != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if sid := strings.TrimSpace(r.FormValue("CallSid")); sid == "" || (call.CarrierSID != "" && sid != call.CarrierSID) {
		http.Error(w, "call does not match recording", http.StatusForbidden)
		return
	}
	recordingSID := strings.TrimSpace(r.FormValue("RecordingSid"))
	if !strings.HasPrefix(recordingSID, "RE") || len(recordingSID) > 64 {
		http.Error(w, "invalid recording SID", http.StatusBadRequest)
		return
	}
	status := strings.ToLower(strings.TrimSpace(r.FormValue("RecordingStatus")))
	if status != "completed" && status != "absent" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	durationSeconds, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("RecordingDuration")), 10, 64)
	channels, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("RecordingChannels")))
	if channels <= 0 {
		channels = 1
	}
	recording, err := a.db().upsertTwilioRecording(call, recordingSID, status, durationSeconds*1000, channels)
	if err != nil {
		http.Error(w, "persist recording", http.StatusInternalServerError)
		return
	}
	a.saveCallNotification(call.ProjectID, call.ID, "recording.ready", recordingPublic(*recording))
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) runRecordingTick(workerCtx context.Context, ctx *sdk.AppCtx) error {
	projectID := currentProject(ctx)
	if projectID == "" {
		return nil
	}
	if _, err := ctx.AppDB().ExecContext(workerCtx, `UPDATE recordings INDEXED BY idx_recordings_importing SET storage_status = 'failed',
        last_error = 'recording import interrupted', next_attempt_at = ?, import_started_at = ''
        WHERE project_id = ? AND storage_status = 'importing' AND import_started_at < ?`,
		time.Now().UTC().Format(time.RFC3339), projectID, time.Now().UTC().Add(-15*time.Minute).Format(time.RFC3339)); err != nil {
		return err
	}
	recording, err := a.db().claimRecordingImport(projectID)
	if err != nil {
		return err
	}
	if recording != nil {
		if err := a.importRecording(workerCtx, ctx, recording); err != nil {
			if errors.Is(err, errRecordingStorageUnbound) {
				a.db().markRecordingProviderOnly(recording.ID, recording.ImportStartedAt)
			} else {
				a.db().failRecordingImport(recording.ID, err, recording.ImportStartedAt)
				ctx.Logger().Warn("recording import", "recording", recording.ID, "call", recording.CallID, "err", err)
			}
		}
	}
	if err := a.reconcileProviderRecording(ctx); err != nil {
		ctx.Logger().Warn("recording reconciliation", "err", err)
	}
	if err := a.cleanupRecordingJobs(ctx); err != nil {
		ctx.Logger().Warn("recording cleanup retry", "err", err)
	}
	return a.expireRecordings(ctx)
}

func (c *callsDB) claimRecordingImport(projectID string) (*recordingRow, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	var id string
	err = tx.QueryRow(`SELECT id FROM recordings INDEXED BY idx_recordings_import_candidates WHERE project_id = ? AND deleted_at = ''
		AND provider_status = 'completed' AND storage_status IN ('pending','failed','provider_only')
        AND (import_blocked_reason='' OR import_limit_bytes <> COALESCE((SELECT max_file_bytes FROM recording_settings WHERE project_id=recordings.project_id),1073741824))
 AND (next_attempt_at = '' OR next_attempt_at <= ? OR (import_blocked_reason<>'' AND import_limit_bytes <> COALESCE((SELECT max_file_bytes FROM recording_settings WHERE project_id=recordings.project_id),1073741824))) ORDER BY created_at LIMIT 1`, projectID, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(`UPDATE recordings SET storage_status = 'importing', import_started_at = ?,
		last_error = '', import_blocked_reason='', import_limit_bytes=COALESCE((SELECT max_file_bytes FROM recording_settings WHERE project_id=recordings.project_id),1073741824) WHERE id = ? AND storage_status IN ('pending','failed','provider_only')`, now, id)
	if err != nil {
		return nil, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return nil, nil
	}
	row, err := scanRecording(tx.QueryRow(`SELECT `+recordingSelectColumns+` FROM recordings WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return row, nil
}

func (a *App) importRecording(workerCtx context.Context, ctx *sdk.AppCtx, recording *recordingRow) error {
	call, err := a.db().findCall(recording.CallID)
	if err != nil || call == nil {
		return errors.New("recording call metadata is unavailable")
	}
	fields := map[string]string{}
	if recording.Provider != localRecordingProvider {
		creds, err := ctx.PlatformAPI().GetConnectionCredentials(recording.CarrierConnectionID)
		if err != nil || creds == nil {
			return errors.New("load carrier credentials failed")
		}
		fields = creds.Fields
	}
	importCtx, cancel := context.WithTimeout(workerCtx, 12*time.Minute)
	defer cancel()
	settings, err := a.db().recordingSettings(recording.ProjectID)
	if err != nil {
		return err
	}
	limit := recording.ImportLimitBytes
	if limit <= 0 {
		limit = settings.MaxFileBytes
	}
	importCtx = context.WithValue(importCtx, playbackDownloadContextKey{}, limit)
	select {
	case playbackCache.slots <- struct{}{}:
	case <-importCtx.Done():
		return importCtx.Err()
	}
	defer func() { <-playbackCache.slots }()
	storageClient := newRecordingStorageClient()
	probeCtx, probeCancel := context.WithTimeout(importCtx, 10*time.Second)
	storageBound, err := storageClient.bindingAvailable(probeCtx, recording.ProjectID)
	probeCancel()
	if err != nil {
		return fmt.Errorf("check Storage binding: %w", err)
	}
	if !storageBound {
		return errRecordingStorageUnbound
	}
	path, size, err := a.downloadProviderRecording(importCtx, ctx, recording, fields)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	sourceSize := size
	storedPath, storedFormat, err := prepareRecordingStorage(importCtx, path, recording.Format, settings.StorageFormat, recording.Channels)
	if err != nil {
		return fmt.Errorf("prepare stored recording: %w", err)
	}
	if storedPath != path {
		defer os.Remove(storedPath)
	}
	info, err := os.Stat(storedPath)
	if err != nil {
		return err
	}
	size = info.Size()
	if size > limit {
		return &recordingSizeLimitError{limit}
	}
	uploadRow := *recording
	uploadRow.Format = storedFormat
	file, err := storageClient.upload(importCtx, recording.ProjectID, &uploadRow, storedPath)
	if err != nil {
		return err
	}
	if file.SizeBytes != size {
		a.queueOrphanRecordingFile(ctx, file.ID)
		return fmt.Errorf("downloaded %d bytes but Storage retained %d", size, file.SizeBytes)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := ctx.AppDB().Exec(`UPDATE recordings SET storage_file_id = ?, size_bytes = ?,
        storage_status = 'stored', stored_at = ?, import_started_at = '', next_attempt_at = '',
        last_error = '', import_blocked_reason='', storage_format=?, source_size_bytes=? WHERE id = ? AND deleted_at='' AND storage_status='importing' AND import_started_at=?`, file.ID, file.SizeBytes, now, storedFormat, sourceSize, recording.ID, recording.ImportStartedAt)
	if err != nil {
		a.queueOrphanRecordingFile(ctx, file.ID)
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		a.queueOrphanRecordingFile(ctx, file.ID)
		return errors.New("recording import claim expired or recording was deleted")
	}
	recording.SourceSizeBytes = sourceSize
	recording.StorageFormat = storedFormat
	recording.StorageFileID = file.ID
	recording.SizeBytes = file.SizeBytes
	recording.StorageStatus = "stored"
	recording.StoredAt = now
	if call.RecordingStorageMode == recordingStorageMove {
		if err := a.deleteProviderRecording(ctx, recording); err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE recordings SET last_error = ? WHERE id = ?`, "stored; provider cleanup failed: "+err.Error(), recording.ID)
		} else {
			recording.ProviderDeletedAt = now
		}
	}
	ctx.Emit("recording.stored", recordingPublic(*recording))
	return nil
}

func (c *callsDB) markRecordingProviderOnly(id string, leases ...string) {
	lease := ""
	if len(leases) > 0 {
		lease = leases[0]
	}
	_, _ = c.db.Exec(`UPDATE recordings SET storage_status = ?, import_started_at = '',
		next_attempt_at = ?, last_error = '' WHERE id = ? AND deleted_at='' AND storage_status='importing' AND (?='' OR import_started_at=?)`, recordingStorageProvider,
		time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339), id, lease, lease)
}

func (c *callsDB) failRecordingImport(id string, importErr error, leases ...string) {
	lease := ""
	if len(leases) > 0 {
		lease = leases[0]
	}
	var attempts int
	_ = c.db.QueryRow(`SELECT import_attempts FROM recordings WHERE id = ?`, id).Scan(&attempts)
	blocked := recordingImportBlockReason(importErr)
	delay := time.Duration(math.Min(3600, math.Pow(2, float64(min(attempts+1, 11))))) * time.Second
	_, _ = c.db.Exec(`UPDATE recordings SET storage_status = 'failed', import_attempts = import_attempts + 1,
        import_started_at = '', next_attempt_at = ?, last_error = ?, import_blocked_reason=? WHERE id = ? AND deleted_at='' AND storage_status='importing' AND (?='' OR import_started_at=?)`,
		time.Now().UTC().Add(delay).Format(time.RFC3339), importErr.Error(), blocked, id, lease, lease)
}

func (a *App) reconcileProviderRecording(ctx *sdk.AppCtx) error {
	now := time.Now().UTC()
	var id string
	err := ctx.AppDB().QueryRow(`SELECT id FROM calls INDEXED BY idx_calls_recording_reconcile
 WHERE project_id = ? AND recording_known=0 AND carrier_slug IN ('twilio','telnyx','plivo') AND ingress_path <> 'sip_direct' AND recording_mode = 'always'
 AND status IN ('completed','failed') AND carrier_sid <> '' AND ended_at <> '' AND ended_at <= ? AND ended_at >= ?
 AND (recording_checked_at = '' OR recording_checked_at <= ?) AND NOT EXISTS (SELECT 1 FROM recordings r WHERE r.call_id = calls.id)
 ORDER BY ended_at LIMIT 1`, ctx.CurrentProject(), now.Add(-10*time.Second).Format(time.RFC3339), now.Add(-24*time.Hour).Format(time.RFC3339), now.Add(-5*time.Minute).Format(time.RFC3339)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	call, err := a.db().findCall(id)
	if err != nil || call == nil {
		return err
	}

	if _, err := ctx.AppDB().Exec(`UPDATE calls SET recording_checked_at = ? WHERE id = ?`, now.Format(time.RFC3339), call.ID); err != nil {
		return err
	}
	return a.reconcileCallRecordings(ctx, call)
}

func (a *App) expireRecordings(ctx *sdk.AppCtx) error {
	// Split the queues so neither a stored archive nor completed tombstones
	// participate in the candidate scan. Merge only the two eligible heads.
	now := time.Now().UTC().Format(time.RFC3339)
	row, err := scanRecording(ctx.AppDB().QueryRow(`SELECT `+recordingSelectColumns+` FROM recordings WHERE id=(
 SELECT id FROM (
 SELECT * FROM (SELECT id,cleanup_next_at,retention_expires_at FROM recordings INDEXED BY idx_recordings_retention_due
 WHERE project_id=? AND deleted_at='' AND retention_expires_at<>'' AND retention_expires_at<=? AND (cleanup_next_at='' OR cleanup_next_at<=?) ORDER BY retention_expires_at LIMIT 1)
 UNION ALL
 SELECT * FROM (SELECT id,cleanup_next_at,retention_expires_at FROM recordings INDEXED BY idx_recordings_deleted_cleanup
 WHERE project_id=? AND deleted_at<>'' AND (storage_file_id>0 OR provider_deleted_at='') AND (cleanup_next_at='' OR cleanup_next_at<=?) ORDER BY cleanup_next_at,retention_expires_at LIMIT 1)
 ) ORDER BY cleanup_next_at,retention_expires_at LIMIT 1)`, ctx.CurrentProject(), now, now, ctx.CurrentProject(), now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.deleteRecording(ctx, row)
}

func (a *App) deleteProviderRecording(ctx *sdk.AppCtx, recording *recordingRow) error {
	if recording.ProviderDeletedAt != "" || recording.ProviderRecordingID == "" {
		return nil
	}
	if recording.Provider == localRecordingProvider {
		return a.deleteLocalRecording(recording)
	}
	input := map[string]any{"recording_id": recording.ProviderRecordingID}
	if recording.Provider == "twilio" {
		input = map[string]any{"RecordingSid": recording.ProviderRecordingID}
	}
	if _, err := executeCarrierTool(ctx, recording.CarrierConnectionID, "delete_recording", input); err != nil {
		if !strings.Contains(err.Error(), "status=404") {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := ctx.AppDB().Exec(`UPDATE recordings SET provider_deleted_at = ? WHERE id = ?`, now, recording.ID)
	if err == nil {
		recording.ProviderDeletedAt = now
	}
	return err
}

func (a *App) deleteRecording(ctx *sdk.AppCtx, recording *recordingRow) error {
	// Tombstone before any network operation; an in-flight upload may never
	// restore visibility. Failed cleanup stays eligible for the retry worker.
	if _, err := ctx.AppDB().Exec(`UPDATE recordings SET deleted_at=CASE WHEN deleted_at='' THEN ? ELSE deleted_at END, storage_status='deleted',cleanup_next_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Add(time.Minute).Format(time.RFC3339), recording.ID); err != nil {
		return err
	}
	if recording.StorageFileID > 0 {
		var result map[string]any
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", map[string]any{"id": recording.StorageFileID}, &result); err != nil && !strings.Contains(err.Error(), "404") && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return fmt.Errorf("delete Storage file: %w", err)
		}
		if _, err := ctx.AppDB().Exec(`UPDATE recordings SET storage_file_id = 0, storage_status = 'deleted' WHERE id = ?`, recording.ID); err != nil {
			return err
		}
		recording.StorageFileID = 0
		recording.StorageStatus = "deleted"
	}
	if err := a.deleteProviderRecording(ctx, recording); err != nil {
		return fmt.Errorf("delete provider recording: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := ctx.AppDB().Exec(`UPDATE recordings SET deleted_at = ?, storage_status = 'deleted', last_error = '' WHERE id = ?`, now, recording.ID)
	if err == nil {
		ctx.Emit("recording.deleted", map[string]any{"recording_id": recording.ID, "call_id": recording.CallID})
	}
	return err
}

func recordingPublic(recording recordingRow) map[string]any {
	out := map[string]any{
		"id": recording.ID, "call_id": recording.CallID, "provider": recording.Provider,
		"provider_recording_id": recording.ProviderRecordingID, "provider_status": recording.ProviderStatus,
		"channels": recording.Channels, "track": recording.Track, "format": recordingPlaybackFormat(&recording), "source_format": recording.Format, "storage_format": recording.StorageFormat,
		"duration_ms": recording.DurationMS, "size_bytes": recording.SizeBytes, "source_size_bytes": recording.SourceSizeBytes,
		"storage_file_id": recording.StorageFileID, "storage_status": recording.StorageStatus,
		"import_attempts": recording.ImportAttempts, "last_error": recording.LastError, "retry_blocked": recording.ImportBlockedReason != "", "import_blocked_reason": recording.ImportBlockedReason, "storage_state_label": recordingStorageLabel(recording), "recorded_locally": recording.Provider == localRecordingProvider && recording.ProviderStatus == "completed" && recording.ProviderDeletedAt == "",
		"provider_deleted": recording.ProviderDeletedAt != "", "retention_expires_at": recording.RetentionExpiresAt,
		"created_at": recording.CreatedAt, "completed_at": recording.CompletedAt, "stored_at": recording.StoredAt,
	}
	if recording.StorageFileID > 0 && recording.StorageStatus == "stored" {
		out["playback_url"] = providerPlaybackURL(recording.ProjectID, recording.ID)
		out["playback_source"] = "storage"
	} else if recording.ProviderStatus == "completed" && recording.ProviderDeletedAt == "" {
		out["playback_url"] = providerPlaybackURL(recording.ProjectID, recording.ID)
		out["playback_source"] = "provider"
		if recording.Provider == localRecordingProvider {
			out["playback_source"] = "local"
		}
	}
	if out["playback_url"] != nil && recordingPlaybackFormat(&recording) == "mp3" && !recordingSupportsVariants(&recording) {
		original := recordingVariantPlaybackURL(recording.ProjectID, recording.ID, recordingVariantOriginal)
		out["playback_url"] = original
		out["playback_urls"] = map[string]string{recordingVariantOriginal: original}
		return out
	}
	if out["playback_url"] != nil {
		urls := map[string]string{
			recordingVariantMix:      providerPlaybackURL(recording.ProjectID, recording.ID),
			recordingVariantOriginal: recordingVariantPlaybackURL(recording.ProjectID, recording.ID, recordingVariantOriginal),
		}
		if recording.Channels >= 2 {
			urls[recordingVariantCaller] = recordingVariantPlaybackURL(recording.ProjectID, recording.ID, recordingVariantCaller)
			urls[recordingVariantAgent] = recordingVariantPlaybackURL(recording.ProjectID, recording.ID, recordingVariantAgent)
		}
		out["playback_urls"] = urls
	}
	return out
}

func (a *App) toolRecordingsList(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := a.db().listRecordings(currentProject(ctx), strings.TrimSpace(strArg(args, "call_id", "")), intArg(args, "limit", 50))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		call, callErr := a.db().findCall(row.CallID)
		if callErr != nil || call == nil || call.AgentID != callerAgentID(callerCtx) {
			continue
		}
		out = append(out, recordingPublic(row))
	}
	return map[string]any{"recordings": out}, nil
}

func (a *App) ownedRecording(callerCtx context.Context, ctx *sdk.AppCtx, id string) (*recordingRow, string) {
	recording, err := a.db().findRecording(id)
	if err != nil || recording == nil || recording.ProjectID != currentProject(ctx) || recording.DeletedAt != "" {
		return nil, "recording not found"
	}
	call, err := a.db().findCall(recording.CallID)
	if err != nil || call == nil || call.AgentID != callerAgentID(callerCtx) {
		return nil, "recording belongs to another agent"
	}
	return recording, ""
}

func (a *App) toolRecordingGet(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	recording, message := a.ownedRecording(callerCtx, ctx, strings.TrimSpace(strArg(args, "recording_id", "")))
	if message != "" {
		return mcpError(message), nil
	}
	return recordingPublic(*recording), nil
}

func (a *App) toolRecordingRetry(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	recording, message := a.ownedRecording(callerCtx, ctx, strings.TrimSpace(strArg(args, "recording_id", "")))
	if message != "" {
		return mcpError(message), nil
	}
	if err := a.db().retryRecordingImport(recording.ID); err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"ok": true, "recording_id": recording.ID}, nil
}

func (c *callsDB) retryRecordingImport(id string) error {
	result, err := c.db.Exec(`UPDATE recordings SET storage_status='pending', next_attempt_at=?, last_error='', import_blocked_reason=''
		WHERE id=? AND deleted_at='' AND provider_status='completed' AND storage_status IN ('failed','pending','provider_only')`, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return errors.New("recording is not eligible for import retry")
	}
	return nil
}

func (a *App) toolRecordingDelete(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	recording, message := a.ownedRecording(callerCtx, ctx, strings.TrimSpace(strArg(args, "recording_id", "")))
	if message != "" {
		return mcpError(message), nil
	}
	if err := a.deleteRecording(ctx, recording); err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{"deleted": true, "recording_id": recording.ID}, nil
}

func (a *App) handleRecordingSettings(w http.ResponseWriter, r *http.Request) {
	projectID, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ctx := globalCtx.WithProject(projectID)
	settings, err := a.db().recordingSettings(projectID)
	if err != nil {
		http.Error(w, "load recording settings", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, a.recordingSettingsPublic(ctx, settings))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&args); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	applyRecordingSettingsArgs(&settings, args)
	if err := validateRecordingSettings(settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	carrier, supported := recordingCarrierSupport(ctx)
	if settings.DefaultMode == recordingModeAlways && !supported {
		http.Error(w, "recording is not yet supported for the bound carrier "+carrier, http.StatusConflict)
		return
	}
	if err := a.db().saveRecordingSettings(settings); err != nil {
		http.Error(w, "save recording settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, a.recordingSettingsPublic(ctx, settings))
}

func (a *App) handleRecordings(w http.ResponseWriter, r *http.Request) {
	projectID, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/recordings/"), "/")
	if path == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rows, err := a.db().listRecordings(projectID, strings.TrimSpace(r.URL.Query().Get("call_id")), 100)
		if err != nil {
			http.Error(w, "load recordings", http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, recordingPublic(row))
		}
		writeJSON(w, map[string]any{"recordings": out})
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	recording, err := a.db().findRecording(parts[0])
	if err != nil || recording == nil || recording.ProjectID != projectID || recording.DeletedAt != "" {
		http.Error(w, "recording not found", http.StatusNotFound)
		return
	}
	ctx := globalCtx.WithProject(projectID)
	if parts[1] == "content" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.serveRecordingContent(w, r, ctx, recording)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch parts[1] {
	case "retry":
		if err := a.db().retryRecordingImport(recording.ID); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	case "delete":
		err = a.deleteRecording(ctx, recording)
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) serveRecordingContent(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, recording *recordingRow) {
	variant, err := normalizedRecordingVariant(strings.TrimSpace(r.URL.Query().Get("variant")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !recordingSupportsVariants(recording) {
		if r.URL.Query().Get("variant") == "" {
			variant = recordingVariantOriginal
		}
		if variant != recordingVariantOriginal {
			http.Error(w, "playback variants are unavailable for this recording format", http.StatusConflict)
			return
		}
	}
	if variant == recordingVariantOriginal && recording.StorageFileID > 0 && recording.StorageStatus == "stored" {
		w.Header().Set("Cache-Control", "private, no-store")
		http.Redirect(w, r, storagePlaybackURL(recording.ProjectID, recording.StorageFileID), http.StatusTemporaryRedirect)
		return
	}
	settings, err := a.db().recordingSettings(recording.ProjectID)
	if err != nil {
		http.Error(w, "recording settings unavailable", http.StatusServiceUnavailable)
		return
	}
	playbackCtx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
	defer cancel()
	r = r.WithContext(context.WithValue(playbackCtx, playbackDownloadContextKey{}, settings.MaxFileBytes))
	key := fmt.Sprintf("%s:%s:%d:%s:%s:%d", recording.ProjectID, recording.ID, recording.StorageFileID, recording.CompletedAt, variant, settings.MaxFileBytes)
	file, err := cachedRecording(r.Context(), key, resourceRecordingBuild(r.Context(), ctx, func() (string, error) {
		var path string
		var err error
		if recording.StorageFileID > 0 && recording.StorageStatus == "stored" {
			path, _, err = newRecordingStorageClient().download(r.Context(), recording.ProjectID, recording.StorageFileID, recordingPlaybackFormat(recording))
		} else {
			if recording.ProviderStatus != "completed" || recording.ProviderDeletedAt != "" {
				return "", errors.New("recording unavailable")
			}
			if recording.Provider == localRecordingProvider {
				path, _, err = a.downloadLocalRecording(r.Context(), recording)
			} else {
				creds, e := ctx.PlatformAPI().GetConnectionCredentials(recording.CarrierConnectionID)
				if e != nil || creds == nil {
					return "", errors.New("provider credentials unavailable")
				}
				path, _, err = a.downloadProviderRecording(r.Context(), ctx, recording, creds.Fields)
			}
		}
		if err != nil {
			return "", err
		}
		if variant != recordingVariantOriginal && recordingPlaybackFormat(recording) == "mp3" {
			decoded, decodeErr := decodeRecordingMP3(r.Context(), path, recording.Channels)
			_ = os.Remove(path)
			if decodeErr != nil {
				return "", decodeErr
			}
			path = decoded
		}
		playbackPath, err := buildRecordingVariant(path, variant, r.Context())
		if err != nil || playbackPath != path {
			_ = os.Remove(path)
		}
		return playbackPath, err
	}))
	if err != nil {
		ctx.Logger().Warn("recording playback", "recording", recording.ID, "err", err)
		if variant == recordingVariantMix && r.Context().Err() == nil {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("X-Telephony-Playback-Variant", recordingVariantOriginal)
			w.Header().Set("X-Telephony-Playback-Fallback", "mix_unavailable")
			http.Redirect(w, r, recordingVariantPlaybackURL(recording.ProjectID, recording.ID, recordingVariantOriginal), http.StatusTemporaryRedirect)
			return
		}
		if variant == recordingVariantCaller || variant == recordingVariantAgent {
			http.Error(w, variant+"-only playback unavailable; the original recording remains available separately", http.StatusUnprocessableEntity)
		} else {
			http.Error(w, "recording unavailable", http.StatusBadGateway)
		}
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "provider recording is unavailable", http.StatusBadGateway)
		return
	}
	format := recordingPlaybackFormat(recording)
	if variant != recordingVariantOriginal {
		format = "wav"
	}
	filename := "call-" + recording.CallID + "-" + variant + "." + recordingExtension(format)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", recordingContentType(format))
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Telephony-Playback-Variant", variant)
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
