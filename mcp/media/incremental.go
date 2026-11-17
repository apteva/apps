package main

// Incremental indexing.
//
// The indexer tick used to list every Storage file (deep OFFSET pages of
// full rows) and re-verify every media row against Storage on every run,
// which on a 100k+ file project meant dozens of large requests every 30 s
// for an unchanged catalog. The tick now does bounded work only:
//
//  1. syncChangeFeed — reads new entries from Storage's ordered change feed
//     (Storage 0.14+) and queues them in index_queue. The queue rows and
//     the advanced cursor commit in one transaction, so a crash can neither
//     lose a change nor queue it twice.
//  2. drainIndexQueue — resolves a small batch of queued file IDs plus the
//     existing pending/failed and derivation-repair rows, and returns the
//     files that actually need processing.
//  3. reconcileStep — advances a slow reconciliation sweep by one page.
//     It does initial discovery, catches anything the feed or events
//     missed, and checks a bounded slice of media rows for orphans. Its
//     progress is persisted; a finished sweep waits reconcile_interval_hours
//     before the next one starts.
//
// Older Storage (no change feed) runs in "legacy" mode: SSE events feed the
// queue, the sweep pages by offset, and every restart starts a sweep
// because the platform's event replay buffer is short.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	feedModeFeed   = "feed"
	feedModeLegacy = "legacy"

	feedPageSize        = 1000
	feedPagesPerTick    = 10
	queueScanPerTick    = 200
	reconcilePageSize   = 1000
	orphanSlicePerTick  = 500
	legacyRecheckPeriod = time.Hour
	orphanCursorDone    = "~" // sorts after every numeric file_id
)

type syncState struct {
	FeedMode         string
	FeedCheckedAt    time.Time
	FeedAfter        int64
	EventSeq         int64
	SweepActive      bool
	SweepCursor      int64
	SweepCompletedAt time.Time
	OrphanCursor     string
}

func loadSyncState(db *sql.DB, projectID string) (*syncState, error) {
	if _, err := db.Exec(`INSERT OR IGNORE INTO index_sync_state(project_id) VALUES (?)`, projectID); err != nil {
		return nil, err
	}
	var st syncState
	var checked, completed sql.NullString
	err := db.QueryRow(`SELECT feed_mode, feed_checked_at, feed_after, event_seq, sweep_active, sweep_cursor,
		sweep_completed_at, orphan_cursor FROM index_sync_state WHERE project_id=?`, projectID).
		Scan(&st.FeedMode, &checked, &st.FeedAfter, &st.EventSeq, &st.SweepActive, &st.SweepCursor, &completed, &st.OrphanCursor)
	if err != nil {
		return nil, err
	}
	st.FeedCheckedAt = parseSQLTime(checked.String)
	st.SweepCompletedAt = parseSQLTime(completed.String)
	return &st, nil
}

func parseSQLTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// enqueueIndexWork records that a file needs a look. Re-enqueueing an
// existing row keeps one row (dedupe by project + file), keeps the highest
// revision, lets the newest op win and bumps gen so an in-flight attempt
// cannot acknowledge work that arrived after it started.
func enqueueIndexWork(db execer, projectID, fileID string, revision int64, op, source string) error {
	if projectID == "" || fileID == "" {
		return nil
	}
	if op != "deleted" {
		op = "changed"
	}
	_, err := db.Exec(`INSERT INTO index_queue(project_id, file_id, revision, op, source)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, file_id) DO UPDATE SET
			revision = MAX(index_queue.revision, excluded.revision),
			op = excluded.op,
			source = excluded.source,
			enqueued_at = CURRENT_TIMESTAMP,
			gen = index_queue.gen + 1`,
		projectID, fileID, revision, op, source)
	return err
}

func ackIndexWork(db *sql.DB, projectID, fileID string, gen int64) {
	_, _ = db.Exec(`DELETE FROM index_queue WHERE project_id=? AND file_id=? AND gen=?`, projectID, fileID, gen)
}

