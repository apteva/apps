package main

// Tier 1 — transcripts DB layer + tool handlers + Deepgram response
// parser. The live integration call (PlatformAPI.ExecuteIntegrationTool
// → Deepgram) isn't covered here because it needs a real API key and
// burns money; an isolated unit test on parseDeepgramResponse stands
// in for the response-handling half, and a Tier 2 test (deferred —
// would require a Deepgram-stub integration in the testkit) would
// cover the call wiring.

import (
	"encoding/json"
	"errors"
	"testing"
)

func sampleAVProbe(durationMs int64) *Probe {
	return &Probe{
		FormatName: "mov,mp4,m4a,3gp,3g2,mj2",
		DurationMs: durationMs,
		HasVideo:   true,
		HasAudio:   true,
		Width:      320, Height: 240,
		VideoCodec: "h264",
		AudioCodec: "aac",
		Raw:        `{}`,
	}
}

func sampleVideoOnlyProbe() *Probe {
	return &Probe{
		FormatName: "mp4",
		DurationMs: 5000,
		HasVideo:   true,
		HasAudio:   false,
		Width:      320, Height: 240,
		VideoCodec: "h264",
		Raw:        `{}`,
	}
}

// ─── DB layer ───────────────────────────────────────────────────────

func TestInsertPendingTranscript_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto"); err != nil {
		t.Fatal(err)
	}
	tr, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Status != "pending" {
		t.Errorf("status=%q want pending", tr.Status)
	}
	if tr.SourceKind != "auto" {
		t.Errorf("source_kind=%q want auto", tr.SourceKind)
	}
}

func TestInsertPendingTranscript_RequiresIDs(t *testing.T) {
	ctx := newTestCtx(t)
	if err := insertPendingTranscript(ctx.AppDB(), "", "1", "auto"); err == nil {
		t.Error("expected error when project_id empty")
	}
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "", "auto"); err == nil {
		t.Error("expected error when file_id empty")
	}
}

func TestInsertPendingTranscript_IdempotentOnRunningRow(t *testing.T) {
	// Re-queueing a file that's currently running shouldn't reset it.
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	_, _ = claimNextPendingTranscript(ctx.AppDB()) // → running

	// A second auto-queue must not flip status back to pending.
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto"); err != nil {
		t.Fatal(err)
	}
	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "running" {
		t.Errorf("re-queue clobbered running row: %q", tr.Status)
	}
}

func TestInsertPendingTranscript_RetriesFailedRow(t *testing.T) {
	// Failed rows should re-enter the queue when re-inserted; failure
	// is supposed to be retryable.
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	_, _ = claimNextPendingTranscript(ctx.AppDB())
	_ = transcriptMarkFailed(ctx.AppDB(), "1", "boom")

	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "manual"); err != nil {
		t.Fatal(err)
	}
	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "pending" {
		t.Errorf("expected re-queue from failed → pending, got %q", tr.Status)
	}
}

