package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func expiredUploadFixture(t *testing.T) (*App, *sdk.AppCtx, *membershipPlatform, int64, int64, int64) {
	t.Helper()
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
	at := time.Now().UTC().Add(-2 * time.Hour)
	d := loadDeliveryDiagnostics(ctx, target)
	d.CorrelationID = "original-upload-receipt"
	d.OperationID = "existing-operation"
	d.ProgressVersion = 1
	d.Method = "FILE_UPLOAD"
	d.SourceFileID = 7
	d.SourceBytes = 24
	d.UploadURLSHA256 = uploadURLHash("https://upload.tiktok.test/upload/?upload_token=SECRET")
	d.AttemptedBytes = bytePointer(24)
	d.AcknowledgedBytes = bytePointer(0)
	d.Initialization = &deliveryOutcome{StartedAt: at.Add(-time.Second).Format(time.RFC3339Nano), At: at.Format(time.RFC3339Nano), Result: "operation_persisted", HTTPStatus: 200, RequestID: "immutable-init-receipt"}
	d.LastUpload = &deliveryOutcome{StartedAt: at.Add(time.Second).Format(time.RFC3339Nano), At: at.Add(2 * time.Second).Format(time.RFC3339Nano), Result: "http_rejected", HTTPStatus: 404, ExpectedStatus: 201, Chunk: 1, ContentRange: "bytes 0-23/24", URLSHA256: d.UploadURLSHA256}
	d.FirstUploadError = d.LastUpload
	d.LatestUploadError = d.LastUpload
	if err := d.save(ctx, target); err != nil {
		t.Fatal(err)
	}
	stopProof, _ := json.Marshal(uploadStopEvidence{AcknowledgedBytes: d.AcknowledgedBytes, LastUpload: d.LastUpload})
	_, err := ctx.AppDB().Exec(`INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,uploader_stopped_at,provenance,stop_evidence) VALUES('test-proj',?,'existing-operation',?,1,?,'stopped',?,'social_upload_receipt',?)`, target, d.CorrelationID, d.Initialization.At, d.LastUpload.At, string(stopProof))
	if err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0},"error":{"code":"ok"}}`)
	return a, ctx, pf, post, target, sibling
}
func reviewExpiry(t *testing.T, a *App, ctx *sdk.AppCtx, post, target int64) map[string]any {
	t.Helper()
	out, err := a.toolPostDeliveryRevalidate(ctx, map[string]any{"post_id": post, "target_id": target, "review_expired_upload": true})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func TestExpiredTikTokUploadRecoveryAuditedTargetOnlyOnce(t *testing.T) {
	a, ctx, pf, post, target, sibling := expiredUploadFixture(t)
	// Give the successful siblings their real, separate account/platform identities.
	ctx.AppDB().Exec(`UPDATE social_accounts SET platform='facebook' WHERE id=(SELECT social_account_id FROM post_targets WHERE id=?)`, sibling)
	result, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','tiktok',42,'TikTok','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := result.LastInsertId()
	ctx.AppDB().Exec(`UPDATE post_targets SET social_account_id=? WHERE id=?`, account, target)
	result, err = ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','youtube',43,'YouTube','active')`)
	if err != nil {
		t.Fatal(err)
	}
	youtube, _ := result.LastInsertId()
	ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts) VALUES(?,?,'published','keep-youtube',1)`, post, youtube)
	if grant := mustRevalidate(t, a, ctx, post, target); grant["decision"] != "pending" {
		t.Fatal("ordinary retry adopted expiry", grant)
	}
	grant := reviewExpiry(t, a, ctx, post, target)
	if grant["decision"] != "retry" || grant["recovery_kind"] != "expired_zero_byte_upload" {
		t.Fatal(grant)
	}
	proof := grant["expired_upload_review"].(map[string]any)
	if len(proof["observations"].([]tikTokProgressObservation)) != 2 || grant["source_bytes"] != int64(24) {
		t.Fatal(grant)
	}
	for _, c := range pf.executeCalls {
		if c.Tool != "get_publish_status" && c.Tool != "query_creator_info" {
			t.Fatal("review published", c)
		}
	}
	puts := 0
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		puts++
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(testMP4) {
			t.Error("source changed")
		}
		w.WriteHeader(201)
	}))
	defer upload.Close()
	init, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "new-operation", "upload_url": upload.URL + "?upload_token=SECRET"}, "error": map[string]any{"code": "ok"}})
	pf.executeResponses["post_video"] = analyticsOK(string(init))
	zero := pf.executeResponses["get_publish_status"]
	pf.executeQueues["get_publish_status"] = []*sdk.ExecuteResult{zero, zero, zero, zero}
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["new-tiktok-video"]},"error":{"code":"ok"}}`)
	out, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	inits := 0
	for _, c := range pf.executeCalls {
		if c.Tool == "post_video" {
			inits++
		}
	}
	if inits != 1 || puts != 1 {
		t.Fatal("recovery was not one shot", inits, puts, out)
	}
	var status, id string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, target).Scan(&status, &id, &attempts)
	if status != "published" || id != "new-tiktok-video" || attempts != 2 {
		t.Fatal(status, id, attempts, out)
	}
	var siblings int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=? AND id<>? AND status='published' AND attempts=1 AND platform_post_id IN ('keep-this-post','keep-youtube')`, post, target).Scan(&siblings)
	if siblings != 2 {
		t.Fatal("successful siblings changed")
	}
	var oldState, oldProof, oldStatus string
	ctx.AppDB().QueryRow(`SELECT uploader_state,evidence FROM delivery_upload_operations WHERE target_id=? AND operation_id='existing-operation'`, target).Scan(&oldState, &oldProof)
	ctx.AppDB().QueryRow(`SELECT status FROM target_delivery_attempts WHERE target_id=? AND operation_id='existing-operation'`, target).Scan(&oldStatus)
	if oldState != "locally_abandoned" || oldStatus != "locally_abandoned" || !strings.Contains(oldProof, "PROCESSING_UPLOAD") || strings.Contains(oldProof, "SECRET") {
		t.Fatal(oldState, oldStatus, oldProof)
	}
	var original string
	ctx.AppDB().QueryRow(`SELECT diagnostics FROM video_delivery_attempts WHERE target_id=? AND correlation_id='original-upload-receipt'`, target).Scan(&original)
	if !strings.Contains(original, `"http_status":404`) || strings.Contains(original, `"provider_status":"FAILED"`) {
		t.Fatal(original)
	}
	inspect, err := a.toolPostDeliveryInspect(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspect.(map[string]any)["upload_operations"].([]map[string]any)) != 2 {
		t.Fatal(inspect)
	}
}
func TestExpiredTikTokUploadUncertainEvidenceCannotAuthorizeRecovery(t *testing.T) {
	for _, scenario := range []string{"young", "future", "active", "stopped_unknown", "attempt_mismatch", "operation_mismatch", "missing_bytes", "positive_bytes", "positive_download", "prior_progress", "acknowledged_bytes", "unknown_acknowledgement", "unknown_legacy", "failed_query", "second_check_failed", "second_check_progress", "second_check_identity", "public_identity", "approval", "privacy", "no_receipt", "stale_observation", "missing_stop_evidence", "accepted_stop_evidence"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, pf, post, target, _ := expiredUploadFixture(t)
			switch scenario {
			case "missing_stop_evidence":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET stop_evidence='{}'`)
			case "accepted_stop_evidence":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET stop_evidence=json_set(stop_evidence,'$.acknowledged_upload_bytes',24)`)
			case "young":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET initialized_at=?,uploader_stopped_at=?`, time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339Nano), time.Now().Add(-29*time.Minute).UTC().Format(time.RFC3339Nano))
			case "future":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET uploader_stopped_at=?`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
			case "active":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET uploader_state='active'`)
			case "stopped_unknown":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET uploader_stopped_at=NULL`)
			case "attempt_mismatch":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET source_attempt=9`)
			case "operation_mismatch":
				ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='another-operation' WHERE id=?`, target)
			case "missing_bytes":
				pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD"}}`)
			case "positive_bytes":
				pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":1}}`)
			case "positive_download":
				pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0,"downloaded_bytes":1}}`)
			case "prior_progress":
				d := loadDeliveryDiagnostics(ctx, target)
				d.ProviderUploadedHighWater = bytePointer(1)
				d.save(ctx, target)
			case "acknowledged_bytes":
				d := loadDeliveryDiagnostics(ctx, target)
				d.AcknowledgedBytes = bytePointer(1)
				d.save(ctx, target)
			case "unknown_acknowledgement":
				ctx.AppDB().Exec(`UPDATE video_delivery_attempts SET diagnostics=json_remove(diagnostics,'$.acknowledged_upload_bytes')`)
				ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics=json_remove(delivery_diagnostics,'$.acknowledged_upload_bytes') WHERE id=?`, target)
			case "unknown_legacy":
				ctx.AppDB().Exec(`DELETE FROM delivery_upload_operations`)
				ctx.AppDB().Exec(`DELETE FROM video_delivery_attempts`)
				ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics='{"method":"FILE_UPLOAD","transferred_bytes":24}',last_error='TikTok chunk upload returned HTTP 404' WHERE id=?`, target)
			case "failed_query":
				pf.executeErr = errors.New("unavailable")
			case "second_check_failed":
				pf.executeQueues["get_publish_status"] = []*sdk.ExecuteResult{pf.executeResponses["get_publish_status"], {Status: 503, Data: json.RawMessage(`{}`)}}
			case "second_check_progress":
				pf.executeQueues["get_publish_status"] = []*sdk.ExecuteResult{pf.executeResponses["get_publish_status"], analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":1}}`)}
			case "second_check_identity":
				pf.executeQueues["get_publish_status"] = []*sdk.ExecuteResult{pf.executeResponses["get_publish_status"], analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0,"publicaly_available_post_id":["already-published"]}}`)}
			case "public_identity":
				ctx.AppDB().Exec(`UPDATE post_targets SET platform_post_id='already-published' WHERE id=?`, target)
			case "approval":
				ctx.AppDB().Exec(`UPDATE posts SET approval_required=1,approved_revision=0 WHERE id=?`, post)
			case "privacy":
				ctx.AppDB().Exec(`UPDATE post_targets SET options='{"privacy_level":"UNSUPPORTED"}' WHERE id=?`, target)
			case "no_receipt":
				ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics=json_remove(delivery_diagnostics,'$.initialization') WHERE id=?`, target)
				ctx.AppDB().Exec(`DELETE FROM delivery_upload_operations`)
				ctx.AppDB().Exec(`DELETE FROM video_delivery_attempts`)
			case "stale_observation":
				d := loadDeliveryDiagnostics(ctx, target)
				d.ProviderObservation = &tikTokProgressObservation{At: time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339Nano), Status: "PROCESSING_UPLOAD", UploadedBytes: bytePointer(0), HTTPStatus: 200, QuerySucceeded: true}
				d.save(ctx, target)
				r, _ := loadRecoveryTarget(ctx, post, target)
				if evidence, eligible := a.expiredTikTokRecovery(ctx, r); eligible {
					t.Fatal(evidence)
				}
				return
			}
			grant := reviewExpiry(t, a, ctx, post, target)
			if grant["decision"] == "retry" || grant["decision"] == "resume" {
				t.Fatal("unsafe grant", scenario, grant)
			}
			for _, c := range pf.executeCalls {
				if c.Tool != "get_publish_status" && c.Tool != "query_creator_info" {
					t.Fatal("unsafe submission", c)
				}
			}
			if scenario == "unknown_legacy" {
				if d := loadDeliveryDiagnostics(ctx, target); d.AcknowledgedBytes != nil {
					t.Fatal("unknown became zero")
				}
			}
		})
	}
}
func TestExpiredTikTokUploadReviewsOnlyCompleteStoredReceipts(t *testing.T) {
	a, ctx, _, post, target, _ := expiredUploadFixture(t)
	ctx.AppDB().Exec(`DELETE FROM delivery_upload_operations`)
	grant := reviewExpiry(t, a, ctx, post, target)
	if grant["decision"] != "retry" {
		t.Fatal(grant)
	}
	r, _ := loadRecoveryTarget(ctx, post, target)
	receipt, err := loadUploadReceipt(ctx, r)
	if err != nil || receipt.Provenance != "reviewed_stored_receipt" {
		t.Fatal(receipt, err)
	}
	var retired int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM target_delivery_attempts WHERE target_id=?`, target).Scan(&retired)
	if retired != 0 {
		t.Fatal("review retired without confirmation")
	}
}
func TestExpiredTikTokUploadRechecksBeforeConsumption(t *testing.T) {
	for _, scenario := range []string{"progress", "active", "source_changed", "changed_attempt", "cross_project"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, pf, post, target, _ := expiredUploadFixture(t)
			grant := reviewExpiry(t, a, ctx, post, target)
			if grant["decision"] != "retry" {
				t.Fatal(grant)
			}
			switch scenario {
			case "progress":
				pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":1}}`)
			case "active":
				ctx.AppDB().Exec(`UPDATE delivery_upload_operations SET uploader_state='active'`)
			case "source_changed":
				ctx.AppDB().Exec(`UPDATE posts SET media_storage_ids='[8]' WHERE id=?`, post)
			case "changed_attempt":
				ctx.AppDB().Exec(`UPDATE post_targets SET attempts=attempts+1 WHERE id=?`, target)
			case "cross_project":
				ctx = ctx.WithProject("other")
			}
			if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
				t.Fatal(err)
			}
			for _, c := range pf.executeCalls {
				if c.Tool == "post_video" {
					t.Fatal("stale review reposted")
				}
			}
			var state string
			ctx.AppDB().QueryRow(`SELECT uploader_state FROM delivery_upload_operations WHERE target_id=?`, target).Scan(&state)
			if state == "locally_abandoned" {
				t.Fatal("invalid review retired operation")
			}
		})
	}
}
func TestTikTokOldPollAndUploadResultsCannotChangeReplacement(t *testing.T) {
	for _, scenario := range []string{"different_operation", "same_operation_new_attempt"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, _, _, target, _ := expiredUploadFixture(t)
			// Capture exactly the binding the status client holds while the network call runs.
			bindings := tikTokStatusBindings(ctx, 42, "existing-operation")
			old := loadDeliveryDiagnostics(ctx, target)
			operation := "replacement-operation"
			if scenario == "same_operation_new_attempt" {
				operation = "existing-operation"
			}
			ctx.AppDB().Exec(`UPDATE post_targets SET attempts=2,publish_operation_id=?,delivery_diagnostics='{"correlation_id":"replacement","provider_status":"NEW_OPERATION"}' WHERE id=?`, operation, target)
			observation := tikTokProgressObservation{At: time.Now().UTC().Format(time.RFC3339Nano), Status: "PROCESSING_UPLOAD", UploadedBytes: bytePointer(0), QuerySucceeded: true, HTTPStatus: 200}
			a.recordTikTokStatus(ctx, bindings, "existing-operation", "PROCESSING_UPLOAD", "", "ok", "old-poll", analyticsOK(`{}`), nil, observation)
			old.LastUpload = &deliveryOutcome{Result: "http_rejected", HTTPStatus: 404, At: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := old.save(ctx, target); err == nil {
				t.Fatal("late upload saved")
			}
			a.markTargetError(ctx, target, errors.New("old request failed"), 1)
			if err := persistTikTokUploadOperation(ctx, target, &old, "late-initialization"); err == nil {
				t.Fatal("late init overwrote replacement")
			}
			d := loadDeliveryDiagnostics(ctx, target)
			if d.CorrelationID != "replacement" || d.ProviderStatus != "NEW_OPERATION" {
				t.Fatal("old result contaminated replacement", d)
			}
		})
	}
}

