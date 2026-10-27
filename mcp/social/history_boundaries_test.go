package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type historyPageRequest struct {
	Size  int
	Token string
}
type boundaryHistoryFixture struct {
	*historyFixturePlatform
	requests               []historyPageRequest
	sameGap                bool
	changingTotal          bool
	failReconciliationOnce bool
}

func newBoundaryHistoryFixture() *boundaryHistoryFixture {
	return &boundaryHistoryFixture{historyFixturePlatform: newHistoryFixture(300)}
}
func (p *boundaryHistoryFixture) ExecuteIntegrationTool(conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "list_playlist_items" {
		return p.historyFixturePlatform.ExecuteIntegrationTool(conn, tool, args)
	}
	p.nativeCalls++
	size := intArg(args, "maxResults", 50)
	token := toString(args["pageToken"])
	p.requests = append(p.requests, historyPageRequest{size, token})
	start := 0
	if token != "" {
		parts := strings.Split(token, ":")
		if len(parts) != 2 || parts[0] != strconv.Itoa(size) {
			return nil, fmt.Errorf("provider cursor reused with different page size")
		}
		var err error
		start, err = strconv.Atoi(parts[1])
		if err != nil {
			return nil, err
		}
	}
	if p.failReconciliationOnce && size == 49 && token != "" {
		p.failReconciliationOnce = false
		return nil, fmt.Errorf("interrupted reconciliation read")
	}
	// Missing IDs depend only on page size, never the traversal count. Even an
	// unlimited number of size-50 traversals cannot recover these two IDs.
	omitted := 298
	if size == 49 && !p.sameGap {
		omitted = 296
	}
	ids := []string{}
	for i := 0; i < 300; i++ {
		if i == omitted || i == omitted+1 {
			continue
		}
		ids = append(ids, fmt.Sprintf("v-%03d", i))
	}
	end := min(start+size, len(ids))
	items := []any{}
	for _, id := range ids[start:end] {
		items = append(items, map[string]any{"contentDetails": map[string]any{"videoId": id}})
	}
	total := 300
	if p.changingTotal && size == 49 {
		total = 301
	}
	raw := map[string]any{"items": items, "pageInfo": map[string]any{"totalResults": total}}
	if end < len(ids) {
		raw["nextPageToken"] = fmt.Sprintf("%d:%d", size, end-2)
	} // Deliberate overlapping page boundaries.
	return historyFixtureResult(raw), nil
}

