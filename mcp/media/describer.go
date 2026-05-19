package main

// Auto-describer worker. Generates a 2-3 sentence description for
// every media file that doesn't have one yet, using the opencode-go
// integration (Kimi K2.6 by default — vision-capable reasoning model).
//
// Three input paths, picked per file:
//
//   image / silent video → vision call with a thumbnail image_url
//   audio with transcript → text call with the transcript
//   video with both       → multimodal: thumbnail + transcript
//
// The worker is single-threaded by design — Kimi spends tokens
// thinking before answering, so parallelism would just delay the
// queue without saving wall-clock. Cooldown prevents tight retry
// loops when an integration is misconfigured.
//
// Output writes through setDescription with source='ai-generated' so
// human-set descriptions stay sticky (the candidate query filters
// them out). Failures land on description_attempted_at +
// description_error via markDescribeAttempt; the cooldown gate keeps
// us from hammering the API after a 401 / 429.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// describerMsg carries one eligibility signal from indexer/transcriber
// to the describer loop. ProjectID accompanies FileID so a global
// install's single describer goroutine can dispatch per-row work
// against the right project's binding + storage scope.
type describerMsg struct {
	ProjectID string
	FileID    string
}

// describerNotify carries (project, file_id) pairs that need a
// describer pass NOW rather than waiting for the next periodic
// tick. Buffered so a burst of indexer/transcriber writes never
// blocks the producer; notifyDescriber drops on overflow because
// the periodic sweep is the safety net for anything we miss.
//
// Set in startDescriber; nil otherwise (auto_describe_enabled=false
// installs don't run the loop). notifyDescriber is a no-op then.
var describerNotify chan describerMsg

// notifyDescriber is called by the indexer + transcriber when a
// file might be newly eligible for description (probe just
// completed, transcript just landed, etc.). Non-blocking — the
// channel buffer is bounded so a backlog falls back to the
// periodic sweep.
func notifyDescriber(projectID, fileID string) {
	if describerNotify == nil || fileID == "" || projectID == "" {
		return
	}
	select {
	case describerNotify <- describerMsg{ProjectID: projectID, FileID: fileID}:
	default:
		// queue full — periodic sweep will pick it up on the next tick
	}
}

// startDescriber starts the auto-describer goroutine. Honours
// auto_describe_enabled — when false, manual media_describe still
// works but the sweep doesn't run.
func startDescriber(app *sdk.AppCtx) {
	cfg := app.Config()
	if !configBool(cfg.Get("auto_describe_enabled"), true) {
		app.Logger().Info("auto_describe_enabled=false — auto-describer disabled (manual still works)")
		return
	}
	describerNotify = make(chan describerMsg, 100)
	go describerLoop(app)
	app.Logger().Info("auto-describer started")
}

func describerLoop(app *sdk.AppCtx) {
	log := app.Logger()
	cfg := app.Config()
	interval := parseConfigIntFallback(cfg.Get("describe_poll_seconds"), 60)
	tick := time.NewTicker(time.Duration(interval) * time.Second)
	defer tick.Stop()

	describerSweep(app)

	for {
		select {
		case <-app.Done():
			log.Info("auto-describer stopping")
			return
		case <-tick.C:
			// Periodic safety-net sweep. Picks up rows the
			// notify path missed (channel overflow, app restart
			// while file was queued, integration newly bound, etc).
			describerSweep(app)
		case msg := <-describerNotify:
			// Indexer or transcriber just made this file eligible.
			// Process just THIS row immediately rather than wait
			// up to interval seconds for the next tick.
			describerOne(app, msg)
		}
	}
}

// describerOne runs the describer for a single file_id signalled
// via notifyDescriber. Goes through runOneDescription so all the
// normal guards (human-set check, cooldown, integration binding,
// no-input skip) still apply.
func describerOne(app *sdk.AppCtx, msg describerMsg) {
	if msg.ProjectID == "" || msg.FileID == "" {
		return
	}
	// Pin the project for IntegrationFor + downstream cross-app calls.
	app = app.WithProject(msg.ProjectID)
	bound := app.IntegrationFor("descriptions")
	if bound == nil {
		return
	}
	runOneDescription(app, bound, msg.ProjectID, msg.FileID)
}

// describerSweep fans out across every project this install can
// dispatch against, then runs sweepOne per project. See
// transcriberSweep for the rationale — same pattern.
func describerSweep(app *sdk.AppCtx) {
	log := app.Logger()
	projects, err := app.PlatformAPI().ListProjects()
	if err != nil || len(projects) == 0 {
		if err != nil {
			log.Warn("describer: list projects failed; sweeping current project only", "err", err)
		}
		describerSweepOne(app)
		return
	}
	for _, p := range projects {
		if p.ID == "" {
			continue
		}
		describerSweepOne(app.WithProject(p.ID))
	}
}