// Simulate a response from an old operation arriving after another process has
// installed a new attempt. Exercise the callers, not only diagnostic storage.
type lateTikTokPlatform struct {
	*membershipPlatform
	onStatus func()
}

func (p *lateTikTokPlatform) ExecuteIntegrationTool(conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if tool == "get_publish_status" && p.onStatus != nil {
		f := p.onStatus
		p.onStatus = nil
		f()
	}
	return p.membershipPlatform.ExecuteIntegrationTool(conn, tool, args)
}
func TestTikTokLateCompletionCannotResolveOrRetryReplacement(t *testing.T) {
	for _, path := range []string{"background", "identity", "selected_retry"} {
		t.Run(path, func(t *testing.T) {
			base := &membershipPlatform{recordingPlatform: newRecordingPlatform()}
			base.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["old-public-id"]},"error":{"code":"ok"}}`)
			pf := &lateTikTokPlatform{membershipPlatform: base}
			ctx := newSocialCtx(t, pf)
			a := &App{}
			ctx.AppDB().Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status) VALUES(1,'test-proj','tiktok',42,'TT','active')`)
			ctx.AppDB().Exec(`INSERT INTO posts(id,project_id,body,status) VALUES(1,'test-proj','copy','publishing')`)
			ctx.AppDB().Exec(`INSERT INTO post_targets(id,post_id,social_account_id,status,attempts,publish_operation_id) VALUES(1,1,1,'publishing',1,'old-operation')`)
			pf.onStatus = func() {
				_, err := ctx.AppDB().Exec(`UPDATE post_targets SET attempts=2,publish_operation_id='replacement',status='publishing',delivery_diagnostics='{"correlation_id":"new-attempt","provider_status":"NEW_OPERATION"}' WHERE id=1`)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch path {
			case "background":
				a.recoverTikTokDeliveries(ctx)
			case "identity":
				if _, err := a.resolveTikTokPublicPostID(ctx, metricsTarget{TargetID: 1, ConnID: 42, PublishOperationID: "old-operation"}); err == nil {
					t.Fatal("stale identity accepted")
				}
			case "selected_retry":
				a.reconcileSelectedRetry(ctx, 1, 1)
			}
			var operation, status string
			var publicID any
			if err := ctx.AppDB().QueryRow(`SELECT publish_operation_id,status,platform_post_id FROM post_targets WHERE id=1`).Scan(&operation, &status, &publicID); err != nil {
				t.Fatal(err)
			}
			if operation != "replacement" || status != "publishing" || publicID != nil {
				t.Fatal("old completion altered replacement", operation, status, publicID)
			}
			if d := loadDeliveryDiagnostics(ctx, 1); d.ProviderStatus != "NEW_OPERATION" {
				t.Fatal(d)
			}
		})
	}
}

