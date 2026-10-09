package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func seedDiagnosticHistory(t testing.TB, ctx *sdk.AppCtx, n int) {
	t.Helper()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO read_diagnostics(project_id,recorded_at_ms,operation,call_id,request_id,query_id,outcome,total_ms) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < n; i++ {
		outcome := "ok"
		if i%3 == 0 {
			outcome = "error"
		}
		// Repeated timestamps exercise the deterministic id tie-breaker.
		if _, err := stmt.Exec(ctx.CurrentProject(), 1000+i/4, "tables_query", fmt.Sprintf("call-%d", i), fmt.Sprintf("request-%d", i%5), fmt.Sprintf("query-%d", i%7), outcome, i%500); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func diagnosticPage(t *testing.T, a *App, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	out, err := a.toolDiagnosticsList(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func TestDiagnosticHistoryCursorNoDuplicatesWithConcurrentInsertAndRetention(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	seedDiagnosticHistory(t, ctx, 143)
	first := diagnosticPage(t, a, ctx, map[string]any{"limit": 11})
	if _, exists := first["total"]; exists {
		t.Fatal("default page scanned full-history totals")
	}
	if first["summary_included"] != false {
		t.Fatal("unexpected summary")
	}
	seen := map[int64]bool{}
	for _, item := range first["diagnostics"].([]readDiagnostic) {
		seen[item.ID] = true
	}
	// Arrivals at the newest boundary are excluded from continuation pages.
	_, err := ctx.AppDB().Exec(`INSERT INTO read_diagnostics(project_id,recorded_at_ms,operation,call_id,query_id,outcome) VALUES('test-proj',1035,'new','','q','ok')`)
	if err != nil {
		t.Fatal(err)
	}
	// The cursor boundary remains valid even if retention removes its row.
	last := first["diagnostics"].([]readDiagnostic)[10]
	if _, err := ctx.AppDB().Exec(`DELETE FROM read_diagnostics WHERE id=?`, last.ID); err != nil {
		t.Fatal(err)
	}
	page := first
	for page["has_more"] == true {
		page = diagnosticPage(t, a, ctx, map[string]any{"limit": 13, "cursor": page["next_cursor"]})
		for _, item := range page["diagnostics"].([]readDiagnostic) {
			if seen[item.ID] || item.ID > 143 {
				t.Fatalf("duplicate/new arrival in continuation: %d", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 143 {
		t.Fatalf("original rows seen=%d", len(seen))
	}
	if _, exists := page["next_cursor"]; exists {
		t.Fatal("end page must not offer a cursor")
	}
}
func TestDiagnosticHistoryFiltersSummaryDetailAndHTTPParity(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	seedDiagnosticHistory(t, ctx, 200)
	from := time.UnixMilli(1010).UTC().Format(time.RFC3339Nano)
	to := time.UnixMilli(1040).UTC().Format(time.RFC3339Nano)
	args := map[string]any{"outcome": "error", "operation": "tables_query", "request_id": "request-0", "query_id": "query-0", "from": from, "to": to, "limit": 1, "include_summary": true}
	page := diagnosticPage(t, a, ctx, args)
	if page["total"] != int64(1) || page["has_more"] != false {
		t.Fatalf("summary=%v", page)
	}
	item := page["diagnostics"].([]readDiagnostic)[0]
	if item.CallID != "call-105" {
		t.Fatalf("filtered record=%v", item)
	}
	detail, err := a.toolDiagnosticsGet(ctx, map[string]any{"id": item.ID})
	if err != nil || detail.(map[string]any)["diagnostic"].(readDiagnostic).ID != item.ID {
		t.Fatalf("detail=%v err=%v", detail, err)
	}
	if _, err := a.toolDiagnosticsGet(ctx, map[string]any{"id": 9999}); err == nil {
		t.Fatal("missing detail succeeded")
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO read_diagnostics(project_id,recorded_at_ms,operation,call_id,query_id,outcome) VALUES('other',5000,'private','','q','error')`)
	if err != nil {
		t.Fatal(err)
	}
	var foreignID int64
	_ = ctx.AppDB().QueryRow(`SELECT id FROM read_diagnostics WHERE project_id='other'`).Scan(&foreignID)
	if _, err := a.toolDiagnosticsGet(ctx, map[string]any{"id": foreignID}); err == nil {
		t.Fatal("foreign detail leaked")
	}
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	request := httptest.NewRequest("GET", "/diagnostics?project_id=test-proj&limit=1&call_id=call-105&include_summary=false", nil)
	request.Header.Set("X-Request-ID", "request-of-this-history-call")
	response := httptest.NewRecorder()
	a.handleDiagnostics(response, request)
	var body map[string]json.RawMessage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("HTTP=%d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private") || len(body["total"]) != 0 {
		t.Fatal("HTTP leakage or unwanted summary")
	}
	var items []readDiagnostic
	_ = json.Unmarshal(body["diagnostics"], &items)
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatal("X-Request-ID incorrectly used as a history filter")
	}
	detailResponse := httptest.NewRecorder()
	a.handleDiagnosticItem(detailResponse, httptest.NewRequest("GET", fmt.Sprintf("/diagnostics/%d?project_id=test-proj", item.ID), nil))
	if detailResponse.Code != 200 {
		t.Fatal(detailResponse.Body.String())
	}
	offset := diagnosticPage(t, a, ctx, map[string]any{"limit": 5, "offset": 7, "include_summary": true})
	if offset["next_offset"] != 12 || offset["total"] != int64(200) {
		t.Fatalf("legacy offset=%v", offset)
	}
}
func TestDiagnosticHistoryValidationAndPermissionRevocation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	seedDiagnosticHistory(t, ctx, 20)
	first := diagnosticPage(t, a, ctx, map[string]any{"limit": 2, "outcome": "error"})
	cursor := first["next_cursor"]
	cases := []map[string]any{
		{"cursor": "broken"}, {"cursor": cursor, "outcome": "ok"}, {"cursor": cursor, "outcome": "error", "offset": 1},
		{"from": "bad"}, {"from": "2026-01-02T00:00:00Z", "to": "2026-01-01T00:00:00Z"}, {"limit": -1}, {"limit": 1.5}, {"include_summary": "true"}, {"operation": []any{"sql"}}, {"cursor": base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"p":"other","id":2,"t":1}`))},
	}
	for _, args := range cases {
		if _, err := a.toolDiagnosticsList(ctx, args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	allowed := &sdk.Caller{ProjectID: "test-proj", DefaultEffect: "deny", Grants: []sdk.Grant{{Effect: "allow", Permission: "diagnostics.read", Resource: "*"}}}
	args := map[string]any{"_request_context": sdk.WithCaller(context.Background(), allowed), "limit": 2, "outcome": "error", "cursor": cursor}
	if _, err := a.toolDiagnosticsList(ctx, args); err != nil {
		t.Fatal(err)
	}
	revoked := &sdk.Caller{ProjectID: "test-proj", DefaultEffect: "deny"}
	args["_request_context"] = sdk.WithCaller(context.Background(), revoked)
	if _, err := a.toolDiagnosticsList(ctx, args); err == nil {
		t.Fatal("cursor bypassed revoked permission")
	}
	if _, err := a.toolDiagnosticsGet(ctx, map[string]any{"id": 1, "_request_context": sdk.WithCaller(context.Background(), revoked)}); err == nil {
		t.Fatal("detail bypassed revoked permission")
	}
	allowed.ProjectID = "other"
	args["_request_context"] = sdk.WithCaller(context.Background(), allowed)
	if _, err := a.toolDiagnosticsList(ctx, args); err == nil {
		t.Fatal("authenticated project mismatch accepted")
	}
}
func TestDiagnosticHistoryIndexedSeeksAndLargeCorpus(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "history")
	seedDiagnosticHistory(t, ctx, 20000)
	for _, filter := range []string{"", "outcome", "request_id", "query_id", "operation", "call_id"} {
		args := map[string]any{"limit": 10}
		if filter != "" {
			args[filter] = map[string]string{"outcome": "error", "request_id": "request-0", "query_id": "query-0", "operation": "tables_query", "call_id": "call-10000"}[filter]
		}
		q, err := parseDiagnosticHistory("history", args)
		if err != nil {
			t.Fatal(err)
		}
		q.Boundary = &diagnosticCursor{1, "history", q.Filter, 4000, 12000}
		query, params := q.pageSQL()
		rows, err := ctx.AppDB().Query("EXPLAIN QUERY PLAN "+query, params...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail)
		}
		_ = rows.Close()
		if !strings.Contains(plan.String(), "SEARCH read_diagnostics USING INDEX") || strings.Contains(plan.String(), "TEMP B-TREE") || strings.Contains(plan.String(), "SCAN read_diagnostics") {
			t.Fatalf("filter=%s plan=%s", filter, plan.String())
		}
		// Cursor queries must not issue OFFSET or a COUNT query.
		if strings.Contains(query, "OFFSET") || strings.Contains(query, "COUNT(") {
			t.Fatal(query)
		}
	}
	page := diagnosticPage(t, &App{}, ctx, map[string]any{"limit": 10000})
	if len(page["diagnostics"].([]readDiagnostic)) != 100 || page["has_more"] != true {
		t.Fatal("large corpus page unbounded")
	}
}
func BenchmarkDiagnosticsHistoryPages(b *testing.B) {
	ctx, _, _ := newFileBackedTestCtx(b, "history")
	seedDiagnosticHistory(b, ctx, 20000)
	for _, kind := range []string{"cursor_deep", "offset_deep", "cursor_with_summary"} {
		b.Run(kind, func(b *testing.B) {
			args := map[string]any{"limit": 25}
			q, err := parseDiagnosticHistory("history", args)
			if err != nil {
				b.Fatal(err)
			}
			if kind == "offset_deep" {
				q.Offset = 16000
			} else {
				q.Boundary = &diagnosticCursor{1, "history", q.Filter, 1999, 4000}
			}
			q.IncludeSummary = kind == "cursor_with_summary"
			a := &App{}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root, finish, err := a.beginOperation(ctx, map[string]any{}, "diagnostics_list", false)
				if err != nil {
					b.Fatal(err)
				}
				_, err = a.readDiagnosticHistory(root, "history", q)
				finish()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDiagnosticsRecordingIndexMaintenance(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprint(indexed), func(b *testing.B) {
			ctx, _, _ := newFileBackedTestCtx(b, "recording")
			if !indexed {
				for _, key := range []string{"outcome", "request", "query", "operation", "call"} {
					if _, err := ctx.AppDB().Exec("DROP INDEX read_diagnostics_project_" + key + "_time"); err != nil {
						b.Fatal(err)
					}
				}
			}
			d := &readObservation{app: ctx, projectID: "recording", operation: "tables_query", queryID: "fingerprint", callID: "call", requestID: "request", phases: map[string]time.Duration{}}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.started = time.Now()
				recordReadDiagnostic(d, nil, "ok", "", 1, false, nil)
			}
		})
	}
}
