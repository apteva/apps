package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regressions discovered in Social v0.16.2. No real providers are contacted.
func auditSeed(t *testing.T, platform, provider, status string) (*sdk.AppCtx, *recordingPlatform, int64, int64) {
	t.Helper()
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,page_credentials,provider_slug,provider_account_id) VALUES ('test-proj',?,7,'destination','Audit','{"access_token":"test-token"}',?,?)`, platform, provider, map[bool]string{true: "za1", false: ""}[provider == "zernio"])
	if e != nil {
		t.Fatal(e)
	}
	aid, _ := r.LastInsertId()
	r, e = ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status,schedule_at) VALUES ('test-proj','original',?,?)`, status, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	if e != nil {
		t.Fatal(e)
	}
	pid, _ := r.LastInsertId()
	ts := "pending"
	ext := ""
	if status == "published" {
		ts = "published"
		ext = "p1"
	}
	if status == "draft" || status == "approved" {
		ts = "draft"
	}
	_, e = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id) VALUES (?,?,?,?)`, pid, aid, ts, nullable(ext))
	if e != nil {
		t.Fatal(e)
	}
	return ctx, pf, aid, pid
}
func TestAuditApprovalCannotBeBypassedByLegacyEdit(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "approved")
	_, e := ctx.AppDB().Exec(`UPDATE posts SET approval_required=1,approval_status='approved',approved_revision=1 WHERE id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	a := &App{}
	_, e = a.toolPostEdit(ctx, map[string]any{"post_id": pid, "body": "never reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.toolPostDraftPublish(ctx, map[string]any{"post_id": pid, "expected_revision": 1, "mode": "publish"})
	if e != nil {
		t.Fatal(e)
	}
	for _, call := range pf.executeCalls {
		if call.Input["message"] == "never reviewed" {
			t.Fatal("unreviewed content published")
		}
	}
	var body string
	if err := ctx.AppDB().QueryRow(`SELECT body FROM posts WHERE id=?`, pid).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "original" {
		t.Fatalf("legacy edit changed approved body: %q", body)
	}
}
func TestAuditCreationHonorsApprovalRequired(t *testing.T) {
	ctx, pf, aid, _ := auditSeed(t, "facebook", "native", "draft")
	out, e := (&App{}).toolPostCreate(ctx, map[string]any{"mode": "publish", "body": "needs approval", "approval_required": true, "social_account_ids": []any{aid}})
	if e != nil {
		t.Fatal(e)
	}
	if len(pf.executeCalls) > 0 {
		t.Fatalf("published with approval_required=true: %+v", out)
	}
}
func TestAuditRepeatedBodyEditsReachProvider(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "published")
	a := &App{}
	for _, s := range []string{"edit one", "edit two"} {
		if _, e := a.toolPostEdit(ctx, map[string]any{"post_id": pid, "body": s}); e != nil {
			t.Fatal(e)
		}
	}
	got := pf.executeCalls[len(pf.executeCalls)-1].Input["message"]
	if got != "edit two" {
		t.Fatalf("second edit sent %q", got)
	}
}
func TestAuditPostDeletionWithInbox(t *testing.T) {
	ctx, _, aid, pid := auditSeed(t, "facebook", "native", "published")
	_, _, e := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: aid, Platform: "facebook", Kind: "comment", ExternalID: "c1", PostID: pid})
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).toolPostDelete(ctx, map[string]any{"post_id": pid, "force_local_only": true})
	if e != nil {
		t.Fatalf("post with comment cannot be deleted: %v", e)
	}
}
func TestAuditFallbackRetryActuallyRetries(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "failed")
	_, e := ctx.AppDB().Exec(`UPDATE post_targets SET status='failed',attempts=1,retryable=1 WHERE post_id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	a := &App{}
	_, e = a.toolPostRetry(ctx, map[string]any{"post_id": pid})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.runScheduledPublisher(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if len(pf.executeCalls) == 0 {
		t.Fatalf("retry followed by due worker made no publish attempt; post=%s", a.postStatus(ctx, pid))
	}
}
func TestAuditScheduledCallbackChecksDueTime(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "scheduled")
	_, e := ctx.AppDB().Exec(`UPDATE posts SET schedule_at=? WHERE id=?`, time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339), pid)
	if e != nil {
		t.Fatal(e)
	}
	(&App{}).publishScheduledPost(ctx, "test-proj", pid)
	if len(pf.executeCalls) > 0 {
		t.Fatal("post scheduled tomorrow published immediately by callback")
	}
}
func TestAuditZernioFailedResponseIsNotPublished(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "linkedin", "zernio", "publishing")
	pf.executeResponses["create_post"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"post":{"_id":"provider1","status":"failed","platforms":[{"platform":"linkedin","accountId":"za1","status":"failed","error":"rejected"}]}}`)}
	a := &App{}
	a.publishPostTargets(ctx, pid)
	if got := a.postStatus(ctx, pid); got == "published" {
		t.Fatal("HTTP success with provider status=failed stored as published")
	}
}
func TestAuditProviderPublishingStateIsNotPublished(t *testing.T) {
	status, _, _ := zernioWorkflowStatus(map[string]any{"status": "publishing"}, nil)
	if status == "published" {
		t.Fatal("provider status publishing converted to published")
	}
}
func TestAuditProviderReconcileKeepsReview(t *testing.T) {
	ctx, _, aid, pid := auditSeed(t, "linkedin", "zernio", "draft")
	_, e := ctx.AppDB().Exec(`UPDATE posts SET status='in_review',approval_required=1,approval_status='pending' WHERE id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ctx.AppDB().Exec(`UPDATE post_targets SET provider_post_id='provider1' WHERE post_id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).reconcileImportedProviderPost(ctx, "test-proj", aid, 0, "provider body", "provider1", "", "", "draft", "draft", "", "", "", nil, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if got := (&App{}).postStatus(ctx, pid); got != "in_review" {
		t.Fatalf("background reconciliation changed in_review to %s", got)
	}
}
func TestAuditProviderReconcileRollsUpAllTargets(t *testing.T) {
	ctx, _, aid, pid := auditSeed(t, "linkedin", "zernio", "publishing")
	_, e := ctx.AppDB().Exec(`UPDATE post_targets SET provider_post_id='provider1' WHERE post_id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name) VALUES ('test-proj','facebook',8,'second')`)
	if e != nil {
		t.Fatal(e)
	}
	other, _ := r.LastInsertId()
	_, e = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status) VALUES (?,?,'pending')`, pid, other)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).reconcileImportedProviderPost(ctx, "test-proj", aid, 0, "provider body", "provider1", "up1", "", "published", "publish", "", "", "", nil, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if got := (&App{}).postStatus(ctx, pid); got == "published" {
		t.Fatal("one provider target marked entire post published while another remains pending")
	}
}
func TestAuditReimportReconnectsProviderAccount(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "linkedin", "zernio", "draft")
	_, e := ctx.AppDB().Exec(`UPDATE social_accounts SET status='disconnected' WHERE id=?`, aid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).upsertZernioSocialAccount(ctx, "test-proj", 9, 0, zernioAccount{AccountID: "za1", Platform: "linkedin", Name: "reconnected"})
	if e != nil {
		t.Fatal(e)
	}
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM social_accounts WHERE id=?`, aid).Scan(&status)
	if status != "active" {
		t.Fatalf("reconnect leaves account %s", status)
	}
}
func TestAuditNativeConnectDoesNotReuseProviderConnection(t *testing.T) {
	ctx, pf, _, _ := auditSeed(t, "facebook", "zernio", "draft")
	out, e := (&App{}).toolAccountAdd(ctx, map[string]any{"platform": "facebook"})
	if e != nil {
		t.Fatal(e)
	}
	if len(pf.startOAuthCalls) == 0 {
		t.Fatalf("native connect reused Zernio connection: %+v", out)
	}
}
func TestAuditInboxThreadIncludesDescendants(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "facebook", "native", "draft")
	var last int64
	for i := 0; i < 3; i++ {
		parent := ""
		if i > 0 {
			parent = fmt.Sprint(i - 1)
		}
		id, _, e := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: aid, Platform: "facebook", Kind: "dm", ExternalID: fmt.Sprint(i), ParentExternalID: parent})
		if e != nil {
			t.Fatal(e)
		}
		last = id
	}
	item, e := getInboxItem(ctx.AppDB(), "test-proj", last)
	if e != nil {
		t.Fatal(e)
	}
	thread, e := getInboxThread(ctx.AppDB(), "test-proj", item)
	if e != nil {
		t.Fatal(e)
	}
	if len(thread) != 3 {
		t.Fatalf("3-message chain returns %d messages, omitting selected grandchild", len(thread))
	}
}
func TestAuditZeroSnapshotReplacesPreviousValue(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "youtube", "native", "draft")
	a := &App{}
	q := analyticsQuery{RangeDays: 28}
	for _, value := range []int64{100, 0} {
		if e := a.persistAccountMetrics(ctx, "test-proj", accountMetricsResult{SocialAccountID: aid, Platform: "youtube", Status: "ok", Followers: value}, q); e != nil {
			t.Fatal(e)
		}
	}
	if out := a.storedAccountMetrics(ctx, "test-proj", aid, q); out.Followers != 0 {
		t.Fatalf("latest zero followers still displayed as %d", out.Followers)
	}
}
func TestAuditBreakdownCacheDoesNotMixFilters(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "youtube", "native", "draft")
	a := &App{}
	q := analyticsQuery{RangeDays: 28, Filters: map[string][]string{"country": {"US"}}}
	res := accountMetricsResult{SocialAccountID: aid, Platform: "youtube", Status: "ok", Views: 10, Breakdowns: []analyticsBreakdown{{Dimension: "device", Status: "ok", Rows: []analyticsBreakdownRow{{Dimensions: map[string]string{"device": "mobile"}, Metrics: map[string]int64{"views": 10}}}}}}
	if e := a.persistAccountMetrics(ctx, "test-proj", res, q); e != nil {
		t.Fatal(e)
	}
	out := a.storedAccountMetrics(ctx, "test-proj", aid, analyticsQuery{RangeDays: 28, Breakdowns: []string{"device"}})
	if len(out.Breakdowns) > 0 {
		t.Fatalf("unfiltered request receives US-only cache: %+v", out.Breakdowns)
	}
}
func TestAuditMediaLimitDetectsOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "1234567890") }))
	defer server.Close()
	b, e := readMediaURL(server.URL, 5)
	if e == nil {
		t.Fatalf("oversize media silently truncated to %q", b)
	}
}
func TestAuditMultiImageNeverSilentlyDropsAttachments(t *testing.T) {
	ctx, pf, _, _ := auditSeed(t, "facebook", "native", "draft")
	_, _, e := (&App{}).publishSingle(ctx, platforms["facebook"], publishJob{connID: 7, platform: "facebook", extID: "destination", body: "two photos", media: []mediaItem{{URL: "https://example.com/1.jpg", Mime: "image/jpeg"}, {URL: "https://example.com/2.jpg", Mime: "image/jpeg"}}})
	if e == nil && len(pf.executeCalls) == 1 && !strings.Contains(fmt.Sprint(pf.executeCalls[0].Input), "2.jpg") {
		t.Fatal("two-image post succeeded while only first image was sent")
	}
}

