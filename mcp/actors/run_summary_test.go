package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// assertRunSummaryReceipt checks that the summary and the final dataset page
// carry the same completeness evidence as the full run, without its bulk.
func assertRunSummaryReceipt(t *testing.T, ctx *sdk.AppCtx, app *App, full map[string]any) {
	t.Helper()
	id := full["id"].(int64)
	got, err := app.toolActorRunGet(ctx, map[string]any{"id": id, "detail": "summary"})
	if err != nil {
		t.Fatal(err)
	}
	summary := got.(map[string]any)["run"].(map[string]any)
	for _, key := range []string{"definition_snapshot", "input", "trigger", "artifacts", "items", "trace_preview", "assertion_evidence"} {
		if _, ok := summary[key]; ok {
			t.Fatalf("summary kept %s", key)
		}
	}
	if summary["detail"] != "summary" || summary["status"] != full["status"] || summary["actor_revision"] != full["actor_revision"] {
		t.Fatalf("summary identity: %#v", summary)
	}
	fullOutput := full["output"].(map[string]any)
	fullCoverage := fullOutput["coverage"].(map[string]any)
	coverage := summary["coverage"].(map[string]any)
	for _, key := range []string{"inspection_complete", "more_results_remaining", "read_only", "checked_views", "required_views", "creator_url", "context_id"} {
		if !reflect.DeepEqual(coverage[key], fullCoverage[key]) {
			t.Fatalf("coverage %s: summary=%#v full=%#v", key, coverage[key], fullCoverage[key])
		}
	}
	fullViews := fullCoverage["views"].([]any)
	views := coverage["views"].([]map[string]any)
	if len(views) != len(fullViews) {
		t.Fatalf("views: %d != %d", len(views), len(fullViews))
	}
	for i, raw := range fullViews {
		want := raw.(map[string]any)
		for _, key := range []string{"name", "checked", "inspection_complete", "more_results_remaining", "pages_checked", "unique_items", "expected_total", "end_evidence", "error", "stop_reason"} {
			if !reflect.DeepEqual(views[i][key], want[key]) {
				t.Fatalf("view %v %s: summary=%#v full=%#v", want["name"], key, views[i][key], want[key])
			}
		}
		if rounds, _ := want["pagination"].([]any); len(rounds) > 0 && views[i]["pagination"].(map[string]any)["rounds"] != len(rounds) {
			t.Fatalf("pagination rounds: %#v", views[i]["pagination"])
		}
	}
	if summary["dataset_count"] != intFromAny(fullOutput["item_count"]) {
		t.Fatalf("dataset_count=%v item_count=%v", summary["dataset_count"], fullOutput["item_count"])
	}
	// Reading every row in small pages: only the final page has the receipt.
	after := int64(0)
	for {
		out, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id, "after": after, "limit": 2})
		if err != nil {
			t.Fatal(err)
		}
		page := out.(map[string]any)
		receipt, ok := page["run_summary"]
		if page["has_more"] == true {
			if ok {
				t.Fatal("receipt before the final page")
			}
			after = page["next_cursor"].(int64)
			continue
		}
		if !ok || !sameJSON(t, receipt, summary) {
			t.Fatalf("final page receipt=%#v summary=%#v", receipt, summary)
		}
		break
	}
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(x) == string(y)
}

func scanIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return ids
}

