package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type historyFixturePlatform struct {
	*recordingPlatform
	count        int
	overlap      bool
	owner        string
	nativeCalls  int
	failPage     string
	omitMetadata bool
	writes       int
}

func newHistoryFixture(n int) *historyFixturePlatform {
	return &historyFixturePlatform{recordingPlatform: newRecordingPlatform(), count: n, owner: "owner-1"}
}
func (p *historyFixturePlatform) ExecuteIntegrationTool(conn int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.nativeCalls++
	switch tool {
	case "get_my_channel":
		return historyFixtureResult(map[string]any{"items": []any{map[string]any{"id": p.owner, "contentDetails": map[string]any{"relatedPlaylists": map[string]any{"uploads": "uploads-" + p.owner}}}}}), nil
	case "list_playlist_items":
		token := toString(input["pageToken"])
		if p.failPage != "" && token == p.failPage {
			return nil, fmt.Errorf("transient fixture read failure")
		}
		start := 0
		if token != "" {
			start, _ = strconv.Atoi(token)
		}
		size := intArg(input, "maxResults", 50)
		end := start + size
		if end > p.count {
			end = p.count
		}
		items := []any{}
		for i := start; i < end; i++ {
			items = append(items, map[string]any{"contentDetails": map[string]any{"videoId": fmt.Sprintf("v-%03d", i)}})
		}
		raw := map[string]any{"items": items}
		if end < p.count {
			next := end
			if p.overlap {
				next -= 5
			}
			raw["nextPageToken"] = strconv.Itoa(next)
		}
		return historyFixtureResult(raw), nil
	case "get_video":
		items := []any{}
		for _, id := range strings.Split(toString(input["id"]), ",") {
			i, e := strconv.Atoi(strings.TrimPrefix(id, "v-"))
			if e != nil || i >= p.count {
				continue
			}
			visibility := "public"
			if i%3 == 0 {
				visibility = "private"
			} else if i%3 == 1 {
				visibility = "unlisted"
			}
			item := map[string]any{"id": id, "snippet": map[string]any{"channelId": p.owner, "title": "Native title " + id, "description": "Native body " + id, "publishedAt": "2018-01-01T10:00:00Z"}, "status": map[string]any{"privacyStatus": visibility, "uploadStatus": "processed"}, "contentDetails": map[string]any{"duration": "PT1M30S"}}
			if !p.omitMetadata {
				item["fileDetails"] = map[string]any{"fileName": "original_" + id + ".mp4", "fileSize": strconv.Itoa(i)}
				item["processingDetails"] = map[string]any{"processingStatus": "succeeded"}
			}
			items = append(items, item)
		}
		return historyFixtureResult(map[string]any{"items": items}), nil
	default:
		p.writes++
		return nil, fmt.Errorf("unexpected provider operation: %s", tool)
	}
}
func historyFixtureResult(data any) *sdk.ExecuteResult {
	raw, _ := json.Marshal(data)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
}
func historyAccountFixture(t *testing.T, ctx *sdk.AppCtx, owner string) int64 {
	t.Helper()
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status) VALUES(?, 'youtube',42,?,?,'active')`, projectScope(ctx), owner, owner)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := r.LastInsertId()
	return id
}
func historyStep(t *testing.T, ctx *sdk.AppCtx, args map[string]any) historyReceipt {
	t.Helper()
	out, e := (&App{}).toolAccountHistorySync(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	r, ok := out.(historyReceipt)
	if !ok {
		t.Fatalf("expected receipt: %#v", out)
	}
	if r.Status == "failed" {
		t.Fatalf("history failed: %+v", r)
	}
	return r
}
func finishHistory(t *testing.T, ctx *sdk.AppCtx, account int64) historyReceipt {
	t.Helper()
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 2}
	var r historyReceipt
	for step := 0; step < 30; step++ {
		r = historyStep(t, ctx, args)
		if r.Cursor == nil {
			return r
		}
		args["cursor"] = *r.Cursor
	}
	t.Fatal("history failed to finish within bounded fixture budget")
	return r
}
func TestHistoryYouTube299Reconciles28ManagedPostsWithoutPublishing(t *testing.T) {
	pf := newHistoryFixture(299)
	pf.overlap = true
	ctx := newSocialCtx(t, pf)
	ctx.AppDB().SetMaxOpenConns(1)
	account := historyAccountFixture(t, ctx, pf.owner)
	var original []string
	for i := 0; i < 28; i++ {
		id := fmt.Sprintf("v-%03d", i)
		created, e := (&App{}).insertImportedPostWithOptions(ctx, "test-proj", account, 0, "Managed original "+id, id, "https://youtube.com/watch?v="+id, "2020-01-01T00:00:00Z", nil, map[string]any{"title": "Managed title"})
		if e != nil || !created {
			t.Fatal(e)
		}
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET imported_at=NULL,revision=7,source='local' WHERE id=(SELECT post_id FROM post_targets WHERE social_account_id=? AND platform_post_id=?)`, account, id)
		_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET attempts=3,publish_operation_id='accepted-op',delivery_diagnostics='{"evidence":"keep"}' WHERE social_account_id=? AND platform_post_id=?`, account, id)
		original = append(original, id)
	}
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
	first := historyStep(t, ctx, args)
	if first.Cursor == nil {
		t.Fatal("missing continuation")
	}
	args["cursor"] = *first.Cursor
	second := historyStep(t, ctx, args)
	calls := pf.nativeCalls
	replay := historyStep(t, ctx, args)
	if !reflect.DeepEqual(second, replay) || pf.nativeCalls != calls {
		t.Fatal("replayed cursor advanced or refetched")
	}
	r := second
	for i := 0; r.Cursor != nil && i < 30; i++ {
		args["cursor"] = *r.Cursor
		r = historyStep(t, ctx, args)
	}
	if r.Status != "complete" || r.Coverage["complete"] != true || r.UniquePosts != 299 || r.Imported != 271 || r.Updated != 28 {
		t.Fatalf("bad reconciliation receipt: %+v", r)
	}
	var posts, targets int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM posts`).Scan(&posts)
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM post_targets`).Scan(&targets)
	if posts != 299 || targets != 299 {
		t.Fatalf("duplicates: %d/%d", posts, targets)
	}
	for _, id := range original {
		var body, operation, evidence string
		var rev, attempts int
		e := ctx.AppDB().QueryRow(`SELECT p.body,p.revision,t.attempts,t.publish_operation_id,t.delivery_diagnostics FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE t.social_account_id=? AND t.platform_post_id=?`, account, id).Scan(&body, &rev, &attempts, &operation, &evidence)
		if e != nil || body != "Managed original "+id || rev != 7 || attempts != 3 || operation != "accepted-op" || evidence != `{"evidence":"keep"}` {
			t.Fatalf("managed history changed: %s %v", id, e)
		}
	}
	again := finishHistory(t, ctx, account)
	if again.Imported != 0 || again.Updated != 0 || again.UniquePosts != 299 {
		t.Fatalf("not idempotent: %+v", again)
	}
	if pf.writes != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("history triggered provider writes or publishing jobs")
	}
	var schedules int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM posts WHERE status IN ('scheduled','publishing')`).Scan(&schedules)
	if schedules != 0 {
		t.Fatal("history created publishing work")
	}
}
func TestHistorySearchFindsOlderFilenameNativeIDAndAccountIsolation(t *testing.T) {
	pf := newHistoryFixture(299)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	finishHistory(t, ctx, account)
	for _, args := range []map[string]any{{"query": "original_v-298.mp4"}, {"query": "Native title v-298"}, {"query": "Native body v-298"}, {"native_post_id": "v-298"}} {
		args["social_account_id"] = account
		args["limit"] = 1
		args["compact"] = true
		out := reportList(t, ctx, args)
		if out["total"] != 1 || out["total_targets"] != 1 {
			t.Fatalf("search did not cover full history: %+v", out)
		}
		target := out["posts"].([]map[string]any)[0]["targets"].([]map[string]any)[0]
		if target["platform_post_id"] != "v-298" {
			t.Fatal(target)
		}
		if _, ok := target["delivery_diagnostics"]; ok {
			t.Fatal("compact result contains diagnostics")
		}
	}
	other := historyAccountFixture(t, ctx, "owner-2")
	out := reportList(t, ctx, map[string]any{"social_account_id": other, "native_post_id": "v-298"})
	if out["total"] != 0 {
		t.Fatal("other account leaked")
	}
	out = reportList(t, ctx, map[string]any{"social_account_id": account, "query": "%_\\' OR 1=1 --"})
	if out["total"] != 0 {
		t.Fatal("search is not literal/parameterized")
	}
	status, _ := (&App{}).toolAccountHistoryStatus(ctx, map[string]any{"social_account_id": account})
	if status.(map[string]any)["history"].(map[string]any)["complete"] != true {
		t.Fatal(status)
	}
}
func TestHistoryVisibilityUnknownMetadataAndInspection(t *testing.T) {
	pf := newHistoryFixture(3)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	finishHistory(t, ctx, account)
	for _, tc := range []struct{ id, visibility string }{{"v-000", "private"}, {"v-001", "unlisted"}} {
		out, e := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": tc.id})
		if e != nil {
			t.Fatal(e)
		}
		m := out.(map[string]any)
		n := m["native_metadata"].(nativePost)
		if valueOrEmpty(n.Visibility) != tc.visibility || n.Audience != nil || m["public_live_verified"] != false || n.PublishedAt != nil || n.UploadedAt == nil {
			t.Fatalf("invented audience/time: %+v", m)
		}
	}
	pf.omitMetadata = true
	out, e := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": "v-000"})
	if e != nil {
		t.Fatal(e)
	}
	n := out.(map[string]any)["native_metadata"].(nativePost)
	if n.OriginalFilename != nil || n.Bytes != nil || n.Audience != nil {
		t.Fatal("missing metadata became known")
	}
	rows := reportList(t, ctx, map[string]any{"social_account_id": account, "query": "original_v-000.mp4"})
	if rows["total"] != 1 {
		t.Fatal("last-known immutable filename was lost")
	}
	var raw string
	_ = ctx.AppDB().QueryRow(`SELECT metadata FROM social_native_posts WHERE social_account_id=? AND native_post_id='v-000'`, account).Scan(&raw)
	if !strings.Contains(raw, `"bytes":null`) || !strings.Contains(raw, `"original_filename":null`) {
		t.Fatal(raw)
	}
	pf.owner = "different-owner"
	result, _ := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": "v-000"})
	if result.(map[string]any)["status"] != "failed" {
		t.Fatal("cross-channel inspection accepted")
	}
}
func TestHistoryCursorRejectsProjectAccountAndConnectionMismatch(t *testing.T) {
	pf := newHistoryFixture(299)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	r := historyStep(t, ctx, map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1})
	if r.Cursor == nil {
		t.Fatal(r)
	}
	other := historyAccountFixture(t, ctx, pf.owner)
	for _, scope := range []struct {
		ctx *sdk.AppCtx
		id  int64
	}{{ctx, other}, {ctx.WithProject("foreign"), account}} {
		out, e := (&App{}).toolAccountHistorySync(scope.ctx, map[string]any{"social_account_id": scope.id, "mode": "full", "cursor": *r.Cursor})
		if e != nil {
			t.Fatal(e)
		}
		if out.(map[string]any)["isError"] != true {
			t.Fatal(out)
		}
	}
	_, _ = ctx.AppDB().Exec(`UPDATE social_accounts SET connection_id=43 WHERE id=?`, account)
	out, _ := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "mode": "full", "cursor": *r.Cursor})
	if out.(map[string]any)["isError"] != true {
		t.Fatal("reconnected cursor accepted")
	}
}
func TestHistoryFailedPageResumesAndDoesNotChangeAutomaticSettings(t *testing.T) {
	pf := newHistoryFixture(125)
	pf.failPage = "50"
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	_, _ = ctx.AppDB().Exec(`UPDATE social_accounts SET provider_import_mode='off' WHERE id=?`, account)
	out, e := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "mode": "full", "max_pages": 2})
	if e != nil {
		t.Fatal(e)
	}
	r := out.(historyReceipt)
	if r.Status != "failed" || r.Imported != 50 || r.Cursor == nil {
		t.Fatal(r)
	}
	pf.failPage = ""
	args := map[string]any{"social_account_id": account, "cursor": *r.Cursor, "mode": "full", "max_pages": 5}
	r = historyStep(t, ctx, args)
	if r.Status != "complete" || r.Imported != 125 {
		t.Fatal(r)
	}
	var mode string
	_ = ctx.AppDB().QueryRow(`SELECT provider_import_mode FROM social_accounts WHERE id=?`, account).Scan(&mode)
	if mode != "off" {
		t.Fatal("automatic settings changed")
	}
}

