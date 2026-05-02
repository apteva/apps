package main

// Tier 1 — auto-describer with stub PlatformClient. Same shape as
// transcript_worker_test.go: stub WhoAmI/GetConnection/Execute,
// mock storage's signed-URL endpoint for thumbnails, drive
// describerSweep + runOneDescription through the real code paths.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// boundOpencodeGo is the describer's equivalent of boundDeepgram —
// stub a connection bound to the descriptions role.
func boundOpencodeGo() *stubPlatform {
	return &stubPlatform{
		whoami: &sdk.InstallIdentity{
			Bindings: map[string]any{"descriptions": float64(11)},
		},
		connections: map[int64]*sdk.PlatformConnection{
			11: {ID: 11, AppSlug: "opencode-go", Status: "active"},
		},
	}
}

// canonOK is a minimal opencode-go-shape happy-path response.
func canonOK(content string) json.RawMessage {
	return json.RawMessage(`{
	  "choices": [
	    { "message": { "content": ` + jsonStr(content) + `, "role": "assistant" }, "finish_reason": "stop" }
	  ]
	}`)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ─── candidate query ───────────────────────────────────────────────

func TestDescribeCandidates_FiltersHumanSet(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAVProbe(3000), "sha")

	// File 2 has a human description — must be filtered out.
	d := "human prose"
	if _, err := setDescription(ctx.AppDB(), testProj, "2", DescriptionFields{Description: &d}); err != nil {
		t.Fatal(err)
	}

	cands, err := describeCandidates(ctx.AppDB(), testProj, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0] != "1" {
		t.Errorf("expected only file 1 as candidate, got %v", cands)
	}
}

func TestDescribeCandidates_RespectsCooldown(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	if err := markDescribeAttempt(ctx.AppDB(), testProj, "1", "boom"); err != nil {
		t.Fatal(err)
	}
	// Cooldown 60s — file just attempted, should be hidden.
	cands, _ := describeCandidates(ctx.AppDB(), testProj, 100, 60)
	if len(cands) != 0 {
		t.Errorf("expected no candidate during cooldown, got %v", cands)
	}
	// Cooldown 0 — no gating, file shows up again.
	cands, _ = describeCandidates(ctx.AppDB(), testProj, 100, 0)
	if len(cands) != 1 {
		t.Errorf("expected file with cooldown=0, got %v", cands)
	}
}

func TestDescribeCandidates_RequiresProbeOk(t *testing.T) {
	// Pending / failed probe means we don't know the file's media
	// shape yet — describing it would be guessing.
	ctx := newTestCtx(t)
	probe := sampleAVProbe(3000)
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha")
	// Manually flip probe_status to simulate a probe in flight.
	ctx.AppDB().Exec(`UPDATE media SET probe_status='pending' WHERE file_id='1'`)
	cands, _ := describeCandidates(ctx.AppDB(), testProj, 100, 0)
	if len(cands) != 0 {
		t.Errorf("expected no candidate while probe pending, got %v", cands)
	}
}

// ─── sweep gating ──────────────────────────────────────────────────

func TestDescriberSweep_NoIntegrationBound_NoOp(t *testing.T) {
	// Without a description integration we degrade silently — no
	// description_error noise, no row mutation. The rationale is the
	// integration may be installed later; we don't want to wedge
	// rows in a "skipped" state.
	ctx := newTestCtxWithPlatform(t, noBindings())
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.DescriptionSource != "" {
		t.Errorf("description_source touched: %q", got.DescriptionSource)
	}
	if got.DescriptionError != "" {
		t.Errorf("description_error touched: %q", got.DescriptionError)
	}
}

// ─── runOneDescription happy paths ─────────────────────────────────