// A completed duplicate scan shaped like a large creator library, stored in
// the pre-bound format: every scroll round repeats all loaded post IDs.
func TestRunSummaryCompactsLargeCompletedScan(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	posts, drafts := scanIDs("p", 225), scanIDs("d", 4)
	rounds := []any{}
	for loaded := 20; loaded <= 225; loaded += 15 {
		ids := posts[:minInt(loaded+15, 225)]
		rounds = append(rounds, map[string]any{"status": "accepted", "before_ids": posts[:loaded], "after_ids": ids, "new_ids": ids[loaded:], "control_present": true})
	}
	rounds = append(rounds, map[string]any{"status": "unchanged", "stop_reason": "no_new_ids", "before_ids": posts, "after_ids": posts, "new_ids": []string{}})
	coverage := map[string]any{
		"inspection_complete": true, "read_only": true, "more_results_remaining": false,
		"checked_views": []string{"published", "scheduled", "draft"}, "required_views": []string{"published", "scheduled", "draft"},
		"creator_url": "https://www.patreon.com/c/creator", "creator_identity": "creator", "context_id": "ctx-1",
		"views": []any{
			map[string]any{"name": "posts", "covers": []string{"published", "scheduled"}, "checked": true, "inspection_complete": true, "more_results_remaining": false, "pages_checked": len(rounds), "unique_items": 225, "expected_total": 225, "end_evidence": "verified_scroll_end_stable_no_loading", "pagination": rounds},
			map[string]any{"name": "drafts", "covers": []string{"draft"}, "checked": true, "inspection_complete": true, "more_results_remaining": false, "pages_checked": 1, "unique_items": 4, "expected_total": 4, "end_evidence": "verified_scroll_end_stable_no_loading"},
		},
	}
	preview := []map[string]any{}
	for _, id := range posts[:20] {
		preview = append(preview, map[string]any{"id": id, "title": strings.Repeat("Title ", 40), "status": "published"})
	}
	output, _ := json.Marshal(map[string]any{"item_count": 229, "page_count": len(rounds) + 1, "coverage": coverage, "inspection_complete": true, "items": preview, "trace_preview": []any{map[string]any{"action": "inspect_views"}}, "execution_progress": map[string]any{"commit_attempted": false}})
	snapshot := `{"schema_version":1,"read_only":true,"steps":[{"action":"inspect_views"}],"padding":"` + strings.Repeat("x", 7000) + `"}`
	res, err := ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,output_json,status,actor_id,actor_revision,definition_snapshot_json,created_at,completed_at) VALUES(?,?,?,?,?,?,?,?,datetime('now'),datetime('now'))`, projectID(ctx), "actor", `{"operation":"fetch_posts","input":{}}`, string(output), "completed", 1, 122, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for _, key := range append(posts, drafts...) {
		if _, err := ctx.AppDB().Exec(`INSERT INTO actors_dataset_items(project_id,run_id,item_json) VALUES(?,?,?)`, projectID(ctx), id, fmt.Sprintf(`{"id":%q}`, key)); err != nil {
			t.Fatal(err)
		}
	}
	full, err := getActorRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	fullBytes, _ := json.Marshal(full)
	assertRunSummaryReceipt(t, ctx, app, full)
	got, _ := app.toolActorRunGet(ctx, map[string]any{"id": id, "detail": "summary"})
	summary := got.(map[string]any)["run"].(map[string]any)
	summaryBytes, _ := json.Marshal(summary)
	if len(summaryBytes) > 3*1024 || len(fullBytes) < 20*1024 {
		t.Fatalf("summary=%d bytes full=%d bytes", len(summaryBytes), len(fullBytes))
	}
	if summary["operation"] != "fetch_posts" || summary["dataset_count"] != 229 || summary["inspection_complete"] != true {
		t.Fatalf("summary: %s", summaryBytes)
	}
	last := summary["coverage"].(map[string]any)["views"].([]map[string]any)[0]["pagination"].(map[string]any)["last"].(map[string]any)
	if last["after_count"] != 225 || last["new_count"] != 0 || last["stop_reason"] != "no_new_ids" {
		t.Fatalf("pagination end: %#v", last)
	}
	// Full detail stays the default for existing callers.
	got, _ = app.toolActorRunGet(ctx, map[string]any{"id": id})
	if _, ok := got.(map[string]any)["run"].(map[string]any)["definition_snapshot"]; !ok {
		t.Fatal("default read lost the definition snapshot")
	}
	if _, err := app.toolActorRunGet(ctx, map[string]any{"id": id, "detail": "compact"}); err == nil {
		t.Fatal("unknown detail accepted")
	}
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, route.Handler)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/runs/%d?detail=summary", id), nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"detail":"summary"`) || strings.Contains(w.Body.String(), "definition_snapshot") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestDatasetReadOmitsReceiptWhileRunning(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	res, err := ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status,definition_snapshot_json) VALUES(?,?,?,?,?)`, projectID(ctx), "actor", "{}", "running", `{"schema_version":1}`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(`INSERT INTO actors_dataset_items(project_id,run_id,item_json) VALUES(?,?,?)`, projectID(ctx), id, `{"id":"1"}`); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolDatasetRead(ctx, map[string]any{"run_id": id})
	if err != nil {
		t.Fatal(err)
	}
	page := out.(map[string]any)
	if _, ok := page["run_summary"]; ok || page["has_more"] != false {
		t.Fatalf("active run page: %#v", page)
	}
}

func TestReadDiagnosticBoundsSavedIDs(t *testing.T) {
	ids := scanIDs("p", 229)
	d := &actorReadDiagnostic{BeforeIDs: ids[:200], AfterIDs: ids, NewIDs: ids[200:], Status: "accepted"}
	cov := actorReadCoverage{ReadOnly: true, Complete: true, CheckedViews: []string{"published"}, ContextID: "ctx", IdentityURL: "https://example.com/creator", Views: []*actorViewCoverage{{Name: "posts", Covers: []string{"published"}, Checked: true, Complete: true, EndEvidence: "verified_scroll_end_stable_no_loading", Pagination: []*actorReadDiagnostic{d}}}}
	raw, err := json.Marshal(cov)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	round := decoded["views"].([]any)[0].(map[string]any)["pagination"].([]any)[0].(map[string]any)
	after := round["after_ids"].([]any)
	if round["before_count"] != float64(200) || round["after_count"] != float64(229) || round["new_count"] != float64(29) || round["ids_truncated"] != true {
		t.Fatalf("counts: %#v", round)
	}
	if len(after) != maxReadDiagnosticIDs || after[0] != "p0000" || after[len(after)-1] != "p0228" || len(round["new_ids"].([]any)) != 29 {
		t.Fatalf("bounded ids: %v", after)
	}
	if len(d.AfterIDs) != 229 || d.AfterIDs[25] != "p0025" {
		t.Fatal("marshal changed in-memory evidence")
	}
	// Recovery still decodes bounded coverage.
	if err := checkRecoveryCoverage(map[string]any{"coverage": decoded}, &actorRecoveryPolicy{RequiredViews: []string{"published"}}, "ctx", "https://example.com/creator"); err != nil {
		t.Fatal(err)
	}
	small, _ := json.Marshal(&actorReadDiagnostic{BeforeIDs: []string{"a"}, AfterIDs: []string{"a", "b"}, NewIDs: []string{"b"}})
	if strings.Contains(string(small), "ids_truncated") || !strings.Contains(string(small), `"after_ids":["a","b"]`) || !strings.Contains(string(small), `"after_count":2`) {
		t.Fatal(string(small))
	}
}
