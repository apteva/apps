package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func optionsFixture(t *testing.T, platform string) (*sdk.AppCtx, *recordingPlatform, int64, int64, int64) {
	t.Helper()
	pf := newRecordingPlatform()
	ctx, post, target := retryFixture(t, pf, platform)
	_, err := ctx.AppDB().Exec(`UPDATE post_targets SET options='{}',retryable=0,failure_code='validation_error',last_error='choose a valid TikTok privacy_level from the creator''s current options' WHERE id=?`, target)
	if err != nil {
		t.Fatal(err)
	}
	var account int64
	if err := ctx.AppDB().QueryRow(`SELECT social_account_id FROM post_targets WHERE id=?`, target).Scan(&account); err != nil {
		t.Fatal(err)
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts,options,published_at) VALUES(?,?,'published','successful-sibling',1,'{"body":"keep me"}','2026-11-01 11:00:00')`, post, account)
	if err != nil {
		t.Fatal(err)
	}
	sibling, _ := res.LastInsertId()
	return ctx, pf, post, target, sibling
}

func optionsArgs(post, target int64, options map[string]any) map[string]any {
	return map[string]any{"post_id": post, "target_id": target, "expected_revision": 1, "expected_options_revision": 1, "options": options, "reason": "Publisher explicitly corrected this destination's options", "_caller": "regression-test"}
}

func rowSnapshot(t *testing.T, ctx *sdk.AppCtx, table string, id int64) string {
	t.Helper()
	if table != "posts" && table != "post_targets" {
		t.Fatal("invalid fixture table")
	}
	rows, err := ctx.AppDB().Query("SELECT * FROM "+table+" WHERE id=?", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatal("missing fixture row")
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertOptionsBlocked(t *testing.T, ctx *sdk.AppCtx, pf *recordingPlatform, post, target int64, args map[string]any) {
	t.Helper()
	before := rowSnapshot(t, ctx, "post_targets", target)
	out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] == "corrected" {
		t.Fatalf("unexpected correction: %+v", out)
	}
	if after := rowSnapshot(t, ctx, "post_targets", target); after != before {
		t.Fatalf("blocked correction changed target: %s -> %s", before, after)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "query_creator_info" {
			t.Fatalf("unexpected provider call: %+v", call)
		}
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM target_options_corrections WHERE target_id=?`, target).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit: count=%d err=%v", count, err)
	}
}

