package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func recoveryFixture(t *testing.T, platform string) (*App, *sdk.AppCtx, *membershipPlatform, int64, int64, int64) {
	t.Helper()
	pf := &membershipPlatform{recordingPlatform: newRecordingPlatform(), connection: &sdk.PlatformConnection{ID: 42, AppSlug: platforms[platform].IntegrationSlug, ProjectID: "test-proj", Status: "active"}}
	configureTestVideo(t, pf.recordingPlatform, 7, 24)
	ctx := newSocialCtx(t, pf)
	execID := func(q string, args ...any) int64 {
		r, err := ctx.AppDB().Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	account := execID(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj',?,42,'Channel','active')`, platform)
	post := execID(`INSERT INTO posts(project_id,body,media_storage_ids,media_project_id,status) VALUES('test-proj','Original body','[7]','test-proj','partial')`)
	failure := `upstream 400: {"error":{"code":389,"error_subcode":1363057,"message":"Unable to fetch video file from URL."}}`
	target := execID(`INSERT INTO post_targets(post_id,social_account_id,status,attempts,failure_code,last_error,retryable,options) VALUES(?,?,'failed',1,'upstream_rejected',?,0,'{"privacy_level":"PUBLIC_TO_EVERYONE"}')`, post, account, failure)
	sibling := execID(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts,retryable) VALUES(?,?,'published','keep-this-post',1,0)`, post, account)
	if platform == "tiktok" {
		_, err := ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='existing-operation',last_error='upstream 422: TikTok publish failed: internal' WHERE id=?`, target)
		if err != nil {
			t.Fatal(err)
		}
		pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":{"status":"FAILED","fail_reason":"internal"},"error":{"code":"ok","log_id":"native-log"}}`)}
	}
	return &App{}, ctx, pf, post, target, sibling
}
func mustRevalidate(t *testing.T, a *App, ctx *sdk.AppCtx, post, target int64) map[string]any {
	t.Helper()
	out, err := a.toolPostDeliveryRevalidate(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func recoverArgs(post, target int64, grant map[string]any) map[string]any {
	return map[string]any{"post_id": post, "target_id": target, "recovery_id": grant["recovery_id"], "confirm": true}
}
func TestDeliveryRecoverySelectedAuditedIdempotent(t *testing.T) {
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "facebook")
	grant := mustRevalidate(t, a, ctx, post, target)
	if grant["decision"] != "retry" || grant["source_sha256"] == "" {
		t.Fatal(grant)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("revalidation submitted a post")
	}
	pf.executeResponses[platforms["facebook"].VideoPostTool] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"new-video"}`)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("duplicate recovery submitted %d times", len(pf.executeCalls))
	}
	var status, identity string
	var attempts int
	if err := ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, sibling).Scan(&status, &identity, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "published" || identity != "keep-this-post" || attempts != 1 {
		t.Fatal("sibling changed", status, identity, attempts)
	}
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, target).Scan(&status, &identity, &attempts)
	if status != "published" || identity != "new-video" || attempts != 2 {
		t.Fatal(status, identity, attempts)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM target_delivery_attempts WHERE target_id=?`, target).Scan(&count)
	if count != 1 {
		t.Fatal("original failure history lost", count)
	}
}
func TestDeliveryRecoveryRejectsStaleCrossProjectUnconfirmedAndDuplicateGrants(t *testing.T) {
	for _, scenario := range []string{"expired", "revision", "options", "source", "account", "connection", "external_id", "cross_project", "unconfirmed", "duplicate_grant"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, pf, post, target, _ := recoveryFixture(t, "facebook")
			grant := mustRevalidate(t, a, ctx, post, target)
			args := recoverArgs(post, target, grant)
			switch scenario {
			case "expired":
				ctx.AppDB().Exec(`UPDATE delivery_recovery_requests SET expires_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
			case "revision":
				ctx.AppDB().Exec(`UPDATE posts SET revision=revision+1 WHERE id=?`, post)
			case "options":
				ctx.AppDB().Exec(`UPDATE post_targets SET options='{"body":"changed"}' WHERE id=?`, target)
			case "source":
				ctx.AppDB().Exec(`UPDATE posts SET media_storage_ids='[8]' WHERE id=?`, post)
			case "account":
				ctx.AppDB().Exec(`UPDATE social_accounts SET status='disconnected'`)
			case "connection":
				pf.connection.ProjectID = "other"
			case "external_id":
				ctx.AppDB().Exec(`UPDATE social_accounts SET external_account_id='other-page'`)
			case "cross_project":
				ctx = ctx.WithProject("other")
			case "unconfirmed":
				args["confirm"] = false
			case "duplicate_grant":
				second := mustRevalidate(t, a, ctx, post, target)
				// A transient source failure consumes this grant without creating a provider operation.
				dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
				defer dead.Close()
				// Reserve another grant's allowance to simulate a completed or interrupted dispatch.
				ctx.AppDB().Exec(`UPDATE delivery_recovery_requests SET status='consumed' WHERE id=?`, second["recovery_id"])
			}
			out, err := a.toolPostDeliveryRecover(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if len(pf.executeCalls) != 0 {
				t.Fatalf("unsafe recovery %s: %+v %+v", scenario, out, pf.executeCalls)
			}
		})
	}
}
func TestDeliveryRecoveryBlocksNonDeliveryAndUnknownFailures(t *testing.T) {
	for _, scenario := range []string{"authentication", "invalid", "duplicate", "unknown", "no_operation", "inactive", "unapproved"} {
		t.Run(scenario, func(t *testing.T) {
			a, ctx, pf, post, target, _ := recoveryFixture(t, "facebook")
			switch scenario {
			case "authentication":
				ctx.AppDB().Exec(`UPDATE post_targets SET last_error='upstream 401: unauthorized' WHERE id=?`, target)
			case "invalid":
				ctx.AppDB().Exec(`UPDATE post_targets SET last_error='upstream 400: invalid video' WHERE id=?`, target)
			case "duplicate":
				ctx.AppDB().Exec(`UPDATE post_targets SET failure_code='duplicate_content' WHERE id=?`, target)
			case "unknown":
				ctx.AppDB().Exec(`UPDATE post_targets SET failure_code='outcome_unknown' WHERE id=?`, target)
			case "no_operation":
				ctx.AppDB().Exec(`UPDATE social_accounts SET platform='tiktok'`)
			case "inactive":
				ctx.AppDB().Exec(`UPDATE social_accounts SET status='needs_reauth'`)
			case "unapproved":
				ctx.AppDB().Exec(`UPDATE posts SET approval_required=1,approved_revision=0 WHERE id=?`, post)
			}
			grant := mustRevalidate(t, a, ctx, post, target)
			if grant["decision"] != "blocked" || len(pf.executeCalls) != 0 {
				t.Fatal(scenario, grant, pf.executeCalls)
			}
		})
	}
}
func TestTikTokInspectionNativeStatesAndOpaqueReason(t *testing.T) {
	for _, status := range []string{"FAILED", "PROCESSING_UPLOAD", "PUBLISH_COMPLETE", "UNRECOGNIZED"} {
		t.Run(status, func(t *testing.T) {
			a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
			data := map[string]any{"status": status, "fail_reason": "internal"}
			if status == "PUBLISH_COMPLETE" {
				data["publicaly_available_post_id"] = []string{"public-id"}
			}
			body, _ := json.Marshal(map[string]any{"data": data, "error": map[string]any{"code": "ok", "log_id": "exact-log-id"}})
			pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: body}
			out, err := a.toolPostDeliveryInspect(ctx, map[string]any{"post_id": post, "target_id": target})
			if err != nil {
				t.Fatal(err)
			}
			got := out.(map[string]any)
			want := "pending"
			if status == "UNRECOGNIZED" {
				want = "blocked"
			}
			if status == "FAILED" {
				want = "revalidation_required"
			}
			if status == "PUBLISH_COMPLETE" {
				want = "published"
			}
			if got["decision"] != want {
				t.Fatal(got)
			}
			d := loadDeliveryDiagnostics(ctx, target)
			if d.FailReason != "internal" || d.ProviderRequestID != "exact-log-id" || d.OperationID != "existing-operation" {
				t.Fatal(d)
			}
			var local string
			ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, target).Scan(&local)
			if local != "failed" {
				t.Fatal("inspect mutated publication status", local)
			}
			for _, call := range pf.executeCalls {
				if call.Tool != "get_publish_status" {
					t.Fatal("inspect initialized publication", call)
				}
			}
			targets := a.loadTargets(ctx, post)
			if targets[0]["publish_operation_id"] != "existing-operation" || targets[0]["platform_post_id"] != "" {
				t.Fatal("operation/public identity conflated", targets)
			}
		})
	}
}
func TestTikTokRecoveryReconcilesWithoutResubmitting(t *testing.T) {
	for _, status := range []string{"PROCESSING_UPLOAD", "PUBLISH_COMPLETE"} {
		t.Run(status, func(t *testing.T) {
			a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
			data := map[string]any{"status": status}
			if status == "PUBLISH_COMPLETE" {
				data["publicaly_available_post_id"] = []string{"public-video"}
			}
			raw, _ := json.Marshal(map[string]any{"data": data})
			pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
			grant := mustRevalidate(t, a, ctx, post, target)
			if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
				t.Fatal(err)
			}
			for _, call := range pf.executeCalls {
				if call.Tool != "get_publish_status" {
					t.Fatal("reconciled operation was resubmitted", call)
				}
			}
			var local string
			ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, target).Scan(&local)
			want := "publishing"
			if status == "PUBLISH_COMPLETE" {
				want = "published"
			}
			if local != want {
				t.Fatal(local, want)
			}
		})
	}
}

type rewriteUploadTransport struct {
	base        http.RoundTripper
	destination *url.URL
}

func (r rewriteUploadTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.destination.Scheme
	u.Host = r.destination.Host
	copy.URL = &u
	return r.base.RoundTrip(copy)
}
func TestOrdinaryYouTubeInterruptedUploadProbesSameSession(t *testing.T) {
	pf := newRecordingPlatform()
	configureTestVideo(t, pf, 7, 24)
	ctx := newSocialCtx(t, pf)
	a := &App{}
	var puts, probes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Range") == "bytes */24" {
			probes++
			if len(body) != 0 || r.ContentLength != 0 {
				t.Error("status PUT was not empty")
			}
			w.Header().Set("Range", "bytes=0-11")
			w.WriteHeader(308)
			return
		}
		puts++
		if puts == 1 {
			if string(body) != string(testMP4) {
				t.Error("upload source changed")
			}
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Content-Range") != "bytes 12-23/24" || string(body) != string(testMP4[12:]) {
			t.Error("resume repeated bytes", r.Header, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"youtube-complete"}`))
	}))
	defer server.Close()
	dest, _ := url.Parse(server.URL)
	old := youtubeVideoUploadClient
	youtubeVideoUploadClient = &http.Client{Transport: rewriteUploadTransport{http.DefaultTransport, dest}}
	t.Cleanup(func() { youtubeVideoUploadClient = old })
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Headers: map[string]string{"Location": testYouTubeSession}, Data: json.RawMessage(`{}`)}
	result, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','youtube',42,'YT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := result.LastInsertId()
	out, err := a.toolPostCreate(ctx, map[string]any{"mode": "publish", "body": "video", "social_account_ids": []any{account}, "media_storage_ids": []any{int64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	post := out.(map[string]any)["post_id"].(int64)
	var target int64
	var retained string
	ctx.AppDB().QueryRow(`SELECT id,youtube_upload_session FROM post_targets WHERE post_id=?`, post).Scan(&target, &retained)
	if retained != testYouTubeSession {
		t.Fatal("session lost")
	}
	if _, err = a.toolPostRetry(ctx, map[string]any{"post_id": post, "target_id": target}); err != nil {
		t.Fatal(err)
	}
	init := 0
	for _, c := range pf.executeCalls {
		if c.Tool == "upload_video_init" {
			init++
		}
	}
	if init != 1 || probes != 1 || puts != 2 {
		t.Fatal("upload reinitialized or not resumed", init, probes, puts)
	}
	targets := a.loadTargets(ctx, post)
	if targets[0]["platform_post_id"] != "youtube-complete" || targets[0]["status"] != "published" {
		t.Fatal(targets)
	}
	raw, _ := json.Marshal(targets)
	if strings.Contains(string(raw), "upload_id") || strings.Contains(string(raw), "fixture") {
		t.Fatal("upload session exposed", string(raw))
	}
}

func TestTikTokRecoveryConfirmedFailureRetriesOnceAndRetainsHistory(t *testing.T) {
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
	grant := mustRevalidate(t, a, ctx, post, target)
	if grant["decision"] != "retry" || grant["fail_reason"] != "internal" {
		t.Fatal(grant)
	}
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(testMP4) {
			t.Error("wrong source bytes")
		}
		w.WriteHeader(201)
	}))
	defer upload.Close()
	init, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "second-operation", "upload_url": upload.URL + "?token=UPLOADSECRET"}})
	pf.executeResponses["post_video"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: init}
	// Both old and new operations are confirmed FAILED. internal stays opaque.
	if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	second := mustRevalidate(t, a, ctx, post, target)
	if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, second)); err != nil {
		t.Fatal(err)
	}
	inits := 0
	for _, call := range pf.executeCalls {
		if call.Tool == "post_video" {
			inits++
		}
	}
	if inits != 1 {
		t.Fatal("unbounded recovery", inits)
	}
	var op, reason string
	ctx.AppDB().QueryRow(`SELECT operation_id,reason FROM target_delivery_attempts WHERE target_id=?`, target).Scan(&op, &reason)
	if op != "existing-operation" || !strings.Contains(reason, "internal") {
		t.Fatal("original operation history lost", op, reason)
	}
	var attempts int
	ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, sibling).Scan(&attempts)
	if attempts != 1 {
		t.Fatal("successful sibling retried")
	}
}
func TestTikTokUploadInterruptionRetainsNativeOperation(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',delivery_revision=1,publish_operation_id='',delivery_diagnostics='{}' WHERE id=?`, target)
	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(500) }))
	defer upload.Close()
	init, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "retained-operation", "upload_url": upload.URL + "?token=TOPSECRET"}})
	pf.executeResponses["post_video"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: init}
	_, _, err := a.publishTikTokVideo(ctx, platforms["tiktok"], publishJob{targetID: target, connID: 42, platform: "tiktok", media: []mediaItem{testSourceItem()}, mediaProjectID: "test-proj"})
	warning, ok := err.(*publishedWarningError)
	if !ok || !warning.pending || warning.operationID != "retained-operation" {
		t.Fatal("lost pending identity", err)
	}
	var op string
	ctx.AppDB().QueryRow(`SELECT publish_operation_id FROM post_targets WHERE id=?`, target).Scan(&op)
	if op != "retained-operation" {
		t.Fatal("identity was not persisted before upload")
	}
	d := loadDeliveryDiagnostics(ctx, target)
	if d.Method != "FILE_UPLOAD" || d.Stage != "upload" || d.OperationState != "pending" {
		t.Fatal(d)
	}
	raw, _ := json.Marshal(a.loadTargets(ctx, post))
	if strings.Contains(string(raw), "TOPSECRET") {
		t.Fatal("upload credential leaked")
	}
}
func TestProviderInitializationUnknownIsNeverAutomaticallyRetried(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "facebook")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='pending',retryable=1 WHERE id=?`, target)
	pf.executeResponses[platforms["facebook"].VideoPostTool] = &sdk.ExecuteResult{Status: 502, Data: json.RawMessage(`{"error":"gateway timeout"}`)}
	a.publishSelectedPostTargets(ctx, post, target)
	a.toolPostRetry(ctx, map[string]any{"post_id": post})
	if len(pf.executeCalls) != 1 {
		t.Fatal("ambiguous submission repeated", pf.executeCalls)
	}
	var code string
	var retryable int
	ctx.AppDB().QueryRow(`SELECT failure_code,retryable FROM post_targets WHERE id=?`, target).Scan(&code, &retryable)
	if code != "outcome_unknown" || retryable != 0 {
		t.Fatal(code, retryable)
	}
	if grant := mustRevalidate(t, a, ctx, post, target); grant["decision"] != "blocked" {
		t.Fatal(grant)
	}
}

func TestVideoTargetOutputRedactsLegacySignedErrors(t *testing.T) {
	a, ctx, _, post, target, _ := recoveryFixture(t, "facebook")
	ctx.AppDB().Exec(`UPDATE post_targets SET last_error='fetch https://storage.test/video?sig=LEGACYSECRET' WHERE id=?`, target)
	raw, _ := json.Marshal(a.loadTargets(ctx, post))
	if strings.Contains(string(raw), "LEGACYSECRET") {
		t.Fatal("legacy signed URL exposed")
	}
}

func TestDeliveryRecoveryPreservesCompletedMembershipHandoff(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "youtube")
	_, err := ctx.AppDB().Exec(`UPDATE post_targets SET options='{"audience":{"type":"members"}}',youtube_upload_session=?,failure_code='upload_incomplete' WHERE id=?`, testYouTubeSession, target)
	if err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["upload_video_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"completed-member-video"}`)}
	grant := mustRevalidate(t, a, ctx, post, target)
	if grant["decision"] != "audience_setup" {
		t.Fatal(grant)
	}
	if _, err = a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	var status, audience, identity string
	var published any
	ctx.AppDB().QueryRow(`SELECT status,audience_status,platform_post_id,published_at FROM post_targets WHERE id=?`, target).Scan(&status, &audience, &identity, &published)
	if status != "awaiting_audience_setup" || audience != "awaiting_setup" || identity != "completed-member-video" || published != nil {
		t.Fatal(status, audience, identity, published)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "upload_video_status" {
			t.Fatal("completed membership upload repeated", call)
		}
	}
	// A later recovery request must not bypass the human confirmation step.
	again := mustRevalidate(t, a, ctx, post, target)
	if _, err = a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, again)); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, target).Scan(&status)
	if status != "awaiting_audience_setup" {
		t.Fatal("membership handoff was marked complete", status)
	}
}

func TestTikTokConflictingFailureAndPublicIdentityBlocksRecovery(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":{"status":"FAILED","fail_reason":"internal","publicaly_available_post_id":["already-created"]}}`)}
	grant := mustRevalidate(t, a, ctx, post, target)
	if grant["decision"] != "blocked" {
		t.Fatal("conflicting remote evidence authorized a retry", grant)
	}
	d := loadDeliveryDiagnostics(ctx, target)
	if d.OperationState != "unknown" || d.FailReason != "internal" {
		t.Fatal(d)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "get_publish_status" {
			t.Fatal("conflicting operation reposted", call)
		}
	}
}
