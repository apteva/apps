package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func mustSucceed(t *testing.T, out any, err error) map[string]any {
	t.Helper()
	if err != nil || mcpResultError(out) != nil {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	return asMap(out)
}

func TestAuditNativeMutationsCannotRetarget(t *testing.T) {
	t.Run("google operations", func(t *testing.T) {
		pf := newRecordingPlatform()
		ctx := newAdsCtx(t, pf)
		app := &App{}
		id := seedResourceTestAccount(t, ctx, "google", "123")
		for _, op := range []any{
			map[string]any{"update": map[string]any{"resourceName": "customers/123/adGroupAds/20~999", "status": "ENABLED"}, "updateMask": "status"},
			map[string]any{"remove": "customers/123/adGroupAds/20~999"},
		} {
			out, err := app.toolAdUpdate(ctx, map[string]any{"ad_account_id": id, "adset_id": "20", "ad_id": "30", "platform_options": map[string]any{"operations": []any{op}}})
			if err != nil || mcpResultError(out) == nil {
				t.Fatalf("retarget accepted: %#v %v", out, err)
			}
		}
		if len(pf.executeCalls) != 0 {
			t.Fatalf("unsafe mutation reached provider: %#v", pf.executeCalls)
		}
		out, err := app.toolAdUpdate(ctx, map[string]any{"ad_account_id": id, "adset_id": "20", "ad_id": "30", "platform_options": map[string]any{"operations": []any{map[string]any{"update": map[string]any{"resourceName": "customers/123/adGroupAds/20~30", "status": "PAUSED"}, "updateMask": "status"}}}})
		mustSucceed(t, out, err)
		if len(pf.executeCalls) != 1 {
			t.Fatal("valid native update not forwarded")
		}
	})
	t.Run("x account and line item", func(t *testing.T) {
		pf := newRecordingPlatform()
		ctx := newAdsCtx(t, pf)
		id := seedResourceTestAccount(t, ctx, "x", "original")
		out, err := (&App{}).toolAdSetUpdate(ctx, map[string]any{"ad_account_id": id, "adset_id": "selected", "status": "PAUSED", "platform_options": map[string]any{"account_id": "foreign", "line_item_id": "foreign-line"}})
		mustSucceed(t, out, err)
		call := findExecuteCall(t, pf, "update_line_item")
		if call.Input["account_id"] != "original" || call.Input["line_item_id"] != "selected" {
			t.Fatalf("retargeted: %#v", call)
		}
	})
	t.Run("reddit parent and creative", func(t *testing.T) {
		pf := newRecordingPlatform()
		ctx := newAdsCtx(t, pf)
		id := seedResourceTestAccount(t, ctx, "reddit", "account")
		out, err := (&App{}).toolAdCreate(ctx, map[string]any{"ad_account_id": id, "adset_id": "selected", "creative_id": "selected-post", "platform_options": map[string]any{"data": map[string]any{"ad_group_id": "foreign", "post_id": "foreign-post", "ad_account_id": "foreign"}}})
		mustSucceed(t, out, err)
		data := asMap(findExecuteCall(t, pf, "create_ad").Input["data"])
		if data["ad_group_id"] != "selected" || data["post_id"] != "selected-post" || data["ad_account_id"] != nil {
			t.Fatalf("retargeted: %#v", data)
		}
	})
}

func TestAuditGoogleBudgetUpdateReportsPartialFailure(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	pf.executeResponses["search"] = executeJSON(`{"results":[{"campaignBudget":{"resourceName":"customers/123/campaignBudgets/9"}}]}`)
	pf.executeResponses["campaign_mutate"] = &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"message":"cannot pause"}}`)}
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	out, err := app.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": id, "campaign_id": "10", "daily_budget_cents": 2000, "status": "PAUSED"})
	if err != nil || mcpResultError(out) == nil || asMap(out)["status"] != "partial" || asMap(out)["budget"] == nil {
		t.Fatalf("hidden failure: %#v %v", out, err)
	}
	if len(recorder.EventsByTopic("entity.changed")) != 0 {
		t.Fatal("partial update emitted success")
	}
	before := len(pf.executeCalls)
	out, err = app.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": id, "campaign_id": "10", "daily_budget_cents": 2000, "status": "invalid"})
	if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) != before {
		t.Fatal("invalid status mutated budget")
	}
	out, err = app.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": id, "campaign_id": "10", "daily_budget_cents": 2000, "platform_options": map[string]any{"campaignBudgetResource": "customers/123/campaignBudgets/foreign"}})
	if err != nil || mcpResultError(out) == nil || len(pf.executeCalls) != before+1 {
		t.Fatal("foreign budget was mutated")
	}
}

func enqueueAuditJob(t *testing.T, ctx *sdk.AppCtx, app *App, pf *recordingPlatform, platform, key string, count int) int64 {
	t.Helper()
	id := addPerformanceAccount(t, ctx, ctx.CurrentProject(), platform, "account-"+key, "EUR", "UTC")
	audience := seedAudience(t, app, ctx, id, platform, "audience-"+key)
	pf.callAppResponses["storage:files_get_content"] = json.RawMessage(`{"content_base64":"` + base64.StdEncoding.EncodeToString([]byte("email\n"+strings.Repeat("member@example.com\n", count))) + `"}`)
	out, err := app.toolAudienceMembersSync(ctx, map[string]any{"ad_account_id": id, "audience_id": audience.ID, "operation": "add", "source": map[string]any{"kind": "storage", "ref": "8"}, "idempotency_key": key, "consent": map[string]any{"ad_user_data": "granted", "ad_personalization": "granted"}})
	return int64ArgAny(mustSucceed(t, out, err)["id"])
}

func TestAuditAudienceIdempotencyUsesKeyAndChecksPayload(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	first := enqueueAuditJob(t, ctx, app, pf, "meta", "first", 1)
	_ = enqueueAuditJob(t, ctx, app, pf, "meta", "second", 1)
	job, err := app.getAudienceJob(ctx, "test-proj", first)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"ad_account_id": job.AdAccountID, "audience_id": job.AudienceResourceID, "operation": job.Operation, "source": map[string]any{"kind": job.SourceKind, "ref": job.SourceRef}, "idempotency_key": "first", "consent": job.Consent}
	out, err := app.toolAudienceMembersSync(ctx, args)
	if int64ArgAny(mustSucceed(t, out, err)["id"]) != first {
		t.Fatal("retry returned another job")
	}
	args["operation"] = "remove"
	out, err = app.toolAudienceMembersSync(ctx, args)
	if err != nil || mcpResultError(out) == nil {
		t.Fatal("different request accepted with same key")
	}
}

func TestAuditAudienceWorkerRespectsDispatchedProject(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	foreign := enqueueAuditJob(t, ctx.WithProject("other"), app, pf, "meta", "foreign", 1)
	local := enqueueAuditJob(t, ctx, app, pf, "meta", "local", 1)
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{foreign: "queued", local: "completed"} {
		job, err := app.getAudienceJobAnyProject(ctx, id)
		if err != nil || job.Status != want {
			t.Fatalf("job %d: %#v %v", id, job, err)
		}
	}
	// Polling must not consume another project's provider-processing job either.
	_, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET status='provider_processing',provider_request_id='foreign-request' WHERE id=?`, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.pollGoogleAudienceJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project polling: %v", err)
	}
}

