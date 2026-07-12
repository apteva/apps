package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDashboardDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"001_init.sql", "004_dashboards.sql", "005_event_specs.sql", "006_dashboard_config.sql", "007_integrity_performance.sql"} {
		b, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatalf("exec migration %s: %v", name, err)
		}
	}
	return db
}

func TestDashboardNumericAggregationRejectsTextValues(t *testing.T) {
	db := testDashboardDB(t)
	if _, err := insertEvent(db, EventInsert{
		TS: time.Now().UnixMilli(), App: "patreon", Topic: "revenue", ProjectID: "p1", Source: "test", Props: `{"amount":"$372"}`,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := evaluateWidget(db, "p1", DashboardWidget{
		Type: "stat", Config: map[string]any{"app": "patreon", "topic": "revenue", "window": "all", "value": "props.amount"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "non-numeric") {
		t.Fatalf("numeric aggregation error = %v, want non-numeric error", err)
	}
}

func TestDashboardCompositeIndexMigration(t *testing.T) {
	db := testDashboardDB(t)
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='ix_events_project_app_topic_ts'`).Scan(&name); err != nil {
		t.Fatalf("composite dashboard index missing: %v", err)
	}
}

func TestIntegrityMigrationUpgradesExistingDateSpecsAndUSDWidgets(t *testing.T) {
	db := testDashboardDB(t)
	spec, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "patreon", Topic: "daily_earnings_snapshot", IngestMode: "upsert",
		UpsertPolicy: &EventIngestPolicy{Bucket: "day", Operation: "replace", Value: "props.amount"},
		Properties:   []EventPropertySpec{{Key: "props.date", Type: "string", Required: true}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_specs SET upsert_policy='{"bucket":"day","operation":"replace"}' WHERE id=?`, spec.ID); err != nil {
		t.Fatal(err)
	}
	dashboard, err := createDashboard(db, "p1", "Finance", "", nil, []DashboardWidget{{
		Type: "stat", Title: "Revenue / MRR Proxy", Config: map[string]any{"app": "patreon", "value": "props.mrr_proxy"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("migrations", "007_integrity_performance.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("reapply integrity migration: %v", err)
	}
	upgraded, err := getEventSpecByID(db, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.UpsertPolicy == nil || upgraded.UpsertPolicy.TimestampProperty != "props.date" {
		t.Fatalf("upgraded policy=%#v want props.date timestamp", upgraded.UpsertPolicy)
	}
	dashboard, err = getDashboard(db, dashboard.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Widgets[0].Config["format"] != "currency" || dashboard.Widgets[0].Config["currency"] != "USD" {
		t.Fatalf("upgraded widget config=%#v want USD currency", dashboard.Widgets[0].Config)
	}
}

func TestWebsiteTrafficTemplateEvaluatesActiveSessions(t *testing.T) {
	db := testDashboardDB(t)
	now := time.Now().UnixMilli()
	for _, sid := range []string{"s1", "s2", "s1"} {
		if _, err := insertEvent(db, EventInsert{
			TS:        now,
			App:       "site",
			Topic:     "page_view",
			ProjectID: "p1",
			SessionID: sid,
			Source:    "web",
			Props:     `{"path":"/","device":"desktop"}`,
		}); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	d, err := createDashboard(db, "p1", "Website Traffic", "", templateDashboardConfig("website_traffic"), templateWidgets("website_traffic"))
	if err != nil {
		t.Fatalf("create dashboard: %v", err)
	}
	var active DashboardWidget
	for _, w := range d.Widgets {
		if w.Title == "Active Sessions" {
			active = w
			break
		}
	}
	if active.Type == "" {
		t.Fatal("missing Active Sessions widget")
	}
	got, err := evaluateWidget(db, "p1", active, nil)
	if err != nil {
		t.Fatalf("evaluate widget: %v", err)
	}
	if got["value"] != int64(2) {
		t.Fatalf("active sessions = %v, want 2", got["value"])
	}
}

func TestTimeseriesWidgetCanSumNumericProperty(t *testing.T) {
	db := testDashboardDB(t)
	events := []struct {
		ts    int64
		props string
	}{
		{time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC).UnixMilli(), `{"views":10,"post_id":"a"}`},
		{time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC).UnixMilli(), `{"views":15,"post_id":"b"}`},
		{time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC).UnixMilli(), `{"views":7,"post_id":"a"}`},
	}
	for _, ev := range events {
		if _, err := insertEvent(db, EventInsert{
			TS:        ev.ts,
			App:       "patreon",
			Topic:     "post_views_daily_observed",
			ProjectID: "p1",
			Source:    "api",
			Props:     ev.props,
		}); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	got, err := evaluateWidget(db, "p1", DashboardWidget{
		Type:   "timeseries",
		Title:  "Daily Views",
		Config: map[string]any{"app": "patreon", "topic": "post_views_daily_observed", "window": "all", "interval": "day", "value": "props.views"},
	}, nil)
	if err != nil {
		t.Fatalf("evaluate widget: %v", err)
	}
	series := got["series"].([]map[string]any)
	if len(series) != 2 {
		t.Fatalf("series len = %d, want 2: %#v", len(series), series)
	}
	if series[0]["bucket"] != "2026-06-15" || series[0]["count"] != int64(2) || series[0]["value"] != 25.0 {
		t.Fatalf("first bucket = %#v, want 2026-06-15 count 2 value 25", series[0])
	}
	if series[1]["bucket"] != "2026-06-16" || series[1]["count"] != int64(1) || series[1]["value"] != 7.0 {
		t.Fatalf("second bucket = %#v, want 2026-06-16 count 1 value 7", series[1])
	}
}

func TestWidgetFilterPlaceholders(t *testing.T) {
	db := testDashboardDB(t)
	ts := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC).UnixMilli()
	for _, ev := range []EventInsert{
		{TS: ts, App: "patreon", Topic: "daily_traffic_snapshot", ProjectID: "p1", Source: "seed", Props: `{"page_id":"a","total_views":10}`},
		{TS: ts, App: "patreon", Topic: "daily_traffic_snapshot", ProjectID: "p1", Source: "seed", Props: `{"page_id":"b","total_views":30}`},
	} {
		if _, err := insertEvent(db, ev); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	widget := DashboardWidget{
		Type: "stat",
		Config: map[string]any{
			"app":    "patreon",
			"topic":  "daily_traffic_snapshot",
			"window": "all",
			"value":  "props.total_views",
			"where":  map[string]any{"props.page_id": "$filters.page_id"},
		},
	}
	got, err := evaluateWidget(db, "p1", widget, map[string]any{"page_id": "a"})
	if err != nil {
		t.Fatalf("evaluate filtered widget: %v", err)
	}
	if got["value"] != 10.0 {
		t.Fatalf("filtered value = %v, want 10", got["value"])
	}
	got, err = evaluateWidget(db, "p1", widget, map[string]any{"page_id": "all"})
	if err != nil {
		t.Fatalf("evaluate all widget: %v", err)
	}
	if got["value"] != 40.0 {
		t.Fatalf("all value = %v, want 40", got["value"])
	}
}

func TestDashboardFilterOptions(t *testing.T) {
	db := testDashboardDB(t)
	ts := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC).UnixMilli()
	for _, ev := range []EventInsert{
		{TS: ts, App: "patreon", Topic: "daily_traffic_snapshot", ProjectID: "p1", Source: "seed", Props: `{"page_id":"a","page_name":"Page A","total_views":10}`},
		{TS: ts, App: "patreon", Topic: "daily_traffic_snapshot", ProjectID: "p1", Source: "seed", Props: `{"page_id":"b","page_name":"Page B","total_views":30}`},
		{TS: ts, App: "patreon", Topic: "daily_traffic_snapshot", ProjectID: "p1", Source: "seed", Props: `{"page_id":"a","page_name":"Page A","total_views":12}`},
	} {
		if _, err := insertEvent(db, ev); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	options, err := dashboardFilterOptions(db, "p1", map[string]any{
		"source": map[string]any{
			"app":         "patreon",
			"topic":       "daily_traffic_snapshot",
			"value_field": "props.page_id",
			"label_field": "props.page_name",
		},
	})
	if err != nil {
		t.Fatalf("filter options: %v", err)
	}
	if len(options) != 2 {
		t.Fatalf("options len = %d, want 2: %#v", len(options), options)
	}
	if options[0]["value"] != "a" || options[0]["label"] != "Page A" || options[0]["count"] != int64(2) {
		t.Fatalf("first option = %#v, want Page A count 2", options[0])
	}
}
