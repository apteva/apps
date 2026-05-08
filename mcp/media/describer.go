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
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// describerNotify carries file_ids that need a describer pass NOW
// rather than waiting for the next periodic tick. Buffered so a
// burst of indexer/transcriber writes never blocks the producer;
// notifyDescriber drops on overflow because the periodic sweep is
// the safety net for anything we miss.
//
// Set in startDescriber; nil otherwise (auto_describe_enabled=false
// installs don't run the loop). notifyDescriber is a no-op then.
var describerNotify chan string

// notifyDescriber is called by the indexer + transcriber when a
// file might be newly eligible for description (probe just
// completed, transcript just landed, etc.). Non-blocking — the
// channel buffer is bounded so a backlog falls back to the
// periodic sweep.
func notifyDescriber(fileID string) {
	if describerNotify == nil || fileID == "" {
		return
	}
	select {
	case describerNotify <- fileID:
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
	describerNotify = make(chan string, 100)
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
		case fid := <-describerNotify:
			// Indexer or transcriber just made this file eligible.
			// Process just THIS row immediately rather than wait
			// up to interval seconds for the next tick.
			describerOne(app, fid)
		}
	}
}

// describerOne runs the describer for a single file_id signalled
// via notifyDescriber. Goes through runOneDescription so all the
// normal guards (human-set check, cooldown, integration binding,
// no-input skip) still apply.
func describerOne(app *sdk.AppCtx, fileID string) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if pid == "" || fileID == "" {
		return
	}
	bound := app.IntegrationFor("descriptions")
	if bound == nil {
		return
	}
	runOneDescription(app, bound, pid, fileID)
}

func describerSweep(app *sdk.AppCtx) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if pid == "" {
		log.Info("describer: no APTEVA_PROJECT_ID; skipping sweep")
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

	desc, err := extractChatContent(res.Data)
	if err != nil {
		_ = markDescribeAttempt(db, projectID, fileID, "parse describe: "+err.Error())
		return
	}
	if strings.TrimSpace(desc) == "" {
		// The reasoning model can come back empty when max_tokens was
		// too tight to fit reasoning + answer. Treat as a soft failure
		// — cooldown applies, we'll retry next sweep.
		_ = markDescribeAttempt(db, projectID, fileID, "describe returned empty content (reasoning may have consumed token budget)")
		return
	}

	if _, err := setDescription(db, projectID, fileID, DescriptionFields{
		Description: &desc,
		Source:      "ai-generated",
	}); err != nil {
		log.Error("describe setDescription failed", "file_id", fileID, "err", err)
		return
	}
	log.Info("auto-described", "file_id", fileID, "model", model, "chars", len(desc))
	app.Emit("media.described", map[string]any{
		"file_id": fileID,
		"chars":   len(desc),
		"source":  "ai-generated",
	})
}

// ─── prompt building ───────────────────────────────────────────────

const describeSystemPrompt = "You write very brief media descriptions. Output 1-2 short sentences, ~200 characters maximum. Plain prose, no preamble, no headings, no quotes. Focus on subjects, setting, and action — concrete what's-there observations only. No speculation about emotion or intent. Don't mention the medium ('this video', 'this image', 'this audio') — describe the content directly.\n\nIMPORTANT: respond with ONLY the final 1-2 sentences. No reasoning, no drafts, no character counts, no thinking out loud. Just the description."

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

	var thumbURL string
	if media.HasVideo || media.IsImage {
		// Use thumbnail for video, the file itself for image. Fall
		// back to nothing when not available — vision models can't
		// describe what they can't see.
		var ref *DerivationRow
		for i := range media.Derivations {
			if media.Derivations[i].Kind == "thumbnail" && media.Derivations[i].Status == "ok" {
				ref = &media.Derivations[i]
				break
			}
		}
		if ref != nil {
			sc := newStorageClient()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ref64, _ := strconv.ParseInt(ref.StorageFileID, 10, 64)
			if u, err := sc.GetSignedURL(ctx, projectID, ref64, 30*60); err == nil {
				thumbURL = u
			}
		} else if media.IsImage {
			// Images: the file itself is the input.
			sc := newStorageClient()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fid, _ := strconv.ParseInt(media.FileID, 10, 64)
			if u, err := sc.GetSignedURL(ctx, projectID, fid, 30*60); err == nil {
				thumbURL = u
			}
		}
	}

	switch {
	case thumbURL != "" && hasTranscript:
		// Multimodal: video frame + transcript. Best signal.
		return append(systemMessages, map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "A representative frame and the transcript follow. Describe the content in 1-2 short sentences (under ~200 chars).\n\nTranscript:\n" + transcript.Text},
				{"type": "image_url", "image_url": map[string]any{"url": thumbURL}},
			},
		}), nil

	case thumbURL != "":
		// Vision-only: image or silent video.
		return append(systemMessages, map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "Describe the content of this image in 1-2 short sentences (under ~200 chars)."},
				{"type": "image_url", "image_url": map[string]any{"url": thumbURL}},
			},
		}), nil

	case hasTranscript:
		// Audio-only: transcript is all we have.
		return append(systemMessages, map[string]any{
			"role":    "user",
			"content": "Summarise what's said in 1-2 short sentences (under ~200 chars).\n\nTranscript:\n" + transcript.Text,
		}), nil
	}

	// Nothing usable yet — silent video without a thumbnail derivation,
	// or an image where the file isn't readable. Skip without marking.
	return nil, nil
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