// syncChangeFeed pulls new Storage change-feed entries into index_queue and
// returns the mode for this tick: feed, legacy, or "" when Storage could not
// be reached (the sweep then waits for the next tick).
func syncChangeFeed(ctx context.Context, app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID string, st *syncState) string {
	if st.FeedMode == feedModeLegacy && time.Since(st.FeedCheckedAt) < legacyRecheckPeriod {
		return feedModeLegacy
	}
	for i := 0; i < feedPagesPerTick; i++ {
		page, err := sc.FileChanges(ctx, projectID, st.FeedAfter, feedPageSize)
		if errors.Is(err, errChangeFeedUnsupported) {
			if st.FeedMode != feedModeLegacy {
				app.Logger().Info("storage has no change feed; using event + sweep indexing", "project_id", projectID)
			}
			_, _ = db.Exec(`UPDATE index_sync_state SET feed_mode=?, feed_checked_at=CURRENT_TIMESTAMP WHERE project_id=?`, feedModeLegacy, projectID)
			st.FeedMode = feedModeLegacy
			st.FeedCheckedAt = time.Now()
			return feedModeLegacy
		}
		if err != nil {
			app.Logger().Warn("storage change feed read failed", "project_id", projectID, "err", err)
			if st.FeedMode == feedModeFeed {
				return feedModeFeed
			}
			return ""
		}
		if err := applyChangePage(db, projectID, st, page); err != nil {
			app.Logger().Warn("queueing storage changes failed", "project_id", projectID, "err", err)
			return st.FeedMode
		}
		if !page.HasMore {
			break
		}
	}
	return feedModeFeed
}

// applyChangePage queues one feed page and advances the cursor in the same
// transaction. A cursor behind Storage's pruning watermark has missed
// entries, so the reconciliation sweep restarts from the beginning.
func applyChangePage(db *sql.DB, projectID string, st *syncState, page *FileChangesPage) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range page.Changes {
		if err := enqueueIndexWork(tx, projectID, strconv.FormatInt(c.FileID, 10), c.Revision, c.Op, "feed"); err != nil {
			return err
		}
	}
	restart := page.PrunedThrough > st.FeedAfter
	switchedMode := st.FeedMode != feedModeFeed
	q := `UPDATE index_sync_state SET feed_mode=?, feed_checked_at=CURRENT_TIMESTAMP, feed_after=?`
	if restart || switchedMode {
		// Offset cursors from legacy mode mean nothing to id-ordered
		// discovery; a gap means the sweep must cover everything again.
		q += `, sweep_cursor=0`
	}
	if restart {
		q += `, sweep_active=1, sweep_started_at=CURRENT_TIMESTAMP, orphan_cursor=''`
	}
	if _, err := tx.Exec(q+` WHERE project_id=?`, feedModeFeed, page.NextAfter, projectID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	st.FeedMode = feedModeFeed
	st.FeedAfter = page.NextAfter
	if restart || switchedMode {
		st.SweepCursor = 0
	}
	if restart {
		st.SweepActive = true
		st.OrphanCursor = ""
	}
	return nil
}

// queuedWork ties a candidate back to the queue row it came from so the
// row is acknowledged only after processOne has run.
type queuedWork struct {
	fileID string
	gen    int64
}

