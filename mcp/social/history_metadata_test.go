package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type metadataHistoryFixture struct {
	basicOmissions int
	*historyFixturePlatform
	missing          map[string]bool
	retries          map[string]int
	permanent        bool
	resolveOnOverlap bool
	omitOnlyOverlap  bool
	listings         int
	failBasic        bool
	foreignBasic     bool
	incompleteItems  bool
}

func newMetadataHistoryFixture() *metadataHistoryFixture {
	return &metadataHistoryFixture{historyFixturePlatform: newHistoryFixture(127), missing: map[string]bool{"v-005": true, "v-126": true}, retries: map[string]int{}}
}
func (p *metadataHistoryFixture) ExecuteIntegrationTool(conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if conn != 42 {
		return nil, fmt.Errorf("unexpected account connection %d", conn)
	}
	if tool == "list_playlist_items" && toString(args["pageToken"]) == "" {
		p.listings++
	}
	if tool == "get_video" && !strings.Contains(toString(args["part"]), "fileDetails") {
		id := toString(args["id"])
		if !p.missing[id] || strings.Contains(id, ",") {
			return nil, fmt.Errorf("metadata retry was not narrowly scoped: %s", id)
		}
		p.retries[id]++
		if p.failBasic {
			return &sdk.ExecuteResult{Success: false, Status: 401, Data: json.RawMessage(`{"error":"unauthorized"}`)}, nil
		}
	}
	res, err := p.historyFixturePlatform.ExecuteIntegrationTool(conn, tool, args)
	if err != nil || res == nil || tool != "get_video" && tool != "list_playlist_items" {
		return res, err
	}
	var raw map[string]any
	json.Unmarshal(res.Data, &raw)
	if tool == "list_playlist_items" {
		raw["pageInfo"] = map[string]any{"totalResults": p.count}
		return historyFixtureResult(raw), nil
	}
	basic := !strings.Contains(toString(args["part"]), "fileDetails")
	items := []any{}
	for _, value := range raw["items"].([]any) {
		item := value.(map[string]any)
		id := historyString(item, "id")
		if p.missing[id] && (p.permanent || basic && p.retries[id] <= p.basicOmissions || !basic && (p.omitOnlyOverlap && p.listings > 1 || !p.omitOnlyOverlap && !(p.resolveOnOverlap && p.listings > 1))) {
			if p.incompleteItems {
				items = append(items, map[string]any{"id": id})
			}
			continue
		}
		if basic {
			delete(item, "fileDetails")
			delete(item, "processingDetails")
			if p.foreignBasic {
				item["snippet"].(map[string]any)["channelId"] = "foreign-owner"
			}
		}
		items = append(items, item)
	}
	raw["items"] = items
	return historyFixtureResult(raw), nil
}
func storedMetadataRun(t *testing.T, ctx *sdk.AppCtx, id string) historyRun {
	t.Helper()
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT state FROM social_history_runs WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var run historyRun
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		t.Fatal(err)
	}
	return run
}
func TestHistory127MissingMetadataAutomaticallyResolvesAcrossRestart(t *testing.T) {
	pf := newMetadataHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	if _, err := (&App{}).insertImportedPostWithOptions(ctx, "test-proj", account, 0, "Approved managed body", "v-126", "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET imported_at=NULL,source='local',revision=7`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET attempts=3,publish_operation_id='accepted-op',delivery_diagnostics='{"keep":true}'`); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
	var result historyReceipt
	restarted := false
	for i := 0; i < 20; i++ {
		result = historyStep(t, ctx, args)
		if result.Cursor == nil {
			break
		}
		if len(pf.retries) > 0 && !restarted {
			path := filepath.Join(t.TempDir(), "history-restart.sqlite")
			if _, err := ctx.AppDB().Exec(`VACUUM INTO ?`, path); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(on)")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			ctx = sdk.NewAppCtxForTest(ctx.Manifest(), db, sdk.Config{}, pf, nil).WithProject("test-proj")
			restarted = true
		}
		// Initial calls without a cursor start a new run; only step cursors replay.
		if args["cursor"] != nil {
			calls := pf.nativeCalls
			replay := historyStep(t, ctx, args)
			if !reflect.DeepEqual(replay, result) || pf.nativeCalls != calls {
				t.Fatal("replay advanced history", replay, result)
			}
		}
		args["cursor"] = *result.Cursor
	}
	if !restarted || result.Status != "complete" || result.UniquePosts != 127 || result.Imported != 126 || result.Coverage["complete"] != true || result.Coverage["all_listed_ids_resolved"] != true {
		t.Fatalf("metadata did not automatically reconcile: %+v", result)
	}
	m := result.Coverage["metadata"].(map[string]any)
	if m["unresolved_posts"] != 0 || m["retry_readbacks"] != 2 || pf.retries["v-005"] != 1 || pf.retries["v-126"] != 1 {
		t.Fatal("incorrect bounded retries", m, pf.retries)
	}
	var body, operation, diagnostics string
	var revision, attempts int
	if err := ctx.AppDB().QueryRow(`SELECT p.body,p.revision,t.attempts,t.publish_operation_id,t.delivery_diagnostics FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE t.platform_post_id='v-126'`).Scan(&body, &revision, &attempts, &operation, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if body != "Approved managed body" || revision != 7 || attempts != 3 || operation != "accepted-op" || diagnostics != `{"keep":true}` {
		t.Fatal("managed delivery changed", body, revision, attempts, operation, diagnostics)
	}
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT metadata FROM social_native_posts WHERE native_post_id='v-126'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var n nativePost
	json.Unmarshal([]byte(raw), &n)
	if n.MetadataUnavailable || n.Title == nil || n.OriginalFilename != nil || n.Bytes != nil || valueOrEmpty(n.Visibility) != "private" || n.Audience != nil || n.PublishedAt != nil || n.UploadedAt == nil {
		t.Fatal("basic metadata semantics changed", raw)
	}
	other := historyAccountFixture(t, ctx, "another-owner")
	out := reportList(t, ctx, map[string]any{"social_account_id": other, "native_post_id": "v-126"})
	if out["total"] != 0 {
		t.Fatal("account metadata leaked", out)
	}
	again := finishHistory(t, ctx, account)
	if again.UniquePosts != 127 || again.Imported != 0 || again.Coverage["complete"] != true {
		t.Fatal("repeated sync duplicated history", again)
	}
	var posts, targets, jobs int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts`).Scan(&posts)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM post_targets`).Scan(&targets)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts WHERE status IN ('scheduled','publishing')`).Scan(&jobs)
	if posts != 127 || targets != 127 || jobs != 0 || pf.writes != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("history created duplicates or publishing work", posts, targets, jobs)
	}
}
func TestHistoryMetadataResolvedByOverlapDoesNotStayIncomplete(t *testing.T) {
	pf := newMetadataHistoryFixture()
	pf.resolveOnOverlap = true
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	result := finishHistory(t, ctx, account)
	if result.Status != "complete" || result.Coverage["complete"] != true || len(pf.retries) != 0 {
		t.Fatal("one omission permanently poisoned coverage", result, pf.retries)
	}
}
func TestHistoryPermanentlyMissingMetadataStaysLimited(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			pf := newMetadataHistoryFixture()
			pf.permanent = true
			pf.incompleteItems = incomplete
			ctx := newSocialCtx(t, pf)
			account := historyAccountFixture(t, ctx, pf.owner)
			result := finishHistory(t, ctx, account)
			m := result.Coverage["metadata"].(map[string]any)
			if result.Status != "limited" || result.UniquePosts != 127 || result.Coverage["count_verified"] != true || result.Coverage["complete"] != false || result.Coverage["all_listed_ids_resolved"] != false || m["unresolved_posts"] != 2 || m["retry_readbacks"] != 4 || result.Cursor != nil || len(result.Limitations) == 0 {
				t.Fatal("omitted metadata declared complete", result)
			}
			var raw string
			ctx.AppDB().QueryRow(`SELECT metadata FROM social_native_posts WHERE native_post_id='v-126'`).Scan(&raw)
			var n nativePost
			json.Unmarshal([]byte(raw), &n)
			if !n.MetadataUnavailable || n.Visibility != nil || n.PublishedAt != nil || n.ProcessingState != nil || n.Audience != nil {
				t.Fatal("unavailable record invented facts", raw)
			}
		})
	}
}
func TestHistoryMetadataAuthAndOwnerFailuresRemainFailures(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			pf := newMetadataHistoryFixture()
			ctx := newSocialCtx(t, pf)
			account := historyAccountFixture(t, ctx, pf.owner)
			args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
			var r historyReceipt
			for i := 0; i < 10; i++ {
				r = historyStep(t, ctx, args)
				if storedMetadataRun(t, ctx, r.RunID).Phase == "metadata" {
					break
				}
				args["cursor"] = *r.Cursor
			}
			pf.foreignBasic = foreign
			pf.failBasic = !foreign
			args["cursor"] = *r.Cursor
			out, err := (&App{}).toolAccountHistorySync(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			failed := out.(historyReceipt)
			if failed.Status != "failed" || failed.Coverage["complete"] != false || failed.Cursor == nil || failed.Error == "" {
				t.Fatal("auth/owner failure was hidden", failed)
			}
			var raw string
			ctx.AppDB().QueryRow(`SELECT metadata FROM social_native_posts WHERE native_post_id='v-005'`).Scan(&raw)
			if strings.Contains(raw, "foreign-owner") {
				t.Fatal("unverified metadata persisted", raw)
			}
			// The failure remains resumable after the same connection is repaired.
			pf.foreignBasic = false
			pf.failBasic = false
			args["cursor"] = *failed.Cursor
			r = historyStep(t, ctx, args)
			for i := 0; r.Cursor != nil && i < 10; i++ {
				args["cursor"] = *r.Cursor
				r = historyStep(t, ctx, args)
			}
			if r.Status != "complete" || r.Coverage["complete"] != true {
				t.Fatal("read failure could not resume", r)
			}
		})
	}
}
func TestHistoryMetadataBudgetExhaustionRemainsLimited(t *testing.T) {
	pf := newMetadataHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
	var r historyReceipt
	for i := 0; i < 10; i++ {
		r = historyStep(t, ctx, args)
		if storedMetadataRun(t, ctx, r.RunID).Phase == "metadata" {
			break
		}
		args["cursor"] = *r.Cursor
	}
	// Persist consumed readback budget to simulate a restart after prior attempts.
	if _, err := ctx.AppDB().Exec(`UPDATE social_history_seen SET metadata_attempts=1 WHERE run_id=? AND native_post_id<='v-100' AND metadata_missing=0`, r.RunID); err != nil {
		t.Fatal(err)
	}
	args["cursor"] = *r.Cursor
	limited := historyStep(t, ctx, args)
	if limited.Status != "limited" || limited.Cursor != nil || limited.Coverage["complete"] != false || len(pf.retries) != 0 {
		t.Fatal("budget reset or unresolved coverage completed", limited)
	}
}
func TestPostInspectRetriesOmittedYouTubeMetadataWithoutPublishing(t *testing.T) {
	pf := newMetadataHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	out, err := (&App{}).toolPostNativeInspect(ctx, map[string]any{"social_account_id": account, "native_post_id": "v-126"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	n := result["native_metadata"].(nativePost)
	if n.MetadataUnavailable || n.Title == nil || pf.retries["v-126"] != 1 || pf.writes != 0 || n.PublishedAt != nil || n.Audience != nil {
		t.Fatal("single inspection did not resolve safe metadata", result)
	}
}

func TestHistoryLaterBatchOmissionPreservesVerifiedRunMetadata(t *testing.T) {
	pf := newMetadataHistoryFixture()
	pf.omitOnlyOverlap = true
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	result := finishHistory(t, ctx, account)
	if result.Status != "complete" || result.Coverage["complete"] != true || len(pf.retries) != 0 {
		t.Fatal("later omission erased verified evidence", result)
	}
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT metadata FROM social_native_posts WHERE native_post_id='v-005'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var n nativePost
	json.Unmarshal([]byte(raw), &n)
	if n.MetadataUnavailable || n.Title == nil || n.OriginalFilename == nil {
		t.Fatal("verified metadata was replaced by ID-only placeholder", raw)
	}
}

func TestHistoryMetadataSecondBoundedReadCanResolveTransientOmission(t *testing.T) {
	pf := newMetadataHistoryFixture()
	pf.missing = map[string]bool{"v-126": true}
	pf.basicOmissions = 1
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	result := finishHistory(t, ctx, account)
	if result.Status != "complete" || result.Coverage["complete"] != true || pf.retries["v-126"] != 2 {
		t.Fatal("transient metadata omission stayed incomplete", result, pf.retries)
	}
}
