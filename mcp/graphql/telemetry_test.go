package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTelemetryErrorFiltersIncludePartialErrorsAndIsolateAPIs(t *testing.T) {
	db := testDB(t)
	for _, r := range []struct {
		project, name, message, codes, allErrors, env string
		status, duration                              int
	}{
		{"p1", "Success", "", "[]", "[]", "production", 200, 10},
		{"p1", "Partial", "first error", `["source_error"]`, `[{"message":"first error"},{"message":"second failure","path":["orders",0,"total"]}]`, "production", 200, 1200},
		{"p1", "Rejected", "forbidden", `["permission_denied"]`, `[]`, "staging", 403, 30},
		{"p1", "CodeOnly", "", `["execution_timeout"]`, `[]`, "production", 200, 2000},
		{storageProject("p1", "analytics"), "OtherAPI", "forbidden", `["permission_denied"]`, `[]`, "production", 403, 5000},
		{"p2", "OtherProject", "forbidden", `["permission_denied"]`, `[]`, "production", 403, 5000},
	} {
		if _, err := db.Exec(`INSERT INTO graphql_request_logs(project_id,operation_name,status_code,duration_ms,error,error_codes_json,errors_json,environment,created_at,request_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, r.project, r.name, r.status, r.duration, r.message, r.codes, r.allErrors, r.env, "2026-10-06T12:00:00.123Z", "req-"+r.name); err != nil {
			t.Fatal(err)
		}
	}
	f, err := parseLogFiltersQuery(url.Values{"has_errors": {"true"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := publicLogsFiltered(db, "p1", f)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	summary, err := logSummary(db, "p1", f, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if summary["requests"] != int64(3) || summary["errors"] != int64(3) || summary["slow"] != int64(2) {
		t.Fatalf("summary ignores list limit and scopes to API: %v", summary)
	}
	f, err = parseLogFiltersArgs(map[string]any{"has_errors": true, "error_code": "source_error", "search": "SECOND FAILURE", "environment": "production"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = publicLogsFiltered(db, "p1", f)
	if err != nil || len(rows) != 1 || rows[0]["operation_name"] != "Partial" {
		t.Fatalf("search all errors: %v %v", rows, err)
	}
	if len(rows[0]["errors"].([]any)) != 2 {
		t.Fatal("full errors missing")
	}
	f, err = parseLogFiltersQuery(url.Values{"has_errors": {"false"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = publicLogsFiltered(db, "p1", f)
	if err != nil || len(rows) != 1 || rows[0]["operation_name"] != "Success" {
		t.Fatalf("success filter: %v %v", rows, err)
	}
	f, err = parseLogFiltersQuery(url.Values{"since": {"2026-10-06T14:00:00+02:00"}, "until": {"2026-10-06T12:00:00.123Z"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = publicLogsFiltered(db, "p1", f)
	if err != nil || len(rows) != 4 {
		t.Fatalf("timezone/fractional boundary: %v %v", rows, err)
	}
	for _, values := range []url.Values{{"has_errors": {"sometimes"}}, {"sort_by": {"id; DROP TABLE graphql_request_logs"}}} {
		if _, err := parseLogFiltersQuery(values); err == nil {
			t.Fatalf("accepted invalid filter: %v", values)
		}
	}
}

func TestHTTPObservationLogsEarlyFailuresAndFullErrorsOnce(t *testing.T) {
	a := secureTestApp(t, &trustedPlatform{})
	if _, err := setSecurity(a.ctx.AppDB(), "p1", "default", testSecurityPolicy()); err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		method, path, body string
		handler            http.HandlerFunc
		status             int
	}{
		{"POST", "/graphql", "{", a.handleGraphQL, 400},
		{"POST", "/public/graphql/default", `{"query":"{test}"}`, a.handlePublicGraphQL, 401},
		{"DELETE", "/graphql", "", a.handleGraphQL, 405},
	}
	for _, req := range requests {
		w := httptest.NewRecorder()
		aReq := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
		req.handler(w, aReq)
		if w.Code != req.status || w.Header().Get("X-Request-ID") == "" {
			t.Fatalf("response %d headers %v", w.Code, w.Header())
		}
	}
	// Nested observation reuses its parent, records all errors, and includes auth time.
	w := httptest.NewRecorder()
	a.observeGraphQL(w, httptest.NewRequest("POST", "/graphql", nil), func(w http.ResponseWriter, r *http.Request) {
		telemetry := requestTelemetryFrom(r)
		time.Sleep(3 * time.Millisecond)
		telemetry.environment = "production"
		telemetry.phases["auth"] = 3
		telemetry.result = executeResult{OperationName: "Partial", HasData: true, Errors: []map[string]any{{"message": "first", "extensions": map[string]any{"code": "source_error"}}, {"message": "second", "path": []any{"order", "total"}}}}
		telemetry.hasResult = true
		writeJSON(w, map[string]any{"data": map[string]any{"ok": true}, "errors": telemetry.result.Errors})
	})
	a.stopRequestLogger()
	rows, err := publicLogsFiltered(a.ctx.AppReadDB(), "p1", defaultLogFilter())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected one log per request: %v", rows)
	}
	if rows[0]["error"] != "first" || rows[0]["status_code"] != 200 || len(rows[0]["errors"].([]any)) != 2 || rows[0]["duration_ms"].(int64) < 3 {
		t.Fatalf("partial request: %v", rows[0])
	}
	for _, row := range rows {
		if row["request_id"] == "" || row["error"] == "" || !hasCodes(row) {
			t.Fatalf("missing early failure error details: %v", row)
		}
	}
	admin := httptest.NewRecorder()
	a.handleAdminHTTP(admin, httptest.NewRequest("GET", "/admin/logs?has_errors=true&limit=1", nil))
	var body struct {
		Summary map[string]any `json:"summary"`
		Logs    []any          `json:"logs"`
	}
	if err := json.Unmarshal(admin.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if admin.Code != 200 || len(body.Logs) != 1 || body.Summary["errors"] != float64(4) {
		t.Fatalf("admin response: %s", admin.Body.String())
	}
}
func hasCodes(row map[string]any) bool { codes, _ := row["error_codes"].([]any); return len(codes) > 0 }

func TestGraphQLExportsDashboardTelemetryWidget(t *testing.T) {
	m := (&App{}).Manifest()
	if len(m.Provides.UIComponents) != 1 {
		t.Fatalf("components: %v", m.Provides.UIComponents)
	}
	widget := m.Provides.UIComponents[0]
	if widget.Name != "graphql-telemetry" || widget.Entry != "/ui/GraphQLTelemetryWidget.mjs" || len(widget.Slots) != 1 || widget.Slots[0] != "dashboard.home" {
		t.Fatalf("widget: %+v", widget)
	}
	if widget.DefaultSize != "half" || len(widget.SupportedSizes) != 2 || widget.SettingsSchema == nil {
		t.Fatalf("missing host settings: %+v", widget)
	}
	if _, err := os.Stat(strings.TrimPrefix(widget.Entry, "/")); err != nil {
		t.Fatal(err)
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "graphql_logs" {
			properties := tool.InputSchema["properties"].(map[string]any)
			for _, name := range []string{"has_errors", "error_code", "search", "environment"} {
				if properties[name] == nil {
					t.Fatalf("missing MCP filter %s", name)
				}
			}
			return
		}
	}
	t.Fatal("missing logs tool")
}

func TestHTTPObservationRecordsPanicsAndPreservesRecovery(t *testing.T) {
	a := secureTestApp(t, &trustedPlatform{})
	func() {
		defer func() {
			if recovered := recover(); recovered != "private panic detail" {
				t.Fatalf("panic was not propagated: %v", recovered)
			}
		}()
		a.observeGraphQL(httptest.NewRecorder(), httptest.NewRequest("POST", "/graphql", nil), func(http.ResponseWriter, *http.Request) { panic("private panic detail") })
	}()
	a.stopRequestLogger()
	rows, err := publicLogsFiltered(a.ctx.AppReadDB(), "p1", defaultLogFilter())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["status_code"] != 500 || rows[0]["error"] != "internal GraphQL handler failure" {
		t.Fatalf("panic telemetry: %v", rows)
	}
	encoded, _ := json.Marshal(rows)
	if strings.Contains(string(encoded), "private panic detail") {
		t.Fatal("raw panic leaked into request details")
	}
}