func TestFailedTargetOptionsExplicitPrivacyAuditedNoPublish(t *testing.T) {
	ctx, pf, post, target, sibling := optionsFixture(t, "tiktok")
	beforePost, beforeSibling := rowSnapshot(t, ctx, "posts", post), rowSnapshot(t, ctx, "post_targets", sibling)
	if _, err := ctx.AppDB().Exec(`INSERT INTO delivery_recovery_requests(id,project_id,post_id,target_id,revision,source_attempt,fingerprint,decision,evidence,actor,expires_at) VALUES('old-grant','test-proj',?,?,1,1,'old-options','retry','{}','test',datetime('now','+10 minutes'))`, post, target); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "corrected" || result["options_revision"] != 2 || result["published"] != false {
		t.Fatalf("%+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "query_creator_info" {
		t.Fatalf("published during correction: %+v", pf.executeCalls)
	}
	if rowSnapshot(t, ctx, "posts", post) != beforePost || rowSnapshot(t, ctx, "post_targets", sibling) != beforeSibling {
		t.Fatal("publication or successful sibling changed")
	}
	var status, options, code string
	var attempts, revision, retryable, selected int
	if err := ctx.AppDB().QueryRow(`SELECT status,options,failure_code,attempts,options_revision,retryable,retry_target_only FROM post_targets WHERE id=?`, target).Scan(&status, &options, &code, &attempts, &revision, &retryable, &selected); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 1 || revision != 2 || retryable != 1 || selected != 1 || code != "options_corrected" || !strings.Contains(options, "SELF_ONLY") {
		t.Fatalf("%s %s %s %d %d %d %d", status, options, code, attempts, revision, retryable, selected)
	}
	var grant string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM delivery_recovery_requests WHERE id='old-grant'`).Scan(&grant); err != nil || grant != "invalidated" {
		t.Fatalf("stale grant retained: %s %v", grant, err)
	}
	if repeated, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "PUBLIC_TO_EVERYONE"})); err != nil || repeated.(map[string]any)["status"] == "corrected" {
		t.Fatalf("stale correction accepted: %+v %v", repeated, err)
	}
	history, err := loadTargetOptionsCorrections(ctx, target)
	if err != nil || len(history) != 1 {
		t.Fatalf("%+v %v", history, err)
	}
	if history[0]["actor"] != "reported:regression-test" || history[0]["source_attempt"] != 1 || history[0]["previous_options"].(map[string]any)["privacy_level"] != nil || history[0]["corrected_options"].(map[string]any)["privacy_level"] != "SELF_ONLY" {
		t.Fatalf("audit: %+v", history)
	}
	inspection, err := (&App{}).toolPostDeliveryInspect(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil || len(inspection.(map[string]any)["options_corrections"].([]map[string]any)) != 1 {
		t.Fatalf("inspection %+v %v", inspection, err)
	}
	for _, args := range []map[string]any{{"post_id": post}, {"post_id": post, "target_id": target}, {"post_id": post, "target_id": target, "expected_options_revision": 1}} {
		if _, err := (&App{}).toolPostRetry(ctx, args); err != nil {
			t.Fatal(err)
		}
		if rowSnapshot(t, ctx, "post_targets", sibling) != beforeSibling {
			t.Fatal("sibling retried")
		}
		if err := ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, target).Scan(&attempts); err != nil || attempts != 1 {
			t.Fatal("bulk or stale retry attempted corrected target")
		}
	}
	// Retrying remains a separate action, guarded by the current target revision.
	_, err = (&App{}).toolPostRetry(ctx, result["retry"].(map[string]any))
	if err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, target).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("selected retry did not claim same target: %d %v", attempts, err)
	}
	if rowSnapshot(t, ctx, "post_targets", sibling) != beforeSibling {
		t.Fatal("selected retry changed successful sibling")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, post).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate target created")
	}
}

func TestFailedTargetOptionsPrivacyNeverDefaults(t *testing.T) {
	for _, privacy := range []any{"PUBLIC_TO_EVERYONE", "", nil, 42, "FRIENDS_ONLY"} {
		t.Run(fmt.Sprint(privacy), func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
			pf.executeResponses["query_creator_info"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"privacy_level_options":["SELF_ONLY"]},"error":{"code":"ok"}}`)}
			assertOptionsBlocked(t, ctx, pf, post, target, optionsArgs(post, target, map[string]any{"privacy_level": privacy}))
		})
	}
	for _, failure := range []struct {
		name   string
		result *sdk.ExecuteResult
		err    error
	}{
		{"permission denied", &sdk.ExecuteResult{Success: false, Status: 403}, nil},
		{"no privacy choices", &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{}}`)}, nil},
		{"network failure", nil, errors.New("permission lookup unavailable")},
	} {
		t.Run(failure.name, func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
			pf.executeResponses["query_creator_info"], pf.executeErr = failure.result, failure.err
			assertOptionsBlocked(t, ctx, pf, post, target, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
		})
	}
}

func TestFailedTargetOptionsRevisionScopeAndIdentityGuards(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		mutate    func(map[string]any)
	}{
		{"stale post revision", "", func(a map[string]any) { a["expected_revision"] = 2 }},
		{"stale options revision", "", func(a map[string]any) { a["expected_options_revision"] = 2 }},
		{"missing revision", "", func(a map[string]any) { delete(a, "expected_options_revision") }},
		{"wrong project", "UPDATE posts SET project_id='other' WHERE id=(SELECT post_id FROM post_targets WHERE id=?)", nil},
		{"wrong account", "", func(a map[string]any) { a["social_account_id"] = 999 }},
		{"wrong post", "", func(a map[string]any) { a["post_id"] = 999 }},
		{"missing reason", "", func(a map[string]any) { a["reason"] = " " }},
		{"published", "UPDATE post_targets SET status='published',platform_post_id='existing' WHERE id=?", nil},
		{"provider identity", "UPDATE post_targets SET provider_post_id='existing' WHERE id=?", nil},
		{"page identity", "UPDATE post_targets SET facebook_page_post_id='page_post' WHERE id=?", nil},
		{"duplicate", "UPDATE post_targets SET failure_code='duplicate_content' WHERE id=?", nil},
		{"unknown outcome", "UPDATE post_targets SET failure_code='outcome_unknown' WHERE id=?", nil},
		{"unknown initialization", `UPDATE post_targets SET delivery_diagnostics='{"operation_state":"unknown"}' WHERE id=?`, nil},
		{"submitted without identity", `UPDATE post_targets SET submitted_at=CURRENT_TIMESTAMP WHERE id=?`, nil},
		{"accepted without identity", `UPDATE post_targets SET provider_accepted_at=CURRENT_TIMESTAMP WHERE id=?`, nil},
		{"upload session", "UPDATE post_targets SET youtube_upload_session='https://www.googleapis.com/upload/youtube/v3/videos?upload_id=existing' WHERE id=?", nil},
		{"unverified legacy failure", "UPDATE post_targets SET failure_code='upstream_error',last_error='something failed' WHERE id=?", nil},
		{"diagnostic operation", "UPDATE post_targets SET delivery_diagnostics='{" + `"operation_id":"lost-op","operation_state":"unknown"` + "}' WHERE id=?", nil},
		{"immutable source", "", func(a map[string]any) { a["options"] = map[string]any{"media_storage_ids": []any{1}} }},
		{"immutable audience", "", func(a map[string]any) { a["options"] = map[string]any{"audience": map[string]any{"type": "public"}} }},
		{"immutable destination", "", func(a map[string]any) { a["options"] = map[string]any{"destination_id": "other"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
			if tc.sql != "" {
				if _, err := ctx.AppDB().Exec(tc.sql, target); err != nil {
					t.Fatal(err)
				}
			}
			args := optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"})
			if tc.mutate != nil {
				tc.mutate(args)
			}
			assertOptionsBlocked(t, ctx, pf, post, target, args)
		})
	}
	for _, tc := range []struct{ name, sql string }{
		{"reviewed", "UPDATE posts SET approval_required=1,approved_revision=revision WHERE id=?"},
		{"scheduled", "UPDATE posts SET status='scheduled' WHERE id=?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
			if _, err := ctx.AppDB().Exec(tc.sql, post); err != nil {
				t.Fatal(err)
			}
			assertOptionsBlocked(t, ctx, pf, post, target, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
		})
	}
}

func TestFailedTargetOptionsReconcilesExistingTikTokOperation(t *testing.T) {
	for _, remote := range []string{"FAILED", "PUBLISH_COMPLETE", "PROCESSING_UPLOAD", "unrecognized", "read failure"} {
		t.Run(remote, func(t *testing.T) {
			ctx, pf, post, target, sibling := optionsFixture(t, "tiktok")
			if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='old-operation',failure_code='upstream_rejected' WHERE id=?`, target); err != nil {
				t.Fatal(err)
			}
			before := rowSnapshot(t, ctx, "post_targets", sibling)
			identity := ""
			if remote == "PUBLISH_COMPLETE" {
				identity = `,"publicaly_available_post_id":["existing-post"]`
			}
			pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(fmt.Sprintf(`{"data":{"status":%q,"fail_reason":"privacy_level_option_mismatch"%s}}`, remote, identity))}
			if remote == "read failure" {
				pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: false, Status: 503}
			}
			out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
			if remote == "read failure" {
				if err == nil {
					t.Fatalf("read failure accepted: %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(pf.executeCalls) < 1 || pf.executeCalls[0].Tool != "get_publish_status" {
				t.Fatal("operation not checked first")
			}
			for _, call := range pf.executeCalls {
				if call.Tool != "get_publish_status" && call.Tool != "query_creator_info" {
					t.Fatalf("republished: %+v", pf.executeCalls)
				}
			}
			if rowSnapshot(t, ctx, "post_targets", sibling) != before {
				t.Fatal("sibling changed")
			}
			var options, status, operation string
			if err := ctx.AppDB().QueryRow(`SELECT options,status,COALESCE(publish_operation_id,'') FROM post_targets WHERE id=?`, target).Scan(&options, &status, &operation); err != nil {
				t.Fatal(err)
			}
			if remote == "FAILED" {
				if out.(map[string]any)["status"] != "corrected" || operation != "" || status != "failed" || !strings.Contains(options, "SELF_ONLY") {
					t.Fatalf("%+v %s %s %s", out, status, options, operation)
				}
				var reason string
				if err := ctx.AppDB().QueryRow(`SELECT reason FROM target_delivery_attempts WHERE target_id=? AND operation_id='old-operation'`, target).Scan(&reason); err != nil || reason != "privacy_level_option_mismatch" {
					t.Fatalf("previous operation lost: %s %v", reason, err)
				}
			} else if options != "{}" || operation != "old-operation" || out.(map[string]any)["status"] == "corrected" {
				t.Fatalf("changed submitted options: %+v %s %s", out, options, operation)
			}
		})
	}
}

