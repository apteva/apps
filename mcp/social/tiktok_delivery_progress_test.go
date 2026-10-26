package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTikTok404NeverAcknowledgesRejectedBytes(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',publish_operation_id='',delivery_diagnostics='{}',last_error=NULL WHERE id=?`, target)
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Tt-Logid", "upload-request-id")
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"no upload task","upload_token":"SECRET","url":"https://upload.tiktok.test/video?upload_token=SECRET"}`)
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "retained-operation", "upload_url": server.URL + "/video?upload_token=A%2FB%2B%3D&upload_id=123"}, "error": map[string]any{"code": "ok", "log_id": "init-log"}})
	pf.executeResponses["post_video"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
	_, _, err := a.publishTikTokVideo(ctx, platforms["tiktok"], publishJob{targetID: target, connID: 42, platform: "tiktok", media: []mediaItem{testSourceItem()}, mediaProjectID: "test-proj"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatal(err)
	}
	d := loadDeliveryDiagnostics(ctx, target)
	if d.AcknowledgedBytes == nil || *d.AcknowledgedBytes != 0 || d.TransferredBytes != 0 || d.AttemptedBytes == nil || *d.AttemptedBytes != 24 || d.VerifiedSourceBytes == nil || *d.VerifiedSourceBytes != 24 {
		t.Fatalf("wrong counters: %+v", d)
	}
	if d.Initialization == nil || d.Initialization.RequestID != "init-log" || d.FirstUploadError == nil || d.FirstUploadError.HTTPStatus != 404 || d.FirstUploadError.RequestID != "upload-request-id" || d.FirstUploadError.Chunk != 1 || d.FirstUploadError.ContentRange != "bytes 0-23/24" {
		t.Fatal(d)
	}
	if query != "upload_token=A%2FB%2B%3D&upload_id=123" || d.UploadURLSHA256 != d.LastUpload.URLSHA256 {
		t.Fatal("initialization/upload binding changed")
	}
	output, _ := json.Marshal(a.loadTargets(ctx, post))
	if strings.Contains(string(output), "SECRET") || strings.Contains(string(output), "A%2FB") {
		t.Fatal("credential leaked", string(output))
	}
}
func TestTikTokPartialFailurePreservesAcknowledgedPrefix(t *testing.T) {
	_, ctx, _, _, target, _ := recoveryFixture(t, "tiktok")
	file, err := os.CreateTemp(t.TempDir(), "source")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	file.Write(testMP4)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.Copy(io.Discard, r.Body)
		if calls == 1 {
			w.Header().Set("Content-Range", "bytes 0-11/24")
			w.WriteHeader(206)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, Method: "FILE_UPLOAD", AttemptedBytes: bytePointer(0), AcknowledgedBytes: bytePointer(0), UploadURLSHA256: uploadURLHash(server.URL)}
	err = uploadTikTokChunks(ctx, target, &preparedVideo{File: file, Size: 24, MIME: "video/mp4"}, &d, server.URL, 12, 2)
	if err == nil || d.TransferredBytes != 12 || *d.AcknowledgedBytes != 12 || *d.AttemptedBytes != 24 || d.LatestUploadError.Chunk != 2 {
		t.Fatalf("%+v %v", d, err)
	}
}
func TestTikTokAcknowledgementsValidateStatusAndRange(t *testing.T) {
	for _, tc := range []struct {
		status     int
		rangeValue string
	}{{200, ""}, {201, "bytes 0-2/24"}, {201, "bytes 0-23/25"}, {201, "broken"}, {206, "bytes 0-23/24"}} {
		t.Run(tc.rangeValue+http.StatusText(tc.status), func(t *testing.T) {
			_, ctx, _, _, target, _ := recoveryFixture(t, "tiktok")
			file, _ := os.CreateTemp(t.TempDir(), "source")
			defer file.Close()
			file.Write(testMP4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if tc.rangeValue != "" {
					w.Header().Set("Content-Range", tc.rangeValue)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, AttemptedBytes: bytePointer(0), AcknowledgedBytes: bytePointer(0), UploadURLSHA256: uploadURLHash(server.URL)}
			if err := uploadTikTokChunks(ctx, target, &preparedVideo{File: file, Size: 24, MIME: "video/mp4"}, &d, server.URL, 24, 1); err == nil || *d.AcknowledgedBytes != 0 || d.LatestUploadError == nil {
				t.Fatal(d, err)
			}
		})
	}
}
func TestTikTokPollingRetainsDurableUploadErrorAndZeroProgress(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	first := &deliveryOutcome{At: at, HTTPStatus: 404, Result: "http_rejected", ResponseExcerpt: "no valid upload task", Chunk: 1, RequestID: "original-request"}
	d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, Method: "FILE_UPLOAD", OperationID: "existing-operation", FirstUploadError: first, LatestUploadError: first, Initialization: &deliveryOutcome{At: at, Result: "operation_persisted"}, AcknowledgedBytes: bytePointer(0)}
	if err := d.save(ctx, target); err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0},"error":{"code":"ok","log_id":"status-request"}}`)
	for i := 0; i < 40; i++ {
		if _, _, _, err := a.getTikTokPublishStatus(ctx, 42, "existing-operation"); err != nil {
			t.Fatal(err)
		}
	}
	d = loadDeliveryDiagnostics(ctx, target)
	if len(d.Events) != 32 || d.FirstUploadError.HTTPStatus != 404 || d.LatestUploadError.RequestID != "original-request" || d.Initialization == nil || d.ProviderObservation.UploadedBytes == nil || *d.ProviderObservation.UploadedBytes != 0 || d.ProviderObservation.DownloadedBytes != nil || d.LocalDiagnostic != "stalled_upload" || d.ProviderStatus != "PROCESSING_UPLOAD" || d.OperationState != "pending" {
		t.Fatalf("lost evidence: %+v", d)
	}
	out, err := a.toolPostDeliveryInspect(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["decision"] != "pending" || len(result["diagnostics_history"].([]map[string]any)) == 0 {
		t.Fatal(result)
	}
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD"},"error":{"code":"ok"}}`)
	a.getTikTokPublishStatus(ctx, 42, "existing-operation")
	d = loadDeliveryDiagnostics(ctx, target)
	if d.ProviderObservation.UploadedBytes != nil {
		t.Fatal("missing progress became zero")
	}
}
func TestTikTokStalePollCannotEraseUploadFailure(t *testing.T) {
	_, ctx, _, _, target, _ := recoveryFixture(t, "tiktok")
	d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, AcknowledgedBytes: bytePointer(0)}
	d.save(ctx, target)
	stale := d
	d.uploadOutcome(ctx, target, deliveryOutcome{HTTPStatus: 404, Result: "http_rejected", ResponseExcerpt: "failed"})
	stale.ProviderObservation = &tikTokProgressObservation{At: time.Now().UTC().Format(time.RFC3339Nano), Status: "PROCESSING_UPLOAD", QuerySucceeded: true}
	stale.save(ctx, target)
	if got := loadDeliveryDiagnostics(ctx, target); got.LatestUploadError == nil || got.LatestUploadError.HTTPStatus != 404 {
		t.Fatal("stale poll erased error", got)
	}
}
func TestTikTokLegacyProgressIsUnknownAndOriginalErrorSurvivesUpgrade(t *testing.T) {
	d := decodeDeliveryDiagnostics(`{"method":"FILE_UPLOAD","transferred_bytes":25565810,"source_bytes":25565810,"source_sha256":"abc","provider_status":"PROCESSING_UPLOAD"}`, "tiktok", "TikTok chunk upload returned HTTP 404; inspect existing operation before recovery")
	if d.AcknowledgedBytes != nil || d.TransferredBytes != 0 || d.LegacyTransferredBytes == nil || *d.LegacyTransferredBytes != 25565810 || d.LatestUploadError.HTTPStatus != 404 || d.LatestUploadError.At != "" {
		t.Fatal(d)
	}
}
func TestTikTokStalledBackoffStopsWithoutReplacingOperation(t *testing.T) {
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',identity_resolve_after=NULL WHERE id=?`, target)
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0},"error":{"code":"ok"}}`)
	d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), ProgressVersion: 1, OperationID: "existing-operation", AutomaticPollAttempts: 11, LatestUploadError: &deliveryOutcome{At: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), HTTPStatus: 404, Result: "http_rejected"}}
	d.save(ctx, target)
	a.recoverTikTokDeliveries(ctx)
	d = loadDeliveryDiagnostics(ctx, target)
	if !d.AutomaticPollingStopped || d.AutomaticPollAttempts != 12 || d.LocalDiagnostic != "stalled_upload" {
		t.Fatal(d)
	}
	calls := len(pf.executeCalls)
	a.recoverTikTokDeliveries(ctx)
	if len(pf.executeCalls) != calls {
		t.Fatal("automatic polling was not bounded")
	}
	out, _ := a.toolPostDeliveryRevalidate(ctx, map[string]any{"post_id": post, "target_id": target})
	if out.(map[string]any)["decision"] != "pending" {
		t.Fatal("stalled operation authorized replacement", out)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "get_publish_status" {
			t.Fatal("reinitialized ambiguous operation", call)
		}
	}
	var state, identity string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, sibling).Scan(&state, &identity, &attempts)
	if state != "published" || identity != "keep-this-post" || attempts != 1 {
		t.Fatal("sibling changed")
	}
}

func TestTikTokMissingProgressDoesNotFakeAnotherAdvance(t *testing.T) {
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	d := deliveryDiagnostics{}
	d.observeTikTokProgress(tikTokProgressObservation{At: at, UploadedBytes: bytePointer(100), QuerySucceeded: true})
	d.observeTikTokProgress(tikTokProgressObservation{At: time.Now().UTC().Format(time.RFC3339Nano), QuerySucceeded: true})
	d.observeTikTokProgress(tikTokProgressObservation{At: time.Now().UTC().Format(time.RFC3339Nano), UploadedBytes: bytePointer(100), QuerySucceeded: true})
	if d.LastProgressAt != at {
		t.Fatal("missing observation created false progress")
	}
}
func TestTikTokSourceValidationArchivesOriginalAttemptEvidence(t *testing.T) {
	_, ctx, pf, _, target, _ := recoveryFixture(t, "tiktok")
	old := deliveryDiagnostics{CorrelationID: "original-attempt", Method: "FILE_UPLOAD", ProgressVersion: 1, SourceFileID: 7, SourceSHA256: "original-source", SourceBytes: 24, VerifiedSourceBytes: bytePointer(24), LatestUploadError: &deliveryOutcome{HTTPStatus: 404, Result: "http_rejected"}}
	if err := old.save(ctx, target); err != nil {
		t.Fatal(err)
	}
	prepared, err := (&App{}).prepareVideoSource(ctx, publishJob{targetID: target, mediaProjectID: "test-proj"}, testSourceItem(), "RECOVERY_VALIDATION")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	_ = pf
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT diagnostics FROM video_delivery_attempts WHERE target_id=? AND correlation_id='original-attempt'`, target).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var kept deliveryDiagnostics
	json.Unmarshal([]byte(raw), &kept)
	if kept.SourceSHA256 != "original-source" || kept.LatestUploadError == nil || prepared.Diagnostics.SourceFileID != 7 || prepared.Diagnostics.CorrelationID == "original-attempt" {
		t.Fatal("source validation rewrote old attempt evidence")
	}
}
func TestTikTokRecoveryDoesNotRepostFacebookOrYouTubeSiblings(t *testing.T) {
	a, ctx, pf, post, target, sibling := recoveryFixture(t, "tiktok")
	makeAccount := func(platform string) int64 {
		r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj',?,99,?,'active')`, platform, platform)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	fb := makeAccount("facebook")
	yt := makeAccount("youtube")
	ctx.AppDB().Exec(`UPDATE post_targets SET social_account_id=? WHERE id=?`, fb, sibling)
	r, err := ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts) VALUES(?,?,'published','youtube-keep',1)`, post, yt)
	if err != nil {
		t.Fatal(err)
	}
	ytTarget, _ := r.LastInsertId()
	grant := mustRevalidate(t, a, ctx, post, target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(201) }))
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"data": map[string]any{"publish_id": "replacement", "upload_url": server.URL}})
	pf.executeResponses["post_video"] = analyticsOK(string(raw))
	if _, err := a.toolPostDeliveryRecover(ctx, recoverArgs(post, target, grant)); err != nil {
		t.Fatal(err)
	}
	for id, expected := range map[int64]string{sibling: "keep-this-post", ytTarget: "youtube-keep"} {
		var state, identity string
		var attempts int
		ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, id).Scan(&state, &identity, &attempts)
		if state != "published" || identity != expected || attempts != 1 {
			t.Fatal("successful sibling changed", id)
		}
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "get_publish_status" && call.Tool != "post_video" && call.Tool != "query_creator_info" {
			t.Fatal("unexpected sibling publish", call.Tool)
		}
	}
}

func TestTikTokDeliveryHTTPUsesPathIDsAndRejectsForeignTarget(t *testing.T) {
	a, ctx, pf, post, target, _ := recoveryFixture(t, "tiktok")
	_ = ctx
	pf.executeResponses["get_publish_status"] = analyticsOK(`{"data":{"status":"PROCESSING_UPLOAD","uploaded_bytes":0},"error":{"code":"ok"}}`)
	w := httptest.NewRecorder()
	a.handlePostsItem(w, httptest.NewRequest("POST", fmt.Sprintf("/posts/%d/delivery/%d/revalidate?project_id=test-proj", post, target), strings.NewReader(`{"post_id":999,"target_id":999,"project_id":"other"}`)))
	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)
	if result["target_id"] != float64(target) || result["decision"] != "pending" {
		t.Fatal(w.Body.String())
	}
	before := len(pf.executeCalls)
	w = httptest.NewRecorder()
	a.handlePostsItem(w, httptest.NewRequest("GET", fmt.Sprintf("/posts/%d/delivery/%d/inspect?project_id=test-proj", post, target+999), nil))
	if !strings.Contains(w.Body.String(), "not found") || len(pf.executeCalls) != before {
		t.Fatal("foreign target was inspected", w.Body.String())
	}
}
