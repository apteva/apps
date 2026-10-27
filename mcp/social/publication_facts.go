package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

type publicationFacts struct {
	URL                 *string `json:"url"`
	SubmittedAt         *string `json:"submitted_at"`
	ProviderPublishedAt *string `json:"provider_reported_published_at"`
	ProviderAcceptedAt  *string `json:"provider_accepted_at"`
	DeliveryRecordedAt  *string `json:"delivery_recorded_at"`
	PublishedAt         *string `json:"native_published_at"`
	UploadedAt          *string `json:"native_uploaded_at"`
	ObservedAt          *string `json:"observed_at"`
	TimeSource          string  `json:"publication_time_source"`
	LegacyPublishedAt   *string `json:"legacy_published_at"`
	Revision            int     `json:"revision"`
}

func normalizeNativeObservation(b historyAccount, n nativePost) nativePost {
	if n.URL != nil {
		n.URL = optionalText(safePublicationURL(b.Platform, *n.URL))
	}
	if n.PublishedAt != nil {
		n.PublishedAt = nativeTimestamp(*n.PublishedAt)
	}
	if n.UploadedAt != nil {
		n.UploadedAt = nativeTimestamp(*n.UploadedAt)
	}
	if n.PublishedAt != nil {
		// Owner-private/unlisted YouTube snippet.publishedAt denotes upload time.
		// It cannot date membership availability or a later audience conversion.
		if b.Provider == zernioProviderSlug {
			n.ProviderPublishedAt = n.PublishedAt
			n.PublishedAt = nil
			n.PublicationTimeSource = "provider_reported:publishedAt"
		} else if b.Platform == "youtube" && valueOrEmpty(n.Visibility) != "public" {
			n.UploadedAt = n.PublishedAt
			n.PublishedAt = nil
			n.PublicationTimeSource = "youtube.snippet.publishedAt:upload"
		} else if at, err := time.Parse(time.RFC3339Nano, *n.PublishedAt); err != nil || at.After(time.Now().UTC()) {
			n.PublishedAt = nil
			n.PublicationTimeSource = "unknown"
		} else {
			n.PublicationTimeSource = map[string]string{"youtube": "youtube.snippet.publishedAt:public", "facebook": "facebook.created_time:published", "instagram": "instagram.timestamp", "twitter": "twitter.created_at", "pinterest": "pinterest.created_at", "tiktok": "tiktok.create_time", "linkedin": "linkedin.publishedAt"}[b.Platform]
			if n.PublicationTimeSource == "" {
				n.PublishedAt = nil
				n.PublicationTimeSource = "unknown"
			}
		}
	}
	if n.PublishedAt != nil {
		state := strings.ToLower(valueOrEmpty(n.ProcessingState))
		switch state {
		case "processing", "uploading", "uploaded", "pending", "scheduled", "queued", "failed", "rejected", "processing_upload", "processing_download":
			n.PublishedAt = nil
			n.PublicationTimeSource = "unknown_processing"
		}
	}
	if n.PublicationTimeSource == "" {
		n.PublicationTimeSource = "unknown"
	}
	return n
}

func loadPublicationFacts(db recoveryQueryer, project string, target int64) (publicationFacts, error) {
	var f publicationFacts
	err := db.QueryRow(`SELECT NULLIF(t.platform_url,''),t.submitted_at,t.provider_accepted_at,t.provider_reported_published_at,t.delivery_recorded_at,t.published_at,t.native_uploaded_at,t.publication_observed_at,t.publication_time_source,t.legacy_published_at,t.publication_revision FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.id=? AND p.project_id=?`, target, project).Scan(&f.URL, &f.SubmittedAt, &f.ProviderAcceptedAt, &f.ProviderPublishedAt, &f.DeliveryRecordedAt, &f.PublishedAt, &f.UploadedAt, &f.ObservedAt, &f.TimeSource, &f.LegacyPublishedAt, &f.Revision)
	return f, err
}

func publicationFactsJSON(f publicationFacts) string { raw, _ := json.Marshal(f); return string(raw) }

