package main

// Transcriber worker. Polls media rows that have audio + no transcript
// (or a stale one), mints a signed URL via storage, calls Deepgram's
// listen tool through the integration, normalises the response into
// our TranscriptRow shape, and persists.
//
// Runs as one goroutine started from OnMount. Single-threaded by
// design — Deepgram is real-time-ish on long media and bills per
// minute; parallelism would just light money on fire faster.
//
// Skips files when:
//   - the deepgram integration isn't bound (degraded gracefully)
//   - the file's duration exceeds transcribe_max_duration_minutes
//   - auto-transcribe is disabled (config kill switch)
//
// Failures land on the row as status=failed; the next sweep doesn't
// retry them automatically (manual media_transcribe with force=true
// is the override).

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

// transcriberNotify carries file_ids that just became eligible —
// indexer fires it the moment probe_status=ok AND has_audio=1.
// Buffered so a burst of file.added events never blocks the
// indexer; on overflow we drop and let the periodic sweep catch
// up. Set in startTranscriber; nil otherwise (auto-transcribe
// disabled), notifyTranscriber is a no-op then.
var transcriberNotify chan string

// notifyTranscriber is called by the indexer when a media row
// finishes probing and has audio. Non-blocking — channel buffer
// is bounded so a backlog falls back to the periodic sweep.
func notifyTranscriber(fileID string) {
	if transcriberNotify == nil || fileID == "" {
		return
	}
	select {
	case transcriberNotify <- fileID:
	default:
	}
}

// startTranscriber kicks off the auto-transcribe goroutine. Honours
// the transcribe_auto config kill switch — when false, the worker
// doesn't tick at all; manual media_transcribe still works.
func startTranscriber(app *sdk.AppCtx) {
	cfg := app.Config()
	if !configBool(cfg.Get("transcribe_auto"), true) {
		app.Logger().Info("transcribe_auto=false — auto-transcriber disabled (manual still works)")
		return
	}
	transcriberNotify = make(chan string, 100)
	go transcriberLoop(app)
	app.Logger().Info("transcriber started")
}

func transcriberLoop(app *sdk.AppCtx) {
	log := app.Logger()
	cfg := app.Config()
	interval := parseConfigIntFallback(cfg.Get("transcribe_poll_seconds"), 60)
	tick := time.NewTicker(time.Duration(interval) * time.Second)
	defer tick.Stop()

	// First tick immediately so a freshly-installed media doesn't
	// wait a full interval before transcribing existing audio.
	transcriberSweep(app)

	for {
		select {
		case <-app.Done():
			log.Info("transcriber stopping")
			return
		case <-tick.C:
			// Periodic safety-net sweep. Picks up rows the notify
			// path missed (channel overflow, app restart while a row
			// was queued, integration newly bound, etc).
			transcriberSweep(app)
		case fid := <-transcriberNotify:
			// Indexer just made this file eligible. Process just
			// THIS row immediately rather than wait the next tick.
			transcriberOne(app, fid)
		}
	}
}

// transcriberOne runs the transcriber for a single file_id signalled
// via notifyTranscriber. Goes through insertPendingTranscript +
// runOneTranscription so all the normal guards (duration cap,
// integration binding, dedup-on-source-sha) still apply.
func transcriberOne(app *sdk.AppCtx, fileID string) {
	log := app.Logger()
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if pid == "" || fileID == "" {
		return
	}
	bound := app.IntegrationFor("transcripts")
	if bound == nil {
		// No integration — fall through silently. Periodic sweep
		// will mark candidates as skipped with a clearer message.
		return
	}
	if err := insertPendingTranscript(app.AppDB(), pid, fileID, "auto"); err != nil {
		log.Error("queue pending transcript failed", "file_id", fileID, "err", err)
		return
	}
	row, err := claimNextPendingTranscript(app.AppDB())
	if err != nil {
		if !isNoRows(err) {
			log.Error("claim transcript failed", "err", err)
		}
		return
	}
	runOneTranscription(app, bound, row)
}

