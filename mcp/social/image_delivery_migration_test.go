package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestImageDeliveryMigrationPreservesExistingHistory(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if strings.Contains(p, "023_") {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("legacy migration %s: %v", p, err)
		}
	}
	_, err = db.Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name) VALUES(9,'project','facebook',42,'Facebook'),(13,'project','tiktok',43,'TikTok'); INSERT INTO posts(id,project_id,body,media_storage_ids,status) VALUES(878,'project','existing caption','[1384]','partial'); INSERT INTO post_targets(id,post_id,social_account_id,status,platform_post_id,last_error) VALUES(949,878,9,'published','122127173174079868',NULL),(950,878,13,'failed',NULL,'TikTok publish failed: picture_size_check_failed');`)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/023_image_delivery.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var raw, identity, status, diagnostics string
	if err = db.QueryRow(`SELECT media_storage_ids FROM posts WHERE id=878`).Scan(&raw); err != nil || raw != "[1384]" {
		t.Fatalf("source changed: %s %v", raw, err)
	}
	if err = db.QueryRow(`SELECT status,platform_post_id,media_delivery_diagnostics FROM post_targets WHERE id=949`).Scan(&status, &identity, &diagnostics); err != nil || status != "published" || identity != "122127173174079868" || diagnostics != "[]" {
		t.Fatalf("Facebook history changed: %s %s %s %v", status, identity, diagnostics, err)
	}
	if err = db.QueryRow(`SELECT status FROM post_targets WHERE id=950`).Scan(&status); err != nil || status != "failed" {
		t.Fatal("migration revived failed target")
	}
}
