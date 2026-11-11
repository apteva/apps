package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedTranscriptAudio(t *testing.T, ctx *sdk.AppCtx, fid, sha string, audioID, duration int64) {
	t.Helper()
	if err := upsertDerivation(ctx.AppDB(), testProj, fid, transcriptAudioKind(sha), audioID, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	v := &transcriptAudioValidation{Method: "full_decode_float_astats", Recipe: transcriptAudioRecipe, DecodeOK: true, Samples: duration * 16, DurationMs: duration}
	e := &transcriptAudioEvidence{StorageFileID: audioID, Validation: v}
	if _, err := ctx.AppDB().Exec(`INSERT INTO transcript_audio_checks(project_id,file_id,kind,storage_file_id,evidence) VALUES(?,?,?,?,?) ON CONFLICT(project_id,file_id,kind) DO UPDATE SET storage_file_id=excluded.storage_file_id,evidence=excluded.evidence`, testProj, fid, transcriptAudioKind(sha), audioID, transcriptAudioEvidenceJSON(e)); err != nil {
		t.Fatal(err)
	}
}

func readTranscriptMap(t *testing.T, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	args["_project_id"] = testProj
	out, err := (&App{}).toolGetTranscript(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["transcript"].(map[string]any)
}

func timedResponse(words string) json.RawMessage {
	return json.RawMessage(`{"metadata":{"duration":5},"results":{"channels":[{"alternatives":[{"transcript":"mind control mind control","words":` + words + `,"paragraphs":{"paragraphs":[{"sentences":[{"text":"mind","start":0,"end":0.5},{"text":"control mind control","start":0.5,"end":5}]}]}}]}]}}`)
}

const phraseWords = `[{"word":"mind","start":0.1,"end":0.3,"speaker":0,"confidence":0.9},{"word":"control","start":0.5,"end":0.8},{"word":"mind","start":4.5,"end":4.7},{"word":"control","start":4.8,"end":5}]`

func TestDeepgramWordFormatsAndRoundTrip(t *testing.T) {
	channels := timedResponse(phraseWords)
	var both map[string]any
	_ = json.Unmarshal(channels, &both)
	results := both["results"].(map[string]any)
	var words any
	_ = json.Unmarshal([]byte(phraseWords), &words)
	results["utterances"] = []any{map[string]any{"start": 0, "end": 5, "channel": 0, "transcript": "mind control mind control", "words": words}}
	bothData, _ := json.Marshal(both)
	var utterOnly map[string]any
	_ = json.Unmarshal(bothData, &utterOnly)
	r := utterOnly["results"].(map[string]any)
	alt := r["channels"].([]any)[0].(map[string]any)["alternatives"].([]any)[0].(map[string]any)
	delete(alt, "words")
	utterData, _ := json.Marshal(utterOnly)
	for _, tc := range []struct {
		name string
		data json.RawMessage
	}{{"channels", channels}, {"utterances", utterData}, {"both", bothData}} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parseDeepgramResponse(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.WordCount != 4 || len(parsed.Segments) != 2 || len(parsed.Segments[0].Words) != 1 {
				t.Fatalf("word/sentence preservation: %+v", parsed)
			}
			w := transcriptWords(parsed.Segments)
			if w[0].Speaker == nil || *w[0].Speaker != 0 || w[0].Channel == nil || *w[0].Channel != 0 || w[0].Confidence == nil {
				t.Fatalf("metadata lost: %+v", w[0])
			}
			ctx := newTestCtx(t)
			if err = upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "source", "", "source.wav"); err != nil {
				t.Fatal(err)
			}
			segs, _ := formatSegments(parsed.Segments)
			if err = upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: parsed.Text, SourceSHA256: "source", DurationMs: 5000, Segments: segs}); err != nil {
				t.Fatal(err)
			}
			full := readTranscriptMap(t, ctx, map[string]any{"file_id": "1"})
			if full["word_count"] != 4 || full["timing_granularity"] != "word" {
				t.Fatalf("retrieval: %+v", full)
			}
			summary := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "summary"})
			if _, ok := summary["segments"]; ok {
				t.Fatal("summary includes segments")
			}
			if _, ok := summary["text"]; ok {
				t.Fatal("summary includes text")
			}
			match := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "words", "query": "MIND control", "occurrence": 2})
			matches := match["matches"].([]transcriptMatch)
			if len(matches) != 1 || matches[0].StartMs != 4500 || matches[0].EndMs != 5000 || matches[0].Occurrence != 2 {
				t.Fatalf("phrase: %+v", matches)
			}
			first := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "words", "limit": 2})
			cursor := first["next_cursor"].(string)
			second := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "words", "limit": 2, "cursor": cursor})
			if second["has_more"] != false || second["words"].([]indexedTranscriptWord)[0].Index != 2 {
				t.Fatalf("pagination: %+v", second)
			}
			_, err = (&App{}).toolGetTranscript(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "view": "words", "cursor": cursor, "start_ms": 100})
			if err == nil {
				t.Fatal("cursor allowed changed filter")
			}
			if err = upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "revised"}); err != nil {
				t.Fatal(err)
			}
			_, err = (&App{}).toolGetTranscript(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "view": "words", "cursor": cursor})
			if err == nil || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("stale cursor: %v", err)
			}
		})
	}
}

