package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestMediaOnlyCreate(t *testing.T) {
	db := newTestDB(t)
	created, err := createTestimonial(db, "p1", &Testimonial{
		Kind:     "video",
		MediaURL: "https://cdn.example.com/review.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.MediaURL == "" || created.Kind != "video" {
		t.Fatalf("created = %+v", created)
	}
}

func TestPublishedRequiresConsentAndPublicPermission(t *testing.T) {
	db := newTestDB(t)
	created, err := createTestimonial(db, "p1", &Testimonial{
		Quote:           "Private feedback",
		ConsentStatus:   "denied",
		PermissionScope: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setTestimonialStatus(db, "p1", created.ID, "published"); err == nil || !strings.Contains(err.Error(), "granted consent") {
		t.Fatalf("publish error = %v", err)
	}
	published, err := updateTestimonial(db, "p1", created.ID, map[string]any{
		"status": "published", "consent_status": "granted", "permission_scope": "marketing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != "published" || published.PublishedAt == "" {
		t.Fatalf("published = %+v", published)
	}
	if _, err := updateTestimonial(db, "p1", created.ID, map[string]any{"consent_status": "revoked"}); err == nil {
		t.Fatal("revoking consent on a published testimonial succeeded")
	}
}

func TestUpdatePreservesContentInvariantAndRejectsNullStrings(t *testing.T) {
	db := newTestDB(t)
	created, err := createTestimonial(db, "p1", &Testimonial{Title: "Only content"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateTestimonial(db, "p1", created.ID, map[string]any{
		"title": "", "quote": "", "body": "", "media_file_id": "", "media_url": "",
	}); err == nil {
		t.Fatal("clearing every content field succeeded")
	}
	if _, err := updateTestimonial(db, "p1", created.ID, map[string]any{"title": nil}); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("null title error = %v", err)
	}
	got, err := getTestimonial(db, "p1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Only content" {
		t.Fatalf("failed patches changed title to %q", got.Title)
	}
}

func TestExactTagPaginationAndArchivedDefault(t *testing.T) {
	db := newTestDB(t)
	for i, tag := range []string{"homepage", "pricing", "launch"} {
		if _, err := createTestimonial(db, "p1", &Testimonial{Title: "Item " + string(rune('A'+i)), Tags: []string{tag}}); err != nil {
			t.Fatal(err)
		}
	}
	partial, _, err := listTestimonialsPage(db, "p1", TestimonialFilter{Tag: "page"})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial) != 0 {
		t.Fatalf("substring tag matched: %+v", partial)
	}
	exact, _, err := listTestimonialsPage(db, "p1", TestimonialFilter{Tag: "homepage"})
	if err != nil || len(exact) != 1 {
		t.Fatalf("exact tag items=%+v err=%v", exact, err)
	}
	page, total, err := listTestimonialsPage(db, "p1", TestimonialFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || total != 3 {
		t.Fatalf("page len=%d total=%d", len(page), total)
	}
	if err := deleteTestimonial(db, "p1", exact[0].ID, false); err != nil {
		t.Fatal(err)
	}
	visible, total, err := listTestimonialsPage(db, "p1", TestimonialFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || total != 2 {
		t.Fatalf("default list len=%d total=%d", len(visible), total)
	}
	archived, _, err := listTestimonialsPage(db, "p1", TestimonialFilter{Status: "archived"})
	if err != nil || len(archived) != 1 {
		t.Fatalf("archived items=%+v err=%v", archived, err)
	}
}

func TestPublishedProjectionOmitsPrivateFields(t *testing.T) {
	rating := 5
	views := publicTestimonials([]Testimonial{{
		ID: 1, ProjectID: "secret-project", Status: "published", Title: "Safe",
		Rating: &rating, AuthorEmail: "private@example.com", MediaFileID: "internal-file",
		Metadata: map[string]any{"private": true}, Tags: []string{"homepage"},
	}})
	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, secret := range []string{"secret-project", "private@example.com", "internal-file", "metadata"} {
		if strings.Contains(body, secret) {
			t.Fatalf("public projection leaked %q: %s", secret, body)
		}
	}
}
