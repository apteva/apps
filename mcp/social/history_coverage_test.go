package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type shiftingHistoryFixture struct {
	*historyFixturePlatform
	traversals    int
	permanentGap  bool
	changingTotal bool
}

func (p *shiftingHistoryFixture) ExecuteIntegrationTool(conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "list_playlist_items" {
		return p.historyFixturePlatform.ExecuteIntegrationTool(conn, tool, args)
	}
	p.nativeCalls++
	token := toString(args["pageToken"])
	if token == "" {
		p.traversals++
	}
	ids := []string{}
	missingStart := 296
	if !p.permanentGap && p.traversals%2 == 0 {
		missingStart = 293
	}
	for i := 0; i < 299; i++ {
		if i >= missingStart && i < missingStart+3 {
			continue
		}
		ids = append(ids, fmt.Sprintf("v-%03d", i))
	}
	start, _ := strconv.Atoi(token)
	end := min(start+intArg(args, "maxResults", 50), len(ids))
	items := []any{}
	for _, id := range ids[start:end] {
		items = append(items, map[string]any{"contentDetails": map[string]any{"videoId": id}})
	}
	total := 299
	if p.changingTotal && p.traversals%2 == 0 {
		total = 300
	}
	raw := map[string]any{"items": items, "pageInfo": map[string]any{"totalResults": total}}
	if end < len(ids) {
		raw["nextPageToken"] = strconv.Itoa(end)
	}
	return historyFixtureResult(raw), nil
}

func TestHistoryTwo296TraversalsReconcile299WithoutPublishing(t *testing.T) {
	pf := &shiftingHistoryFixture{historyFixturePlatform: newHistoryFixture(299)}
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	for i := 0; i < 28; i++ {
		id := fmt.Sprintf("v-%03d", i)
		if _, err := (&App{}).insertImportedPostWithOptions(ctx, "test-proj", account, 0, "Managed body", id, "https://youtube.com/watch?v="+id, "2020-01-01T00:00:00Z", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	result := finishHistory(t, ctx, account)
	if result.Status != "complete" || result.UniquePosts != 299 || result.Imported != 271 || result.Updated != 28 || result.Coverage["complete"] != true || result.Coverage["traversal_unique_posts"] != 296 || result.Coverage["reconciled_unique_posts"] != 299 || result.Coverage["count_verified"] != true {
		t.Fatalf("incorrect union coverage: %+v", result)
	}
	reconciliation := result.Coverage["reconciliation"].(map[string]any)
	if reconciliation["unique_posts"] != 296 || reconciliation["added_posts"] != 3 || reconciliation["pagination_exhausted"] != true || reconciliation["pages"] != 7 {
		t.Fatal(reconciliation)
	}
	total := result.Coverage["provider_total"].(*int64)
	if *total != 299 || pf.traversals != 2 {
		t.Fatal("provider total or bounded second traversal lost", result)
	}
	again := finishHistory(t, ctx, account)
	if again.Imported != 0 || again.Updated != 0 || again.UniquePosts != 299 || again.Coverage["complete"] != true || pf.traversals != 4 {
		t.Fatal("repeat was not idempotent", again)
	}
	var posts, targets, jobs int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts`).Scan(&posts)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM post_targets`).Scan(&targets)
	ctx.AppDB().QueryRow(`SELECT count(*) FROM posts WHERE status IN ('scheduled','publishing')`).Scan(&jobs)
	if posts != 299 || targets != 299 || jobs != 0 || pf.writes != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("duplicates or publishing work", posts, targets, jobs)
	}
	out, err := (&App{}).toolAccountHistoryStatus(ctx, map[string]any{"social_account_id": account})
	if err != nil || out.(map[string]any)["history"].(map[string]any)["complete"] != true {
		t.Fatal(out, err)
	}
}

