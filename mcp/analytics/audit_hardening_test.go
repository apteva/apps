package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTypedFilterAndDiscoveryMatrix(t *testing.T) {
	db := testDashboardDB(t)
	for _, raw := range []string{`{"v":42}`, `{"v":"42"}`, `{"v":true}`, `{"v":1}`, `{"v":false}`, `{"v":0}`, `{"v":null}`, `{}`, `{"v":"all"}`} {
		if _, err := insertEvent(db, EventInsert{ProjectID: "p1", App: "a", Topic: "t", TS: 1000, Props: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []any{42, "42", true, 1, false, 0, nil, "all"} {
		n, err := countEvents(db, Filter{ProjectID: "p1", Where: map[string]any{"props.v": v}})
		if err != nil || n != 1 {
			t.Errorf("%#v => %d %v", v, n, err)
		}
	}
	for _, v := range []any{map[string]any{"operator": "bad"}, []any{map[string]any{}}, []any{[]any{1}}} {
		if _, err := countEvents(db, Filter{Where: map[string]any{"props.v": v}}); err == nil {
			t.Errorf("accepted unsupported filter %#v", v)
		}
	}
	for _, key := range []string{"props..x", "props.x.", "props.x[0]", "topic", "props.x'"} {
		if _, err := countEvents(db, Filter{Where: map[string]any{key: 1}}); err == nil {
			t.Errorf("accepted %s", key)
		}
	}
	n, err := countEvents(db, Filter{Where: map[string]any{"props.v": []any{true, "42", nil}}})
	if err != nil || n != 3 {
		t.Fatalf("multi selection=%d %v", n, err)
	}
	options, err := dashboardFilterOptions(db, "p1", map[string]any{"source": map[string]any{"app": "a", "value_field": "props.v"}})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, option := range options {
		found[mustJSON(option["value"])] = true
	}
	for _, v := range []any{42, "42", true, 1, false, 0, nil, "all"} {
		if !found[mustJSON(v)] {
			t.Errorf("missing typed option %#v: %#v", v, options)
		}
	}
	for _, v := range []any{"all", nil} {
		result, err := evaluateWidget(db, "p1", DashboardWidget{Type: "stat", Config: map[string]any{"window": "all", "where": map[string]any{"props.v": "$filters.v"}}}, map[string]any{"v": map[string]any{"value": v}})
		if err != nil || result["value"] != int64(1) {
			t.Errorf("wrapped %#v => %#v %v", v, result, err)
		}
	}
}
func TestPolicyArithmeticConcurrentAndIdempotent(t *testing.T) {
	for _, op := range []string{"sum", "increment", "min", "max", "replace"} {
		t.Run(op, func(t *testing.T) {
			db := testDashboardDB(t)
			_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", IngestMode: "upsert", UpsertPolicy: &EventIngestPolicy{Operation: op, ValueKey: "props.amount", OutputProperty: "props.metrics.total"}}, true)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 100)
			for i := 0; i < 100; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					ev := EventInsert{ProjectID: "p1", App: "a", Topic: "t", TS: 1000, Props: `{"amount":-2}`, DeliveryID: fmt.Sprint(i)}
					for retry := 0; retry < 2; retry++ {
						if _, err := insertEvent(db, ev); err != nil {
							errs <- err
							return
						}
					}
				}(i)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			var n float64
			if err = db.QueryRow(`SELECT json_extract(props,'$.metrics.total') FROM events`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := -2.0
			if op == "sum" || op == "increment" {
				want = -200
			}
			if n != want {
				t.Errorf("%s result=%v want%v", op, n, want)
			}
			if _, err := insertEvent(db, EventInsert{ProjectID: "p1", App: "a", Topic: "t", TS: 1000, Props: `{"amount":999}`, DeliveryID: "0"}); err == nil {
				t.Fatal("changed delivery accepted")
			}
		})
	}
}
func TestCorrectedRollupsRebuildMinMaxAndDimensions(t *testing.T) {
	for _, op := range []string{"sum", "min", "max"} {
		t.Run(op, func(t *testing.T) {
			db := testDashboardDB(t)
			_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", IngestMode: "raw_plus_rollup", RollupPolicy: &EventIngestPolicy{Operation: op, ValueKey: "props.amount", OutputProperty: "total", Dimensions: []string{"props.group"}}}, true)
			if err != nil {
				t.Fatal(err)
			}
			ev := EventInsert{ProjectID: "p1", App: "a", Topic: "t", TS: 1000, UpsertKey: "one", Props: `{"group":"a","amount":10}`}
			if _, err = insertEvent(db, ev); err != nil {
				t.Fatal(err)
			}
			ev.UpsertKey = "two"
			ev.Props = `{"group":"a","amount":5}`
			if _, err = insertEvent(db, ev); err != nil {
				t.Fatal(err)
			}
			ev.UpsertKey = "one"
			ev.Props = `{"group":"b","amount":20}`
			if _, err = insertEvent(db, ev); err != nil {
				t.Fatal(err)
			}
			for group, want := range map[string]float64{"a": 5, "b": 20} {
				rows, err := queryRows(db, Filter{Topic: "t_rollup", Where: map[string]any{"props.group": group}}, 10)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows %#v %v", rows, err)
				}
				if got := propsObject(string(rows[0].Props))["total"]; got != want {
					t.Fatalf("%s %s = %v want%v", op, group, got, want)
				}
			}
		})
	}
}
func TestInvalidPolicySavedViaMCPAndHTTP(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	a := &App{}
	for _, policy := range []any{map[string]any{"operation": "typo"}, map[string]any{"bucket": "fortnight"}, map[string]any{"operation": "sum", "value": "Inf"}, map[string]any{"dimensions": []any{"props..id"}}, map[string]any{"timezone": "Mars/Olympus"}} {
		if _, err := a.toolEventSpecUpsert(ctx, map[string]any{"app": "a", "topic": "t", "ingest_mode": "upsert", "upsert_policy": policy}); err == nil {
			t.Errorf("accepted %#v", policy)
		}
	}
}
func TestSpecConcurrentPatchesAndTemplateCreate(t *testing.T) {
	db := testDashboardDB(t)
	spec, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", ValidationMode: "reject", Properties: []EventPropertySpec{{Key: "props.x", Required: true}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	stale := *spec
	spec.Description = "new"
	if _, err = upsertEventSpec(db, *spec, false); err != nil {
		t.Fatal(err)
	}
	stale.Description = "stale"
	if _, err = upsertEventSpec(db, stale, false); err == nil {
		t.Fatal("stale overwrite accepted")
	}
	if _, err = upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t"}, true); err == nil {
		t.Fatal("template replacement accepted")
	}
	got, _ := getEventSpec(db, "p1", "a", "t")
	if len(got.Properties) != 1 || got.ValidationMode != "reject" || got.Description != "new" {
		t.Fatalf("spec corrupted: %#v", got)
	}
}
func TestObjectiveIdentityRetirementAndNoData(t *testing.T) {
	db := testDashboardDB(t)
	o, err := createObjective(db, "p1", objectiveFixture("A", "latest"))
	if err != nil {
		t.Fatal(err)
	}
	oldID := o.Targets[0].ID
	target := o.Targets[0]
	in := objectiveFixture("A", "latest")
	in.Targets = []ObjectiveTarget{target}
	in.Targets[0].TargetValue = 11
	o, err = updateObjective(db, "p1", o.ID, in)
	if err != nil || o.Targets[0].ID != oldID {
		t.Fatalf("identity lost: %#v %v", o, err)
	}
	p := measureObjectiveTarget(db, "p1", o.Targets[0], true)
	if p.Status != "no_data" || p.ActualValue != nil || p.Achieved {
		t.Fatalf("no-data state %#v", p)
	}
	in.Targets[0].ID = 0
	in.Targets[0].Name = "Replacement"
	o, err = updateObjective(db, "p1", o.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Targets) != 1 || o.Targets[0].ID == oldID {
		t.Fatalf("retired target reused %#v", o.Targets)
	}
	if err = validateDashboardGoalLinks(db, "p1", map[string]any{"objective_target_ids": []int64{oldID}}); err == nil {
		t.Fatal("retired link accepted")
	}
	other, err := createObjective(db, "p2", objectiveFixture("Other", "count"))
	if err != nil {
		t.Fatal(err)
	}
	in.Targets[0].ID = other.Targets[0].ID
	if _, err = updateObjective(db, "p1", o.ID, in); err == nil {
		t.Fatal("foreign target accepted")
	}
}
func TestMoneyDecimalRoundingOverflowAndRevisions(t *testing.T) {
	for _, c := range []struct {
		amount string
		want   int64
	}{{"1.005", 101}, {"-1.005", -101}, {"0.004", 0}, {"92233720368547758.07", 9223372036854775807}} {
		got, err := convertMinor(c.amount, moneyRateUse{Rate: 1}, 2, 2, "major")
		if err != nil || got != c.want {
			t.Errorf("%s => %d %v", c.amount, got, err)
		}
	}
	if _, err := convertMinor("92233720368547758.08", moneyRateUse{Rate: 1}, 2, 2, "major"); err == nil {
		t.Fatal("overflow accepted")
	}
	db := testDashboardDB(t)
	first, err := upsertFXRate(db, "p1", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: 1000, Rate: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	next, err := upsertFXRate(db, "p1", FXRate{BaseCurrency: "USD", QuoteCurrency: "EUR", AsOf: 1000, Rate: 0.6})
	if err != nil || next.RevisionID == first.RevisionID {
		t.Fatal("revision not created", err)
	}
	var value float64
	if err = db.QueryRow(`SELECT rate FROM fx_rate_revisions WHERE id=?`, first.RevisionID).Scan(&value); err != nil || value != 0.5 {
		t.Fatal("history changed", value, err)
	}
	if _, err = db.Exec(`UPDATE fx_rate_revisions SET rate=9`); err == nil {
		t.Fatal("revision mutable")
	}
	if currencyMinorDigits("CLF") != 4 || currencyMinorDigits("UYW") != 4 {
		t.Fatal("missing minor units")
	}
}
func TestGridPreservesMissingObservationsAndBounds(t *testing.T) {
	db := testDashboardDB(t)
	for _, ts := range []int64{60000, 600000} {
		if _, err := insertEvent(db, EventInsert{TS: ts, ProjectID: "p1", App: "a", Topic: "t", Props: `{"x":2}`}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := seriesForWidget(db, Filter{ProjectID: "p1", Since: 60000, Until: 660000}, "minute", "props.x", "", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 || rows[1]["value"] != nil || rows[9]["value"] != float64(2) {
		t.Fatalf("missing gap %#v", rows)
	}
	counts, err := seriesForWidget(db, Filter{ProjectID: "p1", Since: 60000, Until: 660000}, "minute", "", "", "count")
	if err != nil || counts[1]["count"] != int64(0) {
		t.Fatal("count fill", err, counts)
	}
	rows, err = seriesForWidget(db, Filter{ProjectID: "p1", Since: 1, Until: time.Now().UnixMilli()}, "minute", "", "", "count")
	if err != nil || len(rows) > 1000 {
		t.Fatal("unbounded grid", len(rows), err)
	}
}
func TestCaptureInboxRestartAndOverflowVisibility(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	captureMu.Lock()
	old := captureCfg
	oldProjects := captureProjects
	captureProjects = nil
	captureCfg = captureConfig{Enabled: true, Mode: "all", SampleRate: 1}
	captureMu.Unlock()
	defer func() { captureMu.Lock(); captureCfg = old; captureProjects = oldProjects; captureMu.Unlock() }()
	raw := `{"seq":50,"app":"a","project_id":"p1","topic":"t","time":"2026-01-01T00:00:00Z","data":{}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "data: "+raw) }))
	defer server.Close()
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER failed_write BEFORE INSERT ON events BEGIN SELECT RAISE(FAIL,'temporary'); END`); err != nil {
		t.Fatal(err)
	}
	cursor := uint64(10)
	_ = streamFirehose(ctx, server.URL, "test", &cursor)
	if cursor != 10 {
		t.Fatal("cursor advanced on failure")
	}
	if _, err := ctx.AppDB().Exec(`DROP TRIGGER failed_write`); err != nil {
		t.Fatal(err)
	}
	if err := replayCaptureInbox(ctx); err != nil {
		t.Fatal(err)
	}
	_ = streamFirehose(ctx, server.URL, "test", &cursor)
	n, err := countEvents(ctx.AppDB(), Filter{ProjectID: "p1"})
	if err != nil || n != 1 {
		t.Fatal("replay duplicate", n, err)
	}
	var gaps int
	if err = ctx.AppDB().QueryRow(`SELECT gaps FROM capture_state`).Scan(&gaps); err != nil || gaps == 0 {
		t.Fatal("gap invisible", gaps, err)
	}
}
func TestCollectorRetryOriginAndURLPrivacy(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	wk, err := createWriteKey(ctx.AppDB(), "site", "p1", []string{"https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"k": {wk.Key}, "eid": {"delivery-one"}, "sid": {"session"}, "url": {"https://example.com/reset?token=secret#private"}, "path": {"/reset?token=secret"}}
	a := &App{}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/collect?"+q.Encode(), nil)
		r.Header.Set("Origin", "https://example.com")
		rr := httptest.NewRecorder()
		a.handleCollect(rr, r)
		if rr.Header().Get("X-Apa") != "ok" {
			t.Fatal(rr.Header())
		}
	}
	rows, err := queryRows(ctx.AppDB(), Filter{ProjectID: "p1"}, 10)
	if err != nil || len(rows) != 1 {
		t.Fatal("retry duplicated", len(rows), err)
	}
	if strings.Contains(string(rows[0].Props), "secret") {
		t.Fatal("URL leaked secret")
	}
	for _, origin := range []string{"", "http://example.com", "https://example.com:8080", "https://evil.example"} {
		r := httptest.NewRequest("GET", "/collect", nil)
		r.Header.Set("Origin", origin)
		if originAllowed(wk, r) {
			t.Errorf("accepted origin %q", origin)
		}
	}
}
func TestReferencePaginationAndPatchMetadata(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertReferenceSet(db, ReferenceSet{ProjectID: "p1", Key: "sites"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 207; i++ {
		_, err = upsertReferenceValue(db, "p1", "sites", ReferenceValue{Value: fmt.Sprint(i), Status: "inactive", Metadata: json.RawMessage(`{"keep":true}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := referencePage(db, "p1", "sites", "inactive", "", 0, 200)
	if err != nil || page["total"] != int64(207) || page["has_more"] != true {
		t.Fatal(page, err)
	}
	next, err := referencePage(db, "p1", "sites", "inactive", "", page["next_cursor"].(int64), 200)
	if err != nil || next["count"] != 7 || next["has_more"] != false {
		t.Fatal(next, err)
	}
	v, err := upsertReferenceValue(db, "p1", "sites", ReferenceValue{Value: "0", Label: "edited"})
	if err != nil || v.Status != "inactive" || string(v.Metadata) != `{"keep":true}` {
		t.Fatal(v, err)
	}
	for _, raw := range []string{`[]`, `null`, `"x"`, `bad`} {
		if _, err = upsertReferenceValue(db, "p1", "sites", ReferenceValue{Value: "0", Metadata: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted metadata %s", raw)
		}
	}
}
func TestRetentionIsOptInScopedAndRecoverable(t *testing.T) {
	db := testDashboardDB(t)
	now := time.Now().UnixMilli()
	for _, project := range []string{"p1", "p2"} {
		if _, err := insertEvent(db, EventInsert{ProjectID: project, App: "a", Topic: "t", TS: now - 40*86400000, Props: `{"preserve":true}`}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := pruneProject(context.Background(), db, "p1", now); err != nil || n != 0 {
		t.Fatal("default pruned", n, err)
	}
	if _, err := db.Exec(`INSERT INTO retention_policy VALUES('p1',30,30,30)`); err != nil {
		t.Fatal(err)
	}
	if n, err := pruneProject(context.Background(), db, "p1", now); err != nil || n != 1 {
		t.Fatal("expiry", n, err)
	}
	var raw string
	if err := db.QueryRow(`SELECT payload FROM event_archive WHERE project_id='p1'`).Scan(&raw); err != nil || !strings.Contains(raw, "preserve") {
		t.Fatal("archive lost", raw, err)
	}
	n, err := countEvents(db, Filter{ProjectID: "p2"})
	if err != nil || n != 1 {
		t.Fatal("cross-project prune", n, err)
	}
}
func TestLegacyKeysAndForeignKeyIntegrity(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{ProjectID: "p1", App: "a", Topic: "t", IngestMode: "upsert", UpsertPolicy: &EventIngestPolicy{Bucket: "day", Operation: "sum", ValueKey: "props.total", OutputProperty: "total", Dimensions: []string{"props.site"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO events(ts,app,topic,project_id,source,upsert_key,props) VALUES(1000,'a','t','p1','track','p1|a|t|day=1970-01-01|props.site=x','{"site":"x","total":10}')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgradeLegacyPolicyKeys(db); err != nil {
		t.Fatal(err)
	}
	if err = upgradeLegacyPolicyKeys(db); err != nil {
		t.Fatal("not idempotent", err)
	}
	if _, err = insertEvent(db, EventInsert{TS: 2000, App: "a", Topic: "t", ProjectID: "p1", Props: `{"site":"x","total":5}`}); err != nil {
		t.Fatal(err)
	}
	rows, err := queryRows(db, Filter{ProjectID: "p1"}, 10)
	if err != nil || len(rows) != 1 || propsObject(string(rows[0].Props))["total"] != float64(15) {
		t.Fatal("legacy duplicate", rows, err)
	}
	r, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Next() {
		t.Fatal("foreign key violation")
	}
}
func TestReadSnapshotDoesNotBlockWriterAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err = writer.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE events(ts INTEGER,app TEXT,topic TEXT,project_id TEXT,source TEXT,props TEXT)`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = countEvents(tx, Filter{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := writer.Exec(`INSERT INTO events VALUES(1000,'a','t','p1','track','{}')`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read snapshot blocked writer")
	}
	if n, _ := countEvents(tx, Filter{}); n != 0 {
		t.Fatal("snapshot changed", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = countEvents(contextualDB{db: reader, ctx: ctx}, Filter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
}
func TestRequestScopeAndPayloadBoundaries(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	r := httptest.NewRequest("GET", "/summary?project_id=forged", nil)
	if _, err := requestProjectID(r); err == nil {
		t.Fatal("untrusted project accepted")
	}
	r.Header.Set(trustedProjectHeader, "p1")
	if _, err := requestProjectID(r); err == nil {
		t.Fatal("project override accepted")
	}
	rr := httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/event-specs?project_id=p1", strings.NewReader(`{"topic":"x","description":"`+strings.Repeat("x", 600000)+`"}`))
	r.Header.Set(trustedProjectHeader, "p1")
	r.Header.Set("X-User-ID", "u1")
	boundedHandler((&App{}).handleEventSpecs)(rr, r)
	if rr.Code != 400 {
		t.Fatal("oversized body accepted", rr.Code)
	}
}
func TestUpgradeFromPopulatedReleaseDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(`PRAGMA foreign_keys=ON`)
	paths, _ := filepath.Glob("migrations/*.sql")
	for i, path := range paths {
		if i == 10 {
			_, err = db.Exec(`INSERT INTO events(ts,app,topic,project_id,source,props) VALUES(1000,'a','t','p1','track','{"x":1}')`)
			if err != nil {
				t.Fatal(err)
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if n, err := countEvents(db, Filter{ProjectID: "p1"}); err != nil || n != 1 {
		t.Fatal("upgrade lost data", n, err)
	}
	var integrity string
	if err = db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
}
