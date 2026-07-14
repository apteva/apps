package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTemplateSummariesExcludeBodies(t *testing.T) {
	db := testDB(t)
	id, err := createTemplate(db, &Template{Slug: "lead-guide", Name: "Lead Guide", Body: strings.Repeat("content ", 100)})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := listTemplateSummaries(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Body != "" || len(summaries[0].VariablesJSON) != 0 {
		t.Fatalf("unexpected summaries: %+v", summaries)
	}
	full, err := getTemplate(db, id, "")
	if err != nil || full == nil || full.Body == "" {
		t.Fatalf("full template = %+v, err = %v", full, err)
	}
}

func TestTemplateValidationAndStrictUpdates(t *testing.T) {
	db := testDB(t)
	if _, err := createTemplate(db, &Template{Slug: "Not Valid", Name: "Bad", Body: "# Body"}); err == nil {
		t.Fatal("invalid slug should fail")
	}
	id, err := createTemplate(db, &Template{Slug: "valid", Name: "Valid", Body: "# Body"})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateTemplate(db, id, map[string]any{"id": id, "name": "Changed"}); err == nil {
		t.Fatal("unknown update field should fail")
	}
	full, _ := getTemplate(db, id, "")
	if full.Name != "Valid" {
		t.Fatalf("failed update mutated row: %+v", full)
	}
}

func TestRenderListUsesParsedTimePaginationAndSummaries(t *testing.T) {
	db := testDB(t)
	id, err := createTemplate(db, &Template{Slug: "audit", Name: "Audit", Body: "# Body"})
	if err != nil {
		t.Fatal(err)
	}
	for index, renderedAt := range []string{"2026-01-01 09:00:00", "2026-01-02 09:00:00", "2026-01-03 09:00:00"} {
		_, err := db.Exec(`INSERT INTO renders
			(template_id, template_slug, output_file_id, data_snapshot, rendered_at)
			VALUES (?, 'audit', ?, '{"secret":"value"}', ?)`, id, index+1, renderedAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := listRenders(db, RenderFilters{Since: "2026-01-01T10:00:00Z", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].OutputFileID != "2" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(rows[0].DataSnapshot) != 0 {
		t.Fatalf("list leaked data snapshot: %s", rows[0].DataSnapshot)
	}
	if _, err := listRenders(db, RenderFilters{Since: "not-a-time"}); err == nil {
		t.Fatal("invalid since should fail")
	}
}
