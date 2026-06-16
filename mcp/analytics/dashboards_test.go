package main

import (
	"database/sql"
	"os"
	"path/filepath"
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
	for _, name := range []string{"001_init.sql", "004_dashboards.sql", "005_event_specs.sql"} {
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
	d, err := createDashboard(db, "p1", "Website Traffic", "", templateWidgets("website_traffic"))
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
	got, err := evaluateWidget(db, "p1", active)
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
	})
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
