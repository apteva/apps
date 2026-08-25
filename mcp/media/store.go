package main

// SQLite reads/writes for the media + derivations tables. Every
// query is project-scoped — the indexer is single-tenant per
// install, but a future global-scope deploy will rely on this.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// MediaRow is the canonical shape returned by reads. JSON tags drive
// what /media and media_get expose; raw_probe stays a string so
// callers that want it pretty-print client-side.
type MediaRow struct {
	FileID       string `json:"file_id"`
	ProjectID    string `json:"project_id"`
	SourceSHA256 string `json:"source_sha256"`
	// Folder mirrors storage.files.folder on the row so media's own
	// queries can filter + paginate by folder without joining to
	// storage. Populated by upsertMedia at probe time + by the
	// storage `file.updated` event handler when storage's
	// files_move changes a folder.
	Folder string `json:"folder,omitempty"`

	// Name mirrors storage.files.name so events + the media.completed
	// payload + UI rows can show a filename without round-tripping to
	// storage. Populated by upsertMedia at probe time; refreshed on
	// every re-index. Migration 010_media_name.sql added the column —
	// rows from before that migration carry "" until their next
	// reindex.
	Name string `json:"name,omitempty"`

	FormatName string `json:"format_name,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Bitrate    int64  `json:"bitrate,omitempty"`

	HasVideo bool `json:"has_video"`
	HasAudio bool `json:"has_audio"`
	IsImage  bool `json:"is_image"`

	// Width/Height in DISPLAY-space — what users see in a player.
	// The indexer pre-swaps these when the source has a 90°/270°
	// rotation tag, so downstream consumers don't need to know about
	// rotation at all. See probe.go::parseProbeBytes for the swap.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Rotation in degrees (0/90/180/270). Renderers read this and
	// emit `transpose=…` + `-noautorotate` so ffmpeg's filter chain
	// sees a frame oriented to match Width/Height above.
	Rotation   int     `json:"rotation,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	VideoCodec string  `json:"video_codec,omitempty"`

	Channels   int    `json:"channels,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`

	ProbeStatus string `json:"probe_status"`
	ProbeError  string `json:"probe_error,omitempty"`
	ProbeAt     string `json:"probe_at,omitempty"`

	RawProbe json.RawMessage `json:"raw_probe,omitempty"`

	// Consumer-owned workflow metadata. Unlike probe fields, these columns
	// are never written by upsertMedia, so re-indexing preserves them. Version
	// is a monotonic compare-and-swap revision incremented by
	// media_patch_metadata.
	Metadata        json.RawMessage `json:"metadata"`
	MetadataVersion int64           `json:"metadata_version"`

	// User/agent-supplied prose. Written via media_set_description;
	// never touched by upsertMedia (the indexer's probe upsert) so a
	// reprobe never wipes them.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	AltText     string `json:"alt_text,omitempty"`

	// Description provenance (v0.5). Lets agents + the panel tell
	// human-set descriptions from ai-generated ones, and lets the
	// auto-describer respect cooldown after a failed attempt.
	DescriptionSource      string `json:"description_source,omitempty"` // human|ai-generated|imported|''
	DescriptionUpdatedAt   string `json:"description_updated_at,omitempty"`
	DescriptionAttemptedAt string `json:"description_attempted_at,omitempty"`
	DescriptionError       string `json:"description_error,omitempty"`

	// Lifted from the transcripts table via LEFT JOIN so the row
	// carries enough state for the panel to render a status icon
	// without a second roundtrip. Empty when no transcript exists.
	TranscriptStatus string `json:"transcript_status,omitempty"`

	// Audience rating populated by the describer (v0.13.0+). One of
	// {unrated, general, mature, adult}. Reasoning is the LLM's short
	// explanation for non-general ratings; empty otherwise.
	AudienceRating    string `json:"audience_rating,omitempty"`
	AudienceReasoning string `json:"audience_reasoning,omitempty"`
	AudienceUpdatedAt string `json:"audience_updated_at,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	Derivations []DerivationRow `json:"derivations,omitempty"`
}

type DerivationRow struct {
	ID            int64  `json:"id"`
	FileID        string `json:"file_id"`
	Kind          string `json:"kind"`
	StorageFileID string `json:"storage_file_id"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	// PositionMs is the source timestamp in ms for keyframes; 0 for
	// thumbnail/waveform/cover (single-frame derivations). Migration
	// 012_keyframes.sql added the column and shifted the UNIQUE
	// constraint to (file_id, kind, position_ms).
	PositionMs  int64  `json:"position_ms,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	GeneratedAt string `json:"generated_at,omitempty"`
}

// upsertMedia INSERT-or-UPDATEs the media row. Source-of-truth fields
// (durations, codecs, folder) are written every time; status fields
// too. folder mirrors storage.files.folder so media can filter +
// paginate by folder without an enrichment roundtrip per query.
func upsertMedia(db *sql.DB, projectID string, fileID string, p *Probe, sha, folder, name string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO media (
			file_id, project_id, source_sha256, folder, name,
			format_name, duration_ms, bitrate,
			has_video, has_audio, is_image,
			width, height, rotation, fps, video_codec,
			channels, sample_rate, audio_codec,
			probe_status, probe_error, probe_at,
			raw_probe, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok', '', ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			project_id=excluded.project_id,
			source_sha256=excluded.source_sha256,
			folder=excluded.folder,
			name=excluded.name,
			format_name=excluded.format_name,
			duration_ms=excluded.duration_ms,
			bitrate=excluded.bitrate,
			has_video=excluded.has_video,
			has_audio=excluded.has_audio,
			is_image=excluded.is_image,
			width=excluded.width,
			height=excluded.height,
			rotation=excluded.rotation,
			fps=excluded.fps,
			video_codec=excluded.video_codec,
			channels=excluded.channels,
			sample_rate=excluded.sample_rate,
			audio_codec=excluded.audio_codec,
			probe_status='ok',
			probe_error='',
			probe_at=excluded.probe_at,
			raw_probe=excluded.raw_probe,
			updated_at=excluded.updated_at,
			force_probe=0`,
		fileID, projectID, sha, folder, name,
		p.FormatName, p.DurationMs, p.Bitrate,
		boolInt(p.HasVideo), boolInt(p.HasAudio), boolInt(p.IsImage),
		nullableInt(p.Width), nullableInt(p.Height), p.Rotation, nullableFloat(p.FPS), nullableStr(p.VideoCodec),
		nullableInt(p.Channels), nullableInt(p.SampleRate), nullableStr(p.AudioCodec),
		now, p.Raw, now,
	)
	return err
}

