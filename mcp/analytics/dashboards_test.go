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