// transcriberSweep does one pass: queue eligible candidates, then
// drain pending rows. Each row is run synchronously — we don't
// parallelise (cost + Deepgram throughput).
func transcriberSweep(app *sdk.AppCtx) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if pid == "" {
		log.Info("transcriber: no APTEVA_PROJECT_ID; skipping sweep")
		return
	}

	// 1. Find eligible media rows that don't yet have a transcript
	// (or whose source has changed). Insert pending rows for each.
	batch := parseConfigIntFallback(cfg.Get("transcribe_batch_size"), 10)
	candidates, err := transcribeCandidates(db, pid, batch)
	if err != nil {
		log.Error("transcribe_candidates failed", "err", err)
	}
	for _, fid := range candidates {
		if err := insertPendingTranscript(db, pid, fid, "auto"); err != nil {
			log.Error("queue pending transcript failed", "file_id", fid, "err", err)
		}
	}

	// 2. Drain pending rows. Stop if integration unavailable so we
	// don't churn the queue against a wall.
	bound := app.IntegrationFor("transcripts")
	if bound == nil {
		// Surface once per sweep; flip pending rows to skipped so we
		// don't pile them up while no integration is bound.
		for _, fid := range candidates {
			_ = transcriptMarkSkipped(db, fid, "no transcripts integration bound — connect Deepgram in app settings")
		}
		log.Info("no transcripts integration bound; skipping pending rows")
		return
	}

	for {
		row, err := claimNextPendingTranscript(db)
		if err != nil {
			// sql.ErrNoRows = queue empty; anything else is a real error.
			if isNoRows(err) {
				return
			}
			log.Error("claim transcript failed", "err", err)
			return
		}
		runOneTranscription(app, bound, row)
		select {
		case <-app.Done():
			return
		default:
		}
	}
}

// runOneTranscription owns a single transcript row's lifecycle: gate
// against duration cap, mint signed URL, invoke Deepgram, normalise,
// persist. All terminal states write the DB; the worker never leaves
// a row stuck in 'running'.
func runOneTranscription(app *sdk.AppCtx, bound *sdk.BoundIntegration, row *TranscriptRow) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()

	// Gate: skip if source duration exceeds the cap.
	maxMinutes := parseConfigIntFallback(cfg.Get("transcribe_max_duration_minutes"), 120)
	media, err := getMedia(db, row.ProjectID, row.FileID)
	if err != nil {
		_ = transcriptMarkFailed(db, row.FileID, "media row missing: "+err.Error())
		return
	}
	if media.DurationMs > int64(maxMinutes)*60_000 {
		_ = transcriptMarkSkipped(db, row.FileID,
			fmt.Sprintf("source duration %d ms exceeds cap of %d minutes", media.DurationMs, maxMinutes))
		return
	}

	// Mint a signed URL Deepgram can fetch directly.
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(parseConfigIntFallback(cfg.Get("transcribe_timeout_seconds"), 600))*time.Second)
	defer cancel()

	sc := newStorageClient()
	fileID, err := strconv.ParseInt(row.FileID, 10, 64)
	if err != nil {
		_ = transcriptMarkFailed(db, row.FileID, "file_id not numeric: "+err.Error())
		return
	}
	signedURL, err := sc.GetSignedURL(ctx, row.ProjectID, fileID, 30*60) // 30-min TTL — long enough for Deepgram
	if err != nil {
		_ = transcriptMarkFailed(db, row.FileID, "signed URL: "+err.Error())
		return
	}

	// Build Deepgram args. Defaults are deliberately conservative —
	// see deepgram integration spec: "Do NOT enable
	// paragraphs/utterances/diarize/topics/intents/sentiment/summarize
	// unless you specifically need that extra data."
	model := strings.TrimSpace(cfg.Get("transcribe_model"))
	if model == "" {
		model = "nova-3"
	}
	language := strings.TrimSpace(cfg.Get("transcribe_language"))
	if language == "" {
		language = "auto"
	}
	args := map[string]any{
		"url":           signedURL,
		"model":         model,
		"smart_format":  true,
	}
	if language == "auto" {
		args["detect_language"] = true
	} else {
		args["language"] = language
	}
	if configBool(cfg.Get("transcribe_diarize"), false) {
		args["diarize"] = true
	}

	// Call Deepgram via the platform.
	res, err := app.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("transcribe"),
		args,
	)
	if err != nil {
		_ = transcriptMarkFailed(db, row.FileID, "deepgram call: "+err.Error())
		return
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		_ = transcriptMarkFailed(db, row.FileID, "deepgram non-2xx: "+truncate(body, 500))
		return
	}

	// Parse Deepgram's response into our TranscriptRow shape.
	parsed, err := parseDeepgramResponse(res.Data)
	if err != nil {
		_ = transcriptMarkFailed(db, row.FileID, "parse deepgram: "+err.Error())
		return
	}

	// Persist.
	final := &TranscriptRow{
		FileID:       row.FileID,
		ProjectID:    row.ProjectID,
		SourceSHA256: media.SourceSHA256,
		Status:       "ok",
		Language:     parsed.Language,
		Text:         parsed.Text,
		Provider:     "deepgram",
		Model:        model,
		DurationMs:   media.DurationMs,
		SourceKind:   row.SourceKind,
	}
	if len(parsed.Segments) > 0 {
		segsJSON, _ := formatSegments(parsed.Segments)
		final.Segments = segsJSON
	}
	if err := transcriptMarkOk(db, final); err != nil {
		log.Error("transcript mark ok failed", "file_id", row.FileID, "err", err)
		return
	}
	log.Info("transcribed", "file_id", row.FileID, "language", parsed.Language, "chars", len(parsed.Text))

	app.Emit("media.transcribed", map[string]any{
		"file_id":  row.FileID,
		"language": parsed.Language,
		"chars":    len(parsed.Text),
	})

	// Wake the describer immediately — for audio + video files we
	// deliberately wait for the transcript before describing so the
	// LLM call gets the multimodal {thumbnail + transcript} input.
	// No-op when describer isn't running or queue is full.
	notifyDescriber(row.FileID)
}