// updateFolder is the lightweight write the storage `file.updated`
// event handler calls when a file's folder changed (storage's
// files_move). Doesn't touch probe state.
func updateFolder(db *sql.DB, projectID, fileID, folder string) error {
	_, err := db.Exec(
		`UPDATE media SET folder = ?, updated_at = ? WHERE project_id = ? AND file_id = ?`,
		folder, time.Now().UTC().Format(time.RFC3339), projectID, fileID,
	)
	return err
}

// listChildFolders returns the immediate child folder names ONE
// level under `parent` that contain media (audio/video/image rows
// with probe_status='ok'). Mirrors storage's dbListChildFolders
// shape so the navigation pattern is identical.
//
// parent="/" means root; the result is the top-level folders that
// contain at least one media file. parent="/clips/" returns the
// children of /clips/ (e.g. "q3", "q4") — single names, dedup'd,
// no trailing slash.
//
// Folders that hold only non-media files (PDFs in /docs/, etc.)
// don't appear — agents browsing media never land somewhere with
// nothing to play.
func listChildFolders(db *sql.DB, projectID, parent string) ([]string, error) {
	if parent == "" {
		parent = "/"
	}
	rows, err := db.Query(
		`SELECT DISTINCT folder FROM media
		 WHERE project_id = ? AND folder LIKE ? AND folder != ?
		   AND probe_status = 'ok'
		 ORDER BY folder`,
		projectID, parent+"%", parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			continue
		}
		// Take the first path segment after parent.
		rel := strings.TrimPrefix(folder, parent)
		if rel == "" {
			continue
		}
		seg := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			seg = rel[:i]
		}
		if seg == "" || seen[seg] {
			continue
		}
		seen[seg] = true
		out = append(out, seg)
	}
	return out, nil
}

// markFailed flips a row to probe_status='failed' with the message
// trimmed so we don't dump an entire ffmpeg blob into the column.
func markFailed(db *sql.DB, projectID, fileID, sha, kind, msg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if len(msg) > 1000 {
		msg = msg[:1000] + "…"
	}
	_, err := db.Exec(`
		INSERT INTO media (file_id, project_id, source_sha256, probe_status, probe_error, probe_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			source_sha256=excluded.source_sha256,
			probe_status=excluded.probe_status,
			probe_error=excluded.probe_error,
			probe_at=excluded.probe_at,
			updated_at=excluded.updated_at,
			force_probe=0`,
		fileID, projectID, sha, kind, msg, now, now,
	)
	return err
}

// upsertDerivation writes a derivation row. positionMs is 0 for
// thumbnail/waveform/cover (single-frame derivations) and the
// source timestamp in ms for keyframes. The UNIQUE constraint moved
// to (file_id, kind, position_ms) in 012_keyframes.sql so multiple
// keyframe rows can coexist for one file.
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertDerivation(db sqlExecer, projectID, fileID, kind string, storageFileID int64, w, h int, positionMs int64) error {
	_, err := db.Exec(`
		INSERT INTO derivations (file_id, project_id, kind, storage_file_id, width, height, position_ms, status, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'ok', '')
		ON CONFLICT(file_id, kind, position_ms) DO UPDATE SET
			project_id=excluded.project_id,
			storage_file_id=excluded.storage_file_id,
			width=excluded.width,
			height=excluded.height,
			status='ok',
			error='',
			generated_at=CURRENT_TIMESTAMP`,
		fileID, projectID, kind, fmt.Sprintf("%d", storageFileID), nullableInt(w), nullableInt(h), positionMs,
	)
	return err
}

// upsertDerivationFailed records a terminal derivation attempt so
// completion coordination can distinguish "still running" from
// "attempted and failed".
func upsertDerivationFailed(db sqlExecer, projectID, fileID, kind string, positionMs int64, cause error) error {
	msg := "derivation failed"
	if cause != nil {
		msg = cause.Error()
	}
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	_, err := db.Exec(`
		INSERT INTO derivations (file_id, project_id, kind, storage_file_id, position_ms, status, error)
		VALUES (?, ?, ?, '', ?, 'failed', ?)
		ON CONFLICT(file_id, kind, position_ms) DO UPDATE SET
			project_id=excluded.project_id,
			storage_file_id='', width=NULL, height=NULL,
			status='failed', error=excluded.error,
			generated_at=CURRENT_TIMESTAMP`,
		fileID, projectID, kind, positionMs, msg,
	)
	return err
}

func clearDerivations(db sqlExecer, projectID, fileID string) error {
	_, err := db.Exec(`DELETE FROM derivations WHERE project_id=? AND file_id=?`, projectID, fileID)
	return err
}

// getMedia loads one row + its derivations.
// DescriptionFields carries a partial-update payload for a media
// row's prose columns. Pointer types let callers distinguish
// "preserve existing value" from "clear to empty string". Empty-
// string pointers explicitly clear; nil pointers leave the column
// untouched.
type DescriptionFields struct {
	Title       *string
	Description *string
	AltText     *string
	// Source defaults to "human" when empty — covers the common case
	// of an agent / panel write. Auto-describer writes 'ai-generated'
	// so subsequent sweeps don't try to overwrite themselves; the
	// auto-describer's gate explicitly skips rows where source
	// is 'human' or 'agent', so re-running is safe.
	Source string
}

