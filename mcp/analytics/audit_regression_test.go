package main

import (
	"encoding/json"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditTypedFilters(t *testing.T) {
	db := testDashboardDB(t)
	_, err := insertEvent(db, EventInsert{TS: 1000, App: "site", Topic: "event", ProjectID: "p1", Source: "track", Props: `{"n":42,"enabled":true,"empty":null}`})
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"props.n": float64(42), "props.enabled": true, "props.empty": nil} {
		n, err := countEvents(db, Filter{ProjectID: "p1", Where: map[string]any{key: value}})
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("filter %s=%v: got %d, want 1", key, value, n)
		}
	}
}

func TestAuditAllWindow(t *testing.T) {
	db := testDashboardDB(t)
	_, err := insertEvent(db, EventInsert{TS: time.Now().Add(-48 * time.Hour).UnixMilli(), App: "site", Topic: "event", ProjectID: "p1", Source: "track"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := evaluateWidget(db, "p1", DashboardWidget{Type: "stat", Config: map[string]any{"window": "$filters.window"}}, map[string]any{"window": "all"})
	if err != nil {
		t.Fatal(err)
	}
	if got["value"] != int64(1) {
		t.Errorf("All window: got %#v, want one old event", got)
	}
}

func TestAuditUpsertArithmetic(t *testing.T) {
	for _, op := range []string{"sum", "max", "min"} {
		t.Run(op, func(t *testing.T) {
			db := testDashboardDB(t)
			_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "site", Topic: "metric", IngestMode: "upsert", UpsertPolicy: &EventIngestPolicy{Operation: op, ValueKey: "props.amount", OutputProperty: "amount"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			nums := []int{10, 5}
			want := 15.0
			if op == "max" {
				want = 10
			}
			if op == "min" {
				nums = []int{5, 10}
				want = 5
			}
			for _, n := range nums {
				_, err = insertEvent(db, EventInsert{TS: 1000, ProjectID: "p1", App: "site", Topic: "metric", Source: "track", Props: fmt.Sprintf(`{"amount":%d}`, n)})
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := queryRows(db, Filter{ProjectID: "p1"}, 10)
			if err != nil {
				t.Fatal(err)
			}
			got := propsObject(string(rows[0].Props))["amount"]
			if got != want {
				t.Errorf("%s(%v): got %v, want %v", op, nums, got, want)
			}
		})
	}
}

func TestAuditDimensionCollision(t *testing.T) {
	p := &EventIngestPolicy{Dimensions: []string{"props.a", "props.b"}}
	ev := EventInsert{ProjectID: "p1", App: "site"}
	a, _ := computedUpsertKey(ev, "event", p, policyBucket{}, map[string]any{"props.a": "x|props.b=y", "props.b": "z"})
	b, _ := computedUpsertKey(ev, "event", p, policyBucket{}, map[string]any{"props.a": "x", "props.b": "y|props.b=z"})
	if a == b {
		t.Errorf("distinct dimension tuples collapse to same key: %s", a)
	}
}

func TestAuditRollupRetry(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "site", Topic: "click", IngestMode: "raw_plus_rollup", RollupPolicy: &EventIngestPolicy{Bucket: "day", Operation: "increment", Value: 1, OutputProperty: "count"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	ev := EventInsert{TS: 1000, ProjectID: "p1", App: "site", Topic: "click", Source: "track", UpsertKey: "request-1"}
	for i := 0; i < 2; i++ {
		if _, err := insertEvent(db, ev); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := queryRows(db, Filter{ProjectID: "p1", Topic: "click_day_rollup"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := propsObject(string(rows[0].Props))["count"]
	if got != float64(1) {
		t.Errorf("retry same raw upsert key: rollup count=%v, want 1", got)
	}
}

func TestAuditDryRunPolicyFailure(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	a := &App{}
	_, err := upsertEventSpec(ctx.AppDB(), EventSpec{ProjectID: "p1", App: "site", Topic: "click", IngestMode: "upsert", ValidationMode: "reject", UpsertPolicy: &EventIngestPolicy{Operation: "replace", Value: 1, Dimensions: []string{"props.id"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"app": "site", "event": "click", "props": map[string]any{}}
	dry, err := a.toolEventValidate(ctx, args)
	if err != nil {
		if _, e := a.toolTrack(ctx, args); e == nil {
			t.Fatal("write accepted invalid dry run")
		}
		return
	}
	_, trackErr := a.toolTrack(ctx, args)
	if dry.(map[string]any)["valid"] == true && trackErr != nil {
		t.Errorf("dry run says valid=true but write fails: %v", trackErr)
	}
}

func TestAuditRejectedViolations(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	a := &App{}
	_, err := upsertEventSpec(ctx.AppDB(), EventSpec{ProjectID: "p1", App: "site", Topic: "click", Status: "blocked"}, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.toolTrack(ctx, map[string]any{"app": "site", "event": "click"})
	if err != nil {
		t.Fatal(err)
	}
	violations, err := listEventSpecViolations(ctx.AppDB(), Filter{ProjectID: "p1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) == 0 {
		t.Errorf("blocked ingest response=%#v but violation log is empty", got)
	}
}

func TestAuditMissingFXCurrency(t *testing.T) {
	db := testDashboardDB(t)
	for _, props := range []string{`{"amount":10,"currency":"EUR"}`, `{"amount":90}`} {
		_, err := insertEvent(db, EventInsert{TS: 1000, ProjectID: "p1", App: "site", Topic: "sale", Source: "track", Props: props})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := moneyScalarForWidget(db, Filter{ProjectID: "p1"}, map[string]any{"value": "props.amount", "currency_field": "props.currency", "reporting_currency": "EUR", "amount_unit": "major"})
	if err == nil {
		t.Errorf("missing currency silently excluded: %#v", got)
	}
}

func TestAuditObjectiveLinkReplacement(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	a, err := createObjective(db, "p1", objectiveFixture("A", "count"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createObjective(db, "p1", objectiveFixture("B", "count")); err != nil {
		t.Fatal(err)
	}
	old := a.Targets[0].ID
	updated, err := updateObjective(db, "p1", a.ID, objectiveFixture("A", "count"))
	if err != nil {
		t.Fatal(err)
	}
	linkErr := validateDashboardGoalLinks(db, "p1", map[string]any{"objective_target_ids": []int64{old}})
	if linkErr != nil {
		t.Errorf("editing unchanged target changes id %d -> %d and breaks link: %v", old, updated.Targets[0].ID, linkErr)
	}
}

func TestAuditObjectiveSource(t *testing.T) {
	for _, source := range []string{"bus", "web", "rollup"} {
		q := ObjectiveMetricQuery{Aggregation: "count", Source: source}
		if err := validateObjectiveMetricQuery(&q); err != nil {
			t.Errorf("real source %q rejected: %v", source, err)
		}
	}
}

func TestAuditEmptySummary(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/summary?project_id=p1", nil)
	(&App{}).handleSummary(rr, r)
	if strings.Contains(rr.Body.String(), `"topics_list":null`) {
		t.Errorf("empty summary gives null topics_list; Home ActivityFallback calls .slice(): %s", rr.Body.String())
	}
}

func TestAuditBadSpecIDBypassesPatch(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	a := &App{}
	_, err := upsertEventSpec(ctx.AppDB(), EventSpec{ProjectID: "p1", App: "site", Topic: "sale", ValidationMode: "reject", Properties: []EventPropertySpec{{Key: "props.id", Required: true}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.toolEventSpecUpsert(ctx, map[string]any{"id": 999999, "app": "site", "topic": "sale", "description": "edited"})
	spec, getErr := getEventSpec(ctx.AppDB(), "p1", "site", "sale")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if err == nil && len(spec.Properties) == 0 {
		t.Errorf("nonexistent id silently overwrites existing tuple: properties=%d validation=%s", len(spec.Properties), spec.ValidationMode)
	}
}

func TestAuditCaptureSequenceRestart(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := getCaptureConfig()
	defer func() { captureMu.Lock(); captureCfg = old; captureMu.Unlock() }()
	captureMu.Lock()
	captureCfg = captureConfig{Enabled: true, Mode: "all", SampleRate: 1}
	captureMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `data: {"seq":1,"app":"site","project_id":"p1","topic":"click","time":"2026-01-01T00:00:00Z","data":{}}`)
	}))
	defer server.Close()
	last := uint64(100)
	_ = streamFirehose(ctx, server.URL, "test", &last)
	n, err := countEvents(ctx.AppDB(), Filter{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("server sequence reset: recorded=%d cursor=%d; post-restart event ignored", n, last)
	}
}

func TestAuditCaptureFailedWriteCursor(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := getCaptureConfig()
	defer func() { captureMu.Lock(); captureCfg = old; captureMu.Unlock() }()
	captureMu.Lock()
	captureCfg = captureConfig{Enabled: true, Mode: "all", SampleRate: 1}
	captureMu.Unlock()
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_ingest BEFORE INSERT ON events BEGIN SELECT RAISE(FAIL, 'transient write error'); END`)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `data: {"seq":1,"app":"site","project_id":"p1","topic":"click","data":{}}`)
	}))
	defer server.Close()
	last := uint64(0)
	_ = streamFirehose(ctx, server.URL, "test", &last)
	if _, err = ctx.AppDB().Exec(`DROP TRIGGER fail_ingest`); err != nil {
		t.Fatal(err)
	}
	_ = streamFirehose(ctx, server.URL, "test", &last)
	n, err := countEvents(ctx.AppDB(), Filter{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("failed write advances cursor; replay after recovery: rows=%d cursor=%d", n, last)
	}
}

func TestAuditNestedPolicyOutput(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "site", Topic: "click", IngestMode: "raw_plus_rollup", RollupPolicy: &EventIngestPolicy{Operation: "increment", Value: 1, OutputProperty: "props.metrics.total", TargetTopic: "rollup"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = insertEvent(db, EventInsert{TS: 1000, ProjectID: "p1", App: "site", Topic: "click", Source: "track"})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	var val any
	if err = db.QueryRow(`SELECT props,json_extract(props,'$.metrics.total') FROM events WHERE topic='rollup'`).Scan(&raw, &val); err != nil {
		t.Fatal(err)
	}
	if val == nil {
		t.Errorf("nested output writes literal dotted key, not nested JSON: %s", raw)
	}
}

func TestAuditCaptureOriginalTimestamp(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := getCaptureConfig()
	defer func() { captureMu.Lock(); captureCfg = old; captureMu.Unlock() }()
	captureMu.Lock()
	captureCfg = captureConfig{Enabled: true, Mode: "all", SampleRate: 1}
	captureMu.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `data: {"seq":1,"app":"site","project_id":"p1","topic":"click","time":"2026-01-01T00:00:00Z","data":{}}`)
	}))
	defer server.Close()
	last := uint64(0)
	_ = streamFirehose(ctx, server.URL, "test", &last)
	rows, err := queryRows(ctx.AppDB(), Filter{ProjectID: "p1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if rows[0].TS != want {
		t.Errorf("replayed event timestamp=%s, want Jan 1 original emission", time.UnixMilli(rows[0].TS))
	}
}

func TestAuditExampleNumber(t *testing.T) {
	prop := EventPropertySpec{Key: "props.views", Type: "number", Required: true, ExampleValue: "150"}
	sample := exampleEventForSpec(&EventSpec{App: "site", Topic: "view", Properties: []EventPropertySpec{prop}})
	raw, _ := json.Marshal(sample)
	if !valueMatchesType(sample["props"].(map[string]any)["views"], "number") {
		t.Errorf("generated numeric example fails own type rule: %s", raw)
	}
}
