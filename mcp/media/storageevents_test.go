package main

// Tier 1 — cascadeDeleteOne (the SQL helper) and the event-payload
// dispatcher. The full SSE wire path (connectAndStream + reconnect
// loop) is harder to unit-test cleanly without an httptest server
// per test; covered by the cascadeDeleteOne path which is the
// load-bearing part of the work.

import (
	"testing"
)

func TestCascadeDeleteOne_DeletesAll(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 100, 320, 240)
	upsertDerivation(ctx.AppDB(), testProj, "1", "waveform", 101, 800, 100)
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "x",
	})

	if err := cascadeDeleteOne(ctx.AppDB(), testProj, "1"); err != nil {
		t.Fatal(err)
	}

	if _, err := getMedia(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("media row should be gone")
	}
	derivs, _ := listDerivations(ctx.AppDB(), testProj, "1")
	if len(derivs) != 0 {
		t.Errorf("derivations should be cascade-deleted: %v", derivs)
	}
	if _, err := getTranscript(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("transcript should be cascade-deleted")
	}
}

func TestCascadeDeleteOne_NoOpOnMissing(t *testing.T) {
	// Storage might emit file.deleted for a row we don't have
	// catalogued (e.g. non-media file). Cascade should be a clean
	// no-op rather than erroring.
	ctx := newTestCtx(t)
	if err := cascadeDeleteOne(ctx.AppDB(), testProj, "9999"); err != nil {
		t.Errorf("cascade on missing file_id should not error: %v", err)
	}
}

func TestCascadeDeleteOne_OtherProjectUntouched(t *testing.T) {
	// Cross-tenant safety: deleting file_id 1 in project A must
	// not touch project B's row with the same file_id.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-a")
	upsertMedia(ctx.AppDB(), "other-proj", "1", sampleAudioProbe(), "sha-b")

	if err := cascadeDeleteOne(ctx.AppDB(), testProj, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := getMedia(ctx.AppDB(), "other-proj", "1"); err != nil {
		t.Errorf("other-project row vanished: %v", err)
	}
}