func TestAuditDeleteCancelsProviderScheduledPost(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "linkedin", "zernio", "scheduled")
	_, e := ctx.AppDB().Exec(`UPDATE posts SET source='provider' WHERE id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ctx.AppDB().Exec(`UPDATE post_targets SET status='scheduled',provider_post_id='remote-scheduled' WHERE post_id=?`, pid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).toolPostDelete(ctx, map[string]any{"post_id": pid})
	if e != nil {
		t.Fatal(e)
	}
	if len(pf.executeCalls) == 0 {
		t.Fatal("deleted local record without cancelling provider scheduled post")
	}
}

func TestAuditAnalyticsRangeDoesNotUseDifferentWindow(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "youtube", "native", "draft")
	a := &App{}
	q := analyticsQuery{RangeDays: 28}
	res := accountMetricsResult{SocialAccountID: aid, Platform: "youtube", Status: "ok", Views: 2800, Breakdowns: []analyticsBreakdown{{Dimension: "country", Status: "ok", Rows: []analyticsBreakdownRow{{Dimensions: map[string]string{"country": "US"}, Metrics: map[string]int64{"views": 2800}}}}}}
	if e := a.persistAccountMetrics(ctx, "test-proj", res, q); e != nil {
		t.Fatal(e)
	}
	out := a.storedAccountMetrics(ctx, "test-proj", aid, analyticsQuery{RangeDays: 7, Breakdowns: []string{"country"}})
	if out.Views == 2800 || len(out.Breakdowns) > 0 {
		t.Fatalf("7-day request returned 28-day snapshot: views=%d breakdowns=%+v", out.Views, out.Breakdowns)
	}
}

func TestAuditProfileCannotReassignToItself(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "facebook", "native", "draft")
	r, e := ctx.AppDB().Exec(`INSERT INTO profiles(project_id,name,slug) VALUES ('test-proj','Profile','profile')`)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := r.LastInsertId()
	_, e = ctx.AppDB().Exec(`UPDATE social_accounts SET profile_id=? WHERE id=?`, id, aid)
	if e != nil {
		t.Fatal(e)
	}
	_, e = (&App{}).toolProfileDelete(ctx, map[string]any{"id": id, "reassign_to": id})
	if e != nil {
		t.Fatal(e)
	}
	var n int
	e = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM social_accounts a LEFT JOIN profiles p ON p.id=a.profile_id WHERE a.id=? AND a.profile_id>0 AND p.id IS NULL`, aid).Scan(&n)
	if e != nil {
		t.Fatal(e)
	}
	if n > 0 {
		t.Fatal("account still points to deleted profile")
	}
}

func TestAuditGlobalSchedulingPropagatesProject(t *testing.T) {
	ctx, pf, aid, _ := auditSeed(t, "facebook", "native", "draft")
	t.Setenv("APTEVA_PROJECT_ID", "")
	pf.identity.ProjectID = ""
	pf.identity.Bindings = map[string]any{"jobs": float64(123)}
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":123}}`)
	_, e := (&App{}).toolPostCreate(ctx, map[string]any{"project_id": "test-proj", "mode": "schedule", "body": "scheduled", "schedule_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "social_account_ids": []any{aid}})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range pf.callAppCalls {
		if c.AppName == "jobs" && c.Tool == "jobs_schedule" {
			if c.Input["_project_id"] != "test-proj" && c.Input["project_id"] != "test-proj" {
				t.Fatal("global HTTP scheduling sends no project scope to Jobs")
			}
			return
		}
	}
	t.Fatal("no Jobs call made")
}
