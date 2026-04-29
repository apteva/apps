package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const testProj = "test-proj"

func newTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	globalCtx = ctx
	return ctx
}

func sampleVideoProbe() *Probe {
	return &Probe{
		FormatName: "mov,mp4,m4a,3gp,3g2,mj2",
		DurationMs: 12500,
		Bitrate:    1_500_000,
		HasVideo:   true,
		HasAudio:   true,
		Width:      1920,
		Height:     1080,
		FPS:        29.97,
		VideoCodec: "h264",
		Channels:   2,
		SampleRate: 48000,
		AudioCodec: "aac",
		Raw:        `{"format":{}}`,
	}
}

func sampleAudioProbe() *Probe {
	return &Probe{
		FormatName: "wav",
		DurationMs: 5000,
		HasAudio:   true,
		Channels:   1,
		SampleRate: 44100,
		AudioCodec: "pcm_s16le",
		Raw:        `{}`,
	}
}

func sampleImageProbe() *Probe {
	return &Probe{
		FormatName: "png_pipe",
		HasVideo:   true, // ffprobe reports images as a single-frame video
		IsImage:    true,
		Width:      640,
		Height:     480,
		VideoCodec: "png",
		Raw:        `{}`,
	}
}

func TestUpsertAndGet(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "abc123"); err != nil {
		t.Fatal(err)
	}
	got, err := getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProbeStatus != "ok" {
		t.Errorf("probe_status=%q want ok", got.ProbeStatus)
	}
	if got.DurationMs != 12500 {
		t.Errorf("duration_ms=%d", got.DurationMs)
	}
	if !got.HasVideo || !got.HasAudio {
		t.Errorf("flags wrong: video=%v audio=%v", got.HasVideo, got.HasAudio)
	}
	if got.VideoCodec != "h264" || got.AudioCodec != "aac" {
		t.Errorf("codecs wrong: %q / %q", got.VideoCodec, got.AudioCodec)
	}
	if got.SourceSHA256 != "abc123" {
		t.Errorf("sha=%q", got.SourceSHA256)
	}
}

func TestUpsertIdempotent_Update(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "v1"); err != nil {
		t.Fatal(err)
	}
	// Re-upsert with new probe (e.g. file replaced in storage).
	v2 := sampleAudioProbe()
	v2.DurationMs = 7777
	if err := upsertMedia(ctx.AppDB(), testProj, "1", v2, "v2"); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.DurationMs != 7777 || got.SourceSHA256 != "v2" {
		t.Errorf("post-update row stale: dur=%d sha=%q", got.DurationMs, got.SourceSHA256)
	}
}

func TestMarkFailed(t *testing.T) {
	ctx := newTestCtx(t)
	if err := markFailed(ctx.AppDB(), testProj, "1", "shaA", "failed", "ffprobe explosion"); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.ProbeStatus != "failed" {
		t.Errorf("status=%q", got.ProbeStatus)
	}
	if got.ProbeError != "ffprobe explosion" {
		t.Errorf("error=%q", got.ProbeError)
	}
}

func TestMarkFailed_TruncatesLong(t *testing.T) {
	ctx := newTestCtx(t)
	long := make([]byte, 2000)
	for i := range long {
		long[i] = 'x'
	}
	if err := markFailed(ctx.AppDB(), testProj, "1", "sha", "failed", string(long)); err != nil {
		t.Fatal(err)
	}
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if len(got.ProbeError) > 1100 {
		t.Errorf("error not truncated: %d bytes", len(got.ProbeError))
	}
}

func TestSearchMedia_Filters(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "vid1", sampleVideoProbe(), "a")
	upsertMedia(ctx.AppDB(), testProj, "aud1", sampleAudioProbe(), "b")
	upsertMedia(ctx.AppDB(), testProj, "img1", sampleImageProbe(), "c")

	hasVideo := true
	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{HasVideo: &hasVideo})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !r.HasVideo {
			t.Errorf("filter has_video=true returned row without video: %+v", r)
		}
	}

	isImage := true
	rows, _ = searchMedia(ctx.AppDB(), testProj, SearchFilters{IsImage: &isImage})
	if len(rows) != 1 || !rows[0].IsImage {
		t.Errorf("is_image filter wrong: %+v", rows)
	}

	rows, _ = searchMedia(ctx.AppDB(), testProj, SearchFilters{DurationMinMs: 6000})
	for _, r := range rows {
		if r.DurationMs < 6000 {
			t.Errorf("duration filter leaked %d-ms row", r.DurationMs)
		}
	}
}

func TestSearchMedia_OmitsNonOk(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "good", sampleAudioProbe(), "a")
	markFailed(ctx.AppDB(), testProj, "bad", "b", "failed", "boom")

	rows, _ := searchMedia(ctx.AppDB(), testProj, SearchFilters{})
	if len(rows) != 1 || rows[0].FileID != "good" {
		t.Errorf("search returned non-ok row: %+v", rows)
	}
}

func TestUpsertDerivation(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "vid1", sampleVideoProbe(), "a")
	if err := upsertDerivation(ctx.AppDB(), testProj, "vid1", "thumbnail", 999, 320, 180); err != nil {
		t.Fatal(err)
	}
	got, err := getMedia(ctx.AppDB(), testProj, "vid1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Derivations) != 1 || got.Derivations[0].StorageFileID != "999" {
		t.Errorf("derivations wrong: %+v", got.Derivations)
	}
	// Re-upsert overwrites — uniqueness is (file_id, kind).
	if err := upsertDerivation(ctx.AppDB(), testProj, "vid1", "thumbnail", 1234, 400, 225); err != nil {
		t.Fatal(err)
	}
	got, _ = getMedia(ctx.AppDB(), testProj, "vid1")
	if len(got.Derivations) != 1 || got.Derivations[0].StorageFileID != "1234" {
		t.Errorf("derivation upsert didn't overwrite: %+v", got.Derivations)
	}
}

func TestIndexerCandidates(t *testing.T) {
	ctx := newTestCtx(t)
	// Pre-populate one row in 'ok' state matching shaA.
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "shaA")

	files := []StorageFile{
		{ID: 1, Name: "old.wav", ContentType: "audio/wav", SHA256: "shaA"}, // unchanged → skip
		{ID: 2, Name: "new.mp4", ContentType: "video/mp4", SHA256: "shaB"}, // new → pick up
		{ID: 1, Name: "replaced.wav", ContentType: "audio/wav", SHA256: "shaZ"}, // sha changed → re-probe
	}
	got := indexerCandidates(ctx.AppDB(), testProj, files, 100)
	gotIDs := map[int64]bool{}
	for _, f := range got {
		gotIDs[f.ID] = true
	}
	// File 2 (new) should be a candidate.
	if !gotIDs[2] {
		t.Errorf("expected file 2 (new) to be a candidate, got %+v", got)
	}
	// File 1 with matching sha shouldn't be picked up (the indexer
	// candidate logic dedupes by file_id, taking the most-recently-
	// seen row from the input list — so the duplicated entry for id=1
	// could come back if the second variant has a different sha).
}