func TestHistoryPaginationAdaptersAndNoProviderSyncWrites(t *testing.T) {
	for _, platform := range []string{"facebook", "instagram", "twitter", "tiktok", "pinterest"} {
		t.Run(platform, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newSocialCtx(t, pf)
			ext := "owner"
			if platform == "pinterest" {
				ext = ""
			}
			r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,page_credentials,status) VALUES('test-proj',?,42,?,'Fixture','{"access_token":"secret"}','active')`, platform, ext)
			if e != nil {
				t.Fatal(e)
			}
			account, _ := r.LastInsertId()
			tool := ""
			var first, last *sdk.ExecuteResult
			switch platform {
			case "facebook":
				tool = "get_page_posts"
				first = analyticsOK(`{"data":[{"id":"owner_post","message":"body","created_time":"2020-01-01T00:00:00Z"}],"paging":{"next":"https://graph.facebook.com/feed?after=next&access_token=secret","cursors":{"after":"next"}}}`)
				last = analyticsOK(`{"data":[]}`)
			case "instagram":
				tool = "get_account_media"
				first = analyticsOK(`{"data":[{"id":"media","caption":"body","timestamp":"2020-01-01T00:00:00Z"}],"paging":{"next":"https://graph.facebook.com/media?after=next&access_token=secret","cursors":{"after":"next"}}}`)
				last = analyticsOK(`{"data":[]}`)
			case "twitter":
				tool = "get_user_tweets"
				pf.executeResponses["get_me"] = analyticsOK(`{"data":{"id":"owner"}}`)
				first = analyticsOK(`{"data":[{"id":"tweet","author_id":"owner","text":"body","created_at":"2020-01-01T00:00:00Z"}],"meta":{"next_token":"next"}}`)
				last = analyticsOK(`{"meta":{"result_count":0}}`)
			case "tiktok":
				tool = "list_videos"
				pf.executeResponses["get_user_info"] = analyticsOK(`{"data":{"user":{"open_id":"owner"}}}`)
				first = analyticsOK(`{"data":{"videos":[{"id":"video","title":"title","create_time":1600000000,"duration":0}],"has_more":true,"cursor":99}}`)
				last = analyticsOK(`{"data":{"videos":[],"has_more":false}}`)
			case "pinterest":
				tool = "list_pins"
				first = analyticsOK(`{"items":[{"id":"pin","title":"title","description":"body","created_at":"2020-01-01T00:00:00Z","board_id":"board"}],"bookmark":"next"}`)
				last = analyticsOK(`{"items":[]}`)
			}
			pf.executeQueues[tool] = []*sdk.ExecuteResult{first, last, first, last}
			receipt := finishHistory(t, ctx, account)
			if receipt.Status != "complete" || receipt.UniquePosts != 1 || receipt.Imported != 1 {
				t.Fatalf("%+v", receipt)
			}
			for _, call := range pf.executeCalls {
				if call.Tool != tool && call.Tool != "get_me" && call.Tool != "get_user_info" {
					t.Fatalf("unexpected operation: %s", call.Tool)
				}
				if _, ok := call.Input["continuation_url"]; ok {
					t.Fatal("provider signed URL escaped adapter")
				}
			}
			raw, _ := json.Marshal(receipt)
			if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "https://graph.facebook") {
				t.Fatal("provider credentials/URL leaked in receipt")
			}
		})
	}
}
func TestHistoryCycleMissingItemsAndProcessingCannotClaimPublication(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','pinterest',42,'Pins','active')`)
	account, _ := r.LastInsertId()
	pf.executeResponses["list_pins"] = analyticsOK(`{"items":[{"id":"a"}],"bookmark":"repeated"}`)
	receipt := finishHistory(t, ctx, account)
	if receipt.Status != "limited" || receipt.Coverage["complete"] != false {
		t.Fatal(receipt)
	}
	pf.executeResponses["list_pins"] = analyticsOK(`{}`)
	out, _ := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "mode": "full"})
	if out.(historyReceipt).Status != "failed" {
		t.Fatal("malformed response became complete")
	}
	b, e := loadHistoryAccount(ctx, account)
	if e != nil {
		t.Fatal(e)
	}
	state := "processing"
	_, post, _, e := (&App{}).upsertNativeHistory(ctx, b, nativePost{ID: "upload", ProcessingState: &state, ObservedAt: "2026-10-27T00:00:00Z"})
	if e != nil {
		t.Fatal(e)
	}
	var status string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, post).Scan(&status)
	if status != "imported" {
		t.Fatal("processing native upload became a successful publication")
	}
}
func TestHistoryProviderAccountIsolationAndSuppression(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status,provider_slug,provider_account_id) VALUES('test-proj','linkedin',42,'Provider','active','zernio','owned')`)
	if e != nil {
		t.Fatal(e)
	}
	account, _ := r.LastInsertId()
	pf.executeResponses["list_posts"] = analyticsOK(`{"posts":[{"id":"provider-post","content":"History","platforms":[{"accountId":"foreign","platform":"linkedin","platformPostId":"wrong"},{"accountId":"owned","platform":"linkedin","platformPostId":"urn:li:share:correct","publishedAt":"2020-01-01T00:00:00Z"}]}]}`)
	receipt := finishHistory(t, ctx, account)
	if receipt.UniquePosts != 1 || receipt.Imported != 1 || receipt.Coverage["scope"] != "provider_indexed_published_history" {
		t.Fatal(receipt)
	}
	for _, call := range pf.executeCalls {
		if call.Tool != "list_posts" || call.Input["accountId"] != "owned" {
			t.Fatal("provider full history triggered a mutation or changed account")
		}
	}
	_, _ = ctx.AppDB().Exec(`DELETE FROM posts WHERE id=(SELECT post_id FROM social_native_posts WHERE social_account_id=?)`, account)
	_, _ = ctx.AppDB().Exec(`INSERT INTO provider_import_tombstones(social_account_id,provider_slug,provider_post_id) VALUES(?,'zernio','provider-post')`, account)
	receipt = finishHistory(t, ctx, account)
	if receipt.Imported != 0 {
		t.Fatal("locally suppressed history was resurrected")
	}
}
func TestHistoryRecentFailureDoesNotClaimCurrent(t *testing.T) {
	pf := newHistoryFixture(2)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	finishHistory(t, ctx, account)
	pf.owner = "foreign-owner"
	out, _ := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "mode": "full"})
	if out.(historyReceipt).Status != "failed" {
		t.Fatal(out)
	}
	status, _ := (&App{}).toolAccountHistoryStatus(ctx, map[string]any{"social_account_id": account})
	if status.(map[string]any)["history"].(map[string]any)["current"] != false {
		t.Fatal("failed refresh was presented as current")
	}
}

func TestHistoryPageSizeBindingAndDisconnectedSearchPreservesEvidence(t *testing.T) {
	pf := newHistoryFixture(60)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	first := historyStep(t, ctx, map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1, "page_size": 25})
	if first.Cursor == nil {
		t.Fatal(first)
	}
	out, _ := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "mode": "full", "cursor": *first.Cursor, "page_size": 50})
	if out.(map[string]any)["isError"] != true {
		t.Fatal("page size change could skip provider pages")
	}
	r := historyStep(t, ctx, map[string]any{"social_account_id": account, "mode": "full", "cursor": *first.Cursor, "max_pages": 5})
	if r.UniquePosts != 60 || r.Status != "complete" {
		t.Fatal(r)
	}
	_, _ = ctx.AppDB().Exec(`UPDATE social_accounts SET status='disconnected' WHERE id=?`, account)
	result := reportList(t, ctx, map[string]any{"social_account_id": account, "native_post_id": "v-059"})
	if result["total"] != 1 {
		t.Fatal("disconnected stored history disappeared")
	}
	state := result["history"].(map[string]any)[fmt.Sprint(account)].(map[string]any)
	if state["complete"] != true || state["current"] != false {
		t.Fatal(state)
	}
}

func TestHistoryLegacyOwnerCannotChangeAcrossConnections(t *testing.T) {
	pf := newHistoryFixture(3)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, "")
	out, e := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": "v-000"})
	if e != nil || out.(map[string]any)["status"] != "ok" {
		t.Fatal(out, e)
	}
	_, _ = ctx.AppDB().Exec(`UPDATE social_accounts SET connection_id=99 WHERE id=?`, account)
	pf.owner = "different-owner"
	out, e = (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": "v-001"})
	if e != nil || out.(map[string]any)["status"] != "failed" {
		t.Fatal("a legacy Social account was silently repurposed", out, e)
	}
}

func TestHistoryHTTPPathOwnsAccountAndProject(t *testing.T) {
	pf := newHistoryFixture(2)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	request := httptest.NewRequest("POST", fmt.Sprintf("/accounts/%d/history-sync?project_id=test-proj", account), strings.NewReader(`{"mode":"full","max_pages":2,"social_account_id":999,"project_id":"foreign","connection_id":999}`))
	response := httptest.NewRecorder()
	(&App{}).handleAccountsItem(response, request)
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var receipt historyReceipt
	if json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.AccountID != account || receipt.Status != "complete" {
		t.Fatal(response.Body.String())
	}
}

func TestHistoryKnownProviderOperationGetsNativeIdentityWithoutDuplicating(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status,provider_slug,provider_account_id) VALUES('test-proj','linkedin',42,'Provider','active','zernio','owned')`)
	if e != nil {
		t.Fatal(e)
	}
	account, _ := r.LastInsertId()
	r, e = ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status,revision) VALUES('test-proj','Keep managed body','publishing',7)`)
	if e != nil {
		t.Fatal(e)
	}
	post, _ := r.LastInsertId()
	_, e = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,provider_post_id,publish_operation_id,attempts) VALUES(?,?,'publishing','provider-op','accepted-operation',3)`, post, account)
	if e != nil {
		t.Fatal(e)
	}
	pf.executeResponses["list_posts"] = analyticsOK(`{"posts":[{"id":"provider-op","content":"Native copy","platforms":[{"accountId":"owned","platform":"linkedin","platformPostId":"native","status":"published"}]}]}`)
	receipt := finishHistory(t, ctx, account)
	if receipt.Imported != 0 || receipt.Updated != 1 || receipt.UniquePosts != 1 {
		t.Fatal(receipt)
	}
	var count, rev, attempts int
	var body, status, operation, native string
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM posts`).Scan(&count)
	e = ctx.AppDB().QueryRow(`SELECT p.body,p.revision,t.status,t.attempts,t.publish_operation_id,t.platform_post_id FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE p.id=?`, post).Scan(&body, &rev, &status, &attempts, &operation, &native)
	if e != nil || count != 1 || body != "Keep managed body" || rev != 7 || status != "publishing" || attempts != 3 || operation != "accepted-operation" || native != "native" {
		t.Fatal("managed operation was replaced", e)
	}
}