// drainIndexQueue returns up to limit files that need processing, in
// priority order: explicit pending/failed rows (media_reindex and failed
// retries), queued changes, then derivation repairs. Queue rows that need
// no work are acknowledged here; the rest are returned in acks.
func drainIndexQueue(ctx context.Context, app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID string, limit int, keyframes bool) ([]StorageFile, map[int64]queuedWork) {
	acks := map[int64]queuedWork{}
	selected := map[int64]bool{}
	var out []StorageFile

	direct, err := resolvePendingIndexerFiles(ctx, sc, db, projectID, min(directIndexerBatchSize, limit))
	if err != nil {
		app.Logger().Warn("resolve pending media failed", "err", err)
	}
	for _, f := range direct {
		selected[f.ID] = true
		out = append(out, f)
	}

	type qrow struct {
		fileID, op string
		gen        int64
	}
	var queued []qrow
	rows, err := db.Query(`SELECT file_id, op, gen FROM index_queue WHERE project_id=? ORDER BY enqueued_at, file_id LIMIT ?`, projectID, queueScanPerTick)
	if err == nil {
		for rows.Next() {
			var r qrow
			if rows.Scan(&r.fileID, &r.op, &r.gen) == nil {
				queued = append(queued, r)
			}
		}
		rows.Close()
	}

	var changedIDs []string
	gens := map[string]int64{}
	for _, r := range queued {
		if r.op == "deleted" {
			// A Storage tombstone is proof of deletion.
			applyStorageDeletion(app, projectID, r.fileID)
			ackIndexWork(db, projectID, r.fileID, r.gen)
			continue
		}
		changedIDs = append(changedIDs, r.fileID)
		gens[r.fileID] = r.gen
	}

	repairIDs := repairDueFileIDs(db, projectID, max(limit-len(out), 0), keyframes)
	lookup := append(append([]string{}, changedIDs...), repairIDs...)
	if len(lookup) == 0 {
		return out, acks
	}
	resolved, err := sc.ResolveFiles(ctx, projectID, lookup)
	if err != nil {
		// Leave the queue untouched; the next tick retries.
		app.Logger().Warn("resolve queued media failed", "err", err)
		return out, acks
	}

	var considered []StorageFile
	for _, fid := range changedIDs {
		f := resolved[fid]
		if f == nil || isExcludedFromCatalog(f.Folder) || (!isMediaContentType(f.ContentType) && !isMediaByExt(f.Name)) {
			// Missing: deleted since (its tombstone/event handles the media
			// row) or not ours to index. Either way nothing to do.
			ackIndexWork(db, projectID, fid, gens[fid])
			continue
		}
		considered = append(considered, *f)
	}
	for _, fid := range repairIDs {
		if f := resolved[fid]; f != nil {
			considered = append(considered, *f)
		}
	}

	need := map[int64]bool{}
	for _, f := range indexerCandidates(db, projectID, considered, len(considered), keyframes) {
		need[f.ID] = true
	}
	for _, f := range considered {
		fid := strconv.FormatInt(f.ID, 10)
		gen, fromQueue := gens[fid]
		if !need[f.ID] || selected[f.ID] {
			if fromQueue {
				// Up to date (any rename/move was written by
				// indexerCandidates) or already selected this tick.
				ackIndexWork(db, projectID, fid, gen)
			}
			continue
		}
		if len(out) >= limit {
			continue // stays queued for the next tick
		}
		selected[f.ID] = true
		out = append(out, f)
		if fromQueue {
			acks[f.ID] = queuedWork{fileID: fid, gen: gen}
		}
	}
	return out, acks
}