func TestClaimNextPendingTranscript_Atomic(t *testing.T) {
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	insertPendingTranscript(ctx.AppDB(), testProj, "2", "auto")

	a, err := claimNextPendingTranscript(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	b, err := claimNextPendingTranscript(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if a.FileID == b.FileID {
		t.Errorf("same row claimed twice: %q", a.FileID)
	}
	if a.Status != "running" || b.Status != "running" {
		t.Errorf("claims not running: %q %q", a.Status, b.Status)
	}
}

func TestClaimNextPendingTranscript_Empty(t *testing.T) {
	ctx := newTestCtx(t)
	_, err := claimNextPendingTranscript(ctx.AppDB())
	if !isNoRows(err) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestTranscriptMarkOk_PersistsFields(t *testing.T) {
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	_, _ = claimNextPendingTranscript(ctx.AppDB())

	segs, _ := formatSegments([]TranscriptSegment{
		{StartMs: 0, EndMs: 1500, Text: "Hello world"},
		{StartMs: 1500, EndMs: 3500, Text: "Second segment"},
	})
	err := transcriptMarkOk(ctx.AppDB(), &TranscriptRow{
		FileID:       "1",
		ProjectID:    testProj,
		SourceSHA256: "abc",
		Language:     "en",
		Text:         "Hello world. Second segment.",
		Segments:     segs,
		Provider:     "deepgram",
		Model:        "nova-3",
		DurationMs:   3500,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if got.Status != "ok" {
		t.Errorf("status=%q", got.Status)
	}
	if got.Text != "Hello world. Second segment." {
		t.Errorf("text=%q", got.Text)
	}
	if got.Language != "en" || got.Provider != "deepgram" || got.Model != "nova-3" {
		t.Errorf("metadata not persisted: %+v", got)
	}
	if len(got.Segments) == 0 {
		t.Error("segments not persisted")
	}
}

func TestTranscriptMarkOk_GuardsNonRunning(t *testing.T) {
	// Marking ok on a row that's still pending should not flip it —
	// only running rows graduate. Catches bugs where a worker writes
	// to a row it never claimed.
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	_ = transcriptMarkOk(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "x"})
	got, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if got.Status == "ok" {
		t.Errorf("guard breached: status=%q", got.Status)
	}
}

func TestTranscriptMarkSkipped_PullsOutOfQueue(t *testing.T) {
	ctx := newTestCtx(t)
	insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto")
	if err := transcriptMarkSkipped(ctx.AppDB(), "1", "too long"); err != nil {
		t.Fatal(err)
	}
	got, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if got.Status != "skipped" {
		t.Errorf("status=%q", got.Status)
	}
	if got.Error != "too long" {
		t.Errorf("error=%q", got.Error)
	}
}

func TestTranscribeCandidates_OnlyAudio(t *testing.T) {
	// Files without audio must not be candidates. Files with audio
	// AND no transcript yet should be picked.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "audio-1", sampleAVProbe(5000), "sha-a")
	upsertMedia(ctx.AppDB(), testProj, "video-only", sampleVideoOnlyProbe(), "sha-b")
	upsertMedia(ctx.AppDB(), testProj, "audio-2", sampleAVProbe(5000), "sha-c")

	cands, err := transcribeCandidates(ctx.AppDB(), testProj, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c] = true
	}
	if !got["audio-1"] || !got["audio-2"] {
		t.Errorf("missing audio candidates: %v", cands)
	}
	if got["video-only"] {
		t.Errorf("video-only file picked: %v", cands)
	}
}

func TestTranscribeCandidates_SkipsAlreadyTranscribed(t *testing.T) {
	// A file that already has an ok transcript with matching sha
	// shouldn't reappear. A drifted sha should re-queue it.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(5000), "sha-A")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAVProbe(5000), "sha-B")

	// 1 gets a transcript with matching sha.
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, SourceSHA256: "sha-A", Text: "x",
	})
	// 2 gets a transcript with stale sha (source has been re-uploaded).
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "2", ProjectID: testProj, SourceSHA256: "stale-sha", Text: "x",
	})

	cands, err := transcribeCandidates(ctx.AppDB(), testProj, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c] = true
	}
	if got["1"] {
		t.Errorf("already-transcribed file returned as candidate: %v", cands)
	}
	if !got["2"] {
		t.Errorf("sha-drift file not returned as candidate: %v", cands)
	}
}

func TestUpsertTranscript_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(5000), "sha")

	err := upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID:    "1",
		ProjectID: testProj,
		Text:      "A manual transcript.",
		Language:  "en",
		Provider:  "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if got.Status != "ok" || got.Text != "A manual transcript." {
		t.Errorf("upsert didn't land: %+v", got)
	}
}

// ─── Tool handlers ──────────────────────────────────────────────────

func TestToolTranscribe_QueuesPending(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolTranscribe(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != "pending" {
		t.Errorf("expected pending: %v", out)
	}
	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "pending" || tr.SourceKind != "manual" {
		t.Errorf("not properly queued: %+v", tr)
	}
}

func TestToolTranscribe_ForceRequeuesOk(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Text: "old",
	})
	// Without force: the row stays ok (insertPending's ON CONFLICT
	// only flips failed/skipped).
	app.toolTranscribe(ctx, map[string]any{
		"_project_id": testProj, "file_id": "1",
	})
	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "ok" {
		t.Errorf("non-force flipped ok row: %v", tr.Status)
	}
	// With force: row resets to pending.
	app.toolTranscribe(ctx, map[string]any{
		"_project_id": testProj, "file_id": "1", "force": true,
	})
	tr, _ = getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Status != "pending" {
		t.Errorf("force didn't requeue: %v", tr.Status)
	}
}