// ─── Deepgram response parsing ─────────────────────────────────────

// parsedTranscript is the lifted subset of Deepgram's response we
// care about. Deepgram returns a deeply-nested envelope (results →
// channels[] → alternatives[]); we walk it once and lift only the
// fields we persist.
type parsedTranscript struct {
	Text     string
	Language string
	Segments []TranscriptSegment
}

// parseDeepgramResponse handles Deepgram's listen response shape.
// Doc: https://developers.deepgram.com/reference/pre-recorded
//
// Shape (simplified):
//
//	{
//	  "metadata": { "detected_language": "en" },
//	  "results": {
//	    "channels": [
//	      {
//	        "detected_language": "en",
//	        "alternatives": [
//	          { "transcript": "...", "paragraphs": { "paragraphs": [...] }, "words": [...] }
//	        ]
//	      }
//	    ]
//	  }
//	}
//
// We grab channels[0].alternatives[0].transcript for the plain text,
// and walk paragraphs[].sentences[] when present for timed segments
// (the segment shape we expose to callers). Words/diarisation aren't
// requested by default so we don't try to lift them here.
func parseDeepgramResponse(data json.RawMessage) (*parsedTranscript, error) {
	var env struct {
		Metadata struct {
			DetectedLanguage string `json:"detected_language"`
		} `json:"metadata"`
		Results struct {
			Channels []struct {
				DetectedLanguage string `json:"detected_language"`
				Alternatives     []struct {
					Transcript string `json:"transcript"`
					Paragraphs struct {
						Paragraphs []struct {
							Sentences []struct {
								Text  string  `json:"text"`
								Start float64 `json:"start"`
								End   float64 `json:"end"`
							} `json:"sentences"`
						} `json:"paragraphs"`
					} `json:"paragraphs"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if len(env.Results.Channels) == 0 || len(env.Results.Channels[0].Alternatives) == 0 {
		return nil, errors.New("deepgram response has no channels/alternatives")
	}
	alt := env.Results.Channels[0].Alternatives[0]

	out := &parsedTranscript{
		Text:     strings.TrimSpace(alt.Transcript),
		Language: env.Metadata.DetectedLanguage,
	}
	if out.Language == "" {
		out.Language = env.Results.Channels[0].DetectedLanguage
	}
	for _, p := range alt.Paragraphs.Paragraphs {
		for _, s := range p.Sentences {
			out.Segments = append(out.Segments, TranscriptSegment{
				StartMs: int64(s.Start * 1000),
				EndMs:   int64(s.End * 1000),
				Text:    s.Text,
			})
		}
	}
	return out, nil
}

// ─── helpers ────────────────────────────────────────────────────────

func configBool(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def
	case "true", "1", "yes", "y":
		return true
	case "false", "0", "no", "n":
		return false
	}
	return def
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isNoRows(err error) bool {
	if err == nil {
		return false
	}
	// sql.ErrNoRows is returned by claim queries when nothing matches.
	return strings.Contains(err.Error(), "no rows")
}
