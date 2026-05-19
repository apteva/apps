package main

// media.completed coordinator.
//
// Goal: one event per file that says "everything that's going to happen
// on this install has happened", regardless of whether the optional
// transcripts / descriptions integrations are bound. Subscribers can
// listen on a single topic and trust that the file is in its
// terminal-for-this-install state.
//
// Why not just use media.described:
//   - media.described only fires when an LLM integration is bound
//     AND the describer actually writes a description. An install
//     with no LLM never emits it — subscribers starve.
//   - media.transcribed only fires when Deepgram is bound — same
//     starvation if absent.
//   - media.derived fires after thumbnail/waveform only — premature
//     if the install DOES have LLM/Deepgram and we want to wait.
//
// media.completed is the union: fires after the LAST applicable stage
// for the file's content + the install's configuration.
//
// Design:
//   - Idempotent via a media.completed_at column (added in 009_*.sql).
//     The UPDATE … WHERE completed_at IS NULL gates the emit so two
//     stages racing to call this only fire once.
//   - Called from FOUR places, whichever runs last actually wins:
//       1. tail of the local indexer (after derive + notify)
//       2. tail of the remote indexer
//       3. tail of the transcriber (after media.transcribed)
//       4. tail of the describer (after media.described)
//   - Eligibility:
//       * probe_status must be 'ok' (row exists + indexable)
//       * required derivations present (thumbnail for video/image,
//         waveform for audio-only)
//       * if has_audio AND transcripts integration is bound: transcript
//         row must exist in a TERMINAL status (ok | failed | skipped).
//         Pending/running means "still in flight, wait".
//       * if descriptions integration is bound: description must be
//         non-empty. The describer's permanent-skip cases (transcript
//         not ok for audio-bearing files) means we'd wait forever
//         there if neither side ever resolves — accepted tradeoff,
//         since "transcript permanently failed" is itself terminal
//         and the describer will then run from the transcript-side
//         tail's call to this helper.
//
// What we deliberately don't do:
//   - We don't fire media.completed on probe failure / unsupported /
//     skipped_size. Those rows aren't "ready" — the indexer skipped
//     them. Subscribers that want to know about them can listen to
//     media.indexed and read its status field.