func TestDeliveryRecoveryPinsSourceAfterAuthorizationConsumed(t *testing.T) {
	a, ctx, pf, post, target, _ := expiredUploadFixture(t)
	grant := reviewExpiry(t, a, ctx, post, target)
	if grant["decision"] != "retry" {
		t.Fatal(grant)
	}
	ctx.AppDB().Exec(`UPDATE delivery_recovery_requests SET status='consumed' WHERE id=?`, grant["recovery_id"])
	ctx.AppDB().Exec(`UPDATE post_targets SET attempts=2,status='publishing',publish_operation_id='',delivery_revision=1 WHERE id=?`, target)
	changed := append([]byte(nil), testMP4...)
	changed[len(changed)-1] ^= 1
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(changed)
	}))
	defer source.Close()
	setSourceURL(pf.recordingPlatform, source.URL)
	_, _, err := a.publishTikTokVideo(ctx, platforms["tiktok"], publishJob{targetID: target, connID: 42, platform: "tiktok", media: []mediaItem{testSourceItem()}, mediaProjectID: "test-proj"})
	if err == nil || !strings.Contains(err.Error(), "source changed after recovery authorization") {
		t.Fatal(err)
	}
	for _, call := range pf.executeCalls {
		if call.Tool == "post_video" {
			t.Fatal("source changed after authorization was uploaded")
		}
	}
}