// setDescription writes the description columns. Probe state
// (status, codecs, duration, etc.) is never touched here, and the
// indexer's upsertMedia never touches these columns — so a reprobe
// preserves any prose written by the agent.
//
// UPSERT semantics: when no media row exists for (project_id,
// file_id) yet, a stub row is inserted with probe_status='pending'
// + source_sha256=”. The indexer's next sweep treats it like any
// other unprobed row and fills in the metadata via upsertMedia,
// which leaves the description columns alone. This lets agents
// attach a description to a file the moment it lands in storage
// (e.g. right after media_extract_frame completes) without
// waiting for the indexer's 30s tick or calling media_reindex.
//
// Returns (created bool, err error). created=true when a stub was
// inserted, false when an existing row was updated. nil err on a
// successful no-op (empty DescriptionFields).
func setDescription(db *sql.DB, projectID, fileID string, f DescriptionFields) (created bool, err error) {
	// Build a partial UPDATE; sqlite happily accepts an empty SET
	// list when nothing is provided, so guard against that.
	sets := []string{}
	args := []any{}
	if f.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *f.Title)
	}
	if f.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *f.Description)
	}
	if f.AltText != nil {
		sets = append(sets, "alt_text = ?")
		args = append(args, *f.AltText)
	}
	if len(sets) == 0 {
		return false, nil // nothing to do; not an error
	}
	// Stamp provenance so the auto-describer can tell ai-generated
	// from human-set rows. Default 'human' covers the panel + tool
	// path; the auto-describer passes 'ai-generated' explicitly.
	source := f.Source
	if source == "" {
		source = "human"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sets = append(sets,
		"description_source = ?",
		"description_updated_at = ?",
		"description_error = ''", // a successful write clears any prior error
		"updated_at = ?",
	)
	args = append(args, source, now, now)
	args = append(args, projectID, fileID)
	q := "UPDATE media SET " + strings.Join(sets, ", ") + " WHERE project_id=? AND file_id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return false, nil
	}

	// No row to update — create a stub flagged for probing, then
	// re-run the UPDATE. We don't use INSERT...ON CONFLICT here
	// because we want the stub to carry probe_status='pending' +
	// empty sha so the indexer treats it as fresh, regardless of
	// what columns the description SET was about to write.
	if _, err := db.Exec(`
		INSERT INTO media (file_id, project_id, source_sha256, probe_status, raw_probe, created_at, updated_at)
		VALUES (?, ?, '', 'pending', '{}', ?, ?)`,
		fileID, projectID, now, now,
	); err != nil {
		return false, fmt.Errorf("insert stub media row: %w", err)
	}
	if _, err := db.Exec(q, args...); err != nil {
		return false, err
	}
	return true, nil
}

// cascadeDeleteOne removes one media row + its derivations + its
// transcript. Used by the SSE event handler when storage emits a
// file.deleted for a row we have. Same shape as purgeOrphans but
// targeted at a single file_id, no diff against current storage
// list needed.
//
// Cleanup order:
//  1. Find all derivation storage_file_ids (thumbnail, waveform,
//     keyframes) for this row.
//  2. Delete each from storage via files_delete (hard delete —
//     derivations are byproducts, not audit history).
//  3. Delete the local DB rows (derivations → transcripts → media).
//
// Storage-side delete failures are logged but DON'T block the DB
// cleanup. The media DB needs to be consistent with what's actually
// present; an orphaned storage row will be reaped by storage's own
// sweeper or a later media run, but a stale derivation row in
// media's DB pointing at a missing storage file is a worse
// foot-gun (callers fetch the URL and 404).
//
// Rows that don't exist are no-ops. app + sc are nullable for tests
// that exercise the DB-only path; production paths always pass both.
func cascadeDeleteOne(app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID, fileID string) error {
	// Step 1+2: storage-side cleanup. Best-effort.
	if sc != nil {
		derivs, _ := listDerivations(db, projectID, fileID)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleteOwnedDerivationFiles(ctx, app, sc, projectID, derivs)
	}

	// Step 3: local DB cleanup.
	if _, err := db.Exec(
		`DELETE FROM derivations WHERE project_id = ? AND file_id = ?`,
		projectID, fileID,
	); err != nil {
		return fmt.Errorf("delete derivations: %w", err)
	}
	if _, err := db.Exec(
		`DELETE FROM transcripts WHERE project_id = ? AND file_id = ?`,
		projectID, fileID,
	); err != nil {
		return fmt.Errorf("delete transcripts: %w", err)
	}
	if _, err := db.Exec(
		`DELETE FROM media WHERE project_id = ? AND file_id = ?`,
		projectID, fileID,
	); err != nil {
		return fmt.Errorf("delete media: %w", err)
	}
	return nil
}