func TestDeepgramRejectsInvalidWordIntervals(t *testing.T) {
	for _, words := range []string{
		`[{"word":"x","start":-0.1,"end":1}]`,
		`[{"word":"x","start":1,"end":0.5}]`,
		`[{"word":"x","start":4.9,"end":5.001}]`,
		`[{"word":"x","start":1}]`,
		`[{"word":"x","start":1e309,"end":2}]`,
		`[{"word":"x","start":1,"end":2,"confidence":2}]`,
		`[{"word":"x","start":1,"end":2},{"word":"x","start":0.9,"end":1.1}]`,
	} {
		if _, err := parseDeepgramResponse(timedResponse(words)); err == nil {
			t.Fatalf("invalid words accepted: %s", words)
		}
	}
}

func roundTrip841Fixture(t *testing.T, data []byte) {
	t.Helper()
	parsed, err := parseDeepgramResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.WordCount != 841 {
		t.Fatalf("words=%d want841", parsed.WordCount)
	}
	ctx := newTestCtx(t)
	probe := sampleAVProbe(482581)
	if err = upsertMedia(ctx.AppDB(), testProj, "27706", probe, "fixture-source", "", "fixture.mov"); err != nil {
		t.Fatal(err)
	}
	segs, err := formatSegments(parsed.Segments)
	if err != nil {
		t.Fatal(err)
	}
	if err = upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "27706", ProjectID: testProj, Text: parsed.Text, Segments: segs, SourceSHA256: "fixture-source", DurationMs: 482581}); err != nil {
		t.Fatal(err)
	}
	result := readTranscriptMap(t, ctx, map[string]any{"file_id": "27706"})
	segments := result["segments"].([]TranscriptSegment)
	count := 0
	for _, s := range segments {
		count += len(s.Words)
	}
	if count != 841 || result["word_count"] != 841 {
		t.Fatalf("MCP round trip: %d", count)
	}
	// Exercise the actual REST handler and JSON serialization, not only Go values.
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	req := httptest.NewRequest("GET", "/media/27706/transcript?project_id="+testProj, nil)
	rec := httptest.NewRecorder()
	(&App{}).handleMediaItem(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP%d %s", rec.Code, rec.Body.String())
	}
	var httpResult struct {
		Transcript struct {
			Segments []TranscriptSegment `json:"segments"`
			Count    int                 `json:"word_count"`
		} `json:"transcript"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &httpResult); err != nil {
		t.Fatal(err)
	}
	count = 0
	for _, s := range httpResult.Transcript.Segments {
		count += len(s.Words)
	}
	if count != 841 || httpResult.Transcript.Count != 841 {
		t.Fatalf("HTTP lost words: %d", count)
	}
	t.Logf("841-word fixture: %d segments, last word %dms, parser -> SQLite -> MCP -> HTTP passed", len(segments), parsed.LastWordEndMs)
}

func TestDeepgram841WordFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/deepgram-841-words.json")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip841Fixture(t, data)
}
func TestDeepgramOriginal841WordFixture(t *testing.T) {
	path := os.Getenv("MEDIA_DEEPGRAM_841_FIXTURE")
	if path == "" {
		t.Skip("private original fixture not configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip841Fixture(t, data)
}

func TestTranscriptUpgradeFailurePreservesCompleted(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	old := &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "previous completed transcript", SourceSHA256: "sha"}
	if err := upsertTranscript(ctx.AppDB(), old); err != nil {
		t.Fatal(err)
	}
	active, _ := getTranscript(ctx.AppDB(), testProj, "1")
	revision := transcriptRevision(active)
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	candidate, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	active, _ = getTranscript(ctx.AppDB(), testProj, "1")
	if active.Status != "ok" || active.Text != old.Text || active.LastAttempt.Status != "running" || transcriptRevision(active) != revision {
		t.Fatalf("upgrade hid active transcript: %+v", active)
	}
	if err = transcriptMarkFailed(ctx.AppDB(), testProj, "1", "provider failed", candidate.StartedAt); err != nil {
		t.Fatal(err)
	}
	active, _ = getTranscript(ctx.AppDB(), testProj, "1")
	if active.Text != old.Text || active.LastAttempt.Status != "failed" || transcriptRevision(active) != revision {
		t.Fatalf("failure lost completed transcript: %+v", active)
	}
	if err = queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	candidate, err = claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Text = "new completed transcript"
	candidate.SourceSHA256 = "sha"
	candidate.RequireSourceMatch = true
	if err = transcriptMarkOk(ctx.AppDB(), candidate); err != nil {
		t.Fatal(err)
	}
	active, _ = getTranscript(ctx.AppDB(), testProj, "1")
	if active.Text != candidate.Text || active.LastAttempt != nil {
		t.Fatalf("promotion failed: %+v", active)
	}
}

func TestTranscriptWordUpgradeWorkerUsesValidatedAudio(t *testing.T) {
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: timedResponse(phraseWords)}
	ctx := newTestCtxWithPlatform(t, stub)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, SourceSHA256: "sha", Text: "old sentences"}); err != nil {
		t.Fatal(err)
	}
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), row)
	result := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "summary"})
	if result["word_count"] != 4 || result["redaction_ready"] != true {
		t.Fatalf("upgrade: %+v", result)
	}
	timing := result["timing"].(*transcriptTiming)
	if timing.AudioFileID != 9001 || timing.AudioSHA256 != "proxy-sha" {
		t.Fatalf("identity: %+v", timing)
	}
	if len(stub.ExecuteCalls) != 1 || stub.ExecuteCalls[0].Tool != "listen" || !strings.Contains(fmt.Sprint(stub.ExecuteCalls[0].Input["url"]), "9001") {
		t.Fatalf("provider input: %+v", stub.ExecuteCalls)
	}
}

func TestTranscriptImportValidatesAudioIdentityAndSource(t *testing.T) {
	stub := boundDeepgram()
	ctx := newTestCtxWithPlatform(t, stub)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
	var response map[string]any
	_ = json.Unmarshal(timedResponse(phraseWords), &response)
	args := map[string]any{"deepgram_response": response, "source_sha256": "sha", "transcription_audio_file_id": 9001}
	if _, err := transcriptImportFromArgs(ctx, testProj, "1", args); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("unverified saved response accepted: %v", err)
	}
	response["metadata"].(map[string]any)["sha256"] = "proxy-sha"
	tr, err := transcriptImportFromArgs(ctx, testProj, "1", args)
	if err != nil {
		t.Fatal(err)
	}
	if err = upsertTranscript(ctx.AppDB(), tr); err != nil {
		t.Fatal(err)
	}
	summary := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "summary"})
	if summary["redaction_ready"] != true || summary["word_count"] != 4 {
		t.Fatalf("verified import: %+v", summary)
	}
	args["source_sha256"] = "changed"
	if _, err = transcriptImportFromArgs(ctx, testProj, "1", args); err == nil {
		t.Fatal("wrong source import accepted")
	}
	// A source change between validation and commit must not relabel old words.
	if _, err = ctx.AppDB().Exec(`UPDATE media SET source_sha256='changed' WHERE file_id='1'`); err != nil {
		t.Fatal(err)
	}
	if err = upsertTranscript(ctx.AppDB(), tr); err == nil {
		t.Fatal("source changed during import but commit succeeded")
	}
	saved, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if saved.SourceSHA256 != "sha" {
		t.Fatalf("failed import overwrote previous: %+v", saved)
	}
}

func TestTranscriptWordUpgradeRejectsWordlessSuccessAndRetainsPrevious(t *testing.T) {
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: timedResponse(`[]`)}
	ctx := newTestCtxWithPlatform(t, stub)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "keep completed text", SourceSHA256: "sha"}); err != nil {
		t.Fatal(err)
	}
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), row)
	active, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if active.Status != "ok" || active.Text != "keep completed text" || active.LastAttempt == nil || active.LastAttempt.Status != "failed" || !strings.Contains(active.LastAttempt.Error, "word_timing_unavailable") {
		t.Fatalf("wordless upgrade replaced completed: %+v", active)
	}
}

func TestTranscriptTimeMappingAndFinalWordMute(t *testing.T) {
	stub := boundDeepgram()
	ctx := newTestCtxWithPlatform(t, stub)
	probe := sampleAVProbe(5500)
	probe.Raw = `{"format":{"start_time":"0"},"streams":[{"codec_type":"audio","start_time":"0.5"}]}`
	if err := upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha", "", "delayed.mov"); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseDeepgramResponse(timedResponse(phraseWords))
	if err != nil {
		t.Fatal(err)
	}
	evidence := &transcriptAudioEvidence{StorageFileID: 9001, Validation: &transcriptAudioValidation{Method: "full_decode_float_astats", Recipe: transcriptAudioRecipe, DecodeOK: true, Samples: 80000, DurationMs: 5000}}
	media, err := getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	timing, err := buildTranscriptTiming(t.Context(), ctx, newStorageClient(), media, evidence, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if timing.SourceOffsetMs != 500 {
		t.Fatalf("mapping: %+v", timing)
	}
	diagnostics, _ := json.Marshal(map[string]any{"timing": timing})
	segs, _ := formatSegments(parsed.Segments)
	tr := &TranscriptRow{FileID: "1", ProjectID: testProj, SourceSHA256: "sha", Provider: "deepgram", Text: parsed.Text, Segments: segs, Diagnostics: diagnostics, DurationMs: 5500}
	if err = upsertTranscript(ctx.AppDB(), tr); err != nil {
		t.Fatal(err)
	}
	original := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "words"})["words"].([]indexedTranscriptWord)
	projected := readTranscriptMap(t, ctx, map[string]any{"file_id": "1", "view": "words", "timebase": "source", "start_ms": 4900})["words"].([]indexedTranscriptWord)
	if original[3].EndMs != 5000 || projected[len(projected)-1].EndMs != 5500 {
		t.Fatalf("mapping applied incorrectly: %v %v", original, projected)
	}
	active, _ := getTranscript(ctx.AppDB(), testProj, "1")
	words, err := transcriptSourceWords(active, media)
	if err != nil {
		t.Fatal(err)
	}
	padding := int64(50)
	plan, err := makeAudioRedactionPlan(audioRedactParams{Method: "mute", PaddingMs: &padding, Selections: []audioSelection{{Text: "mind control", Occurrence: 2}}}, words, 5500, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Ranges) != 1 || plan.Ranges[0] != (audioInterval{4950, 5500}) {
		t.Fatalf("last-word bounds or buffer wrong: %+v", plan)
	}
}

func TestDeepgramMultichannelWordsStaySeparate(t *testing.T) {
	raw := json.RawMessage(`{"metadata":{"duration":2},"results":{"channels":[{"alternatives":[{"transcript":"mind control","words":[{"word":"mind","start":0.1,"end":0.3},{"word":"control","start":0.5,"end":0.8}]}]},{"alternatives":[{"transcript":"mind control","words":[{"word":"mind","start":0.2,"end":0.4},{"word":"control","start":0.6,"end":0.9}]}]}]}}`)
	parsed, err := parseDeepgramResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.WordCount != 4 {
		t.Fatal("multichannel words lost")
	}
	matches := transcriptPhraseMatches(transcriptWords(parsed.Segments), transcriptReadOptions{Query: "mind control"})
	if len(matches) != 2 || matches[0].Channel != 0 || matches[1].Channel != 1 {
		t.Fatalf("cross-channel phrase matches: %+v", matches)
	}
	if _, err = resolveAudioSelections([]audioSelection{{Text: "mind control", Occurrence: 1}}, transcriptWords(parsed.Segments), 2000); err == nil {
		t.Fatal("ambiguous multichannel words permitted implicit channel redaction")
	}
}

func TestTranscriptUpgradeCannotPromoteStaleSourceOrClaim(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "original", SourceSHA256: "sha"}); err != nil {
		t.Fatal(err)
	}
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", true, true); err != nil {
		t.Fatal(err)
	}
	candidate, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	candidate.SourceSHA256 = "sha"
	candidate.RequireSourceMatch = true
	candidate.Text = "stale candidate"
	if _, err = ctx.AppDB().Exec(`UPDATE media SET source_sha256='new-source' WHERE file_id='1'`); err != nil {
		t.Fatal(err)
	}
	if err = transcriptMarkOk(ctx.AppDB(), candidate); err == nil {
		t.Fatal("stale source promoted")
	}
	failedTranscriptCommit(ctx.AppDB(), candidate, fmt.Errorf("source changed"))
	active, err := getTranscript(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if active.Text != "original" || active.LastAttempt.Status != "failed" {
		t.Fatalf("failed stale candidate removed active: %+v", active)
	}
	if err = upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "newer manual transcript", SourceSHA256: "new-source"}); err != nil {
		t.Fatal(err)
	}
	candidate.SourceSHA256 = "new-source"
	if err = transcriptMarkOk(ctx.AppDB(), candidate); err == nil {
		t.Fatal("superseded claim replaced newer transcript")
	}
	active, _ = getTranscript(ctx.AppDB(), testProj, "1")
	if active.Text != "newer manual transcript" {
		t.Fatalf("superseded claim overwrote active: %+v", active)
	}
}

func TestTranscriptUpgradeRejectsEmptyProviderResponse(t *testing.T) {
	stub := boundDeepgram()
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"metadata":{"duration":5},"results":{"channels":[{"alternatives":[{"transcript":"","words":[]}]}]}}`)}
	ctx := newTestCtxWithPlatform(t, stub)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	seedTranscriptAudio(t, ctx, "1", "sha", 9001, 5000)
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, Text: "previous recognized speech", SourceSHA256: "sha"}); err != nil {
		t.Fatal(err)
	}
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), row)
	active, _ := getTranscript(ctx.AppDB(), testProj, "1")
	if active.Status != "ok" || active.Text != "previous recognized speech" || active.LastAttempt.Status != "failed" {
		t.Fatalf("empty response erased completed speech: %+v", active)
	}
}

func TestTranscriptAutomaticRetryRetainsWordRequirement(t *testing.T) {
	ctx := newTestCtx(t)
	if err := queueTranscript(ctx.AppDB(), testProj, "1", "manual", false, true); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	if err = transcriptMarkFailed(ctx.AppDB(), testProj, "1", "provider unavailable", row.StartedAt); err != nil {
		t.Fatal(err)
	}
	if err = insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto"); err != nil {
		t.Fatal(err)
	}
	if !transcriptRequiresWords(ctx.AppDB(), row) {
		t.Fatal("automatic retry silently downgraded required word timing")
	}
}
