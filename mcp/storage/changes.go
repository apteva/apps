package main

// Change feed + cursor discovery for inventory consumers.
//
// Media's indexer used to re-list every file in a project (OFFSET pages of
// full rows, each with a minted URL) on every 30 s tick just to notice new
// uploads. Two endpoints replace that:
//
//   GET /files/changes?project_id=&after=<seq>&limit=
//       Ordered rows from file_changes (written by triggers in the same
//       transaction as the file write; see migrations/009). Consumers keep
//       `after` durably and only ever move it forward.
//
//   GET /files?project_id=&after_id=<id>&limit=&fields=discovery
//       Keyset pagination over live files ordered by id. Stable under
//       concurrent uploads/updates (no OFFSET window shifting). With
//       fields=discovery rows carry only what a consumer needs to decide
//       whether to look closer — no URLs, tags or storage keys.

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	changeFeedDefaultLimit = 500
	changeFeedMaxLimit     = 5000
	// Changes older than this are pruned. A consumer whose cursor falls
	// behind pruned_through must rebuild by cursor discovery.
	changeFeedRetention  = 30 * 24 * time.Hour
	changeFeedPruneBatch = 5000
	changeFeedPrunedKey  = "file_changes_pruned_through"
)

type FileChange struct {
	Seq       int64  `json:"seq"`
	FileID    int64  `json:"file_id"`
	Revision  int64  `json:"revision"`
	Op        string `json:"op"`
	ChangedAt int64  `json:"changed_at"`
}

// DiscoveryFile is the slim listing row used by inventory consumers.
type DiscoveryFile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Folder      string `json:"folder"`
	ContentType string `json:"content_type,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	Revision    int64  `json:"revision"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func boundedLimit(raw string, def, max int) int {
	n, _ := strconv.Atoi(raw)
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func (a *App) httpFileChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	after, err := strconv.ParseInt(strings.TrimSpace(q.Get("after")), 10, 64)
	if q.Get("after") != "" && (err != nil || after < 0) {
		httpErr(w, http.StatusBadRequest, "after must be a non-negative integer")
		return
	}
	limit := boundedLimit(q.Get("limit"), changeFeedDefaultLimit, changeFeedMaxLimit)
	page, err := dbFileChanges(globalCtx.AppDB(), pid, after, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, page)
}

type fileChangesPage struct {
	Changes []FileChange `json:"changes"`
	// NextAfter is the cursor to send next. When the page is empty it is
	// the feed head, so an idle consumer skips other projects' sequence
	// numbers instead of rescanning them.
	NextAfter int64 `json:"next_after"`
	HasMore   bool  `json:"has_more"`
	HeadSeq   int64 `json:"head_seq"`
	// PrunedThrough: every seq <= this value may have been deleted. A
	// cursor below it has missed changes and must rebuild.
	PrunedThrough int64 `json:"pruned_through"`
}

func dbFileChanges(db *sql.DB, pid string, after int64, limit int) (*fileChangesPage, error) {
	page := &fileChangesPage{Changes: []FileChange{}}
	// Head first: any change committed after this read has seq > head and
	// is picked up next call, so advancing an empty page to head is safe.
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM file_changes`).Scan(&page.HeadSeq); err != nil {
		return nil, err
	}
	page.PrunedThrough = changeFeedPrunedThrough(db)
	rows, err := db.Query(`SELECT seq,file_id,revision,op,changed_at FROM file_changes
		WHERE project_id=? AND seq>? AND seq<=? ORDER BY seq LIMIT ?`, pid, after, page.HeadSeq, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c FileChange
		if err := rows.Scan(&c.Seq, &c.FileID, &c.Revision, &c.Op, &c.ChangedAt); err != nil {
			return nil, err
		}
		page.Changes = append(page.Changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Changes) > limit {
		page.Changes = page.Changes[:limit]
		page.HasMore = true
	}
	switch {
	case page.HasMore:
		page.NextAfter = page.Changes[len(page.Changes)-1].Seq
	case after > page.HeadSeq:
		page.NextAfter = after
	default:
		page.NextAfter = page.HeadSeq
	}
	return page, nil
}

func changeFeedPrunedThrough(db *sql.DB) int64 {
	var raw string
	if err := db.QueryRow(`SELECT value FROM storage_state WHERE key=?`, changeFeedPrunedKey).Scan(&raw); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// pruneFileChanges removes feed rows past retention, one bounded batch per
// sweep, and records the highest removed seq in the same transaction so a
// consumer can never observe a gap without also seeing pruned_through move.
func pruneFileChanges(app *sdk.AppCtx, now time.Time) {
	db := app.AppDB()
	cutoff := now.Add(-changeFeedRetention).Unix()
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM (SELECT seq FROM file_changes WHERE changed_at<? ORDER BY seq LIMIT ?)`,
		cutoff, changeFeedPruneBatch).Scan(&maxSeq); err != nil || !maxSeq.Valid {
		return
	}
	if _, err := tx.Exec(`DELETE FROM file_changes WHERE seq<=?`, maxSeq.Int64); err != nil {
		return
	}
	if _, err := tx.Exec(`INSERT INTO storage_state(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=CASE WHEN CAST(value AS INTEGER) < CAST(excluded.value AS INTEGER) THEN excluded.value ELSE value END`,
		changeFeedPrunedKey, strconv.FormatInt(maxSeq.Int64, 10)); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		app.Logger().Warn("file change feed prune failed", "error", err)
	}
}

// httpDiscoveryPage serves keyset-paginated listings (after_id). Ordered
// by id, so concurrent writes cannot shift rows between pages: a file is
// either before or after the cursor for the whole sweep.
func httpDiscoveryPage(w http.ResponseWriter, r *http.Request, pid string) {
	q := r.URL.Query()
	afterID, err := strconv.ParseInt(strings.TrimSpace(q.Get("after_id")), 10, 64)
	if err != nil || afterID < 0 {
		httpErr(w, http.StatusBadRequest, "after_id must be a non-negative integer")
		return
	}
	limit := boundedLimit(q.Get("limit"), 50, 10000)
	slim := q.Get("fields") == "discovery"
	db := globalCtx.AppDB()
	cols := fileSelectColumns
	if slim {
		cols = `id, name, folder, COALESCE(content_type,''), COALESCE(size_bytes,0), COALESCE(sha256,''), COALESCE(revision,1), COALESCE(updated_at,'')`
	}
	rows, err := db.Query(`SELECT `+cols+` FROM files
		WHERE project_id=? AND deleted_at IS NULL AND id>? ORDER BY id LIMIT ?`, pid, afterID, limit+1)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var files []any
	var lastID int64
	n := 0
	hasMore := false
	for rows.Next() {
		if n == limit {
			hasMore = true
			break
		}
		if slim {
			var d DiscoveryFile
			if err := rows.Scan(&d.ID, &d.Name, &d.Folder, &d.ContentType, &d.SizeBytes, &d.SHA256, &d.Revision, &d.UpdatedAt); err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			lastID = d.ID
			files = append(files, d)
		} else {
			f, err := scanFile(rows, false)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			lastID = f.ID
			files = append(files, f)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows.Close()
	if !slim {
		for _, f := range files {
			populateFileURL(f.(*File))
		}
	}
	if files == nil {
		files = []any{}
	}
	if n == 0 {
		lastID = afterID
	}
	httpJSON(w, map[string]any{
		"files":         files,
		"next_after_id": lastID,
		"has_more":      hasMore,
	})
}