// deleteDerivationReferencesByStorageID invalidates cached pointers when
// Storage deletes a hidden derivative directly. A derivative ID is not itself
// a Media source ID, so the old event handler ignored these events and left a
// dangling row that could later resolve to a reused legacy Storage ID.
func deleteDerivationReferencesByStorageID(db *sql.DB, projectID, storageFileID string) (int64, error) {
	if projectID == "" || storageFileID == "" {
		return 0, nil
	}
	res, err := db.Exec(
		`DELETE FROM derivations WHERE project_id = ? AND storage_file_id = ?`,
		projectID, storageFileID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// purgeOrphans removes media rows (and their derivations + transcripts)
// whose storage file is no longer present — typically because the
// user soft-deleted the file via the storage app and either the
// file.deleted SSE event was missed or the install was down when it
// fired.
//
// Reconciliation strategy:
//  1. Read every media row's file_id for this project.
//  2. Ask storage about EXACTLY those ids via ResolveFiles (batched
//     at storage's per-call cap, internally chunked).
//  3. Any media row whose file_id is missing from the resolved
//     response is an orphan.
//
// Why this beats the old "list all + diff" approach: storage's
// listing endpoint caps the page size, and a project with more
// files than the cap would silently roll through orphan-deleting
// every row beyond the page boundary. The verify-by-id path
// doesn't care about storage's listing limits — it asks storage
// about specific ids it could otherwise miss and trusts a missing
// id as a definitive "deleted". Storage's dbGetByID already
// returns nil for soft-deleted rows, so this is correct under the
// existing delete semantics.
//
// Cascade order: storage bytes (derivations) → derivations rows →
// transcripts rows → media row. Renders are NOT touched (the
// renders table is the audit log of operations attempted; dropping
// renders silently would lose history).
//
// `sc` is mandatory in production. Tests can pass nil — the
// storage-side derivation cleanup is then skipped and we fall back
// to the "what's in `precomputedPresent` (the deprecated parameter
// path)" mode for back-compat. Passing nil + nil for both `sc` and
// `precomputedPresent` purges every media row in the project,
// useful for "wipe this catalog" admin paths.
func purgeOrphans(app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID string, precomputedPresent []string) (int64, error) {
	// 1. Collect all media row file_ids for this project.
	rows, err := db.Query(`SELECT file_id FROM media WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, err
	}
	allMediaIDs := make([]string, 0)
	for rows.Next() {
		var fid string
		if err := rows.Scan(&fid); err != nil {
			rows.Close()
			return 0, err
		}
		allMediaIDs = append(allMediaIDs, fid)
	}
	rows.Close()
	if len(allMediaIDs) == 0 {
		return 0, nil
	}

	// 2. Determine which of those still exist in storage. Two paths
	//    depending on what the caller supplied:
	//
	//    a. sc != nil → verify-by-id via storage.ResolveFiles. The
	//       authoritative path; doesn't depend on listing caps.
	//    b. sc == nil → use precomputedPresent as the "still here"
	//       set. Back-compat for tests + the rare admin path that
	//       wants to drive purging manually. nil slice means "purge
	//       everything".
	present := make(map[string]struct{}, len(allMediaIDs))
	if sc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		resolved, rerr := sc.ResolveFiles(ctx, projectID, allMediaIDs)
		cancel()
		if rerr != nil {
			// Storage unreachable / errored mid-resolve. Conservative
			// choice: SKIP this sweep entirely rather than purge
			// rows we couldn't verify. The next tick retries.
			if app != nil {
				app.Logger().Warn("purgeOrphans: storage resolve failed; skipping sweep",
					"err", rerr, "row_count", len(allMediaIDs))
			}
			return 0, nil
		}
		for fid := range resolved {
			present[fid] = struct{}{}
		}
	} else {
		for _, fid := range precomputedPresent {
			present[fid] = struct{}{}
		}
	}

	// 3. Compute the orphan set.
	orphanIDs := make([]string, 0)
	for _, fid := range allMediaIDs {
		if _, ok := present[fid]; ok {
			continue
		}
		orphanIDs = append(orphanIDs, fid)
	}
	if len(orphanIDs) == 0 {
		return 0, nil
	}

	// 4. Storage-side cleanup: delete the orphans' derivation bytes
	//    (thumbnail, waveform, keyframes) before dropping the
	//    media rows. Best-effort — DB cleanup must still happen
	//    even if storage is unreachable. Without this, the
	//    derivation bytes accumulate under /.media/* forever.
	if sc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		allDerivs := make([]DerivationRow, 0)
		for start := 0; start < len(orphanIDs); start += 400 {
			end := start + 400
			if end > len(orphanIDs) {
				end = len(orphanIDs)
			}
			chunk := orphanIDs[start:end]
			derivByFile, _ := listDerivationsByFiles(db, projectID, chunk)
			for _, fid := range chunk {
				allDerivs = append(allDerivs, derivByFile[fid]...)
			}
		}
		deleteOwnedDerivationFiles(ctx, app, sc, projectID, allDerivs)
		cancel()
	}

	// SQLite commonly limits statements to 999 bind parameters.
	// Delete in bounded chunks so large catalogs reconcile safely.
	var deleted int64
	for start := 0; start < len(orphanIDs); start += 400 {
		end := start + 400
		if end > len(orphanIDs) {
			end = len(orphanIDs)
		}
		chunk := orphanIDs[start:end]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, projectID)
		for _, id := range chunk {
			args = append(args, id)
		}
		for _, table := range []string{"derivations", "transcripts"} {
			if _, err := db.Exec(`DELETE FROM `+table+` WHERE project_id=? AND file_id IN (`+marks+`)`, args...); err != nil {
				return deleted, fmt.Errorf("delete orphan %s: %w", table, err)
			}
		}
		res, err := db.Exec(`DELETE FROM media WHERE project_id=? AND file_id IN (`+marks+`)`, args...)
		if err != nil {
			return deleted, fmt.Errorf("delete orphan media: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// describeCandidates returns media file_ids the auto-describer
// should consider this tick: probed-ok rows with no description and
// no human/agent override, where the last attempt (if any) is older
// than cooldownSeconds. Caller queues each one through the LLM.
func describeCandidates(db *sql.DB, projectID string, limit int, cooldownSeconds int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	cutoff := time.Now().UTC().Add(-time.Duration(cooldownSeconds) * time.Second).Format(time.RFC3339)
	rows, err := db.Query(`
		SELECT file_id
		  FROM media
		 WHERE project_id = ?
		   AND probe_status = 'ok'
		   AND description = ''
		   AND description_source NOT IN ('human','agent')
		   AND (description_attempted_at IS NULL OR description_attempted_at <= ?)
		 ORDER BY created_at DESC
		 LIMIT ?`,
		projectID, cutoff, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// markDescribeAttempt stamps the attempt timestamp + error so the
// cooldown gate works on the next sweep. Doesn't touch description
// itself — only the bookkeeping columns. Use after a failed attempt;
// successful writes go through setDescription which clears error
// and stamps updated_at instead.
func markDescribeAttempt(db *sql.DB, projectID, fileID, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE media
		   SET description_attempted_at = ?,
		       description_error        = ?
		 WHERE project_id = ? AND file_id = ?`,
		now, errMsg, projectID, fileID,
	)
	return err
}

func getMedia(db *sql.DB, projectID, fileID string) (*MediaRow, error) {
	row := db.QueryRow(`
		SELECT m.file_id, m.project_id, m.source_sha256, m.folder, m.name,
			m.format_name, m.duration_ms, m.bitrate,
			m.has_video, m.has_audio, m.is_image,
			m.width, m.height, m.rotation, m.fps, m.video_codec,
			m.channels, m.sample_rate, m.audio_codec,
			m.probe_status, m.probe_error, m.probe_at, m.raw_probe,
			m.title, m.description, m.alt_text,
			m.description_source, COALESCE(m.description_updated_at, ''),
			COALESCE(m.description_attempted_at, ''), m.description_error,
			COALESCE(t.status, ''),
			m.audience_rating, m.audience_reasoning, COALESCE(m.audience_updated_at, ''),
			m.metadata, m.metadata_version,
			m.created_at, m.updated_at
		FROM media m
		LEFT JOIN transcripts t
		  ON t.file_id = m.file_id AND t.project_id = m.project_id
		WHERE m.project_id=? AND m.file_id=?`, projectID, fileID)
	m, err := scanMedia(row)
	if err != nil {
		return nil, err
	}
	if derivs, derr := listDerivations(db, projectID, fileID); derr == nil {
		m.Derivations = visibleDerivations(derivs)
	}
	return m, nil
}

func scanMedia(row interface{ Scan(...any) error }) (*MediaRow, error) {
	var (
		m                              MediaRow
		duration, bitrate              sql.NullInt64
		width, height, channels, srate sql.NullInt64
		fps                            sql.NullFloat64
		formatName, vcodec, acodec     sql.NullString
		probeAt, createdAt, updatedAt  sql.NullString
		probeError                     sql.NullString
		hasVideo, hasAudio, isImage    int
		rawProbe                       string
		title, description, altText    string
		descSource, descUpdatedAt      string
		descAttemptedAt, descError     string
		transcriptStatus               string
		metadata                       string
	)
	err := row.Scan(
		&m.FileID, &m.ProjectID, &m.SourceSHA256, &m.Folder, &m.Name,
		&formatName, &duration, &bitrate,
		&hasVideo, &hasAudio, &isImage,
		&width, &height, &m.Rotation, &fps, &vcodec,
		&channels, &srate, &acodec,
		&m.ProbeStatus, &probeError, &probeAt, &rawProbe,
		&title, &description, &altText,
		&descSource, &descUpdatedAt, &descAttemptedAt, &descError,
		&transcriptStatus,
		&m.AudienceRating, &m.AudienceReasoning, &m.AudienceUpdatedAt,
		&metadata, &m.MetadataVersion,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.HasVideo = hasVideo == 1
	m.HasAudio = hasAudio == 1
	m.IsImage = isImage == 1
	m.FormatName = formatName.String
	m.DurationMs = duration.Int64
	m.Bitrate = bitrate.Int64
	m.Width = int(width.Int64)
	m.Height = int(height.Int64)
	m.FPS = fps.Float64
	m.VideoCodec = vcodec.String
	m.Channels = int(channels.Int64)
	m.SampleRate = int(srate.Int64)
	m.AudioCodec = acodec.String
	m.ProbeError = probeError.String
	m.ProbeAt = probeAt.String
	m.Title = title
	m.Description = description
	m.AltText = altText
	m.DescriptionSource = descSource
	m.DescriptionUpdatedAt = descUpdatedAt
	m.DescriptionAttemptedAt = descAttemptedAt
	m.DescriptionError = descError
	m.TranscriptStatus = transcriptStatus
	m.CreatedAt = createdAt.String
	m.UpdatedAt = updatedAt.String
	if rawProbe != "" {
		m.RawProbe = json.RawMessage(rawProbe)
	}
	if metadata == "" {
		metadata = "{}"
	}
	m.Metadata = json.RawMessage(metadata)
	return &m, nil
}

// SearchFilters drive media_search and the GET /media handler.
type SearchFilters struct {
	// Q is a broad, case-insensitive contains search across filename,
	// title, description, and alt text. Filename and Title are narrower
	// contains filters for callers that know which field they want.
	Q             string
	Filename      string
	Title         string
	DurationMinMs int64
	DurationMaxMs int64
	// MediaType is the canonical type filter agents and UI should
	// prefer over raw probe booleans. Valid values: image, video,
	// audio. Images often have has_video=1 in ffprobe, so video
	// explicitly excludes is_image rows.
	MediaType string
	// Aspect filters by width/height bucket. Valid values:
	// portrait, landscape, square, reel, wide.
	Aspect     string
	HasVideo   *bool
	HasAudio   *bool
	IsImage    *bool
	WidthMin   int
	WidthMax   int
	VideoCodec string
	AudioCodec string
	// Folder filters by storage folder. Empty = no filter.
	// "/clips/" is exact by default. FolderScope="subtree" treats it
	// as a prefix so it also matches "/clips/q3/", etc. Recursive is
	// retained for legacy callers and maps to the same subtree scope.
	Folder      string
	FolderScope string
	Recursive   bool
	Limit       int
	Offset      int
	OrderBy     string // duration_ms | created_at | updated_at
	// AudienceRating filters by the column populated by the
	// describer (v0.13.0+). Nil/empty = no filter (everything,
	// including unrated). Multiple values OR'd: ["general","mature"]
	// returns rows at either rating. Use this rather than a single
	// status string when callers want "exclude adult" semantics.
	AudienceRatingIn    []string
	AudienceRatingNotIn []string
	// MetadataFilters are type-sensitive scalar equality predicates over
	// validated dotted paths such as metadata.patreon.status. All predicates
	// are ANDed with the standard catalog filters.
	MetadataFilters []MetadataCondition
}

const (
	folderScopeExact   = "exact"
	folderScopeSubtree = "subtree"
)

func (f SearchFilters) effectiveFolderScope() string {
	if f.FolderScope == folderScopeSubtree || f.Recursive {
		return folderScopeSubtree
	}
	return folderScopeExact
}

type MediaFacetCounts struct {
	All    int `json:"all"`
	Video  int `json:"video"`
	Audio  int `json:"audio"`
	Image  int `json:"image"`
	Aspect struct {
		Portrait  int `json:"portrait"`
		Landscape int `json:"landscape"`
		Square    int `json:"square"`
		Reel      int `json:"reel"`
		Wide      int `json:"wide"`
	} `json:"aspect"`
	Duration struct {
		Short    int `json:"short"`
		Medium   int `json:"medium"`
		Long     int `json:"long"`
		Extended int `json:"extended"`
	} `json:"duration"`
}

// buildMediaSearchWhere is shared by the normal search and empty-result
// diagnostics. Keeping one predicate builder guarantees that descendant
// counts use precisely the same text, media, duration, aspect, rating,
// dimension, and codec filters as the original request.
func buildMediaSearchWhere(projectID string, f SearchFilters) ([]string, []any) {
	clauses := []string{"m.project_id = ?", "m.probe_status = 'ok'"}
	args := []any{projectID}
	if q := mediaSearchContainsPattern(f.Q); q != "" {
		clauses = append(clauses, `(LOWER(COALESCE(m.name, '')) LIKE ? ESCAPE '\'
			OR LOWER(COALESCE(m.title, '')) LIKE ? ESCAPE '\'
			OR LOWER(COALESCE(m.description, '')) LIKE ? ESCAPE '\'
			OR LOWER(COALESCE(m.alt_text, '')) LIKE ? ESCAPE '\')`)
		args = append(args, q, q, q, q)
	}
	if filename := mediaSearchContainsPattern(f.Filename); filename != "" {
		clauses = append(clauses, `LOWER(COALESCE(m.name, '')) LIKE ? ESCAPE '\'`)
		args = append(args, filename)
	}
	if title := mediaSearchContainsPattern(f.Title); title != "" {
		clauses = append(clauses, `LOWER(COALESCE(m.title, '')) LIKE ? ESCAPE '\'`)
		args = append(args, title)
	}

	if f.DurationMinMs > 0 {
		clauses = append(clauses, "m.duration_ms >= ?")
		args = append(args, f.DurationMinMs)
	}
	if f.DurationMaxMs > 0 {
		clauses = append(clauses, "m.duration_ms <= ?")
		args = append(args, f.DurationMaxMs)
	}
	switch f.MediaType {
	case "image":
		clauses = append(clauses, "m.is_image = 1")
	case "video":
		clauses = append(clauses, "m.has_video = 1", "m.is_image = 0")
	case "audio":
		clauses = append(clauses, "m.has_audio = 1", "m.has_video = 0", "m.is_image = 0")
	}
	switch f.Aspect {
	case "portrait":
		clauses = append(clauses, "m.width > 0", "m.height > 0", "CAST(m.width AS REAL) / m.height < 0.95")
	case "landscape":
		clauses = append(clauses, "m.width > 0", "m.height > 0", "CAST(m.width AS REAL) / m.height > 1.05")
	case "square":
		clauses = append(clauses, "m.width > 0", "m.height > 0", "CAST(m.width AS REAL) / m.height BETWEEN 0.95 AND 1.05")
	case "reel":
		clauses = append(clauses, "m.width > 0", "m.height > 0", "ABS((CAST(m.width AS REAL) / m.height) - ?) <= ?")
		args = append(args, 9.0/16.0, 0.08)
	case "wide":
		clauses = append(clauses, "m.width > 0", "m.height > 0", "ABS((CAST(m.width AS REAL) / m.height) - ?) <= ?")
		args = append(args, 16.0/9.0, 0.15)
	}
	if f.HasVideo != nil {
		clauses = append(clauses, "m.has_video = ?")
		args = append(args, boolInt(*f.HasVideo))
	}
	if f.HasAudio != nil {
		clauses = append(clauses, "m.has_audio = ?")
		args = append(args, boolInt(*f.HasAudio))
	}
	if f.IsImage != nil {
		clauses = append(clauses, "m.is_image = ?")
		args = append(args, boolInt(*f.IsImage))
	}
	if f.WidthMin > 0 {
		clauses = append(clauses, "m.width >= ?")
		args = append(args, f.WidthMin)
	}
	if f.WidthMax > 0 {
		clauses = append(clauses, "m.width <= ?")
		args = append(args, f.WidthMax)
	}
	if f.VideoCodec != "" {
		clauses = append(clauses, "m.video_codec = ?")
		args = append(args, f.VideoCodec)
	}
	if f.AudioCodec != "" {
		clauses = append(clauses, "m.audio_codec = ?")
		args = append(args, f.AudioCodec)
	}
	// Folder filter — exact match by default, prefix LIKE for subtree
	// scope. The trailing slash on storage's normalized
	// folders (e.g. "/clips/") makes the prefix match safe — it
	// won't match "/clips-archive/".
	if f.Folder != "" {
		if f.effectiveFolderScope() == folderScopeSubtree {
			clauses = append(clauses, "m.folder LIKE ?")
			args = append(args, f.Folder+"%")
		} else {
			clauses = append(clauses, "m.folder = ?")
			args = append(args, f.Folder)
		}
	}
	// Audience-rating filter — IN clause when supplied. Empty slice
	// is "no filter" (returns everything, including unrated).
	if len(f.AudienceRatingIn) > 0 {
		placeholders := strings.Repeat("?,", len(f.AudienceRatingIn))
		placeholders = strings.TrimSuffix(placeholders, ",")
		clauses = append(clauses, "m.audience_rating IN ("+placeholders+")")
		for _, r := range f.AudienceRatingIn {
			args = append(args, r)
		}
	}
	if len(f.AudienceRatingNotIn) > 0 {
		placeholders := strings.Repeat("?,", len(f.AudienceRatingNotIn))
		placeholders = strings.TrimSuffix(placeholders, ",")
		clauses = append(clauses, "COALESCE(NULLIF(m.audience_rating, ''), 'unrated') NOT IN ("+placeholders+")")
		for _, r := range f.AudienceRatingNotIn {
			args = append(args, r)
		}
	}
	for _, filter := range f.MetadataFilters {
		if filter.Value == nil {
			clauses = append(clauses, "json_type(m.metadata, ?) = 'null'")
			args = append(args, filter.JSONPath)
		} else {
			clauses = append(clauses, "json_extract(m.metadata, ?) = ?")
			args = append(args, filter.JSONPath, filter.Value)
		}
	}
	return clauses, args
}

// searchMedia returns rows matching f. Joins derivations once at the
// end so each row has its thumbnail/waveform pointers.
func searchMedia(db *sql.DB, projectID string, f SearchFilters) ([]MediaRow, error) {
	clauses, args := buildMediaSearchWhere(projectID, f)

	order := "created_at DESC"
	switch f.OrderBy {
	case "duration_ms":
		order = "duration_ms DESC"
	case "updated_at":
		order = "updated_at DESC"
	case "created_at":
		order = "created_at DESC"
	}

	limit := mediaSearchDefaultLimit
	if f.Limit > 0 && f.Limit <= 500 {
		limit = f.Limit
	}
	offset := 0
	if f.Offset > 0 {
		offset = f.Offset
	}

	query := `SELECT m.file_id, m.project_id, m.source_sha256, m.folder, m.name,
		m.format_name, m.duration_ms, m.bitrate,
		m.has_video, m.has_audio, m.is_image,
		m.width, m.height, m.rotation, m.fps, m.video_codec,
		m.channels, m.sample_rate, m.audio_codec,
		m.probe_status, m.probe_error, m.probe_at, m.raw_probe,
		m.title, m.description, m.alt_text,
		m.description_source, COALESCE(m.description_updated_at, ''),
		COALESCE(m.description_attempted_at, ''), m.description_error,
		COALESCE(t.status, ''),
		m.audience_rating, m.audience_reasoning, COALESCE(m.audience_updated_at, ''),
		m.metadata, m.metadata_version,
		m.created_at, m.updated_at
	FROM media m
	LEFT JOIN transcripts t
	  ON t.file_id = m.file_id AND t.project_id = m.project_id
	WHERE ` + strings.Join(clauses, " AND ") + " ORDER BY m." + order + " LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MediaRow{}
	ids := []string{}
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
		ids = append(ids, m.FileID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	derivByFile, err := listDerivationsByFiles(db, projectID, ids)
	if err == nil {
		for i := range out {
			out[i].Derivations = visibleDerivations(derivByFile[out[i].FileID])
		}
	}
	return out, nil
}

type MediaSearchEmptyDiagnostic struct {
	EmptyReason            string
	HasMatchingDescendants bool
	DescendantMatchCount   int
	SampleMatchingFolders  []string
}

func mediaSearchCount(db *sql.DB, projectID string, f SearchFilters, descendantsOnly bool) (int, error) {
	clauses, args := buildMediaSearchWhere(projectID, f)
	if descendantsOnly {
		clauses = append(clauses, "m.folder <> ?")
		args = append(args, f.Folder)
	}
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM media m WHERE `+strings.Join(clauses, " AND "), args...).Scan(&count)
	return count, err
}

func mediaSearchSampleFolders(db *sql.DB, projectID string, f SearchFilters, limit int) ([]string, error) {
	clauses, args := buildMediaSearchWhere(projectID, f)
	clauses = append(clauses, "m.folder <> ?")
	args = append(args, f.Folder, limit)
	rows, err := db.Query(`SELECT DISTINCT m.folder FROM media m WHERE `+
		strings.Join(clauses, " AND ")+` ORDER BY m.folder LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	folders := []string{}
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}
	return folders, rows.Err()
}

func mediaSearchFolderOnly(f SearchFilters, scope string) SearchFilters {
	return SearchFilters{
		Folder:      f.Folder,
		FolderScope: scope,
		Recursive:   scope == folderScopeSubtree,
	}
}

// diagnoseEmptyExactMediaSearch is called only for the first empty page of
// an exact-folder query. It never broadens the returned media rows; it only
// explains whether the same filtered query would match descendants.
func diagnoseEmptyExactMediaSearch(db *sql.DB, projectID string, f SearchFilters) (MediaSearchEmptyDiagnostic, error) {
	diagnostic := MediaSearchEmptyDiagnostic{SampleMatchingFolders: []string{}}
	descendantFilters := f
	descendantFilters.FolderScope = folderScopeSubtree
	descendantFilters.Recursive = true

	descendantCount, err := mediaSearchCount(db, projectID, descendantFilters, true)
	if err != nil {
		return diagnostic, err
	}
	diagnostic.DescendantMatchCount = descendantCount
	diagnostic.HasMatchingDescendants = descendantCount > 0
	if descendantCount > 0 {
		diagnostic.SampleMatchingFolders, err = mediaSearchSampleFolders(db, projectID, descendantFilters, 5)
		if err != nil {
			return diagnostic, err
		}
	}

	exactAnyCount, err := mediaSearchCount(db, projectID, mediaSearchFolderOnly(f, folderScopeExact), false)
	if err != nil {
		return diagnostic, err
	}
	switch {
	case exactAnyCount > 0:
		diagnostic.EmptyReason = "filters_excluded_exact_folder_media"
	case descendantCount > 0:
		diagnostic.EmptyReason = "exact_folder_has_no_matching_media"
	default:
		subtreeAnyCount, countErr := mediaSearchCount(db, projectID, mediaSearchFolderOnly(f, folderScopeSubtree), false)
		if countErr != nil {
			return diagnostic, countErr
		}
		if subtreeAnyCount > 0 {
			diagnostic.EmptyReason = "filters_excluded_subtree_media"
		} else {
			diagnostic.EmptyReason = "subtree_has_no_media"
		}
	}
	return diagnostic, nil
}

func diagnoseEmptySubtreeMediaSearch(db *sql.DB, projectID string, f SearchFilters) (MediaSearchEmptyDiagnostic, error) {
	diagnostic := MediaSearchEmptyDiagnostic{
		HasMatchingDescendants: false,
		SampleMatchingFolders:  []string{},
	}
	subtreeAnyCount, err := mediaSearchCount(db, projectID, mediaSearchFolderOnly(f, folderScopeSubtree), false)
	if err != nil {
		return diagnostic, err
	}
	if subtreeAnyCount > 0 {
		diagnostic.EmptyReason = "filters_excluded_subtree_media"
	} else {
		diagnostic.EmptyReason = "subtree_has_no_media"
	}
	return diagnostic, nil
}

func mediaFacetCounts(db *sql.DB, projectID string, f SearchFilters) (MediaFacetCounts, error) {
	clauses := []string{"project_id = ?", "probe_status = 'ok'"}
	whereArgs := []any{projectID}
	if f.Folder != "" {
		if f.effectiveFolderScope() == folderScopeSubtree {
			clauses = append(clauses, "folder LIKE ?")
			whereArgs = append(whereArgs, f.Folder+"%")
		} else {
			clauses = append(clauses, "folder = ?")
			whereArgs = append(whereArgs, f.Folder)
		}
	}

	query := `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN has_video = 1 AND is_image = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN has_audio = 1 AND has_video = 0 AND is_image = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN is_image = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN width > 0 AND height > 0 AND CAST(width AS REAL) / height < 0.95 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN width > 0 AND height > 0 AND CAST(width AS REAL) / height > 1.05 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN width > 0 AND height > 0 AND CAST(width AS REAL) / height BETWEEN 0.95 AND 1.05 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN width > 0 AND height > 0 AND ABS((CAST(width AS REAL) / height) - ?) <= ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN width > 0 AND height > 0 AND ABS((CAST(width AS REAL) / height) - ?) <= ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN has_video = 1 AND is_image = 0 AND duration_ms > 0 AND duration_ms < ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN has_video = 1 AND is_image = 0 AND duration_ms >= ? AND duration_ms < ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN has_video = 1 AND is_image = 0 AND duration_ms >= ? AND duration_ms < ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN has_video = 1 AND is_image = 0 AND duration_ms >= ? THEN 1 ELSE 0 END), 0)
	FROM media
	WHERE ` + strings.Join(clauses, " AND ")
	args := []any{
		9.0 / 16.0, 0.08,
		16.0 / 9.0, 0.15,
		int64(30_000),
		int64(30_000), int64(120_000),
		int64(120_000), int64(600_000),
		int64(600_000),
	}
	args = append(args, whereArgs...)

	var c MediaFacetCounts
	err := db.QueryRow(query, args...).Scan(
		&c.All, &c.Video, &c.Audio, &c.Image,
		&c.Aspect.Portrait, &c.Aspect.Landscape, &c.Aspect.Square, &c.Aspect.Reel, &c.Aspect.Wide,
		&c.Duration.Short, &c.Duration.Medium, &c.Duration.Long, &c.Duration.Extended,
	)
	return c, err
}

func mediaSearchContainsPattern(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	// LIKE wildcards in user text are literals. This makes q="100%" search
	// for that phrase instead of accidentally matching every 100-prefixed row.
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func listDerivations(db *sql.DB, projectID, fileID string) ([]DerivationRow, error) {
	// Order by kind then position_ms so the canonical thumbnail/
	// waveform comes first and keyframes are returned in source-
	// timeline order — the UI timeline scrub + describer sampling
	// rely on that order without re-sorting client-side.
	rows, err := db.Query(
		`SELECT id, file_id, kind, storage_file_id, width, height, position_ms, status, error, generated_at
		FROM derivations WHERE project_id=? AND file_id=?
		ORDER BY kind, position_ms`, projectID, fileID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DerivationRow
	for rows.Next() {
		var d DerivationRow
		var w, h sql.NullInt64
		var gen sql.NullString
		if err := rows.Scan(&d.ID, &d.FileID, &d.Kind, &d.StorageFileID, &w, &h, &d.PositionMs, &d.Status, &d.Error, &gen); err != nil {
			return nil, err
		}
		d.Width = int(w.Int64)
		d.Height = int(h.Int64)
		d.GeneratedAt = gen.String
		out = append(out, d)
	}
	return out, nil
}

func listDerivationsByFiles(db *sql.DB, projectID string, fileIDs []string) (map[string][]DerivationRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(fileIDs))
	placeholders = strings.TrimSuffix(placeholders, ",")
	args := []any{projectID}
	for _, id := range fileIDs {
		args = append(args, id)
	}
	rows, err := db.Query(
		`SELECT id, file_id, kind, storage_file_id, width, height, position_ms, status, error, generated_at
		FROM derivations WHERE project_id=? AND file_id IN (`+placeholders+`)
		ORDER BY file_id, kind, position_ms`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]DerivationRow{}
	for rows.Next() {
		var d DerivationRow
		var w, h sql.NullInt64
		var gen sql.NullString
		if err := rows.Scan(&d.ID, &d.FileID, &d.Kind, &d.StorageFileID, &w, &h, &d.PositionMs, &d.Status, &d.Error, &gen); err != nil {
			return nil, err
		}
		d.Width = int(w.Int64)
		d.Height = int(h.Int64)
		d.GeneratedAt = gen.String
		out[d.FileID] = append(out[d.FileID], d)
	}
	return out, nil
}

// indexerCandidates returns file IDs from storage that need probing —
// missing media row, or marked pending/failed (with an exponential
// retry gate so we don't flap).
func indexerCandidates(db *sql.DB, projectID string, all []StorageFile, limit int) []StorageFile {
	if len(all) == 0 {
		return nil
	}
	known := map[string]struct {
		status string
		sha    string
	}{}
	rows, err := db.Query(
		`SELECT file_id, probe_status, source_sha256 FROM media WHERE project_id=?`, projectID,
	)
	if err == nil {
		for rows.Next() {
			var id, st, sh string
			if err := rows.Scan(&id, &st, &sh); err == nil {
				known[id] = struct {
					status string
					sha    string
				}{st, sh}
			}
		}
		rows.Close()
	}
	out := []StorageFile{}
	for _, f := range all {
		fid := fmt.Sprintf("%d", f.ID)
		k, ok := known[fid]
		if !ok {
			out = append(out, f)
			continue
		}
		switch k.status {
		case "ok":
			// Re-probe only if storage's sha has changed.
			if f.SHA256 != k.sha && f.SHA256 != "" {
				out = append(out, f)
			}
		case "pending", "failed":
			out = append(out, f)
		case "skipped_size":
			// Leave alone — only forced reindex retries skipped rows.
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

// pendingIndexerFileIDs returns already-known rows that need another probe.
// The worker resolves these exact IDs through Storage before it performs the
// background inventory sweep, so media_reindex(file_id) is independent of
// both catalog ordering and Storage page size.
func pendingIndexerFileIDs(db *sql.DB, projectID string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query(`
		SELECT file_id
		FROM media
		WHERE project_id=? AND probe_status IN ('pending', 'failed')
		ORDER BY force_probe DESC,
			CASE probe_status WHEN 'pending' THEN 0 ELSE 1 END,
			updated_at ASC,
			file_id ASC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// queueMediaReindex creates a durable pending row even when the file has
// never appeared in Media's catalog. That lets an operator recover an exact
// Storage ID after an outage without depending on a background inventory
// scan to discover it first.
func queueMediaReindex(db *sql.DB, projectID, fileID string, force bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO media (
			file_id, project_id, source_sha256, probe_status,
			probe_error, force_probe, updated_at
		) VALUES (?, ?, '', 'pending', '', ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			project_id=excluded.project_id,
			source_sha256='',
			probe_status='pending',
			probe_error='',
			force_probe=excluded.force_probe,
			updated_at=excluded.updated_at`,
		fileID, projectID, boolInt(force), now,
	)
	return err
}

// boolInt / nullables — helpers because SQLite + database/sql typing
// gets noisy when half the columns can be NULL.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nilOrErr — collapses sql.ErrNoRows so callers can treat "not found"
// as a normal nil-result rather than wrapping every call.
func notFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