func describerSweepOne(app *sdk.AppCtx) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()
	pid := app.CurrentProject()
	if pid == "" {
		return
	}

	bound := app.IntegrationFor("descriptions")
	if bound == nil {
		// Surface once per sweep — same gentle-degrade pattern as the
		// transcriber. Don't write to rows; they'll be picked up next
		// sweep when an integration is bound. This avoids polluting
		// description_error with "no integration" noise that gets
		// confusing if the integration is connected later.
		return
	}

	batch := parseConfigIntFallback(cfg.Get("describe_batch_size"), 5)
	cooldown := parseConfigIntFallback(cfg.Get("describe_retry_cooldown_seconds"), 600)

	candidates, err := describeCandidates(db, pid, batch, cooldown)
	if err != nil {
		log.Error("describe candidates failed", "err", err)
		return
	}
	for _, fid := range candidates {
		select {
		case <-app.Done():
			return
		default:
		}
		runOneDescription(app, bound, pid, fid)
	}
}

// runOneDescription is the per-row lifecycle. Builds a prompt based
// on what's available (transcript? image? both?), calls opencode-go,
// writes the response back as the description.
func runOneDescription(app *sdk.AppCtx, bound *sdk.BoundIntegration, projectID, fileID string) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()

	media, err := getMedia(db, projectID, fileID)
	if err != nil {
		_ = markDescribeAttempt(db, projectID, fileID, "media row missing: "+err.Error())
		return
	}

	// Defence in depth — the candidate query filters by both
	// description='' and source NOT IN ('human','agent'), but a
	// race between a manual write and the worker's claim could
	// theoretically slip a real description past the query. Skip
	// without marking, regardless of source: any non-empty
	// description (human, agent, ai-generated, imported) means
	// someone has already filled this in and we don't overwrite.
	if strings.TrimSpace(media.Description) != "" {
		return
	}
	if media.DescriptionSource == "human" || media.DescriptionSource == "agent" {
		return
	}

	// On audio-bearing files we ONLY describe when there's a usable
	// transcript. Earlier versions of this gate let through
	// terminal-but-not-ok statuses ("skipped" when no Deepgram is
	// bound, "failed" on network errors), which fell through to the
	// thumbnail-only branch in buildDescribePrompt and produced
	// misleading vision-only descriptions for videos whose dialogue
	// carried half the meaning ("Two people on a bridge" for a
	// scripted scene where the line was "admit you're freaked out
	// by my robot hand").
	//
	// Cleaner contract: has_audio=1 + transcript not ok → skip
	// without marking. The row stays a describer candidate, so:
	//   • transcript still pending/running     → next notify wakes it
	//   • transcript skipped / failed          → operator must
	//     re-attempt (bind Deepgram + delete the transcript row, or
	//     call media_transcribe force=true). Better than writing a
	//     bad description that masks the missing signal.
	if media.HasAudio {
		t, _ := getTranscript(db, projectID, media.FileID)
		if t == nil || t.Status != "ok" {
			return
		}
	}

	// Build the prompt + optional image_url. Three branches map onto
	// the three input paths. Each returns the messages array we POST
	// to opencode-go's chat_completion endpoint.
	messages, err := buildDescribePrompt(app, projectID, media)
	if err != nil {
		_ = markDescribeAttempt(db, projectID, fileID, "build prompt: "+err.Error())
		return
	}
	if messages == nil {
		// No usable input (e.g. silent video without a thumbnail yet).
		// Don't mark error — try again on the next sweep when the
		// indexer might have produced a thumbnail.
		return
	}

	model := strings.TrimSpace(cfg.Get("describe_model"))
	if model == "" {
		model = "kimi-k2.6"
	}
	maxTokens := parseConfigIntFallback(cfg.Get("describe_max_tokens"), 4000)
	timeout := time.Duration(parseConfigIntFallback(cfg.Get("describe_timeout_seconds"), 120)) * time.Second
	_ = timeout // timeout is enforced by the platform's HTTP client; reserved for future per-call override.

	args := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.3, // mostly factual; a hair of creativity for nicer prose
		"max_tokens":  maxTokens,
	}

	res, err := app.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("chat.complete"),
		args,
	)
	if err != nil {
		_ = markDescribeAttempt(db, projectID, fileID, "describe call: "+err.Error())
		return
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		_ = markDescribeAttempt(db, projectID, fileID, "describe non-2xx: "+truncate(body, 500))
		return
	}

	rawContent, err := extractChatContent(res.Data)
	if err != nil {
		_ = markDescribeAttempt(db, projectID, fileID, "parse describe: "+err.Error())
		return
	}
	if strings.TrimSpace(rawContent) == "" {
		// The reasoning model can come back empty when max_tokens was
		// too tight to fit reasoning + answer. Treat as a soft failure
		// — cooldown applies, we'll retry next sweep.
		_ = markDescribeAttempt(db, projectID, fileID, "describe returned empty content (reasoning may have consumed token budget)")
		return
	}

	// v0.13.0: prompt requests JSON object {description,
	// audience_rating, audience_reasoning}. Parse defensively — LLMs
	// occasionally wrap in ```json fences, emit prose preamble, or
	// refuse outright. Two retry-friendly fallbacks inside
	// parseDescribeJSON; if all fail we treat the raw text as a
	// description (back-compat) and leave audience_rating at
	// 'unrated' so the next sweep re-tries.
	parsed := parseDescribeJSON(rawContent)
	desc := parsed.Description
	if strings.TrimSpace(desc) == "" {
		// Refusal pattern — LLM declined to process. Empty description
		// IS itself a signal; we mark audience as 'adult' (most
		// conservative bucket) so downstream filters don't accidentally
		// treat refused content as general-audience-safe.
		if looksLikeRefusal(rawContent) {
			_ = setAudienceRating(db, projectID, fileID, "adult", "model declined to process — defaulting to most restrictive rating")
		}
		_ = markDescribeAttempt(db, projectID, fileID, "describe returned empty description (refusal or token budget)")
		return
	}

	if _, err := setDescription(db, projectID, fileID, DescriptionFields{
		Description: &desc,
		Source:      "ai-generated",
	}); err != nil {
		log.Error("describe setDescription failed", "file_id", fileID, "err", err)
		return
	}
	// Persist audience rating alongside description. The LLM may have
	// omitted it (older prompt response, parse fallback) — in that
	// case we leave the column at 'unrated' and the next describer
	// pass re-evaluates.
	if parsed.AudienceRating != "" {
		_ = setAudienceRating(db, projectID, fileID, parsed.AudienceRating, parsed.AudienceReasoning)
	}
	log.Info("auto-described",
		"file_id", fileID, "model", model,
		"chars", len(desc),
		"audience_rating", parsed.AudienceRating,
	)
	app.Emit("media.described", map[string]any{
		"file_id":         fileID,
		"chars":           len(desc),
		"source":          "ai-generated",
		"audience_rating": parsed.AudienceRating,
	})

	// media.completed coordinator. Description was the last
	// applicable stage on installs that have descriptions bound;
	// emit the completion here. Idempotent — earlier stages already
	// tried and bailed because description was empty.
	maybeEmitMediaCompleted(app, projectID, fileID)
}