// Run inside the caller's transaction. All target identities/lifecycle and
// audience fields are untouched; corrections have their own revision/history.
func correctPublicationFacts(tx *sql.Tx, project string, r recoveryTarget, f publicationFacts, evidence any) (string, error) {
	before, err := loadPublicationFacts(tx, project, r.TargetID)
	if err != nil {
		return "", err
	}
	if before.Revision != f.Revision {
		return "", errors.New("publication revision conflict")
	}
	beforeRaw := publicationFactsJSON(before)
	next := f
	next.Revision++
	nextRaw := publicationFactsJSON(next)
	proof, _ := json.Marshal(evidence)
	id := randomDeliveryID()
	res, err := tx.Exec(`UPDATE post_targets SET platform_url=?,published_at=?,native_uploaded_at=?,publication_observed_at=?,publication_time_source=?,publication_revision=publication_revision+1 WHERE id=? AND social_account_id=? AND COALESCE(platform_post_id,'')=? AND attempts=? AND publication_revision=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=? AND p.revision=?)`, f.URL, f.PublishedAt, f.UploadedAt, f.ObservedAt, f.TimeSource, r.TargetID, r.AccountID, r.PublicID, r.Attempts, f.Revision, project, r.Revision)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", errors.New("target changed; no publication correction applied")
	}
	_, err = tx.Exec(`INSERT INTO social_publication_corrections(id,project_id,social_account_id,target_id,post_id,native_post_id,account_binding,content_binding,post_revision,source_attempt,before_facts,after_facts,evidence,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, project, r.AccountID, r.TargetID, r.PostID, r.PublicID, uploadRecoveryAccountBinding(project, r), uploadRecoveryContentBinding(r), r.Revision, r.Attempts, beforeRaw, nextRaw, string(proof), time.Now().UTC().Format(time.RFC3339Nano))
	return id, err
}

func membershipPublicationTimeAllowed(options string, n nativePost) bool {
	var opts map[string]any
	if json.Unmarshal([]byte(options), &opts) != nil {
		return false
	}
	audience, err := parseAudience(opts)
	return err == nil && audience.Type != "members" && valueOrEmpty(n.Audience) != "members" && !strings.HasPrefix(n.PublicationTimeSource, "provider_reported")
}

// A sender request timestamp is recorded only at the actual dispatch boundary.
// Acceptance remains unknown unless an upstream response explicitly dates it.
func recordPublicationSubmission(ctx *sdk.AppCtx, target int64, attempt int) {
	if target <= 0 {
		return
	}
	_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET submitted_at=COALESCE(submitted_at,?) WHERE id=? AND attempts=? AND status='publishing' AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, time.Now().UTC().Format(time.RFC3339Nano), target, attempt, projectScope(ctx))
}

func executePublicationWrite(ctx *sdk.AppCtx, j publishJob, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	attempt := j.deliveryAttempt
	if j.targetID > 0 && attempt == 0 {
		_ = ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, j.targetID).Scan(&attempt)
	}
	recordPublicationSubmission(ctx, j.targetID, attempt)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, tool, input)
	if err == nil && res != nil && res.Success && res.Status >= 200 && res.Status < 300 && j.targetID > 0 {
		var raw map[string]any
		if json.Unmarshal(res.Data, &raw) == nil {
			accepted := nativeTimestamp(firstString(raw, "accepted_at", "acceptedAt"))
			if accepted == nil {
				accepted = nativeTimestamp(firstString(historyMap(raw, "data"), "accepted_at", "acceptedAt"))
			}
			if accepted != nil {
				if at, e := time.Parse(time.RFC3339Nano, *accepted); e == nil && !at.After(time.Now().UTC()) {
					_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET provider_accepted_at=COALESCE(provider_accepted_at,?) WHERE id=? AND attempts=? AND status='publishing'`, *accepted, j.targetID, attempt)
				}
			}
		}
	}
	return res, err
}

func normalizeNativeMetadataMap(platform, provider string, raw any) any {
	m, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	encoded, _ := json.Marshal(m)
	var n nativePost
	if json.Unmarshal(encoded, &n) != nil {
		return nil
	}
	n = normalizeNativeObservation(historyAccount{destinationAccount: destinationAccount{Platform: platform, Provider: provider}}, n)
	encoded, _ = json.Marshal(n)
	normalized := map[string]any{}
	_ = json.Unmarshal(encoded, &normalized)
	for _, key := range []string{"url", "published_at", "uploaded_at", "provider_reported_published_at", "publication_time_source"} {
		m[key] = normalized[key]
	}
	return m
}

func deliveryRecordingHistory(ctx *sdk.AppCtx, target, after int64, limit int) (map[string]any, error) {
	if after < 0 {
		return nil, errors.New("history_after_id must not be negative")
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := ctx.AppDB().Query(`SELECT h.id,h.social_account_id,h.source_attempt,h.operation_id,h.status,h.facts,h.recorded_at,COALESCE(a.platform,'') FROM social_delivery_recordings h LEFT JOIN social_accounts a ON a.id=h.social_account_id AND a.project_id=h.project_id WHERE h.project_id=? AND h.target_id=? AND h.id>? ORDER BY h.id LIMIT ?`, projectScope(ctx), target, after, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []map[string]any{}
	var next any
	for rows.Next() {
		var id, account int64
		var attempt int
		var operation, status, raw, at, platform string
		if err := rows.Scan(&id, &account, &attempt, &operation, &status, &raw, &at, &platform); err != nil {
			return nil, err
		}
		if len(records) == limit {
			next = records[len(records)-1]["id"]
			break
		}
		var facts map[string]any
		_ = json.Unmarshal([]byte(raw), &facts)
		if link, ok := facts["url"].(string); ok {
			facts["url"] = optionalText(safePublicationURL(platform, link))
		}
		records = append(records, map[string]any{"id": id, "social_account_id": account, "source_attempt": attempt, "operation_id": safeRequestID(operation), "status": status, "facts": facts, "recorded_at": at})
	}
	return map[string]any{"records": records, "next_after_id": next, "limit": limit}, rows.Err()
}
