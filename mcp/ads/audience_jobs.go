package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) claimAudienceJob(ctx *sdk.AppCtx) (*audienceJob, error) {
	pid := projectScope(ctx)
	if pid == "" {
		return nil, sql.ErrNoRows
	}
	_, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='queued', lease_token='', available_at=datetime('now')
		WHERE project_id=? AND status='processing' AND lease_expires_at<=datetime('now')`, pid)
	if err != nil {
		return nil, err
	}
	var id int64
	err = ctx.AppDB().QueryRow(`SELECT id FROM ad_audience_jobs WHERE project_id=? AND status='queued' AND available_at<=datetime('now') ORDER BY id LIMIT 1`, pid).Scan(&id)
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	result, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='processing', attempts=attempts+1,
		lease_token=?, lease_expires_at=datetime('now','+15 minutes'),
		started_at=COALESCE(started_at,CURRENT_TIMESTAMP), updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND project_id=? AND status='queued'`, token, id, pid)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, sql.ErrNoRows
	}
	return a.getAudienceJob(ctx, pid, id)
}

func renewAudienceLease(ctx *sdk.AppCtx, job *audienceJob) error {
	result, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET lease_expires_at=datetime('now','+15 minutes')
		WHERE id=? AND project_id=? AND status='processing' AND lease_token=?`, job.ID, job.ProjectID, job.LeaseToken)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("audience job lease lost")
	}
	return nil
}

func (a *App) processAudienceJob(ctx *sdk.AppCtx, job *audienceJob) {
	if ctx.CurrentProject() != job.ProjectID {
		return
	}
	// Long CRM reads can exceed a lease interval. Renew independently, and
	// fence every checkpoint so an old worker cannot overwrite a new owner.
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if renewAudienceLease(ctx, job) != nil {
					return
				}
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	acct, _, errOut := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": job.AdAccountID})
	if errOut != nil {
		a.failAudienceJob(ctx, job, mcpErrorMessage(errOut), false)
		return
	}
	members, _, rejected, err := a.loadAudienceMembers(ctx, job)
	if err != nil {
		a.failAudienceJob(ctx, job, err.Error(), false)
		return
	}
	members, providerRejected := filterAudienceMembers(acct.Platform, members)
	rejected += providerRejected
	if len(members) == 0 {
		a.failAudienceJob(ctx, job, "source contains no usable audience identifiers", false)
		return
	}
	// Hash normalized contents as well as order: a resumed CRM segment must
	// not silently switch identifiers when a contact changes between attempts.
	encoded, _ := json.Marshal(members)
	digest := sha256.Sum256(encoded)
	checksum := hex.EncodeToString(digest[:])
	if job.SourceChecksum != "" && job.SourceChecksum != checksum {
		a.failAudienceJob(ctx, job, "audience source changed; queue a new job with a new idempotency key", false)
		return
	}
	if err := renewAudienceLease(ctx, job); err != nil {
		return
	}
	if _, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET total_rows=?, rejected_rows=?, source_checksum=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND lease_token=?`, len(members)+rejected, rejected, checksum, job.ID, job.LeaseToken); err != nil {
		a.failAudienceJob(ctx, job, "could not checkpoint audience source", true)
		return
	}
	job.TotalRows = len(members) + rejected
	for start := job.ProcessedRows; start < len(members); start += audienceBatchSize(acct.Platform) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := renewAudienceLease(ctx, job); err != nil {
			return
		}
		end := min(start+audienceBatchSize(acct.Platform), len(members))
		parsed, providerErr := a.sendAudienceBatch(ctx, acct, job, members[start:end])
		if providerErr != nil {
			a.failAudienceJob(ctx, job, mcpErrorMessage(providerErr), audienceProviderRetryable(providerErr))
			return
		}
		requestID := firstString(asMap(parsed), "requestId", "request_id")
		if acct.Platform == "google" && requestID == "" {
			a.failAudienceJob(ctx, job, "Google accepted a batch without a diagnostic request ID", false)
			return
		}
		if err := checkpointAudienceBatch(ctx, job, start, end, requestID); err != nil {
			a.failAudienceJob(ctx, job, "could not checkpoint audience batch", true)
			return
		}
		job.ProcessedRows, job.AcceptedRows, job.ProviderRequestID = end, end, requestID
		a.emitAudienceJobEvent(ctx, acct, job, "audience.sync.progress", "processing", end, job.TotalRows, "")
	}
	status, event := "completed", "audience.ready"
	if acct.Platform == "google" {
		status, event = "provider_processing", "audience.sync.progress"
	}
	result, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status=?, last_error='', lease_expires_at='',
		available_at=CASE WHEN ?='provider_processing' THEN datetime('now','+30 minutes') ELSE available_at END,
		completed_at=CASE WHEN ?='completed' THEN CURRENT_TIMESTAMP ELSE NULL END, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND status='processing' AND lease_token=?`, status, status, status, job.ID, job.LeaseToken)
	if err != nil {
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return
	}
	a.emitAudienceJobEvent(ctx, acct, job, event, status, job.AcceptedRows, job.TotalRows, "")
}

func checkpointAudienceBatch(ctx *sdk.AppCtx, job *audienceJob, start, end int, requestID string) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE ad_audience_jobs SET processed_rows=?, accepted_rows=?, provider_request_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND project_id=? AND status='processing' AND lease_token=?`, end, end, requestID, job.ID, job.ProjectID, job.LeaseToken)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("audience job lease lost")
	}
	if _, err := tx.Exec(`INSERT INTO ad_audience_batches(job_id,batch_start,batch_end,provider_request_id)
		VALUES(?,?,?,?) ON CONFLICT(job_id,batch_start) DO UPDATE SET batch_end=excluded.batch_end,provider_request_id=excluded.provider_request_id,status='pending'`, job.ID, start, end, requestID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) pollGoogleAudienceJob(ctx *sdk.AppCtx) error {
	pid := projectScope(ctx)
	if pid == "" {
		return sql.ErrNoRows
	}
	var id int64
	if err := ctx.AppDB().QueryRow(`SELECT id FROM ad_audience_jobs WHERE project_id=? AND status='provider_processing' AND available_at<=datetime('now') ORDER BY id LIMIT 1`, pid).Scan(&id); err != nil {
		return err
	}
	job, err := a.getAudienceJob(ctx, pid, id)
	if err != nil {
		return err
	}
	acct, _, errOut := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": job.AdAccountID})
	if errOut != nil {
		a.failAudienceJob(ctx, job, mcpErrorMessage(errOut), false)
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT batch_start,provider_request_id FROM ad_audience_batches WHERE job_id=? AND status!='completed' ORDER BY batch_start`, job.ID)
	if err != nil {
		return err
	}
	type batch struct {
		start     int
		requestID string
	}
	batches := []batch{}
	for rows.Next() {
		var b batch
		if err := rows.Scan(&b.start, &b.requestID); err != nil {
			rows.Close()
			return err
		}
		batches = append(batches, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var batchCount int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_audience_batches WHERE job_id=?`, job.ID).Scan(&batchCount); err != nil {
		return err
	}
	if batchCount == 0 {
		a.failAudienceJob(ctx, job, "audience batch diagnostics are missing; submit a new sync", false)
		return nil
	}
	pending := false
	for _, b := range batches {
		parsed, providerErr := a.execIntegrationTool(ctx, acct, "data_manager_request_status_get", map[string]any{"requestId": b.requestID})
		if providerErr != nil {
			if !audienceProviderRetryable(providerErr) {
				a.failAudienceJob(ctx, job, mcpErrorMessage(providerErr), false)
				return nil
			}
			pending = true
			continue
		}
		statuses := googleRequestStatuses(parsed)
		if containsString(statuses, "FAILURE") || containsString(statuses, "FAILED") || containsString(statuses, "PARTIAL_SUCCESS") {
			a.failAudienceJob(ctx, job, "Google Data Manager rejected audience members in batch "+b.requestID, false)
			return nil
		}
		complete := len(statuses) > 0
		for _, status := range statuses {
			if status != "SUCCESS" {
				complete = false
			}
		}
		if !complete {
			pending = true
			continue
		}
		if _, err := ctx.AppDB().Exec(`UPDATE ad_audience_batches SET status='completed' WHERE job_id=? AND batch_start=?`, job.ID, b.start); err != nil {
			return err
		}
	}
	if pending {
		_, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now','+60 minutes') WHERE id=?`, job.ID)
		return err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='completed', last_error='', completed_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=?`, job.ID); err != nil {
		return err
	}
	a.emitAudienceJobEvent(ctx, acct, job, "audience.ready", "completed", job.AcceptedRows, job.TotalRows, "")
	return nil
}