// ─── prompt building ───────────────────────────────────────────────

// describeSystemPrompt asks the model for a structured JSON object
// combining the description with an AUDIENCE rating (not a moderation
// review queue — we're answering "who can this be shown to" rather
// than "is this safe"). Single LLM call, two pieces of information
// out. Failure modes (model refuses, output isn't valid JSON,
// missing fields) are handled by parseDescribeJSON below.
const describeSystemPrompt = `You are analysing a media file. Return a SINGLE valid JSON object — no preamble, no markdown fences, no commentary outside the JSON.

Schema (all fields required):
{
  "description": "1-2 short sentences (~200 chars max) describing the content. Concrete what's-there observations only. No emotion or intent speculation. Don't mention the medium ('this video', etc) — describe the content directly.",
  "audience_rating": "general" | "mature" | "adult",
  "audience_reasoning": "short explanation (~100 chars). Empty string when audience_rating is 'general'."
}

audience_rating rubric — pick the LEAST restrictive level the content fits:
  - "general":  appropriate for all audiences. Nothing suggestive, no profanity, no drug/alcohol references, no violence beyond cartoon-level.
  - "mature":   13+. Suggestive themes or attire, mild language, alcohol/tobacco/drug references, mild or stylised violence, distressing imagery.
  - "adult":    18+. Explicit sexual content or nudity, graphic violence/gore, hate symbols, hard drugs in use.

Be precise — don't escalate one tier above what the content actually shows. Output ONLY the JSON object. No reasoning, no drafts, no markdown fences.`

