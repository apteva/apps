package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

func TestOnRestoreRewritesClientURLsAndAuditInstall(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", fixture)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	const oldBase, newBase = "https://runtime.apteva.invalid/rt_old", "https://runtime.apteva.invalid/rt_new"
	if _, err := db.Exec(`INSERT INTO clients (project_id, client_id, name, type, redirect_uris, allowed_origins)
		VALUES ('rt_old', 'akc_1', 'site', 'spa', ?, '["https://runtime.apteva.invalid","https://shop.example"]'),
		       ('other', 'akc_2', 'other', 'spa', '["https://elsewhere.example/cb"]', '[]')`,
		`["`+oldBase+`/api/apps/content/callback","https://shop.example/callback"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log (project_id, event, source_install_id) VALUES ('rt_old', 'login', 5), ('rt_old', 'login', 3)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	rc := sdk.RestoreContext{ID: "auth", OldProjectID: "rt_old", NewProjectID: "rt_new",
		InstallIDs: map[int64]int64{5: 50}, PublicBaseURLs: map[string]string{oldBase: newBase}}
	restored, report, err := testkit.RunRestore(t, &App{}, fixture, rc)
	if err != nil {
		t.Fatal(err)
	}
	var project, redirects, origins string
	if err := restored.QueryRow(`SELECT project_id, redirect_uris, allowed_origins FROM clients WHERE client_id='akc_1'`).Scan(&project, &redirects, &origins); err != nil {
		t.Fatal(err)
	}
	if project != "rt_new" || redirects != `["`+newBase+`/api/apps/content/callback","https://shop.example/callback"]` {
		t.Fatalf("client after restore: %s %s", project, redirects)
	}
	if origins != `["https://runtime.apteva.invalid","https://shop.example"]` {
		t.Fatalf("origins changed: %s", origins)
	}
	if err := restored.QueryRow(`SELECT redirect_uris FROM clients WHERE client_id='akc_2'`).Scan(&redirects); err != nil || redirects != `["https://elsewhere.example/cb"]` {
		t.Fatalf("other project's client changed: %s %v", redirects, err)
	}
	rows, err := restored.Query(`SELECT source_install_id FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != 50 || ids[1] != 3 {
		t.Fatalf("audit install ids %v", ids)
	}
	if report.ProjectRows["clients"] != 1 || report.ProjectRows["audit_log"] != 2 {
		t.Fatalf("report %+v", report.ProjectRows)
	}
}