import (
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// maybeEmitMediaCompleted checks whether file's pipeline has reached
// its terminal state on this install, and if so, emits
// media.completed exactly once. Safe to call from every stage —
// idempotency is enforced by the DB write.
//
// The caller threads through the bound integration check rather than
// re-resolving here, because the resolution depends on app's project
// context which the caller already set.
func maybeEmitMediaCompleted(app *sdk.AppCtx, projectID, fileID string) {
	if app == nil || projectID == "" || fileID == "" {
		return
	}
	db := app.AppDB()
	if db == nil {
		return
	}
	row, err := getMedia(db, projectID, fileID)
	if err != nil || row == nil {
		return
	}
	// Pre-check: already emitted? Avoid wasted derivation/transcript
	// queries on the common idempotent re-call path.
	var already string
	_ = db.QueryRow(`SELECT COALESCE(completed_at,'') FROM media WHERE project_id = ? AND file_id = ?`,
		projectID, fileID).Scan(&already)
	if already != "" {
		return
	}
	if row.ProbeStatus != "ok" {
		return
	}

	// Required derivations. Thumbnail for video/image, waveform for
	// audio-only. Both can fail without blocking — we treat the
	// indexer's "I tried and moved on" as a terminal-enough state to
	// allow downstream stages to fire. (Re-deriving is a separate
	// operator action.)
	derivs, _ := listDerivations(db, projectID, fileID)
	var hasThumb, hasWave bool
	var derivThumbTerminal, derivWaveTerminal bool
	for _, d := range derivs {
		switch d.Kind {
		case "thumbnail":
			if d.Status == "ok" {
				hasThumb = true
			}
			if d.Status == "ok" || d.Status == "failed" {
				derivThumbTerminal = true
			}
		case "waveform":
			if d.Status == "ok" {
				hasWave = true
			}
			if d.Status == "ok" || d.Status == "failed" {
				derivWaveTerminal = true
			}
		}
	}
	expectsThumb := row.HasVideo || row.IsImage
	expectsWave := row.HasAudio && !row.HasVideo
	// If we expect a derivation and there's no terminal record yet,
	// the indexer is still in flight. The local indexer writes a
	// derivation row only on SUCCESS; failure cases leave nothing
	// in the table, so we can't tell "trying" from "tried and
	// failed." We treat "no row at all" as "still in flight" for
	// the first attempt window. The periodic indexer sweep
	// eventually retries failures, so a permanently-broken
	// derivation eventually settles to "failed" (then we proceed)
	// or "ok" (then we proceed). The pragmatic conservative choice
	// is to wait — operators with a stuck thumbnail can re-trigger
	// the indexer to force a terminal state.
	if expectsThumb && !hasThumb && !derivThumbTerminal {
		return
	}
	if expectsWave && !hasWave && !derivWaveTerminal {
		return
	}

	// Transcript stage. Only matters for audio-bearing files when
	// the transcripts integration is bound; otherwise the file is
	// transcript-not-applicable and that's terminal.
	transcriptsBound := app.IntegrationFor("transcripts") != nil
	var hasTranscript bool
	if row.HasAudio && transcriptsBound {
		t, _ := getTranscript(db, projectID, fileID)
		if t == nil {
			// Transcriber hasn't reached this row yet (notify hasn't
			// fired or sweep hasn't picked it up). Wait — the
			// transcriber's tail will retry this check after it does.
			return
		}
		switch strings.ToLower(t.Status) {
		case "ok":
			hasTranscript = true
		case "failed", "skipped":
			// Terminal but not successful. The describer's gate
			// rejects this case (`describer.go:235` — "transcript
			// not ok → skip without marking"), which means a
			// descriptions-bound install with a failed transcript
			// will NEVER produce a description. We still consider
			// the file complete here so subscribers aren't starved
			// forever; has_description will reflect reality (false).
		default:
			// pending / running / queued — still in flight.
			return
		}
	}

	// Description stage. Only matters when an LLM integration is
	// bound; otherwise the file is description-not-applicable.
	descriptionsBound := app.IntegrationFor("descriptions") != nil
	var hasDescription bool
	if descriptionsBound {
		if strings.TrimSpace(row.Description) != "" {
			hasDescription = true
		} else {
			// Three sub-cases:
			//   (a) describer hasn't run yet              → wait
			//   (b) describer is gated on missing transcript → terminal-ish
			//   (c) describer attempted + failed permanently → still retrying
			//
			// We can distinguish (a) from (b/c) via
			// description_attempted_at being non-NULL. For (b), the
			// transcript check above already returned for pending
			// transcripts, so reaching here with has_audio means
			// transcript is in a terminal state — (b) ⇒ describer
			// won't run, which is itself terminal for this install's
			// config, so we accept it as "done with no description."
			audioBlocksDescriber := row.HasAudio && !hasTranscript
			if audioBlocksDescriber {
				// (b) — terminal. Don't wait.
			} else if row.DescriptionAttemptedAt == "" {
				// (a) — describer hasn't even tried. Wait.
				return
			} else {
				// (c) — describer tried + failed. Cooldown will
				// retry; for v1 we treat this as "not terminal
				// yet" to give the retry a chance. Wait.
				return
			}
		}
	}

	// All applicable stages reached terminal state. Single UPDATE
	// races safely — only one caller wins. emit on win.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		`UPDATE media SET completed_at = ? WHERE project_id = ? AND file_id = ? AND completed_at IS NULL`,
		now, projectID, fileID)
	if err != nil {
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return // another caller emitted first
	}

	app.Emit("media.completed", map[string]any{
		"file_id":         fileID,
		"name":            row.Name,
		"folder":          row.Folder,
		"has_video":       row.HasVideo,
		"has_audio":       row.HasAudio,
		"is_image":        row.IsImage,
		"duration_ms":     row.DurationMs,
		"width":           row.Width,
		"height":          row.Height,
		"has_thumbnail":   hasThumb,
		"has_waveform":    hasWave,
		"has_transcript":  hasTranscript,
		"has_description": hasDescription,
	})
}