func TestFailedTargetOptionsGenericAndHTTP(t *testing.T) {
	for _, tc := range []struct {
		platform string
		patch    map[string]any
	}{
		{"twitter", map[string]any{"allow_longform": true}},
		{"youtube", map[string]any{"visibility": "private"}},
		{"facebook", map[string]any{"body": "Corrected caption"}},
		{"instagram", map[string]any{"thumbnail_storage_id": 42}},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			ctx, pf, post, target, sibling := optionsFixture(t, tc.platform)
			before := rowSnapshot(t, ctx, "post_targets", sibling)
			args := optionsArgs(post, target, tc.patch)
			body, _ := json.Marshal(args)
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/posts/%d/delivery/%d/options?project_id=test-proj", post, target), strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			(&App{}).handlePostsItem(w, req)
			var result map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code != 200 || result["status"] != "corrected" || len(pf.executeCalls) != 0 {
				t.Fatalf("%d %s %+v", w.Code, w.Body.String(), pf.executeCalls)
			}
			if rowSnapshot(t, ctx, "post_targets", sibling) != before {
				t.Fatal("sibling changed")
			}
			targets := (&App{}).loadTargets(ctx, post)
			if targets[0]["options_revision"] != 2 || targets[0]["retry_target_only"] != true {
				t.Fatalf("revision not exposed: %+v", targets)
			}
			if tc.platform == "twitter" {
				pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"corrected-target-post"}}`)}
				req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/posts/%d/retry?project_id=test-proj", post), strings.NewReader(fmt.Sprintf(`{"target_id":%d,"expected_options_revision":2}`, target)))
				w = httptest.NewRecorder()
				(&App{}).handlePostsItem(w, req)
				if w.Code != 200 || len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "post_tweet" {
					t.Fatalf("selected retry: %d %s %+v", w.Code, w.Body.String(), pf.executeCalls)
				}
				if rowSnapshot(t, ctx, "post_targets", sibling) != before {
					t.Fatal("successful sibling republished")
				}
			}
		})
	}
}

type optionsRacingPlatform struct {
	*recordingPlatform
	onRead func()
}

func (p *optionsRacingPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if tool == "query_creator_info" && p.onRead != nil {
		p.onRead()
	}
	return p.recordingPlatform.ExecuteIntegrationTool(id, tool, input)
}

func TestFailedTargetOptionsConcurrentChangeAndAuditRollback(t *testing.T) {
	for _, change := range []string{"options", "attempt", "approval", "receipt", "audit failure"} {
		t.Run(change, func(t *testing.T) {
			pf := &optionsRacingPlatform{recordingPlatform: newRecordingPlatform()}
			ctx := newSocialCtx(t, pf)
			_, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status) VALUES(72,'test-proj','tiktok',130,'creator','active'); INSERT INTO posts(id,project_id,body,status) VALUES(1,'test-proj','keep','failed'); INSERT INTO post_targets(id,post_id,social_account_id,status,attempts,options,failure_code) VALUES(1,1,72,'failed',1,'{}','validation_error')`)
			if err != nil {
				t.Fatal(err)
			}
			if change == "audit failure" {
				_, err = ctx.AppDB().Exec(`CREATE TRIGGER reject_options_audit BEFORE INSERT ON target_options_corrections BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				pf.onRead = func() {
					query := map[string]string{"options": `UPDATE post_targets SET options_revision=2,options='{"privacy_level":"PUBLIC_TO_EVERYONE"}' WHERE id=1`, "attempt": `UPDATE post_targets SET attempts=2 WHERE id=1`, "approval": `UPDATE posts SET approval_required=1 WHERE id=1`, "receipt": `INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,provenance) VALUES('test-proj',1,'raced-operation','raced-correlation',1,CURRENT_TIMESTAMP,'active','social_upload_receipt')`}[change]
					if _, err := ctx.AppDB().Exec(query); err != nil {
						t.Fatal(err)
					}
				}
			}
			out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(1, 1, map[string]any{"privacy_level": "SELF_ONLY"}))
			if err == nil && out.(map[string]any)["status"] == "corrected" {
				t.Fatalf("concurrent or unaudited correction: %+v", out)
			}
			var options string
			var count int
			if err := ctx.AppDB().QueryRow(`SELECT options FROM post_targets WHERE id=1`).Scan(&options); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(options, "SELF_ONLY") {
				t.Fatal("correction escaped guard/rollback")
			}
			if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM target_options_corrections`).Scan(&count); err != nil || count != 0 {
				t.Fatal("unexpected audit")
			}
		})
	}
}