func TestHistoryUnexplainedGapAndChangingTotalsStayIncomplete(t *testing.T) {
	for _, changing := range []bool{false, true} {
		t.Run(fmt.Sprint(changing), func(t *testing.T) {
			pf := &shiftingHistoryFixture{historyFixturePlatform: newHistoryFixture(299), permanentGap: !changing, changingTotal: changing}
			ctx := newSocialCtx(t, pf)
			account := historyAccountFixture(t, ctx, pf.owner)
			result := finishHistory(t, ctx, account)
			if result.Status != "limited" || result.Coverage["complete"] != false || len(result.Limitations) == 0 || pf.traversals != 2 {
				t.Fatal("unexplained gap declared complete", result)
			}
			if changing && (result.Coverage["provider_totals_changed"] != true || len(result.Coverage["provider_totals"].([]historyProviderTotal)) != 2) {
				t.Fatal("changing provider totals discarded", result)
			}
			out, err := (&App{}).toolAccountHistoryStatus(ctx, map[string]any{"social_account_id": account})
			if err != nil {
				t.Fatal(err)
			}
			if out.(map[string]any)["history"].(map[string]any)["complete"] != false {
				t.Fatal("stored status claimed complete", out)
			}
		})
	}
}

func TestHistoryReconciliationBudgetCannotClaimComplete(t *testing.T) {
	pf := &shiftingHistoryFixture{historyFixturePlatform: newHistoryFixture(299), permanentGap: true}
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	args := map[string]any{"social_account_id": account, "mode": "full", "max_pages": 1}
	var receipt historyReceipt
	for page := 0; page < 6; page++ {
		receipt = historyStep(t, ctx, args)
		args["cursor"] = *receipt.Cursor
	}
	var raw string
	ctx.AppDB().QueryRow(`SELECT state FROM social_history_runs WHERE id=?`, receipt.RunID).Scan(&raw)
	var run historyRun
	if err := json.Unmarshal([]byte(raw), &run); err != nil {
		t.Fatal(err)
	}
	run.OverlapPages = historyReconciliationPageLimit - 1
	data, _ := json.Marshal(run)
	ctx.AppDB().Exec(`UPDATE social_history_runs SET state=? WHERE id=?`, string(data), run.ID)
	limited := historyStep(t, ctx, args)
	reconciliation := limited.Coverage["reconciliation"].(map[string]any)
	if limited.Status != "limited" || limited.Cursor != nil || limited.Coverage["complete"] != false || reconciliation["pagination_exhausted"] != false || reconciliation["pages"] != historyReconciliationPageLimit {
		t.Fatal("exhausted reconciliation budget claimed complete", limited)
	}
}

func TestHistoryLegacyCoverageRequiresFreshCountedRun(t *testing.T) {
	pf := newHistoryFixture(3)
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	binding, err := loadHistoryAccount(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	old := historyRun{ID: randomDeliveryID(), PageSize: 50, Mode: "full", Status: "complete", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Scope: "provider_accessible_history"}
	raw, _ := json.Marshal(old)
	if _, err = ctx.AppDB().Exec(`INSERT INTO social_history_runs(id,project_id,social_account_id,binding,state) VALUES(?,?,?,?,?)`, old.ID, "test-proj", account, binding.binding(), string(raw)); err != nil {
		t.Fatal(err)
	}
	state := historyState(ctx, binding)
	if state["complete"] != false || state["current"] != false || state["needs_resync"] != true {
		t.Fatal("legacy assertion reused as reconciled evidence", state)
	}
	out, err := (&App{}).toolAccountHistorySync(ctx, map[string]any{"social_account_id": account, "cursor": historyCursor(old), "mode": "full"})
	if err != nil {
		t.Fatal(err)
	}
	assertDestinationError(t, out, "cursor predates")
	fresh := finishHistory(t, ctx, account)
	if fresh.Coverage["complete"] != true || fresh.Coverage["provider_total"] != (*int64)(nil) || fresh.Coverage["count_verified"] != false || len(fresh.Limitations) == 0 {
		t.Fatal("unknown total was treated as a confirmed count", fresh)
	}
}
