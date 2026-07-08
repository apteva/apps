package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "testimonials.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlBytes, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCreateListAndStatus(t *testing.T) {
	db := newTestDB(t)
	rating := 5
	created, err := createTestimonial(db, "p1", &Testimonial{
		Kind:            "review",
		Quote:           "Apteva made the launch simple.",
		Rating:          &rating,
		AuthorName:      "Alex Rivera",
		AuthorCompany:   "Acme",
		ConsentStatus:   "granted",
		PermissionScope: "marketing",
		Tags:            []string{"homepage", "launch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Status != "draft" {
		t.Fatalf("created = %+v", created)
	}
	published, err := setTestimonialStatus(db, "p1", created.ID, "published")
	if err != nil {
		t.Fatal(err)
	}
	if published.PublishedAt == "" || published.ApprovedAt == "" || published.SubmittedAt == "" {
		t.Fatalf("status timestamps not set: %+v", published)
	}
	items, err := listTestimonials(db, "p1", TestimonialFilter{PublishedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("published list = %+v", items)
	}
}

func TestDeleteArchivesByDefault(t *testing.T) {
	db := newTestDB(t)
	created, err := createTestimonial(db, "p1", &Testimonial{Quote: "Strong support."})
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteTestimonial(db, "p1", created.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := getTestimonial(db, "p1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != "archived" {
		t.Fatalf("got = %+v", got)
	}
}
