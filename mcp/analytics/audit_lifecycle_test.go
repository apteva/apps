package main

import (
	"context"
	"encoding/json"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCaptureReconnectsDoNotAccumulateGoroutines(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	captureMu.Lock()
	old := captureCfg
	oldProjects := captureProjects
	captureProjects = nil
	captureCfg = captureConfig{Enabled: true, Mode: "all", SampleRate: 1}
	captureMu.Unlock()
	defer func() { captureMu.Lock(); captureCfg = old; captureProjects = oldProjects; captureMu.Unlock() }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `data: {"seq":1,"app":"a","topic":"t","project_id":"p1","time":"2026-01-01T00:00:00Z","data":{}}`)
	}))
	defer server.Close()
	var seq uint64
	_ = streamFirehose(ctx, server.URL, "test", &seq)
	before := runtime.NumGoroutine()
	for i := 0; i < 80; i++ {
		_ = streamFirehose(ctx, server.URL, "test", &seq)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Fatalf("reconnect goroutines: %d -> %d", before, after)
	}
	n, _ := countEvents(ctx.AppDB(), Filter{ProjectID: "p1"})
	if n != 1 {
		t.Fatal("reconnect duplicate", n)
	}
}
func TestMCPDeadlineCancelsReadsAndWrites(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "analytics_count" || tool.Name == "analytics_track" {
			_, err := tool.HandlerCtx(cancelled, ctx, map[string]any{"event": "t"})
			if err == nil {
				t.Errorf("%s ignored cancellation", tool.Name)
			}
		}
	}
}
func TestGovernanceHTTPRoundTripAndScope(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	a := &App{}
	call := func(handler http.HandlerFunc, method, path, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-User-ID", "u")
		r.Header.Set(trustedProjectHeader, "p1")
		rr := httptest.NewRecorder()
		boundedHandler(handler)(rr, r)
		if rr.Code != status {
			t.Fatalf("%s %s => %d %s", method, path, rr.Code, rr.Body.String())
		}
		out := map[string]any{}
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return out
	}
	call(a.handleReferences, "POST", "/references", `{"key":"governed","label":"Governed"}`, 200)
	call(a.handleReferences, "POST", "/references", `{"reference_set":"governed","value":"a","label":"A","status":"inactive","metadata":{"owner":"test"}}`, 200)
	call(a.handleReferences, "POST", "/references", `{"reference_set":"governed","value":"a","label":"Renamed"}`, 200)
	page := call(a.handleReferences, "GET", "/references?reference_set=governed&status=inactive", "", 200)
	if page["total"] != float64(1) {
		t.Fatal(page)
	}
	call(a.handleReferences, "GET", "/references?project_id=p2", "", 403)
	call(a.handleRetention, "PUT", "/retention", `{"event_days":0,"diagnostic_days":14,"archive_days":30}`, 200)
	call(a.handleRetention, "GET", "/retention", "", 200)
	call(a.handleRetention, "PUT", "/retention", `{"event_days":-1,"diagnostic_days":0}`, 400)
	call(a.handleFX, "POST", "/fx-rates", `{"base_currency":"USD","quote_currency":"EUR","as_of":1000,"rate":0.9}`, 200)
	call(a.handleFX, "GET", "/fx-rates", "", 200)
	call(a.handleHealth, "GET", "/diagnostics", "", 200)
	call(a.handleCaptureSet, "POST", "/capture?project_id=p2", `{"enabled":true,"mode":"all","sample_rate":1}`, 403)
}
func TestSpecHTTPPatchPreservesRequirements(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	spec, err := upsertEventSpec(ctx.AppDB(), EventSpec{ProjectID: "p1", App: "a", Topic: "t", ValidationMode: "reject", Properties: []EventPropertySpec{{Key: "props.id", Required: true}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", fmt.Sprintf("/event-specs/%d", spec.ID), strings.NewReader(`{"description":"updated"}`))
	req.Header.Set("X-User-ID", "u")
	rr := httptest.NewRecorder()
	(&App{}).handleEventSpecItem(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	got, err := getEventSpec(ctx.AppDB(), "p1", "a", "t")
	if err != nil || got.ValidationMode != "reject" || len(got.Properties) != 1 || got.Description != "updated" {
		t.Fatal(got, err)
	}
}
func TestPolicyDSTIdentityAndDimensionOrder(t *testing.T) {
	policy := &EventIngestPolicy{Bucket: "hour", Timezone: "America/New_York", Dimensions: []string{"props.a", "props.b"}}
	ev := EventInsert{ProjectID: "p1", App: "a"}
	values := map[string]any{"props.a": "x", "props.b": "y"}
	a, _ := time.Parse(time.RFC3339, "2026-11-01T01:30:00-04:00")
	b, _ := time.Parse(time.RFC3339, "2026-11-01T01:30:00-05:00")
	ab, _ := bucketForPolicy(a.UnixMilli(), policy)
	bb, _ := bucketForPolicy(b.UnixMilli(), policy)
	ka, _ := computedUpsertKey(ev, "t", policy, ab, values)
	kb, _ := computedUpsertKey(ev, "t", policy, bb, values)
	if ka == kb {
		t.Fatal("repeated DST hour collapsed")
	}
	policy.Dimensions = []string{"props.b", "props.a"}
	reordered, _ := computedUpsertKey(ev, "t", policy, ab, values)
	if reordered != ka {
		t.Fatal("dimension order changed group identity")
	}
}
func FuzzDimensionIdentity(f *testing.F) {
	f.Add("x|props.b=y", "z", "x", "y|props.b=z")
	f.Add("a", "b", "a", "c")
	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		if a == "" || b == "" || c == "" || d == "" || len(a)+len(b)+len(c)+len(d) > 2048 {
			return
		}
		left, _ := json.Marshal([]string{a, b})
		right, _ := json.Marshal([]string{c, d})
		if string(left) == string(right) {
			return
		}
		p := &EventIngestPolicy{Dimensions: []string{"props.a", "props.b"}}
		ev := EventInsert{ProjectID: "p1", App: "a"}
		x, err := computedUpsertKey(ev, "t", p, policyBucket{}, map[string]any{"props.a": a, "props.b": b})
		if err != nil {
			t.Fatal(err)
		}
		y, err := computedUpsertKey(ev, "t", p, policyBucket{}, map[string]any{"props.a": c, "props.b": d})
		if err != nil {
			t.Fatal(err)
		}
		if x == y {
			t.Fatalf("identity collision: %q and %q", left, right)
		}
	})
}

func TestObjectiveMeasurementCannotRestoreCacheAfterEdit(t *testing.T) {
	db := testDashboardDB(t)
	objective, err := createObjective(db, "p1", objectiveFixture("Versioned", "count"))
	if err != nil {
		t.Fatal(err)
	}
	target := objective.Targets[0]
	actual := float64(10)
	progress := TargetProgress{TargetID: target.ID, Status: "ok", ActualValue: &actual}
	if err = persistObjectiveMeasurement(db, target, progress, 100); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	changed := target
	changed.Query.Topic = "changed"
	if err = updateObjectiveTargets(tx, objective.ID, []ObjectiveTarget{changed}, target.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = persistObjectiveMeasurement(db, target, progress, 101); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM objective_progress WHERE target_id=?`, target.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("stale snapshot restored invalidated cache")
	}
	updated, err := getObjective(db, "p1", objective.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Targets[0].UpdatedAt <= target.UpdatedAt {
		t.Fatal("target revision did not advance")
	}
	if err = persistObjectiveMeasurement(db, updated.Targets[0], progress, 102); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM objective_progress WHERE target_id=?`, target.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestCollectorAdmissionHasSeparateGatewayBudget(t *testing.T) {
	admission := &rateLimiter{m: map[string]*tokenBucket{}, rate: 500, burst: 1000}
	perKey := &rateLimiter{m: map[string]*tokenBucket{}}
	for i := 0; i < 200; i++ {
		if !admission.allow("gateway") {
			t.Fatalf("gateway throttled at %d", i)
		}
	}
	accepted := 0
	for i := 0; i < 100; i++ {
		if perKey.allow("key") {
			accepted++
		}
	}
	if accepted >= 100 {
		t.Fatal("per-key budget not enforced")
	}
}

func TestDailyOverviewFillsGapsAndBoundsAllTime(t *testing.T) {
	db := testDashboardDB(t)
	day := int64(86400000)
	for _, ts := range []int64{day, 3 * day, 20000 * day} {
		if _, err := insertEvent(db, EventInsert{TS: ts, App: "a", Topic: "t", ProjectID: "p1", Source: "track", Props: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := dailySeries(db, Filter{ProjectID: "p1", Since: day, Until: 4 * day})
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if rows[1]["count"] != int64(0) {
		t.Fatal("gap not zero", rows)
	}
	rows, err = dailySeries(db, Filter{ProjectID: "p1"})
	if err != nil || len(rows) > 1000 {
		t.Fatal(len(rows), err)
	}
	var count int64
	for _, row := range rows {
		count += row["count"].(int64)
	}
	if count != 3 {
		t.Fatal("bounded grid lost observations", count)
	}
}

func TestLegacyUnbucketedKeysPreserveSingletonAndManualIdentity(t *testing.T) {
	for _, manual := range []string{"", "external|key"} {
		t.Run(manual, func(t *testing.T) {
			db := testDashboardDB(t)
			_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", IngestMode: "upsert", UpsertPolicy: &EventIngestPolicy{Bucket: "none", Operation: "sum", ValueKey: "props.total", OutputProperty: "total"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			key := "p1|a|t"
			if manual != "" {
				key += "|manual=" + manual
			}
			if _, err = db.Exec(`INSERT INTO events(ts,app,topic,project_id,source,upsert_key,props) VALUES(1000,'a','t','p1','track',?,'{"total":10}')`, key); err != nil {
				t.Fatal(err)
			}
			if err = upgradeLegacyPolicyKeys(db); err != nil {
				t.Fatal(err)
			}
			if _, err = insertEvent(db, EventInsert{TS: 2000, App: "a", Topic: "t", ProjectID: "p1", UpsertKey: manual, Props: `{"total":5}`}); err != nil {
				t.Fatal(err)
			}
			rows, err := queryRows(db, Filter{ProjectID: "p1"}, 10)
			if err != nil || len(rows) != 1 || propsObject(string(rows[0].Props))["total"] != float64(15) {
				t.Fatal(rows, err)
			}
		})
	}
}

func TestDiagnosticStorageFailureRollsBackEvent(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", ValidationMode: "observe", Properties: []EventPropertySpec{{Key: "props.amount", Type: "number", Required: true}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER fail_diagnostic BEFORE INSERT ON event_spec_violations BEGIN SELECT RAISE(ABORT,'diagnostic write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = insertEvent(db, EventInsert{TS: 1000, App: "a", Topic: "t", ProjectID: "p1", Props: `{}`}); err == nil {
		t.Fatal("diagnostic write failure swallowed")
	}
	n, err := countEvents(db, Filter{ProjectID: "p1"})
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestGeneratedExamplesRespectTypedEnums(t *testing.T) {
	for _, prop := range []EventPropertySpec{
		{Type: "number", EnumValues: []string{"150"}},
		{Type: "boolean", EnumValues: []string{"false"}},
		{Type: "array", ExampleValue: `[1,2]`},
		{Type: "object", ExampleValue: `{"id":1}`},
		{Type: "number", ExampleValue: "1", EnumValues: []string{"150"}},
	} {
		value := exampleValue(prop)
		if !valueMatchesType(value, prop.Type) {
			t.Fatalf("%+v returned %T", prop, value)
		}
		if len(prop.EnumValues) > 0 && !stringIn(fmt.Sprint(value), prop.EnumValues) {
			t.Fatalf("%+v returned %v outside enum", prop, value)
		}
	}
}