// repairDueFileIDs lists indexed rows whose derivations need regeneration.
func repairDueFileIDs(db *sql.DB, projectID string, limit int, keyframes bool) []string {
	if limit <= 0 {
		return nil
	}
	rows, err := db.Query(`SELECT file_id FROM media WHERE project_id=? AND probe_status='ok' AND `+derivationRepairDueSQL+`
		ORDER BY updated_at, file_id LIMIT ?`, projectID, keyframes, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func reconcileInterval(app *sdk.AppCtx) time.Duration {
	hours := 12
	if app != nil {
		if v, err := strconv.Atoi(strings.TrimSpace(app.Config().Get("reconcile_interval_hours"))); err == nil && v > 0 {
			hours = v
		}
	}
	return time.Duration(hours) * time.Hour
}

// reconcileStep advances the reconciliation sweep by one page: files on it
// that need work are queued (not processed inline), and one slice of media
// rows is checked for orphans. Returns true while a sweep is in progress.
func reconcileStep(ctx context.Context, app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID string, st *syncState, mode string, keyframes bool) bool {
	if mode == "" {
		return false
	}
	if !st.SweepActive {
		if !st.SweepCompletedAt.IsZero() && time.Since(st.SweepCompletedAt) < reconcileInterval(app) {
			return false
		}
		if _, err := db.Exec(`UPDATE index_sync_state SET sweep_active=1, sweep_cursor=0, sweep_started_at=CURRENT_TIMESTAMP, orphan_cursor='' WHERE project_id=?`, projectID); err != nil {
			return false
		}
		st.SweepActive, st.SweepCursor, st.OrphanCursor = true, 0, ""
	}

	inventoryDone := st.SweepCursor < 0
	if !inventoryDone {
		var files []StorageFile
		var next int64
		var more bool
		var err error
		if mode == feedModeFeed {
			files, next, more, err = sc.DiscoveryPage(ctx, projectID, st.SweepCursor, reconcilePageSize)
		} else {
			files, err = sc.SearchFilesPage(ctx, projectID, reconcilePageSize, int(st.SweepCursor))
			next, more = st.SweepCursor+int64(len(files)), len(files) == reconcilePageSize
		}
		if err != nil {
			// Inconclusive: keep the cursor and retry next tick.
			app.Logger().Warn("reconciliation page failed", "project_id", projectID, "mode", mode, "err", err)
			return true
		}
		media := filterMediaFiles(files)
		candidates := indexerCandidates(db, projectID, media, len(media), keyframes)
		if !more {
			next = -1 // inventory pass finished; orphan pass may continue
		}
		tx, err := db.Begin()
		if err != nil {
			return true
		}
		for _, f := range candidates {
			if err := enqueueIndexWork(tx, projectID, strconv.FormatInt(f.ID, 10), f.Revision, "changed", "sweep"); err != nil {
				tx.Rollback()
				return true
			}
		}
		if _, err := tx.Exec(`UPDATE index_sync_state SET sweep_cursor=? WHERE project_id=?`, next, projectID); err != nil {
			tx.Rollback()
			return true
		}
		if err := tx.Commit(); err != nil {
			return true
		}
		st.SweepCursor = next
		inventoryDone = next < 0
	}

	orphansDone := reconcileOrphanSlice(app, sc, db, projectID, st)
	if inventoryDone && orphansDone {
		_, _ = db.Exec(`UPDATE index_sync_state SET sweep_active=0, sweep_cursor=0, sweep_completed_at=CURRENT_TIMESTAMP WHERE project_id=?`, projectID)
		st.SweepActive, st.SweepCursor, st.SweepCompletedAt = false, 0, time.Now()
		app.Logger().Info("media reconciliation sweep complete", "project_id", projectID, "mode", mode)
		return false
	}
	return true
}

// reconcileOrphanSlice checks the next orphanSlicePerTick media rows
// against Storage. Returns true once every row has been checked this sweep.
func reconcileOrphanSlice(app *sdk.AppCtx, sc *storageClient, db *sql.DB, projectID string, st *syncState) bool {
	if st.OrphanCursor == orphanCursorDone {
		return true
	}
	rows, err := db.Query(`SELECT file_id FROM media WHERE project_id=? AND file_id>? ORDER BY file_id LIMIT ?`, projectID, st.OrphanCursor, orphanSlicePerTick)
	if err != nil {
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) > 0 {
		n, err := purgeOrphansAmong(app, sc, db, projectID, ids, nil)
		if err != nil {
			app.Logger().Warn("purge orphan media failed", "err", err)
			return false
		}
		if n > 0 {
			app.Logger().Info("purged orphan media", "count", n)
		}
	}
	next := orphanCursorDone
	if len(ids) == orphanSlicePerTick {
		next = ids[len(ids)-1]
	}
	if _, err := db.Exec(`UPDATE index_sync_state SET orphan_cursor=? WHERE project_id=?`, next, projectID); err != nil {
		return false
	}
	st.OrphanCursor = next
	return next == orphanCursorDone
}

// eventCursorKey is the index_sync_state row holding the SSE replay
// cursor: the project for scoped installs, '*' for the global firehose.
func eventCursorKey(projectID string) string {
	if projectID == "" {
		return "*"
	}
	return projectID
}

func loadEventSeq(db *sql.DB, projectID string) uint64 {
	var seq int64
	_ = db.QueryRow(`SELECT event_seq FROM index_sync_state WHERE project_id=?`, eventCursorKey(projectID)).Scan(&seq)
	if seq < 0 {
		return 0
	}
	return uint64(seq)
}

func saveEventSeq(db *sql.DB, projectID string, seq uint64) {
	_, _ = db.Exec(`INSERT INTO index_sync_state(project_id, event_seq) VALUES (?, ?)
		ON CONFLICT(project_id) DO UPDATE SET event_seq=excluded.event_seq`, eventCursorKey(projectID), int64(seq))
}

// restartLegacySweeps starts a reconciliation sweep for every project not
// on the change feed. Called once per process start: events published while
// Media was down may have fallen out of the platform's replay buffer.
func restartLegacySweeps(db *sql.DB) {
	_, _ = db.Exec(`UPDATE index_sync_state SET sweep_active=1, sweep_cursor=0, sweep_started_at=CURRENT_TIMESTAMP, orphan_cursor=''
		WHERE project_id<>'*' AND feed_mode<>? AND sweep_active=0`, feedModeFeed)
}

func eventFileID(data map[string]any) string {
	switch v := data["id"].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func eventRevision(data map[string]any) int64 {
	switch v := data["file_revision"].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}