func TestTikTokLateUploadAcknowledgementCannotOverwriteReplacement(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='pending',publish_operation_id='',delivery_diagnostics='{}' WHERE id=?`, target)
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		_, err := ctx.AppDB().Exec(`UPDATE post_targets SET attempts=99,publish_operation_id='replacement-operation',delivery_diagnostics='{"correlation_id":"replacement","provider_status":"NEW_OPERATION"}' WHERE id=?`, target)
		if err != nil {
			t.Error(err)
		}
		w.WriteHeader(201)
	}))
	defer upload.Close()
	init, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "old-upload", "upload_url": upload.URL}})
	pf.executeResponses["post_video"] = analyticsOK(string(init))
	a.publishSelectedPostTargets(ctx, post, target)
	var operation, status string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT publish_operation_id,status,attempts FROM post_targets WHERE id=?`, target).Scan(&operation, &status, &attempts)
	if operation != "replacement-operation" || status != "publishing" || attempts != 99 {
		t.Fatal(operation, status, attempts)
	}
	if d := loadDeliveryDiagnostics(ctx, target); d.CorrelationID != "replacement" || d.ProviderStatus != "NEW_OPERATION" {
		t.Fatal(d)
	}
	// Even though saving the old acknowledgement failed its CAS, the exiting
	// uploader's independent receipt retains that it actually received HTTP 201.
	receipt, err := loadUploadReceipt(ctx, recoveryTarget{TargetID: target, Operation: "old-upload"})
	if err != nil || receipt.UploaderState != "stopped" || receipt.StopEvidence.AcknowledgedBytes == nil || *receipt.StopEvidence.AcknowledgedBytes != 24 {
		t.Fatal("lost accepted upload evidence", receipt, err)
	}
}