// buildDescribePrompt assembles the messages array for the chat call
// based on what's available for the file. Returns nil (no error) when
// there's nothing usable to describe yet — caller skips without
// marking, so the next sweep gets another shot.
func buildDescribePrompt(app *sdk.AppCtx, projectID string, media *MediaRow) ([]map[string]any, error) {
	transcript, _ := getTranscript(app.AppDB(), projectID, media.FileID)
	hasTranscript := transcript != nil && transcript.Status == "ok" && strings.TrimSpace(transcript.Text) != ""

	// Project context — operator-set name + description from the
	// platform's projects table, surfaced through WhoAmI. When set,
	// they go in as a second system message so generated descriptions
	// land in the right register ("internal team standups", "cooking
	// show clips"). WhoAmI is sub-second-cached in the SDK so a
	// per-prompt call is cheap.
	systemMessages := []map[string]any{{"role": "system", "content": describeSystemPrompt}}
	if id, _ := app.PlatformAPI().WhoAmI(); id != nil {
		ctx := strings.TrimSpace(projectContextLine(id.ProjectName, id.ProjectDescription))
		if ctx != "" {
			systemMessages = append(systemMessages, map[string]any{"role": "system", "content": ctx})
		}
	}

	// v0.13.0: build an image set, not just a single thumbnail. For
	// videos with keyframes we sample a handful evenly across the
	// timeline so the LLM sees scene variety, not just t=1s. The
	// canonical thumbnail (always at position 0 in our derivations
	// table) goes first as the "best representative" frame.
	imageURLs := buildPromptImageURLs(app, projectID, media)
	hasImages := len(imageURLs) > 0

	imageContentBlocks := make([]map[string]any, 0, len(imageURLs))
	for _, u := range imageURLs {
		imageContentBlocks = append(imageContentBlocks, map[string]any{
			"type": "image_url", "image_url": map[string]any{"url": u},
		})
	}

	switch {
	case hasImages && hasTranscript:
		// Multimodal: keyframe set + transcript. Best signal.
		content := []map[string]any{
			{"type": "text", "text": "Representative frames and the transcript follow. Return the JSON object.\n\nTranscript:\n" + transcript.Text},
		}
		content = append(content, imageContentBlocks...)
		return append(systemMessages, map[string]any{"role": "user", "content": content}), nil

	case hasImages:
		// Vision-only: image or silent video. Up to N keyframes when
		// available, otherwise just the canonical thumbnail.
		content := []map[string]any{
			{"type": "text", "text": "Representative frames follow. Return the JSON object."},
		}
		content = append(content, imageContentBlocks...)
		return append(systemMessages, map[string]any{"role": "user", "content": content}), nil

	case hasTranscript:
		// Audio-only: transcript is all we have.
		return append(systemMessages, map[string]any{
			"role":    "user",
			"content": "The transcript follows. Return the JSON object.\n\nTranscript:\n" + transcript.Text,
		}), nil
	}

	// Nothing usable yet — silent video without a thumbnail derivation,
	// or an image where the file isn't readable. Skip without marking.
	return nil, nil
}

// buildPromptImageURLs collects the image URLs the describer feeds
// into the multimodal LLM call.
//
//   Images:  the file itself (no thumbnail derivation step).
//   Video:   canonical thumbnail + up to N evenly-sampled keyframes
//            (when present). The thumbnail is the "best
//            representative" frame, picked by the indexer via the
//            multi-seek + luma-check pipeline; keyframes are
//            timeline samples that add scene variety.
//   Audio-only: empty (no frames).
//
// Sample count comes from describe_keyframe_sample_count config
// (default 4). When fewer keyframes exist than the sample count, we
// include all of them. Order matters: the LLM weights the first
// image highest, so we put the canonical thumbnail first.
func buildPromptImageURLs(app *sdk.AppCtx, projectID string, media *MediaRow) []string {
	if !media.HasVideo && !media.IsImage {
		return nil
	}
	cfg := app.Config()
	sampleCount := parseConfigIntFallback(cfg.Get("describe_keyframe_sample_count"), 4)
	if sampleCount < 1 {
		sampleCount = 1
	}

	sc := newStorageClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	signURL := func(storageFileIDStr string) (string, bool) {
		id, err := strconv.ParseInt(storageFileIDStr, 10, 64)
		if err != nil {
			return "", false
		}
		u, err := sc.GetSignedURL(ctx, projectID, id, 30*60)
		if err != nil {
			return "", false
		}
		return u, true
	}

	urls := make([]string, 0, sampleCount+1)

	// Canonical thumbnail first (or the image file itself for images).
	var thumbStorageID string
	for _, d := range media.Derivations {
		if d.Kind == "thumbnail" && d.Status == "ok" && d.PositionMs == 0 {
			thumbStorageID = d.StorageFileID
			break
		}
	}
	if thumbStorageID != "" {
		if u, ok := signURL(thumbStorageID); ok {
			urls = append(urls, u)
		}
	} else if media.IsImage {
		// Single-frame image: the source file is the input.
		if u, ok := signURL(media.FileID); ok {
			urls = append(urls, u)
		}
	}

	// Keyframes — sample evenly across whatever's available.
	if media.HasVideo && !media.IsImage {
		keyframes := make([]DerivationRow, 0)
		for _, d := range media.Derivations {
			if d.Kind == "keyframe" && d.Status == "ok" {
				keyframes = append(keyframes, d)
			}
		}
		// Already sorted by position via listDerivations, but defensive.
		if len(keyframes) > 0 {
			// Pick `sampleCount` evenly across the keyframe list.
			picked := sampleEvenly(keyframes, sampleCount)
			for _, k := range picked {
				if u, ok := signURL(k.StorageFileID); ok {
					urls = append(urls, u)
				}
			}
		}
	}
	return urls
}

