package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestConcurrentDraftPublicationDeliversOnce(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "draft")
	a := &App{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := a.toolPostDraftPublish(ctx, map[string]any{"post_id": pid, "expected_revision": 1, "mode": "publish"})
			if err != nil {
				t.Errorf("publish: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	var calls int
	for _, c := range pf.executeCalls {
		if c.Tool == "post_to_page" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("got %d provider deliveries: %+v", calls, pf.executeCalls)
	}
	var attempts int
	if err := ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE post_id=?`, pid).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}
func TestScheduleGenerationRejectsOldDueCallback(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "facebook", "native", "scheduled")
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET schedule_generation=2 WHERE id=?`, pid); err != nil {
		t.Fatal(err)
	}
	(&App{}).publishScheduledPost(ctx, "test-proj", pid, 1)
	if len(pf.executeCalls) != 0 {
		t.Fatal("obsolete callback delivered")
	}
}
func TestProviderPendingRecoveryRequiresConfirmation(t *testing.T) {
	ctx, pf, _, pid := auditSeed(t, "linkedin", "zernio", "publishing")
	pf.executeResponses["create_post"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"post":{"_id":"op","status":"publishing","platforms":[{"accountId":"za1","status":"publishing"}]}}`)}
	a := &App{}
	a.publishPostTargets(ctx, pid)
	if got := a.postStatus(ctx, pid); got != "publishing" {
		t.Fatalf("pending status=%s", got)
	}
	pf.executeResponses["get_post"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"post":{"_id":"op","status":"published","platforms":[{"accountId":"za1","status":"published","platformPostId":"remote-1"}]}}`)}
	a.recoverDeliveries(ctx)
	if got := a.postStatus(ctx, pid); got != "published" {
		t.Fatalf("confirmed status=%s", got)
	}
}
func TestInboxPaginationCheckpointAfterPersistence(t *testing.T) {
	ctx, pf, aid, _ := auditSeed(t, "twitter", "native", "draft")
	done := beginInboxSync(ctx, aid)
	defer done()
	pf.executeQueues["get_dm_events"] = []*sdk.ExecuteResult{
		{Success: true, Data: json.RawMessage(`{"data":[{"id":"first"}],"meta":{"next_token":"next"}}`)},
		{Success: true, Data: json.RawMessage(`{"data":[{"id":"second"}]}`)},
	}
	res, err := collectInboxPages(ctx, aid, 7, "get_dm_events", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonItems(res.Data, "data")) != 2 {
		t.Fatalf("lost page: %s", res.Data)
	}
	if got := pf.executeCalls[1].Input["pagination_token"]; got != "next" {
		t.Fatalf("cursor=%v", got)
	}
	if _, err = ctx.AppDB().Exec(`CREATE TRIGGER reject_inbox BEFORE INSERT ON inbox_items BEGIN SELECT RAISE(ABORT,'disk failure fixture'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: aid, Kind: "dm", ExternalID: "first"})
	if err == nil {
		t.Fatal("fixture did not reject insert")
	}
	if warning := finishInboxPages(ctx, aid, res); warning == "" {
		t.Fatal("write failure hidden")
	}
	var n int
	if err = ctx.AppDB().QueryRow(`SELECT count(*) FROM inbox_cursors WHERE kind LIKE 'pages:%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("cursor committed despite lost items")
	}
}
func TestOutboundInboxItemsAreRead(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "twitter", "native", "draft")
	id, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: aid, Kind: "dm", ExternalID: "outbound", Outbound: true})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err = ctx.AppDB().QueryRow(`SELECT status FROM inbox_items WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "read" {
		t.Fatalf("outbound=%s", status)
	}
}
func TestExternalAvatarPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "192.168.0.1", "fc00::1"} {
		if publicAvatarAddress(net.ParseIP(raw)) {
			t.Errorf("allowed %s", raw)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "https://user:password@example.com/a", "javascript:alert(1)"} {
		u, _ := url.Parse(raw)
		if validateAvatarURL(u) == nil {
			t.Errorf("allowed %s", raw)
		}
	}
	if got := redactedMediaURL("https://user:secret@example.com/a?token=private#secret"); got != "https://example.com/a" {
		t.Fatalf("redaction=%s", got)
	}
}

func TestZernioOAuthNonceStateAndReplay(t *testing.T) {
	ctx, pf, _, _ := auditSeed(t, "linkedin", "zernio", "draft")
	result, err := ctx.AppDB().Exec(`INSERT INTO pending_accounts(project_id,platform,integration_slug,connection_id,status,expires_at,provider_slug,provider_profile_id,provider_state,callback_nonce) VALUES ('test-proj','linkedin','zernio',7,'pending_oauth',datetime('now','+1 hour'),'zernio','profile','expected','nonce')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	row := &pendingRow{id: id, projectID: "test-proj", platform: "linkedin", connectionID: 7, providerProfileID: "profile", providerState: "expected"}
	a := &App{}
	for _, query := range []string{"callback_nonce=wrong&state=expected&code=code", "callback_nonce=nonce&state=wrong&code=code"} {
		if _, ok := a.completeZernioOAuth(ctx, httptest.NewRequest("GET", "/callback?"+query, nil), row); ok {
			t.Fatalf("accepted %s", query)
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("invalid callback reached provider")
	}
	req := httptest.NewRequest("GET", "/callback?callback_nonce=nonce&state=expected&code=code", nil)
	if _, ok := a.completeZernioOAuth(ctx, req, row); !ok {
		t.Fatal("valid callback rejected")
	}
	if _, ok := a.completeZernioOAuth(ctx, req, row); ok {
		t.Fatal("replay accepted")
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("provider calls=%d", len(pf.executeCalls))
	}
}
func TestTikTokCreatorPrivacyAndInteractionValidation(t *testing.T) {
	ctx, pf, _, _ := auditSeed(t, "tiktok", "native", "draft")
	pf.executeResponses["query_creator_info"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"privacy_level_options":["SELF_ONLY"],"comment_disabled":true,"max_video_post_duration_sec":60},"error":{"code":"ok"}}`)}
	j := publishJob{connID: 7, options: map[string]any{"privacy_level": "PUBLIC_TO_EVERYONE"}}
	if _, err := tikTokPostInfo(ctx, j); err == nil {
		t.Fatal("unavailable privacy accepted")
	}
	j.options["privacy_level"] = "SELF_ONLY"
	info, err := tikTokPostInfo(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if info["disable_comment"] != true {
		t.Fatal("creator comment restriction ignored")
	}
}

func TestInboxThreadPagesDoNotSkipEqualTimestampMessages(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "twitter", "native", "draft")
	var selected int64
	for i := 0; i < 7; i++ {
		id, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: aid, Kind: "dm", ExternalID: fmt.Sprint(i), ExternalPostID: "conversation", OccurredAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		selected = id
	}
	item, err := getInboxItem(ctx.AppDB(), "test-proj", selected)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	seen := map[int64]bool{}
	for {
		page, err := getInboxThreadPage(ctx.AppDB(), "test-proj", item, 3, before)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, message := range page {
			if seen[message.ID] {
				t.Fatal("duplicate page item")
			}
			seen[message.ID] = true
		}
		before = page[0].ID
	}
	if len(seen) != 7 {
		t.Fatalf("loaded %d of 7 messages", len(seen))
	}
}
func TestProviderResponseUsesMatchingAccountIdentity(t *testing.T) {
	raw := json.RawMessage(`{"post":{"status":"partial","platforms":[{"accountId":"other","status":"published","platformPostId":"wrong"},{"accountId":"wanted","status":"published","platformPostId":"correct"}]}}`)
	id, _, err := providerPublicationResult(raw, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if id != "correct" {
		t.Fatalf("identity=%s", id)
	}
	if _, _, err = providerPublicationResult(raw, "missing"); err == nil {
		t.Fatal("missing account confirmed")
	}
}
func TestTikTokUnverifiedReceiptRemainsPending(t *testing.T) {
	ctx, pf, _, _ := auditSeed(t, "tiktok", "native", "draft")
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: false, Status: 503}
	_, _, err := (&App{}).waitTikTokPublish(ctx, 7, "operation")
	var warning *publishedWarningError
	if !errors.As(err, &warning) || !warning.pending {
		t.Fatalf("unverified receipt must be pending: %v", err)
	}
}

func TestBreakdownQueryWithRetainedHistory(t *testing.T) {
	ctx, _, aid, _ := auditSeed(t, "youtube", "native", "draft")
	q := analyticsQuery{RangeDays: 28}
	_, err := ctx.AppDB().Exec(`WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<49999)
 INSERT INTO social_metric_points(project_id,social_account_id,platform,scope,metric,period,point_time,value,source,status,dimensions_json,dimensions_key)
 SELECT 'test-proj',?,'youtube','account','views',?,datetime('2024-01-01','+'||(x/100)||' minutes'),x,'youtube:breakdown:country','ok',json_object('country','country-'||(x%100)),'country-'||(x%100) FROM n`, aid, q.cacheKey())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for range 5 {
		groups := loadAccountBreakdowns(ctx, "test-proj", aid, q.cacheKey())
		if len(groups) != 1 || len(groups[0].Rows) != 100 {
			t.Fatalf("latest snapshot incorrect: groups=%d", len(groups))
		}
	}
	t.Logf("50,000 retained metric rows: mean breakdown query %s", time.Since(started)/5)
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT MAX(point_time) FROM social_metric_points WHERE project_id=? AND social_account_id=? AND scope='account' AND period=? AND source=? AND status='ok'`, "test-proj", aid, q.cacheKey(), "youtube:breakdown:country")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
	}
}
func TestNestedCommentReplyPagination(t *testing.T) {
	ctx, pf, aid, _ := auditSeed(t, "facebook", "native", "draft")
	pf.executeResponses["list_media_comments"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":[{"id":"parent","replies":{"data":[{"id":"one"}],"paging":{"next":"https://graph.example/next?after=c1"}}}]}`)}
	pf.executeQueues["get_comment"] = []*sdk.ExecuteResult{
		{Success: true, Data: json.RawMessage(`{"id":"parent","replies":{"data":[{"id":"one"}],"paging":{"next":"https://graph.example/next?after=c1"}}}`)},
		{Success: true, Data: json.RawMessage(`{"id":"parent","replies":{"data":[{"id":"two"}]}}`)},
	}
	res, err := collectInboxPages(ctx, aid, 7, "list_media_comments", map[string]any{"mediaId": "post", "fields": "id,message,replies", "access_token": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Data []struct {
			Replies struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"replies"`
		} `json:"data"`
	}
	if err = json.Unmarshal(res.Data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || len(out.Data[0].Replies.Data) != 2 {
		t.Fatalf("nested replies lost: %s", res.Data)
	}
	if got := fmt.Sprint(pf.executeCalls[2].Input["fields"]); !strings.Contains(got, "after(c1)") {
		t.Fatalf("reply cursor missing: %s", got)
	}
}
