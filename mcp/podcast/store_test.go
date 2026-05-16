package main

import (
	"database/sql"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// freshDB spins up an in-memory SQLite with FK enforcement on (the
// platform runtime sets this; tests must too for cascade coverage),
// applies the v0.1 schema, and pins the pool to one connection so
// :memory: stays a single database.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	wd, _ := os.Getwd()
	schema, err := os.ReadFile(filepath.Join(wd, "migrations", "001_init.sql"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"My Great Show":       "my-great-show",
		"  Trailing  Spaces ": "trailing-spaces",
		"Already-Slugged":     "already-slugged",
		"Episode #1: Launch!": "episode-1-launch",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if slugify("") == "" {
		t.Error("slugify(\"\") should fall back to a non-empty slug")
	}
}

func TestShowCRUD(t *testing.T) {
	db := freshDB(t)

	show, err := dbInsertShow(db, map[string]any{
		"title":       "The Apteva Hour",
		"description": "A show about agents.",
		"author":      "Marco",
		"explicit":    true,
	}, "proj-1")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if show.Slug != "the-apteva-hour" {
		t.Errorf("slug = %q, want the-apteva-hour", show.Slug)
	}
	if !show.Explicit || show.Language != "en" || show.PodcastType != "episodic" {
		t.Errorf("defaults not applied: %+v", show)
	}

	// Duplicate slug in the same project is rejected.
	if _, err := dbInsertShow(db, map[string]any{"title": "The Apteva Hour"}, "proj-1"); err == nil {
		t.Error("expected duplicate-slug rejection")
	}
	// Same slug under a different project is fine.
	if _, err := dbInsertShow(db, map[string]any{"title": "The Apteva Hour"}, "proj-2"); err != nil {
		t.Errorf("cross-project slug should be allowed: %v", err)
	}

	updated, err := dbUpdateShow(db, show.ID, map[string]any{"author": "Marco S.", "explicit": false})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Author != "Marco S." || updated.Explicit {
		t.Errorf("update not applied: %+v", updated)
	}

	list, err := dbListShows(db, "proj-1", 100, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list proj-1 = %d shows (err %v), want 1", len(list), err)
	}

	if err := dbDeleteShow(db, show.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := dbGetShow(db, show.ID); err == nil {
		t.Error("expected errNotFound after delete")
	}
}

func TestEpisodeLifecycleAndCascade(t *testing.T) {
	db := freshDB(t)
	show, err := dbInsertShow(db, map[string]any{"title": "Cascade Show"}, "")
	if err != nil {
		t.Fatalf("insert show: %v", err)
	}

	ep, err := dbInsertEpisode(db, map[string]any{
		"show_id":        float64(show.ID),
		"title":          "Episode One",
		"season_number":  float64(1),
		"episode_number": float64(1),
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	if ep.Status != "draft" || ep.GUID == "" {
		t.Errorf("new episode should be draft with a guid: %+v", ep)
	}

	// Episode against a missing show is rejected.
	if _, err := dbInsertEpisode(db, map[string]any{"show_id": float64(9999), "title": "x"}); err == nil {
		t.Error("expected rejection for episode on nonexistent show")
	}

	// Publishing requires probed audio.
	if err := assertPublishable(ep); err == nil {
		t.Error("episode with no audio should not be publishable")
	}
	if err := dbSetEpisodeAudio(db, ep.ID, "42", "https://cdn.example.com/ep1.mp3", 8_200_000, 1830, "audio/mpeg"); err != nil {
		t.Fatalf("set audio: %v", err)
	}
	probed, _ := dbGetEpisode(db, ep.ID)
	if err := assertPublishable(probed); err != nil {
		t.Errorf("probed episode should be publishable: %v", err)
	}

	now := sqliteTime(time.Now())
	if err := dbSetEpisodeStatus(db, ep.ID, "published", nil, &now); err != nil {
		t.Fatalf("publish: %v", err)
	}
	pub, err := dbListPublishedEpisodes(db, show.ID)
	if err != nil || len(pub) != 1 {
		t.Fatalf("published list = %d (err %v), want 1", len(pub), err)
	}

	// FK cascade: deleting the show removes its episodes.
	if err := dbDeleteShow(db, show.ID); err != nil {
		t.Fatalf("delete show: %v", err)
	}
	if _, err := dbGetEpisode(db, ep.ID); err == nil {
		t.Error("episode should have been cascade-deleted with its show")
	}
}

func TestDueScheduled(t *testing.T) {
	db := freshDB(t)
	show, _ := dbInsertShow(db, map[string]any{"title": "Sched Show"}, "")
	ep, _ := dbInsertEpisode(db, map[string]any{"show_id": float64(show.ID), "title": "Future Ep"})
	_ = dbSetEpisodeAudio(db, ep.ID, "1", "https://cdn.example.com/a.mp3", 100, 60, "audio/mpeg")

	past := sqliteTime(time.Now().Add(-time.Hour))
	future := sqliteTime(time.Now().Add(time.Hour))

	if err := dbSetEpisodeStatus(db, ep.ID, "scheduled", &future, nil); err != nil {
		t.Fatalf("schedule future: %v", err)
	}
	if due, _ := dbListDueScheduled(db); len(due) != 0 {
		t.Errorf("future-scheduled episode should not be due, got %d", len(due))
	}
	if err := dbSetEpisodeStatus(db, ep.ID, "scheduled", &past, nil); err != nil {
		t.Fatalf("schedule past: %v", err)
	}
	if due, _ := dbListDueScheduled(db); len(due) != 1 {
		t.Errorf("past-scheduled episode should be due, got %d", len(due))
	}
}

func TestRenderFeed(t *testing.T) {
	db := freshDB(t)
	show, _ := dbInsertShow(db, map[string]any{
		"title":       "Render Test",
		"description": "Feed rendering.",
		"author":      "Marco",
		"owner_email": "marco@example.com",
		"category":    "Technology",
		"hostname":    "feeds.example.com",
	}, "")
	ep, _ := dbInsertEpisode(db, map[string]any{
		"show_id":     float64(show.ID),
		"title":       "First Episode",
		"description": "<p>Hello <b>world</b></p>",
	})
	_ = dbSetEpisodeAudio(db, ep.ID, "7", "https://cdn.example.com/ep.mp3", 5_500_000, 1200, "audio/mpeg")
	now := sqliteTime(time.Now())
	_ = dbSetEpisodeStatus(db, ep.ID, "published", nil, &now)

	eps, _ := dbListPublishedEpisodes(db, show.ID)
	body, err := renderFeed(show, eps)
	if err != nil {
		t.Fatalf("renderFeed: %v", err)
	}

	// Must be well-formed XML.
	var probe any
	if err := xml.Unmarshal(body, &probe); err != nil {
		t.Fatalf("feed is not well-formed XML: %v", err)
	}

	s := string(body)
	wants := []string{
		`<rss version="2.0"`,
		`xmlns:itunes=`,
		`<title>Render Test</title>`,
		`<itunes:author>Marco</itunes:author>`,
		`<itunes:email>marco@example.com</itunes:email>`,
		// Enclosure points at this sidecar's tracking redirect, not the raw CDN URL.
		`/e/` + ep.GUID + `"`,
		`length="5500000"`,
		`<itunes:duration>1200</itunes:duration>`,
		`<guid isPermaLink="false">` + ep.GUID + `</guid>`,
	}
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("feed missing %q\n---\n%s", w, s)
		}
	}
	// Plain <description> is HTML-stripped; full HTML rides in content:encoded CDATA.
	if !strings.Contains(s, "<description>Hello world</description>") {
		t.Errorf("expected HTML-stripped plain description, got:\n%s", s)
	}
	if !strings.Contains(s, "<![CDATA[<p>Hello <b>world</b></p>]]>") {
		t.Errorf("expected content:encoded CDATA with raw HTML, got:\n%s", s)
	}
}

func TestDownloadDedupe(t *testing.T) {
	d := &dedupeCache{seen: map[string]time.Time{}}
	const key = "1.2.3.4|UA|guid"
	if d.seenRecently(key, time.Hour) {
		t.Error("first sighting should not be a repeat")
	}
	if !d.seenRecently(key, time.Hour) {
		t.Error("second sighting inside the window should be a repeat")
	}
	if d.seenRecently(key, time.Nanosecond) {
		t.Error("sighting outside the window should not be a repeat")
	}
}