func TestToolGetTranscript_NotFound(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, err := app.toolGetTranscript(ctx, map[string]any{
		"_project_id": testProj, "file_id": "999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if found, _ := out.(map[string]any)["found"].(bool); found {
		t.Errorf("expected found=false: %v", out)
	}
}

func TestToolSetTranscript_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(5000), "sha")

	out, err := app.toolSetTranscript(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
		"text":        "Hello world.",
		"language":    "en",
		"segments": []any{
			map[string]any{"start_ms": float64(0), "end_ms": float64(1500), "text": "Hello world."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "ok" {
		t.Errorf("unexpected response: %v", out)
	}
	tr, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if tr.Text != "Hello world." || tr.Language != "en" {
		t.Errorf("not persisted: %+v", tr)
	}
	if len(tr.Segments) == 0 {
		t.Error("segments not persisted")
	}
	// Source sha should auto-snapshot from media row.
	if tr.SourceSHA256 != "sha" {
		t.Errorf("sha not snapshotted: %q", tr.SourceSHA256)
	}
}

func TestToolSetTranscript_RequiresText(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	_, err := app.toolSetTranscript(ctx, map[string]any{
		"_project_id": testProj, "file_id": "1",
	})
	if err == nil {
		t.Error("expected error when text missing")
	}
}

// ─── Response parser ────────────────────────────────────────────────

func TestParseDeepgramResponse_BasicShape(t *testing.T) {
	// Minimal Deepgram response with just a transcript.
	raw := json.RawMessage(`{
	  "metadata": { "detected_language": "en" },
	  "results": {
	    "channels": [
	      { "detected_language": "en",
	        "alternatives": [
	          { "transcript": "Hello world. Second sentence." }
	        ]
	      }
	    ]
	  }
	}`)
	got, err := parseDeepgramResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Hello world. Second sentence." {
		t.Errorf("text=%q", got.Text)
	}
	if got.Language != "en" {
		t.Errorf("language=%q", got.Language)
	}
}

func TestParseDeepgramResponse_WithParagraphs(t *testing.T) {
	// Deepgram with smart_format on returns paragraphs.paragraphs[].sentences[].
	// We lift each sentence into a TranscriptSegment.
	raw := json.RawMessage(`{
	  "metadata": { "detected_language": "fr" },
	  "results": {
	    "channels": [
	      { "alternatives": [
	          { "transcript": "Bonjour. Au revoir.",
	            "paragraphs": {
	              "paragraphs": [
	                { "sentences": [
	                    { "text": "Bonjour.",  "start": 0.0,  "end": 1.5 },
	                    { "text": "Au revoir.", "start": 1.6, "end": 2.8 }
	                ] }
	              ]
	            }
	          }
	      ] }
	    ]
	  }
	}`)
	got, err := parseDeepgramResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(got.Segments))
	}
	if got.Segments[0].Text != "Bonjour." || got.Segments[0].StartMs != 0 || got.Segments[0].EndMs != 1500 {
		t.Errorf("first segment wrong: %+v", got.Segments[0])
	}
	if got.Segments[1].StartMs != 1600 || got.Segments[1].EndMs != 2800 {
		t.Errorf("second segment timing wrong: %+v", got.Segments[1])
	}
}

func TestParseDeepgramResponse_RejectsEmptyChannels(t *testing.T) {
	raw := json.RawMessage(`{"results":{"channels":[]}}`)
	_, err := parseDeepgramResponse(raw)
	if err == nil {
		t.Error("expected error on empty channels")
	}
}

// ─── helpers ────────────────────────────────────────────────────────

func TestIsNoRows(t *testing.T) {
	if isNoRows(nil) {
		t.Error("nil should not be no-rows")
	}
	if !isNoRows(errors.New("sql: no rows in result set")) {
		t.Error("standard sql.ErrNoRows message should match")
	}
}

func TestConfigBool(t *testing.T) {
	cases := map[string]bool{
		"":      true, // default applies
		"true":  true,
		"TRUE":  true,
		"1":     true,
		"yes":   true,
		"false": false,
		"0":     false,
		"no":    false,
	}
	for in, want := range cases {
		got := configBool(in, true)
		if got != want {
			t.Errorf("configBool(%q, true) = %v, want %v", in, got, want)
		}
	}
}
