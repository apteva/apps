package main

// Tier 1 — description columns + media_set_description tool.
//
// Covers:
//   - round-trip via setDescription / getMedia
//   - partial update: omitted fields preserved
//   - empty string explicitly clears
//   - reprobe (upsertMedia) does NOT wipe description
//   - media_search returns description fields
//   - tool handler validation
//   - notFound when file has no media row yet

import (
	"testing"
)

func sampleProbeForDesc() *Probe {
	return &Probe{
		FormatName: "mov,mp4,m4a,3gp,3g2,mj2",
		DurationMs: 5000,
		HasVideo:   true,
		Width:      320,
		Height:     240,
		VideoCodec: "h264",
		Raw:        `{}`,
	}
}

// seedRow inserts a probed media row so setDescription has something
// to update. Returns the file_id.
func seedRow(t *testing.T, ctx interface{ AppDB() any }, fileID string) {
	t.Helper()
	// Use the actual test ctx type; small wrapper because our helper
	// needs ctx.AppDB() but we don't want to pull the real type
	// signature into every callsite.
}

func TestSetDescription_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc"); err != nil {
		t.Fatal(err)
	}
	title := "Q3 board meeting"
	desc := "Recording of the quarterly board sync, ~5 minutes opener."
	alt := "talking head, wide shot"
	if _, err := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{
		Title: &title, Description: &desc, AltText: &alt,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != title {
		t.Errorf("title=%q want %q", got.Title, title)
	}
	if got.Description != desc {
		t.Errorf("description=%q want %q", got.Description, desc)
	}
	if got.AltText != alt {
		t.Errorf("alt_text=%q want %q", got.AltText, alt)
	}
}

func TestSetDescription_PartialUpdatePreservesOthers(t *testing.T) {
	// Set all three, then update just description; title + alt_text
	// must stay put.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	t1, d1, a1 := "T1", "D1", "A1"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{
		Title: &t1, Description: &d1, AltText: &a1,
	})

	d2 := "D2-new"
	if _, err := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{
		Description: &d2,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Title != t1 {
		t.Errorf("title clobbered: %q want %q", got.Title, t1)
	}
	if got.Description != d2 {
		t.Errorf("description=%q want %q", got.Description, d2)
	}
	if got.AltText != a1 {
		t.Errorf("alt_text clobbered: %q want %q", got.AltText, a1)
	}
}

func TestSetDescription_EmptyStringClears(t *testing.T) {
	// Empty string is meaningfully different from "not provided" — it
	// explicitly clears the column. Pointer-distinguishing makes this
	// possible.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	d := "to be cleared"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d})

	empty := ""
	if _, err := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &empty}); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "" {
		t.Errorf("description=%q want cleared", got.Description)
	}
}

func TestSetDescription_NoOpWithEmptyFields(t *testing.T) {
	// Calling setDescription with all nils must not error and must
	// leave the row untouched.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	d := "untouched"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d})

	if _, err := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{}); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "untouched" {
		t.Errorf("no-op call modified row: %q", got.Description)
	}
}

func TestSetDescription_ReprobeDoesNotWipeDescription(t *testing.T) {
	// The whole point of putting description on the same row is that
	// it must survive a reprobe. upsertMedia (the indexer) writes
	// only probe columns; description columns must stay untouched.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	desc := "Persistent description"
	title := "Persistent title"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{
		Title: &title, Description: &desc,
	})

	// Simulate a reprobe — the indexer detects sha changed, re-runs
	// upsertMedia. Description columns must be untouched.
	freshProbe := sampleProbeForDesc()
	freshProbe.DurationMs = 7500 // pretend something changed
	if err := upsertMedia(ctx.AppDB(), testProj, "1", freshProbe, "different-sha"); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != desc {
		t.Errorf("reprobe wiped description: %q want %q", got.Description, desc)
	}
	if got.Title != title {
		t.Errorf("reprobe wiped title: %q want %q", got.Title, title)
	}
	// And probe data did update.
	if got.DurationMs != 7500 {
		t.Errorf("reprobe didn't update probe data: %d", got.DurationMs)
	}
}