func TestRunOneDescription_TextOnly_FromTranscript(t *testing.T) {
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: canonOK("Two people discussing quarterly numbers in a meeting."),
	}
	ctx := newTestCtxWithPlatform(t, stub)

	// Audio-only file with a transcript — should send text-only call.
	probe := &Probe{FormatName: "wav", DurationMs: 3000, HasAudio: true, AudioCodec: "pcm_s16le", Raw: "{}"}
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok",
		Text: "Q3 results were strong, exceeded expectations.",
	})

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description == "" {
		t.Fatalf("description not written: source=%q error=%q", got.DescriptionSource, got.DescriptionError)
	}
	if !strings.Contains(got.Description, "quarterly") {
		t.Errorf("unexpected description content: %q", got.Description)
	}
	if got.DescriptionSource != "ai-generated" {
		t.Errorf("source=%q want ai-generated", got.DescriptionSource)
	}

	// Wiring: text-only call has content as a string, not an array.
	if len(stub.ExecuteCalls) != 1 {
		t.Fatalf("expected 1 ExecuteIntegrationTool call, got %d", len(stub.ExecuteCalls))
	}
	args := stub.ExecuteCalls[0].Input
	msgs, _ := args["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	userContent, isStr := msgs[1]["content"].(string)
	if !isStr {
		t.Errorf("audio-only path should send string content, got %T", msgs[1]["content"])
	}
	if !strings.Contains(userContent, "Q3 results") {
		t.Errorf("user content missing transcript: %q", userContent)
	}
	// Wired with the right tool name from the manifest map.
	if stub.ExecuteCalls[0].Tool != "chat_completion" {
		t.Errorf("tool=%q want chat_completion", stub.ExecuteCalls[0].Tool)
	}
	// Default model from config.
	if model, _ := args["model"].(string); model != "kimi-k2.6" {
		t.Errorf("model=%q want kimi-k2.6", model)
	}
}

func TestRunOneDescription_ImageWithThumbnail_Multimodal(t *testing.T) {
	// Image file with a thumbnail derivation — should send vision
	// call (content is array of parts, includes image_url).
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: canonOK("A red sunset over still water."),
	}
	ctx := newTestCtxWithPlatform(t, stub)

	probe := &Probe{FormatName: "png_pipe", IsImage: true, HasVideo: true, Width: 1024, Height: 768, VideoCodec: "png", Raw: "{}"}
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha")
	if err := upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 99, 320, 240); err != nil {
		t.Fatal(err)
	}

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if !strings.Contains(got.Description, "sunset") {
		t.Errorf("description not written / wrong: %q", got.Description)
	}

	// User message content must be an array of parts with an image_url.
	args := stub.ExecuteCalls[0].Input
	msgs, _ := args["messages"].([]map[string]any)
	parts, ok := msgs[1]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("multimodal path should send array content, got %T", msgs[1]["content"])
	}
	hasImage := false
	for _, p := range parts {
		if p["type"] == "image_url" {
			hasImage = true
			img, _ := p["image_url"].(map[string]any)
			if u, _ := img["url"].(string); !strings.HasPrefix(u, "http") {
				t.Errorf("image_url not absolute: %q", u)
			}
		}
	}
	if !hasImage {
		t.Errorf("no image_url part in multimodal request: %v", parts)
	}
}

func TestRunOneDescription_VideoWithBoth_FullMultimodal(t *testing.T) {
	// Video with both a transcript AND a thumbnail — best signal.
	// Should send both in the same multimodal user message.
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: canonOK("Speaker presents Q3 sales charts on a video call."),
	}
	ctx := newTestCtxWithPlatform(t, stub)

	probe := sampleAVProbe(8000) // has_video + has_audio
	upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 99, 320, 240)
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok",
		Text: "Quarterly numbers came in above forecast.",
	})

	describerSweep(ctx)

	args := stub.ExecuteCalls[0].Input
	msgs, _ := args["messages"].([]map[string]any)
	parts, _ := msgs[1]["content"].([]map[string]any)
	hasText, hasImage := false, false
	for _, p := range parts {
		if p["type"] == "text" {
			hasText = true
			if !strings.Contains(p["text"].(string), "Quarterly numbers") {
				t.Errorf("text part missing transcript: %q", p["text"])
			}
		}
		if p["type"] == "image_url" {
			hasImage = true
		}
	}
	if !hasText || !hasImage {
		t.Errorf("video+transcript should send both parts, got text=%v image=%v parts=%v", hasText, hasImage, parts)
	}
}