func TestAuditAudienceLeaseRecoveryFencesOldWorker(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := enqueueAuditJob(t, ctx, app, pf, "meta", "lease", 1)
	old, err := app.claimAudienceJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.claimAudienceJob(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("live lease stolen")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET lease_expires_at=datetime('now','-1 second') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	current, err := app.claimAudienceJob(ctx)
	if err != nil || current.ID != id || current.LeaseToken == old.LeaseToken {
		t.Fatalf("recovery: %#v %v", current, err)
	}
	if checkpointAudienceBatch(ctx, old, 0, 1, "") == nil {
		t.Fatal("expired worker checkpoint succeeded")
	}
	app.failAudienceJob(ctx, old, "old worker", false)
	app.processAudienceJob(ctx, current)
	job, err := app.getAudienceJob(ctx, "test-proj", id)
	if err != nil || job.Status != "completed" {
		t.Fatalf("recovered job: %#v %v", job, err)
	}
}

func TestAuditAudienceResumesAtCheckpointAndPollsEveryGoogleBatch(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := enqueueAuditJob(t, ctx, app, pf, "google", "batches", 1001)
	uploads := 0
	polling := map[string]string{"request-1": "PROCESSING", "request-3": "SUCCESS"}
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "data_manager_audience_members_ingest" {
			uploads++
			if uploads == 2 {
				return &sdk.ExecuteResult{Success: false, Status: 429, Data: json.RawMessage(`{"error":"rate limit"}`)}, nil
			}
			if uploads == 3 && len(input["audienceMembers"].([]map[string]any)) != 1 {
				t.Fatal("retry resent completed batch")
			}
			return executeJSON(fmt.Sprintf(`{"requestId":"request-%d"}`, uploads)), nil
		}
		if tool == "data_manager_request_status_get" {
			return executeJSON(fmt.Sprintf(`{"requestStatusPerDestination":[{"requestStatus":%q}]}`, polling[firstString(input, "requestId")])), nil
		}
		t.Fatalf("unexpected call %s", tool)
		return nil, nil
	}
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	job, err := app.getAudienceJob(ctx, "test-proj", id)
	if err != nil || job.Status != "queued" || job.ProcessedRows != 1000 {
		t.Fatalf("checkpoint: %#v %v", job, err)
	}
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now') WHERE id=?`, id)
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now') WHERE id=?`, id)
	if err := app.pollGoogleAudienceJob(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ = app.getAudienceJob(ctx, "test-proj", id)
	if job.Status != "provider_processing" || uploads != 3 {
		t.Fatalf("premature completion: %#v uploads=%d", job, uploads)
	}
	polling["request-1"] = "FAILURE"
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now') WHERE id=?`, id)
	if err := app.pollGoogleAudienceJob(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ = app.getAudienceJob(ctx, "test-proj", id)
	if job.Status != "failed" {
		t.Fatal("failure in first batch was ignored")
	}
}

func TestAuditGoogleBatchCompletionStatuses(t *testing.T) {
	for _, status := range []string{"SUCCESS", "FAILED", "PARTIAL_SUCCESS"} {
		t.Run(status, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newAdsCtx(t, pf)
			app := &App{}
			id := enqueueAuditJob(t, ctx, app, pf, "google", "statuses", 1001)
			uploads, polls := 0, 0
			pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
				if tool == "data_manager_audience_members_ingest" {
					uploads++
					return executeJSON(fmt.Sprintf(`{"requestId":"batch-%d"}`, uploads)), nil
				}
				polls++
				batchStatus := "SUCCESS"
				if input["requestId"] == "batch-2" {
					batchStatus = status
				}
				return executeJSON(fmt.Sprintf(`{"requestStatusPerDestination":[{"requestStatus":%q}]}`, batchStatus)), nil
			}
			if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
				t.Fatal(err)
			}
			_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now') WHERE id=?`, id)
			if err := app.pollGoogleAudienceJob(ctx); err != nil {
				t.Fatal(err)
			}
			job, err := app.getAudienceJob(ctx, "test-proj", id)
			want := "failed"
			if status == "SUCCESS" {
				want = "completed"
			}
			if err != nil || job.Status != want || uploads != 2 || polls != 2 {
				t.Fatalf("job=%#v uploads=%d polls=%d err=%v", job, uploads, polls, err)
			}
		})
	}
}