// TestSetDescription_UpsertsStubWhenNoRow — agents calling
// media_set_description on a brand-new file_id (e.g. immediately
// after media_extract_frame finishes) must NOT get a not-found
// error. setDescription upserts a stub flagged probe_status='pending'
// so the description sticks now and the indexer fills in the rest
// on its next sweep.
func TestSetDescription_UpsertsStubWhenNoRow(t *testing.T) {
	ctx := newTestCtx(t)
	d := "freshly attached"
	created, err := setDescription(ctx.AppDB(), testProj, "999",
		DescriptionFields{Description: &d})
	if err != nil {
		t.Fatalf("expected success on missing row (upsert), got %v", err)
	}
	if !created {
		t.Errorf("expected created=true on first-time write")
	}
	got, err := getMedia(ctx.AppDB(), testProj, "999")
	if err != nil {
		t.Fatalf("stub row missing: %v", err)
	}
	if got.Description != "freshly attached" {
		t.Errorf("description not persisted: %q", got.Description)
	}
	if got.ProbeStatus != "pending" {
		t.Errorf("stub should be pending, got %q (so indexer picks it up)", got.ProbeStatus)
	}
	if got.SourceSHA256 != "" {
		t.Errorf("stub should have empty sha to flag for probing, got %q", got.SourceSHA256)
	}
}

func TestSetDescription_StubSurvivesIndexerProbe(t *testing.T) {
	// Once the indexer probes the file, the description must stay
	// — upsertMedia only writes probe columns. The stub flow
	// validated end-to-end.
	ctx := newTestCtx(t)
	d := "set before probe"
	if _, err := setDescription(ctx.AppDB(), testProj, "1",
		DescriptionFields{Description: &d}); err != nil {
		t.Fatal(err)
	}
	// Indexer probes — same call upsertMedia makes after ffprobe.
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "sha-real"); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "set before probe" {
		t.Errorf("description wiped by probe: %q", got.Description)
	}
	if got.ProbeStatus != "ok" {
		t.Errorf("probe didn't update status: %q", got.ProbeStatus)
	}
	if got.DurationMs != 5000 {
		t.Errorf("probe didn't update duration: %d", got.DurationMs)
	}
}

func TestSetDescription_SecondCallDoesntRecreate(t *testing.T) {
	// First call: created=true. Second call on the same file_id:
	// created=false (just an UPDATE).
	ctx := newTestCtx(t)
	d1 := "first"
	d2 := "second"
	c1, _ := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d1})
	c2, _ := setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d2})
	if !c1 {
		t.Errorf("first write should report created=true")
	}
	if c2 {
		t.Errorf("second write should report created=false")
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "second" {
		t.Errorf("second write didn't overwrite: %q", got.Description)
	}
}

func TestSearchMedia_ReturnsDescriptionFields(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	d := "in search results"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d})

	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Description != d {
		t.Errorf("search dropped description: %q want %q", rows[0].Description, d)
	}
}

// ─── Tool handler tests ─────────────────────────────────────────────

func TestToolSetDescription_Roundtrip(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")

	out, err := app.toolSetDescription(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
		"description": "Hello world",
		"title":       "Title",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["updated"] != true {
		t.Errorf("expected updated=true: %v", out)
	}

	// Verify via media_get tool, which is how an agent would read it back.
	got, _ := app.toolGet(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	row := got.(map[string]any)["media"].(*MediaRow)
	if row.Description != "Hello world" {
		t.Errorf("description=%q", row.Description)
	}
	if row.Title != "Title" {
		t.Errorf("title=%q", row.Title)
	}
}

func TestToolSetDescription_RequiresFileID(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolSetDescription(ctx, map[string]any{
		"_project_id": testProj,
		"description": "x",
	})
	if err == nil {
		t.Fatal("expected error on missing file_id")
	}
}

func TestToolSetDescription_RequiresAtLeastOneField(t *testing.T) {
	// Passing only file_id with no fields to update is a misuse —
	// the tool returns an error rather than a silent no-op.
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleProbeForDesc(), "abc")
	_, err := app.toolSetDescription(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	if err == nil {
		t.Fatal("expected error when no fields provided")
	}
}

// TestToolSetDescription_UpsertsNewFile — what used to be a
// "not found" path now upserts a stub. Agents calling this tool
// the moment a file lands in storage (e.g. after media_extract_frame)
// get instant persistence; the indexer's next sweep fills in the
// rest of the metadata.
func TestToolSetDescription_UpsertsNewFile(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolSetDescription(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "999",
		"description": "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if found, _ := m["found"].(bool); !found {
		t.Errorf("expected found=true after upsert: %v", out)
	}
	if created, _ := m["created"].(bool); !created {
		t.Errorf("expected created=true on first-time write: %v", out)
	}
}