// sampleEvenly returns at most `n` items from `rows`, spaced as
// evenly as possible. Identity when len(rows) <= n.
func sampleEvenly(rows []DerivationRow, n int) []DerivationRow {
	if n <= 0 || len(rows) == 0 {
		return nil
	}
	if len(rows) <= n {
		return rows
	}
	out := make([]DerivationRow, 0, n)
	// Use float steps so the last index lands near len-1 instead of
	// halfway through.
	step := float64(len(rows)-1) / float64(n-1)
	for i := 0; i < n; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(rows) {
			idx = len(rows) - 1
		}
		out = append(out, rows[idx])
	}
	return out
}

// projectContextLine builds a short system-prompt addendum with the
// operator-set project name + description. Returns "" when both are
// empty so callers can no-op silently — global installs and projects
// the operator hasn't filled in shouldn't get a stray "Project: " in
// their prompt.
func projectContextLine(name, description string) string {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	switch {
	case name == "" && description == "":
		return ""
	case name != "" && description != "":
		return "This file belongs to project: " + name + " — " + description +
			"\nUse this as context for what the file is likely about, but only mention details you can directly observe in the frame or transcript."
	case name != "":
		return "This file belongs to project: " + name +
			"\nUse this as light context, but only mention details you can directly observe."
	default:
		return "Project context: " + description +
			"\nUse this as light context, but only mention details you can directly observe."
	}
}

// ─── chat-completion response parser ───────────────────────────────

// extractChatContent pulls the assistant message's content out of a
// chat_completion response.
//
// Two failure modes worth surfacing distinctly so the cooldown gate
// + the operator's settings tweak both have actionable info:
//
//  1. finish_reason=length — the model ran out of token budget. With
//     a reasoning model (Kimi K2.6, DeepSeek V4 Pro) and a tight
//     max_tokens this is the common case, and the partial content
//     is the chain-of-thought scratchpad, NOT a user-facing answer.
//     Older versions of this function fell back to writing that raw
//     reasoning into the description column — never the right call.
//     Now we surface the truncation as an error, leave the row's
//     description empty, and the operator can bump max_tokens or
//     switch to a non-reasoning vision model.
//
//  2. Empty content for any other reason — model returned content="",
//     filtered, refused, etc. Same: error out, don't write garbage.
func extractChatContent(data json.RawMessage) (string, error) {
	var env struct {
		Choices []struct {
			Message struct {
				// Content is `string | null` in OpenAI's schema; we
				// decode as RawMessage so we can distinguish the two.
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("decode chat envelope: %w", err)
	}
	if len(env.Choices) == 0 {
		return "", errors.New("response has no choices")
	}
	choice := env.Choices[0]
	msg := choice.Message

	// Truncation: refuse the partial output. The bytes there are
	// either reasoning trace (useless) or a half-finished sentence
	// (writing a 6-letter description doesn't help anyone).
	if choice.FinishReason == "length" {
		return "", fmt.Errorf("response truncated (finish_reason=length) — bump describe_max_tokens or switch to a non-reasoning vision model")
	}

	if len(msg.Content) > 0 && string(msg.Content) != "null" {
		var s string
		if err := json.Unmarshal(msg.Content, &s); err == nil {
			out := strings.TrimSpace(s)
			if out != "" {
				return out, nil
			}
		}
	}
	return "", fmt.Errorf("empty content (finish_reason=%s) — model returned no answer", choice.FinishReason)
}