func TestAuditAudienceResumeRejectsChangedSource(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := enqueueAuditJob(t, ctx, app, pf, "google", "changed", 1001)
	uploads := 0
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		uploads++
		if uploads == 2 {
			return &sdk.ExecuteResult{Success: false, Status: 429}, nil
		}
		return executeJSON(`{"requestId":"first"}`), nil
	}
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	pf.callAppResponses["storage:files_get_content"] = json.RawMessage(`{"content_base64":"` + base64.StdEncoding.EncodeToString([]byte("email\nchanged@example.com\n")) + `"}`)
	_, _ = ctx.AppDB().Exec(`UPDATE ad_audience_jobs SET available_at=datetime('now') WHERE id=?`, id)
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	job, err := app.getAudienceJob(ctx, "test-proj", id)
	if err != nil || job.Status != "failed" || !strings.Contains(job.LastError, "source changed") || uploads != 2 {
		t.Fatalf("unsafe resume: %#v %v uploads=%d", job, err, uploads)
	}
}

func TestAuditAudienceRecoveryMigration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "009" {
			continue
		}
		migration, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		key, status, request string
		count                int
	}{
		{"single", "provider_processing", "request-1", 1000},
		{"multiple", "provider_processing", "last-only", 1001},
		{"missing", "provider_processing", "", 10},
		{"interrupted", "processing", "last-only", 1000},
	} {
		_, err := db.Exec(`INSERT INTO ad_audience_jobs(project_id,ad_account_id,native_audience_id,operation,source_kind,source_ref,idempotency_key,status,processed_rows,accepted_rows,provider_request_id) VALUES('p',1,'a','add','storage','1',?,?,?,?,?)`, fixture.key, fixture.status, fixture.count, fixture.count, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("migrations/009_audience_job_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"single": "provider_processing", "multiple": "failed", "missing": "failed", "interrupted": "queued"} {
		var status string
		var processed int
		if err := db.QueryRow(`SELECT status,processed_rows FROM ad_audience_jobs WHERE idempotency_key=?`, key).Scan(&status, &processed); err != nil {
			t.Fatal(err)
		}
		if status != want || (key == "interrupted" && processed != 0) {
			t.Fatalf("%s: %s processed=%d", key, status, processed)
		}
	}
	var request string
	if err := db.QueryRow(`SELECT provider_request_id FROM ad_audience_batches`).Scan(&request); err != nil || request != "request-1" {
		t.Fatalf("legacy diagnostics: %q %v", request, err)
	}
}

func TestAuditGoogleExhaustiveListsDoNotApplyGAQLLimit(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	acct, def, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	for _, level := range []string{"campaign", "ad_group", "ad"} {
		t.Run(level, func(t *testing.T) {
			calls := 0
			pf.executeResponder = func(_ int64, _ string, input map[string]any) (*sdk.ExecuteResult, error) {
				calls++
				if strings.Contains(firstString(input, "query"), " LIMIT ") {
					t.Fatal("exhaustive query capped")
				}
				if calls == 1 {
					return executeJSON(`{"results":[],"nextPageToken":"page2"}`), nil
				}
				if input["page_token"] != "page2" {
					t.Fatal("page token lost")
				}
				return executeJSON(`{"results":[]}`), nil
			}
			var failure map[string]any
			if level == "campaign" {
				_, failure = app.listAllProviderCampaigns(ctx, acct, def, map[string]any{})
			} else {
				_, failure = app.listAllProviderChildren(ctx, acct, def, level, map[string]any{})
			}
			if failure != nil || calls != 2 {
				t.Fatalf("pagination: %d %#v", calls, failure)
			}
		})
	}
}

func TestAuditCachedPerformanceDoesNotRefreshHierarchy(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedSelectedAccount(t, ctx, "act_cached")
	acct, _, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	if err := app.upsertManagedCampaign(ctx, acct, map[string]any{"id": "10", "name": "Managed"}, "imported"); err != nil {
		t.Fatal(err)
	}
	if err := app.upsertDeliveryEntities(ctx, acct, "ad_group", []map[string]any{{"id": "20", "campaign_id": "10"}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := app.upsertDeliveryEntities(ctx, acct, "ad", []map[string]any{{"id": "30", "campaign_id": "10", "adset_id": "20"}}, ""); err != nil {
		t.Fatal(err)
	}
	pf.executeResponder = func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		t.Fatal("cached read called provider")
		return nil, nil
	}
	for _, level := range []string{"ad_group", "ad"} {
		out, err := app.toolPerformanceGet(ctx, map[string]any{"ad_account_id": id, "level": level, "refresh": false, "date_from": "2026-09-01", "date_to": "2026-09-02"})
		mustSucceed(t, out, err)
	}
}

func TestAuditHierarchyUsesTwoAccountScans(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedSelectedAccount(t, ctx, "act_batch")
	acct, def, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	for i := 0; i < 20; i++ {
		if err := app.upsertManagedCampaign(ctx, acct, map[string]any{"id": fmt.Sprint(i + 1)}, "imported"); err != nil {
			t.Fatal(err)
		}
	}
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if input["objectId"] != "act_batch" {
			t.Fatal("per-parent request")
		}
		if tool == "adset_list" {
			return executeJSON(`{"data":[{"id":"g1","campaign_id":"1"},{"id":"other","campaign_id":"999"}]}`), nil
		}
		return executeJSON(`{"data":[{"id":"a1","adset_id":"g1","campaign_id":"1"}]}`), nil
	}
	if failure := app.refreshManagedHierarchy(ctx, acct, def); failure != nil {
		t.Fatal(failure)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("calls=%d", len(pf.executeCalls))
	}
	var leaked int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_entities WHERE native_entity_id='other'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("unmanaged hierarchy persisted")
	}
	// Previously managed children moved to an unmanaged campaign must lose
	// authorization after a complete refresh, even if IDs are unchanged.
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "adset_list" {
			return executeJSON(`{"data":[{"id":"g1","campaign_id":"999"}]}`), nil
		}
		return executeJSON(`{"data":[{"id":"a1","adset_id":"g1","campaign_id":"999"}]}`), nil
	}
	if failure := app.refreshManagedHierarchy(ctx, acct, def); failure != nil {
		t.Fatal(failure)
	}
	var remaining int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_entities WHERE level IN ('ad_group','ad')`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("stale scope retained: %d %v", remaining, err)
	}
}

func TestAuditHierarchyCoalescesConcurrentRefreshes(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedSelectedAccount(t, ctx, "act_single")
	acct, def, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	if err := app.upsertManagedCampaign(ctx, acct, map[string]any{"id": "10"}, "imported"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return executeJSON(`{"data":[]}`), nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if failure := app.refreshManagedHierarchy(ctx, acct, def); failure != nil {
			t.Error(failure)
		}
	}()
	<-started
	go func() {
		defer wg.Done()
		if failure := app.refreshManagedHierarchy(ctx, acct, def); failure != nil {
			t.Error(failure)
		}
	}()
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("duplicate scans: %d", calls.Load())
	}
}

func TestAuditAnalyticsPreservesHierarchyAndReconcilesOnlyRequestedRange(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedResourceTestAccount(t, ctx, "x", "account")
	acct, _, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	original := map[string]any{"id": "a1", "name": "Real ad", "campaign_id": "c1", "adset_id": "g1", "creative": map[string]any{"id": "creative1"}}
	if err := app.upsertDeliveryEntities(ctx, acct, "ad", []map[string]any{original}, ""); err != nil {
		t.Fatal(err)
	}
	req := &genericPerformanceRequest{Level: "ad", DateFrom: "2026-09-01", DateTo: "2026-09-02"}
	points := []analyticsPoint{
		{Platform: "x", Level: "ad", EntityID: "a1", EntityName: "a1", Date: "2026-09-01", SpendMicros: 10, FetchedAt: "now"},
		{Platform: "x", Level: "ad", EntityID: "a1", Date: "2026-09-02", SpendMicros: 20, FetchedAt: "now"},
		{Platform: "x", Level: "ad", EntityID: "a2", Date: "2026-09-02", SpendMicros: 30, FetchedAt: "now"},
	}
	if err := persistAnalyticsPoints(ctx, "test-proj", acct, req, points); err != nil {
		t.Fatal(err)
	}
	var parent, group, raw string
	if err := ctx.AppDB().QueryRow(`SELECT campaign_id,ad_group_id,provider_data_json FROM ad_entities WHERE level='ad' AND native_entity_id='a1'`).Scan(&parent, &group, &raw); err != nil {
		t.Fatal(err)
	}
	if parent != "c1" || group != "g1" || !strings.Contains(raw, "creative1") || points[0].CampaignID != "c1" {
		t.Fatalf("hierarchy overwritten: %s %s %s", parent, group, raw)
	}
	req.DateFrom = "2026-09-02"
	req.EntityIDs = []string{"a1"}
	if err := persistAnalyticsPoints(ctx, "test-proj", acct, req, nil); err != nil {
		t.Fatal(err)
	}
	all := &genericPerformanceRequest{Level: "ad", DateFrom: "2026-09-01", DateTo: "2026-09-02"}
	remaining, err := loadAnalyticsPoints(ctx, "test-proj", id, all)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("incorrect scope deletion: %#v %v", remaining, err)
	}
	// A malformed complete response must roll back the deletion.
	if err := persistAnalyticsPoints(ctx, "test-proj", acct, all, []analyticsPoint{{Level: "ad", EntityID: "bad", Date: "invalid"}}); err == nil {
		t.Fatal("malformed report accepted")
	}
	remaining, _ = loadAnalyticsPoints(ctx, "test-proj", id, all)
	if len(remaining) != 2 {
		t.Fatal("failed refresh destroyed cache")
	}
}

func TestAuditAudienceDeleteFindsUsageBeyondFirstPage(t *testing.T) {
	for _, platform := range []string{"meta", "reddit", "google"} {
		t.Run(platform, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newAdsCtx(t, pf)
			app := &App{}
			id := seedResourceTestAccount(t, ctx, platform, "123")
			audience := seedAudience(t, app, ctx, id, platform, "77")
			calls := 0
			pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
				calls++
				switch platform {
				case "meta":
					if calls == 1 {
						return executeJSON(`{"data":[],"paging":{"next":"next","cursors":{"after":"second"}}}`), nil
					}
					if tool != "adset_list" || input["after"] != "second" {
						t.Fatalf("unexpected %#v %s", input, tool)
					}
					return executeJSON(`{"data":[{"id":"late","targeting":{"custom_audiences":[{"id":"77"}]}}]}`), nil
				case "reddit":
					if calls == 1 {
						return executeJSON(`{"data":[],"pagination":{"next_url":"next"}}`), nil
					}
					if tool != "list_ad_groups" || input["next_url"] != "next" {
						t.Fatalf("unexpected %#v %s", input, tool)
					}
					return executeJSON(`{"data":[{"id":"late","targeting":{"audience_ids":["77"]}}]}`), nil
				default:
					if strings.Contains(firstString(input, "query"), "FROM campaign_criterion") {
						return executeJSON(`{"results":[{"campaignCriterion":{"resourceName":"campaign-target"}}]}`), nil
					}
					return executeJSON(`{"results":[]}`), nil
				}
			}
			out, err := app.toolAudienceDelete(ctx, map[string]any{"ad_account_id": id, "audience_id": audience.ID})
			if err != nil || mcpResultError(out) == nil || !strings.Contains(mcpErrorMessage(asMap(out)), "used") {
				t.Fatalf("unsafe delete: %#v %v", out, err)
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestAuditRedditUnsupportedFinancialUpdatesFailExplicitly(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	id := seedResourceTestAccount(t, ctx, "reddit", "account")
	app := &App{}
	for _, field := range []string{"daily_budget_cents", "lifetime_budget_cents", "bid_amount_cents"} {
		for _, level := range []string{"campaign", "ad_group"} {
			args := map[string]any{"ad_account_id": id, "campaign_id": "c1", "adset_id": "g1", field: 1000}
			var out any
			var err error
			if level == "campaign" {
				out, err = app.toolCampaignUpdate(ctx, args)
			} else {
				out, err = app.toolAdSetUpdate(ctx, args)
			}
			if err != nil || mcpResultError(out) == nil {
				t.Fatalf("silent %s update: %#v %v", field, out, err)
			}
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("unsupported financial update called provider")
	}
}
