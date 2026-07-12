package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func scopedRequest(method, target, projectID string, body any) *http.Request {
	var raw *bytes.Reader
	if body == nil {
		raw = bytes.NewReader(nil)
	} else {
		data, _ := json.Marshal(body)
		raw = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, target, raw)
	r.Header.Set("X-User-ID", "7")
	r.Header.Set(trustedProjectHeader, projectID)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func withHTTPTestContext(t *testing.T) *App {
	t.Helper()
	previous := globalCtx
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	return &App{}
}

func TestRequestProjectRejectsTrustedQueryMismatch(t *testing.T) {
	r := scopedRequest(http.MethodGet, "/summary?project_id=p2", "p1", nil)
	_, err := requestProjectID(r)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("requestProjectID error = %v, want project mismatch", err)
	}
}

func TestDashboardHTTPHandlersCannotCrossProjectsByBodyOrID(t *testing.T) {
	app := withHTTPTestContext(t)
	db := globalCtx.AppDB()
	p1, err := createDashboard(db, "p1", "P1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := createDashboard(db, "p2", "P2", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	app.handleDashboardItem(get, scopedRequest(http.MethodGet, "/dashboards/"+itoa64ForTest(p2.ID)+"?project_id=p1", "p1", nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("cross-project dashboard GET status=%d body=%s", get.Code, get.Body.String())
	}

	query := httptest.NewRecorder()
	app.handleWidgetQuery(query, scopedRequest(http.MethodPost, "/query-widget?project_id=p1", "p1", map[string]any{
		"project_id": "p2",
		"widget":     map[string]any{"type": "stat", "config": map[string]any{}},
	}))
	if query.Code != http.StatusForbidden {
		t.Fatalf("cross-project widget query status=%d body=%s", query.Code, query.Body.String())
	}

	del := httptest.NewRecorder()
	app.handleDashboardItem(del, scopedRequest(http.MethodDelete, "/dashboards/"+itoa64ForTest(p2.ID)+"?project_id=p1", "p1", nil))
	if del.Code != http.StatusNotFound {
		t.Fatalf("scoped delete status=%d body=%s", del.Code, del.Body.String())
	}
	if _, err := getDashboard(db, p2.ID); err != nil {
		t.Fatalf("project p2 dashboard was deleted: %v", err)
	}
	if _, err := getDashboard(db, p1.ID); err != nil {
		t.Fatalf("project p1 dashboard changed: %v", err)
	}
}

func TestEventSpecAndWriteKeyHTTPHandlersRejectBodyProjectMismatch(t *testing.T) {
	app := withHTTPTestContext(t)
	db := globalCtx.AppDB()
	spec, err := upsertEventSpec(db, EventSpec{ProjectID: "p2", App: "patreon", Topic: "daily"}, true)
	if err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRecorder()
	app.handleEventSpecItem(get, scopedRequest(http.MethodGet, "/event-specs/"+itoa64ForTest(spec.ID)+"?project_id=p1", "p1", nil))
	if get.Code != http.StatusNotFound {
		t.Fatalf("cross-project spec GET status=%d body=%s", get.Code, get.Body.String())
	}

	create := httptest.NewRecorder()
	app.handleKeysCreate(create, scopedRequest(http.MethodPost, "/keys?project_id=p1", "p1", map[string]any{
		"site":       "example",
		"project_id": "p2",
	}))
	if create.Code != http.StatusForbidden {
		t.Fatalf("cross-project key create status=%d body=%s", create.Code, create.Body.String())
	}
	keys, err := listWriteKeys(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("cross-project key create wrote keys: %#v", keys)
	}
}

func TestDashboardQueryBatchesWidgetsAndPreservesIndividualErrors(t *testing.T) {
	app := withHTTPTestContext(t)
	db := globalCtx.AppDB()
	dashboard, err := createDashboard(db, "p1", "Metrics", "", nil, []DashboardWidget{
		{Type: "stat", Title: "Events", Config: map[string]any{"app": "patreon", "topic": "revenue", "window": "all"}},
		{Type: "stat", Title: "Revenue", Config: map[string]any{"app": "patreon", "topic": "revenue", "window": "all", "value": "props.amount"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertEvent(db, EventInsert{
		TS: 1, App: "patreon", Topic: "revenue", ProjectID: "p1", Source: "test", Props: `{"amount":"bad"}`,
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	target := "/query-dashboard?project_id=p1&dashboard_id=" + itoa64ForTest(dashboard.ID) + "&filters=%7B%7D"
	app.handleDashboardQuery(rec, scopedRequest(http.MethodGet, target, "p1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard query status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Widgets []dashboardWidgetResult `json:"widgets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Widgets) != 2 || payload.Widgets[0].Data["value"] != float64(1) {
		t.Fatalf("batched widget payload=%#v", payload.Widgets)
	}
	if !strings.Contains(payload.Widgets[1].Error, "non-numeric") {
		t.Fatalf("widget error=%q want non-numeric", payload.Widgets[1].Error)
	}
}

func itoa64ForTest(v int64) string {
	return strconv.FormatInt(v, 10)
}