func TestTikTokPrivacyFailureRequiresExplicitCorrection(t *testing.T) {
	failure := classifyPublishFailure(errors.New("choose a valid TikTok privacy_level from the creator's current options"))
	if failure.Code != "validation_error" || failure.Retryable {
		t.Fatalf("privacy failure was silently retryable: %+v", failure)
	}
}

func TestFailedTargetOptionsInvalidCorrectionRetainsFailedOperation(t *testing.T) {
	ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='terminal-op',failure_code='upstream_rejected' WHERE id=?`, target); err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"status":"FAILED","fail_reason":"privacy_level_option_mismatch"}}`)}
	before, err := loadFailedOptionsTarget(ctx.AppDB(), projectScope(ctx), post, target)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "invalid"}))
	after, readErr := loadFailedOptionsTarget(ctx.AppDB(), projectScope(ctx), post, target)
	before.Diagnostics = after.Diagnostics
	if err != nil || readErr != nil || out.(map[string]any)["status"] == "corrected" || after != before {
		t.Fatalf("invalid correction stranded operation: %+v %v", out, err)
	}
	out, err = (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
	if err != nil || out.(map[string]any)["status"] != "corrected" {
		t.Fatalf("valid correction failed after invalid choice: %+v %v", out, err)
	}
}

func TestFailedTargetOptionsUnresolvedUploadReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, state         string
		terminal, abandoned bool
	}{
		{"active", "active", false, false},
		{"stopped unknown", "stopped", false, false},
		{"active despite terminal receipt", "active", true, false},
		{"active despite abandonment", "active", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "tiktok")
			if _, err := ctx.AppDB().Exec(`INSERT INTO delivery_upload_operations(project_id,target_id,operation_id,correlation_id,source_attempt,initialized_at,uploader_state,provenance,abandoned_at) VALUES('test-proj',?,'old','corr',1,CURRENT_TIMESTAMP,?,'social_upload_receipt',CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END)`, target, tc.state, tc.abandoned); err != nil {
				t.Fatal(err)
			}
			if tc.terminal {
				if _, err := ctx.AppDB().Exec(`INSERT INTO target_delivery_attempts(target_id,operation_id,status) VALUES(?,'old','failed')`, target); err != nil {
					t.Fatal(err)
				}
			}
			assertOptionsBlocked(t, ctx, pf, post, target, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
		})
	}
}