func managedHistorySnapshot(t *testing.T, ctx *sdk.AppCtx, account int64) []string {
	t.Helper()
	rows, err := ctx.AppDB().Query(`SELECT json_object('id',p.id,'body',p.body,'revision',p.revision,'source',p.source,'status',p.status,'approved_revision',p.approved_revision,'published_at',p.published_at,'target_id',t.id,'target_status',t.status,'attempts',t.attempts,'operation',t.publish_operation_id,'evidence',t.delivery_diagnostics) FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE t.social_account_id=? AND p.source='local' ORDER BY p.id`, account)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHistoryPageSizeBoundariesAutomaticallyReconcile300(t *testing.T) {
	pf := newBoundaryHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	for i := 0; i < 28; i++ {
		id := fmt.Sprintf("v-%03d", i)
		if _, err := (&App{}).insertImportedPostWithOptions(ctx, "test-proj", account, 0, "Managed original "+id, id, "https://youtube.com/watch?v="+id, "2020-01-01T00:00:00Z", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx.AppDB().Exec(`UPDATE posts SET source='local',imported_at=NULL,revision=7 WHERE project_id='test-proj'`)
	ctx.AppDB().Exec(`UPDATE post_targets SET attempts=3,publish_operation_id='accepted-managed-operation',delivery_diagnostics='{"evidence":"keep"}' WHERE social_account_id=?`, account)
	before := managedHistorySnapshot(t, ctx, account)
	result := finishHistory(t, ctx, account)
	reconciliation := result.Coverage["reconciliation"].(map[string]any)
	if result.Coverage["complete"] != true || result.UniquePosts != 300 || result.Imported != 272 || result.Updated != 28 || result.Coverage["traversal_unique_posts"] != 298 || reconciliation["unique_posts"] != 298 || result.Coverage["reconciled_unique_posts"] != 300 || *result.Coverage["unexplained_count_gap"].(*int64) != 0 {
		t.Fatalf("page-size-dependent gap was not reconciled: %+v", result)
	}
	if result.Coverage["traversal_page_size"] != 50 || reconciliation["page_size"] != 49 || result.Coverage["duplicate_entries"].(int) == 0 {
		t.Fatal("page boundaries or overlap evidence lost", result)
	}
	starts := []int{}
	for _, request := range pf.requests {
		if request.Token == "" {
			starts = append(starts, request.Size)
		}
	}
	if !reflect.DeepEqual(starts, []int{50, 49}) {
		t.Fatal("pagination was not restarted at changed boundaries", starts)
	}
	if after := managedHistorySnapshot(t, ctx, account); !reflect.DeepEqual(before, after) {
		t.Fatal("managed records were changed", before, after)
	}
	repeated := finishHistory(t, ctx, account)
	if repeated.Imported != 0 || repeated.Updated != 0 || repeated.UniquePosts != 300 || repeated.Coverage["traversal_unique_posts"] != 298 || repeated.Coverage["reconciliation"].(map[string]any)["unique_posts"] != 298 || repeated.Coverage["complete"] != true {
		t.Fatal("repeated sync was not idempotent", repeated)
	}
	var posts, targets, jobs int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts`).Scan(&posts)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM post_targets`).Scan(&targets)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts WHERE status IN ('scheduled','publishing')`).Scan(&jobs)
	if posts != 300 || targets != 300 || jobs != 0 || pf.writes != 0 || len(pf.callAppCalls) != 0 || len(pf.startOAuthCalls) != 0 || len(pf.disconnectCalls) != 0 {
		t.Fatal("history caused duplicates, jobs, uploads or account operations", posts, targets, jobs)
	}
	if !reflect.DeepEqual(before, managedHistorySnapshot(t, ctx, account)) {
		t.Fatal("repeat changed managed records")
	}
}

func TestHistoryChangedBoundariesPersistThroughFailureResumeAndReplay(t *testing.T) {
	pf := newBoundaryHistoryFixture()
	pf.failReconciliationOnce = true
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	args := map[string]any{"social_account_id": account, "mode": "full", "page_size": 50, "max_pages": 1}
	var result historyReceipt
	for step := 0; step < 20; step++ {
		out, err := (&App{}).toolAccountHistorySync(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		result = out.(historyReceipt)
		if result.Status == "failed" {
			break
		}
		if result.Cursor == nil {
			t.Fatal("fixture never interrupted")
		}
		args["cursor"] = *result.Cursor
	}
	if result.Status != "failed" || result.Cursor == nil {
		t.Fatal("missing resumable failure", result)
	}
	var state string
	var run historyRun
	if err := ctx.AppDB().QueryRow(`SELECT state FROM social_history_runs WHERE id=?`, result.RunID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(state), &run); err != nil {
		t.Fatal(err)
	}
	if run.TraversalPageSize != 50 || run.ReconciliationPageSize != 49 || !strings.HasPrefix(run.Token, "49:") || run.Counts.TraversalUnique != 298 || run.Counts.ReconciledUnique < 298 {
		t.Fatal("reconciliation state was not persisted", run)
	}
	// Replaying the request preceding the failure returns its durable receipt and
	// performs no provider read. A new App value resumes the saved cursor/size.
	calls := pf.nativeCalls
	replay, err := (&App{}).toolAccountHistorySync(ctx, args)
	if err != nil || !reflect.DeepEqual(replay, result) || pf.nativeCalls != calls {
		t.Fatal("replayed step advanced after failure", replay, err)
	}
	args["cursor"] = *result.Cursor
	badResume := map[string]any{"social_account_id": account, "mode": "full", "cursor": *result.Cursor, "page_size": 49}
	invalid, err := (&App{}).toolAccountHistorySync(ctx, badResume)
	if err != nil {
		t.Fatal(err)
	}
	assertDestinationError(t, invalid, "cursor page_size differs")
	if pf.nativeCalls != calls {
		t.Fatal("changed client page_size issued a provider request")
	}
	previous := pf.requests[len(pf.requests)-1]
	for step := 0; step < 20; step++ {
		input := map[string]any{}
		for k, v := range args {
			input[k] = v
		}
		out, err := (&App{}).toolAccountHistorySync(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		next := out.(historyReceipt)
		calls = pf.nativeCalls
		again, err := (&App{}).toolAccountHistorySync(ctx, input)
		if err != nil || !reflect.DeepEqual(again, next) || calls != pf.nativeCalls {
			t.Fatal("cursor replay inflated counts or repeated reads")
		}
		result = next
		if result.Cursor == nil {
			break
		}
		args["cursor"] = *result.Cursor
	}
	foundRetry := false
	for i := 1; i < len(pf.requests); i++ {
		if pf.requests[i-1] == previous && pf.requests[i] == previous {
			foundRetry = true
		}
	}
	if !foundRetry || result.Coverage["complete"] != true || result.UniquePosts != 300 || result.Coverage["traversal_unique_posts"] != 298 || result.Coverage["reconciliation"].(map[string]any)["unique_posts"] != 298 {
		t.Fatal("resume changed provider boundaries or coverage", result, pf.requests)
	}
	if pf.writes != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("resume created publishing work")
	}
}

func TestHistoryAlternateBoundariesKeepUnresolvedGapsIncomplete(t *testing.T) {
	for _, changing := range []bool{false, true} {
		t.Run(fmt.Sprint(changing), func(t *testing.T) {
			pf := newBoundaryHistoryFixture()
			pf.sameGap = !changing
			pf.changingTotal = changing
			ctx := newSocialCtx(t, pf)
			account := historyAccountFixture(t, ctx, pf.owner)
			result := finishHistory(t, ctx, account)
			if result.Coverage["complete"] != false || result.Status != "limited" || len(result.Limitations) == 0 || result.Coverage["reconciliation"].(map[string]any)["page_size"] != 49 {
				t.Fatal("unresolved gap declared complete", result)
			}
			if !changing && (result.UniquePosts != 298 || *result.Coverage["unexplained_count_gap"].(*int64) != 2) {
				t.Fatal("same missing IDs lost", result)
			}
			if changing && result.Coverage["provider_totals_changed"] != true {
				t.Fatal("changing totals ignored", result)
			}
		})
	}
}

func TestHistoryAlternatePageSizesRespectProviderBounds(t *testing.T) {
	for _, tc := range []struct {
		platform                 string
		requested, first, second int
	}{{"youtube", 50, 50, 49}, {"youtube", 1, 1, 2}, {"tiktok", 50, 20, 19}, {"twitter", 1, 5, 6}, {"twitter", 5, 5, 6}, {"pinterest", 50, 50, 49}} {
		first, second := historyPageSizes(historyAccount{destinationAccount: destinationAccount{Platform: tc.platform, Provider: "native"}}, tc.requested)
		if first != tc.first || second != tc.second {
			t.Fatal(tc, first, second)
		}
	}
}

func TestHistoryAlternateBoundaryBudgetRemainsIncomplete(t *testing.T) {
	pf := newBoundaryHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
	var result historyReceipt
	for step := 0; step < 20; step++ {
		result = historyStep(t, ctx, args)
		if result.Cursor == nil {
			t.Fatal("run completed before a reconciliation budget could be exercised")
		}
		args["cursor"] = *result.Cursor
		if result.Coverage["traversal_complete"] == true {
			break
		}
	}
	var raw string
	var run historyRun
	if err := ctx.AppDB().QueryRow(`SELECT state FROM social_history_runs WHERE id=?`, result.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		t.Fatal(err)
	}
	if run.Phase != "overlap" || run.Token != "" || run.ReconciliationPageSize != 49 {
		t.Fatal("not at fresh reconciliation boundary", run)
	}
	run.OverlapPages = historyReconciliationPageLimit - 1
	saved, _ := json.Marshal(run)
	if _, err := ctx.AppDB().Exec(`UPDATE social_history_runs SET state=? WHERE id=?`, string(saved), run.ID); err != nil {
		t.Fatal(err)
	}
	limited := historyStep(t, ctx, args)
	reconciliation := limited.Coverage["reconciliation"].(map[string]any)
	if limited.Coverage["complete"] != false || limited.Status != "limited" || limited.Cursor != nil || reconciliation["pages"] != historyReconciliationPageLimit || reconciliation["pagination_exhausted"] != false || reconciliation["page_size"] != 49 || len(limited.Limitations) == 0 {
		t.Fatal("alternate boundary bypassed reconciliation budget", limited)
	}
}

func TestHistoryPreviousVersionCursorCannotReuseOldBoundaryToken(t *testing.T) {
	pf := newBoundaryHistoryFixture()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	binding, err := loadHistoryAccount(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	old := historyRun{CoverageVersion: 1, ID: randomDeliveryID(), PageSize: 50, Mode: "full", Status: "partial", Phase: "overlap", Token: "50:48"}
	raw, _ := json.Marshal(old)
	if _, err = ctx.AppDB().Exec(`INSERT INTO social_history_runs(id,project_id,social_account_id,binding,state) VALUES(?,?,?,?,?)`, old.ID, "test-proj", account, binding.binding(), string(raw)); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "cursor": historyCursor(old), "mode": "full"})
	if err != nil {
		t.Fatal(err)
	}
	assertDestinationError(t, out, "cursor predates page-boundary")
	if pf.nativeCalls != 0 {
		t.Fatal("old-size token reached provider")
	}
}