func TestRunOneDescription_UsesReasoningWhenContentNull(t *testing.T) {
	// Kimi K2.6 with tight max_tokens returns content=null + a
	// populated reasoning string. The describer should fall back to
	// the reasoning rather than mark the row failed — better to have
	// a noisy description than no description.
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{
		  "choices": [{
		    "message": {
		      "role": "assistant",
		      "content": null,
		      "reasoning": "The image shows two people in a meeting."
		    },
		    "finish_reason": "length"
		  }]
		}`),
	}
	ctx := newTestCtxWithPlatform(t, stub)

	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "x",
	})

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if !strings.Contains(got.Description, "two people") {
		t.Errorf("expected reasoning fallback, got %q", got.Description)
	}
}

// ─── failure modes ─────────────────────────────────────────────────

func TestRunOneDescription_LLMNon2xx_MarksAttempt(t *testing.T) {
	// Non-2xx response → row stays without a description but gets
	// description_attempted_at + description_error set so the
	// cooldown gate keeps us out of a tight retry loop.
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: false, Status: 401,
		Data: json.RawMessage(`{"error":"bad key"}`),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "x",
	})

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "" {
		t.Errorf("description should not be written on non-2xx: %q", got.Description)
	}
	if got.DescriptionAttemptedAt == "" {
		t.Errorf("attempted_at not set after failure")
	}
	if !strings.Contains(got.DescriptionError, "non-2xx") || !strings.Contains(got.DescriptionError, "bad key") {
		t.Errorf("error=%q should reference non-2xx + body", got.DescriptionError)
	}
}

func TestRunOneDescription_NetworkError_MarksAttempt(t *testing.T) {
	stub := boundOpencodeGo()
	stub.executeErr = errors.New("connection reset by peer")
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "x",
	})

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.DescriptionAttemptedAt == "" {
		t.Errorf("attempted_at not set on network error")
	}
	if !strings.Contains(got.DescriptionError, "connection reset") {
		t.Errorf("error=%q should propagate network detail", got.DescriptionError)
	}
}

func TestRunOneDescription_NoUsableInput_SkipsWithoutMarking(t *testing.T) {
	// Silent video without a thumbnail derivation → vision can't
	// help, transcript doesn't exist → return nil messages, skip.
	// Importantly: no markDescribeAttempt, so when the indexer
	// produces a thumbnail the next sweep gets another chance.
	stub := boundOpencodeGo()
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoOnlyProbe(), "sha")
	// No derivations, no transcripts.

	describerSweep(ctx)

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.DescriptionAttemptedAt != "" {
		t.Errorf("attempted_at set on input-empty skip; should retry next sweep")
	}
	if got.DescriptionError != "" {
		t.Errorf("error=%q want empty", got.DescriptionError)
	}
	if len(stub.ExecuteCalls) != 0 {
		t.Errorf("Execute called when there's nothing to describe: %v", stub.ExecuteCalls)
	}
}

func TestRunOneDescription_NonEmptyDescriptionIsRespected(t *testing.T) {
	// Defence-in-depth: any non-empty description is preserved,
	// regardless of source. Catches the race where the candidate
	// query found description='' but a concurrent set wrote prose
	// before runOneDescription claimed the row. Source could be
	// 'ai-generated' (a previous successful run) or even '' (legacy
	// row) — what matters is the text exists.
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: canonOK("ai overwrite"),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	d := "previously generated by ai"
	// Source explicitly 'ai-generated' — without the new guard, the
	// describer would happily overwrite this on a re-run race.
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{
		Description: &d,
		Source:      "ai-generated",
	})

	bound := ctx.IntegrationFor("descriptions")
	if bound == nil {
		t.Fatal("test setup: no descriptions binding")
	}
	runOneDescription(ctx, bound, testProj, "1")

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "previously generated by ai" {
		t.Errorf("non-empty description overwritten: %q", got.Description)
	}
	if len(stub.ExecuteCalls) != 0 {
		t.Errorf("Execute called for already-described row: %v", stub.ExecuteCalls)
	}
}

func TestRunOneDescription_HumanSetIsRespected(t *testing.T) {
	// Belt and braces — even if a human-set row somehow slipped past
	// the candidate query (race), runOneDescription's own check
	// shouldn't overwrite it.
	stub := boundOpencodeGo()
	stub.executeResp = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: canonOK("ai prose"),
	}
	ctx := newTestCtxWithPlatform(t, stub)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	d := "human-written"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d}) // source=human

	// Force-call the runOne path (bypassing candidate filter) to
	// exercise the secondary guard.
	bound := ctx.IntegrationFor("descriptions")
	if bound == nil {
		t.Fatal("test setup: no descriptions binding")
	}
	runOneDescription(ctx, bound, testProj, "1")

	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.Description != "human-written" {
		t.Errorf("human description overwritten: %q", got.Description)
	}
	if len(stub.ExecuteCalls) != 0 {
		t.Errorf("Execute called for human-set row: %v", stub.ExecuteCalls)
	}
}

// ─── extractChatContent unit ───────────────────────────────────────

func TestExtractChatContent_NormalString(t *testing.T) {
	s, err := extractChatContent(canonOK("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if s != "hello" {
		t.Errorf("got %q", s)
	}
}

func TestExtractChatContent_NullContentFallsToReasoning(t *testing.T) {
	raw := json.RawMessage(`{
	  "choices": [{ "message": { "content": null, "reasoning": "fallback" }, "finish_reason": "length" }]
	}`)
	s, err := extractChatContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s != "fallback" {
		t.Errorf("got %q want fallback", s)
	}
}

func TestExtractChatContent_BothEmpty_Errors(t *testing.T) {
	raw := json.RawMessage(`{"choices":[{"message":{"content":"","reasoning":""},"finish_reason":"stop"}]}`)
	if _, err := extractChatContent(raw); err == nil {
		t.Error("expected error when content + reasoning both empty")
	}
}

func TestExtractChatContent_NoChoices_Errors(t *testing.T) {
	if _, err := extractChatContent(json.RawMessage(`{"choices":[]}`)); err == nil {
		t.Error("expected error on empty choices")
	}
}

// ─── tool handler ──────────────────────────────────────────────────

func TestToolDescribe_QueuesCandidate(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")

	out, err := app.toolDescribe(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["queued"] != true {
		t.Errorf("expected queued=true: %v", out)
	}
}

func TestToolDescribe_HumanSetReturnsReason(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	d := "human-only"
	setDescription(ctx.AppDB(), testProj, "1", DescriptionFields{Description: &d})

	out, _ := app.toolDescribe(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
	})
	m := out.(map[string]any)
	if m["queued"] != false {
		t.Errorf("expected queued=false on human-set row: %v", out)
	}
	if reason, _ := m["reason"].(string); !strings.Contains(reason, "human-set") {
		t.Errorf("missing reason explaining the no-op: %v", out)
	}
}

func TestToolDescribe_ForceClearsCooldown(t *testing.T) {
	// force=true wipes the cooldown bookkeeping so the next sweep
	// reattempts even if we just failed.
	ctx := newTestCtx(t)
	app := &App{}
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAVProbe(3000), "sha")
	markDescribeAttempt(ctx.AppDB(), testProj, "1", "boom")

	app.toolDescribe(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
		"force":       true,
	})
	got, _ := getMedia(ctx.AppDB(), testProj, "1")
	if got.DescriptionAttemptedAt != "" {
		t.Errorf("attempted_at not cleared by force: %q", got.DescriptionAttemptedAt)
	}
	if got.DescriptionError != "" {
		t.Errorf("error not cleared by force: %q", got.DescriptionError)
	}
}

func TestToolDescribe_NotFound(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	out, _ := app.toolDescribe(ctx, map[string]any{
		"_project_id": testProj, "file_id": "999",
	})
	if found, _ := out.(map[string]any)["found"].(bool); found {
		t.Errorf("expected found=false: %v", out)
	}
}