func TestFailedTargetOptionsSelectedTikTokPublishesCorrectedPrivacyOnce(t *testing.T) {
	ctx, pf, post, target, sibling := optionsFixture(t, "tiktok")
	var account int64
	if err := ctx.AppDB().QueryRow(`SELECT social_account_id FROM post_targets WHERE id=?`, target).Scan(&account); err != nil {
		t.Fatal(err)
	}
	other, err := ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,attempts,options,retryable,failure_code) VALUES(?,?,'failed',1,'{}',0,'validation_error')`, post, account)
	if err != nil {
		t.Fatal(err)
	}
	otherTarget, _ := other.LastInsertId()
	successBefore, failedBefore := rowSnapshot(t, ctx, "post_targets", sibling), rowSnapshot(t, ctx, "post_targets", otherTarget)
	source := fixturePNG(t, 1080, 1924)
	delivery, _, err := normalizeDeliveryImage(source, tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(delivery)
	}))
	defer server.Close()
	storageImageFixture(pf, source, 109)
	pf.callAppResponses["storage:files_get"], _ = json.Marshal(map[string]any{"id": 9, "content_type": "image/png", "size_bytes": len(source)})
	pf.callAppResponses["storage:files_get_url"], _ = json.Marshal(map[string]any{"url": server.URL + "/delivery.jpg"})
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET media_storage_ids='[9]' WHERE id=?`, post); err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["post_photo"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"publish_id":"corrected-operation"}}`)}
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["corrected-post"]}}`)}
	out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "corrected" {
		t.Fatalf("%+v", result)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "query_creator_info" {
			t.Fatal("correction published")
		}
	}
	if _, err := (&App{}).toolPostRetry(ctx, result["retry"].(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolPostRetry(ctx, result["retry"].(map[string]any)); err != nil {
		t.Fatal(err)
	}
	calls := pf.nonCreatorInfoCalls()
	if len(calls) != 2 || calls[0].Tool != "post_photo" || calls[1].Tool != "get_publish_status" {
		t.Fatalf("duplicate or wrong publication: %+v", calls)
	}
	if calls[0].Input["post_info"].(map[string]any)["privacy_level"] != "SELF_ONLY" {
		t.Fatalf("privacy defaulted or changed: %+v", calls[0].Input)
	}
	if rowSnapshot(t, ctx, "post_targets", sibling) != successBefore || rowSnapshot(t, ctx, "post_targets", otherTarget) != failedBefore {
		t.Fatal("successful or failed sibling changed")
	}
	var status, id string
	var attempts, count int
	if err := ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=?`, target).Scan(&status, &id, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "published" || id != "corrected-post" || attempts != 2 {
		t.Fatalf("%s %s %d", status, id, attempts)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, post).Scan(&count); err != nil || count != 3 {
		t.Fatal("duplicate target created")
	}
}

func TestFailedTargetOptionsLegacyEmptyOptions(t *testing.T) {
	for _, raw := range []any{nil, "", "null"} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			ctx, _, post, target, _ := optionsFixture(t, "tiktok")
			if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET options=? WHERE id=?`, raw, target); err != nil {
				t.Fatal(err)
			}
			out, err := (&App{}).toolPostTargetOptionsUpdate(ctx, optionsArgs(post, target, map[string]any{"privacy_level": "SELF_ONLY"}))
			if err != nil || out.(map[string]any)["status"] != "corrected" {
				t.Fatalf("%+v %v", out, err)
			}
			history, err := loadTargetOptionsCorrections(ctx, target)
			if err != nil || len(history) != 1 || len(history[0]["previous_options"].(map[string]any)) != 0 {
				t.Fatalf("%+v %v", history, err)
			}
		})
	}
}

func TestFailedTargetOptionsNoOpAndInvalidGenericOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch map[string]any
	}{
		{"same privacy", map[string]any{"visibility": "private"}},
		{"invalid privacy", map[string]any{"visibility": "PUBLIC"}},
		{"removed privacy", map[string]any{"visibility": nil}},
		{"invalid boolean", map[string]any{"contains_synthetic_media": "yes"}},
		{"invalid thumbnail", map[string]any{"thumbnail_storage_id": 1.5}},
		{"invalid tags", map[string]any{"tags": []any{42}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, pf, post, target, _ := optionsFixture(t, "youtube")
			if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET options='{"visibility":"private"}' WHERE id=?`, target); err != nil {
				t.Fatal(err)
			}
			assertOptionsBlocked(t, ctx, pf, post, target, optionsArgs(post, target, tc.patch))
		})
	}
}
