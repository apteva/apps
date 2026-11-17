package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeInventory is a Storage stand-in that records which endpoints the
// indexer calls. legacy=true behaves like Storage < 0.14 (no feed, offset
// listing only).
type fakeInventory struct {
	mu      sync.Mutex
	files   map[int64]StorageFile
	changes []FileChange
	pruned  int64
	legacy  bool
	hidden  map[int64]bool // omitted from ids= batches but present by exact id
	calls   map[string]int
}

func newFakeInventory(t *testing.T, legacy bool) *fakeInventory {
	f := &fakeInventory{files: map[int64]StorageFile{}, legacy: legacy, hidden: map[int64]bool{}, calls: map[string]int{}}
	const prefix = "/api/apps/callback/apps/storage/proxy/files"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, prefix)
		q := r.URL.Query()
		switch {
		case path == "/changes":
			if f.legacy {
				f.calls["changes-unsupported"]++
				http.Error(w, "invalid id", 400)
				return
			}
			f.calls["changes"]++
			after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
			limit, _ := strconv.Atoi(q.Get("limit"))
			var head int64
			out := []FileChange{}
			for _, c := range f.changes {
				head = max(head, c.Seq)
				if c.Seq > after && len(out) < limit {
					out = append(out, c)
				}
			}
			next := head
			more := false
			if len(out) == limit && out[len(out)-1].Seq < head {
				next, more = out[len(out)-1].Seq, true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"changes": out, "next_after": next, "has_more": more, "head_seq": head, "pruned_through": f.pruned})
		case path == "" && q.Get("ids") != "":
			f.calls["ids"]++
			out := []StorageFile{}
			for _, s := range strings.Split(q.Get("ids"), ",") {
				id, _ := strconv.ParseInt(s, 10, 64)
				if file, ok := f.files[id]; ok && !f.hidden[id] {
					out = append(out, file)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
		case path == "" && q.Has("after_id") && !f.legacy:
			f.calls["discovery"]++
			after, _ := strconv.ParseInt(q.Get("after_id"), 10, 64)
			limit, _ := strconv.Atoi(q.Get("limit"))
			ids := f.sortedIDs()
			out := []StorageFile{}
			next := after
			more := false
			for _, id := range ids {
				if id <= after {
					continue
				}
				if len(out) == limit {
					more = true
					break
				}
				out = append(out, f.files[id])
				next = id
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": out, "next_after_id": next, "has_more": more})
		case path == "":
			f.calls["offset"]++
			offset, _ := strconv.Atoi(q.Get("offset"))
			limit, _ := strconv.Atoi(q.Get("limit"))
			ids := f.sortedIDs()
			out := []StorageFile{}
			for i := offset; i < len(ids) && len(out) < limit; i++ {
				out = append(out, f.files[ids[i]])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
		case strings.HasPrefix(path, "/") && r.Method == http.MethodGet:
			f.calls["get"]++
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/"), 10, 64)
			file, ok := f.files[id]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"file": file})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), 500)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "dev-test")
	return f
}

func (f *fakeInventory) sortedIDs() []int64 {
	var ids []int64
	for id := range f.files {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

func (f *fakeInventory) add(id int64, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[id] = StorageFile{ID: id, Name: name, Folder: "/", ContentType: "audio/mpeg", SHA256: "sha-" + strconv.FormatInt(id, 10)}
}

func (f *fakeInventory) change(fileID int64, op string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, FileChange{Seq: int64(len(f.changes) + 1), FileID: fileID, Revision: 1, Op: op})
}

func (f *fakeInventory) resetCalls() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.calls
	f.calls = map[string]int{}
	return old
}

func listQueue(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT file_id, op FROM index_queue WHERE project_id=? ORDER BY file_id`, testProj)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, op string
		if err := rows.Scan(&id, &op); err != nil {
			t.Fatal(err)
		}
		out[id] = op
	}
	return out
}

func indexed(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if err := upsertMedia(db, testProj, id, sampleAudioProbe(), "sha-"+id, "/", "f"+id+".mp3"); err != nil {
		t.Fatal(err)
	}
	// Mark derivations done so the row is not a repair candidate.
	if _, err := db.Exec(`INSERT INTO derivations(project_id,file_id,kind,status,storage_file_id) VALUES(?,?,'waveform','ok','9`+id+`')`, testProj, id); err != nil {
		t.Fatal(err)
	}
}

// An unchanged, fully indexed project: after the initial sweep, ticks make
// no inventory listing calls at all — only the cheap change-feed read.
func TestIncremental_UnchangedProjectHasNoRecurringInventoryTraffic(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	inv := newFakeInventory(t, false)
	for id := int64(1); id <= 2500; id++ {
		inv.add(id, "f"+strconv.FormatInt(id, 10)+".mp3")
		indexed(t, db, strconv.FormatInt(id, 10))
	}
	sc := newStorageClient()
	tick := func() (string, bool) {
		st, err := loadSyncState(db, testProj)
		if err != nil {
			t.Fatal(err)
		}
		mode := syncChangeFeed(context.Background(), ctx, sc, db, testProj, st)
		cands, _ := drainIndexQueue(context.Background(), ctx, sc, db, testProj, indexerBatchSize, false)
		if len(cands) != 0 {
			t.Fatalf("unchanged catalog produced candidates: %+v", cands)
		}
		return mode, reconcileStep(context.Background(), ctx, sc, db, testProj, st, mode, false)
	}
	// Initial sweep: one 1000-file page per tick, then the orphan pass.
	ticks := 0
	for {
		ticks++
		mode, sweeping := tick()
		if mode != feedModeFeed {
			t.Fatalf("mode=%q", mode)
		}
		if !sweeping {
			break
		}
		if ticks > 20 {
			t.Fatal("sweep never finished")
		}
	}
	if q := listQueue(t, db); len(q) != 0 {
		t.Fatalf("indexed files were queued: %v", q)
	}
	inv.resetCalls()
	for i := 0; i < 5; i++ {
		tick()
	}
	calls := inv.resetCalls()
	if calls["discovery"]+calls["offset"]+calls["ids"]+calls["get"] != 0 || calls["changes"] != 5 {
		t.Fatalf("steady-state ticks made inventory calls: %v", calls)
	}
}

// Feed entries are queued with the cursor in one transaction; replaying a
// page cannot create duplicate work; tombstones remove media rows.
func TestIncremental_FeedQueuesChangesAndAppliesTombstones(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	inv := newFakeInventory(t, false)
	indexed(t, db, "7")
	inv.add(5, "new.mp3")
	inv.change(5, "added")
	inv.change(5, "updated")
	inv.change(7, "deleted")
	sc := newStorageClient()

	st, _ := loadSyncState(db, testProj)
	if mode := syncChangeFeed(context.Background(), ctx, sc, db, testProj, st); mode != feedModeFeed {
		t.Fatalf("mode=%q", mode)
	}
	if q := listQueue(t, db); len(q) != 2 || q["5"] != "changed" || q["7"] != "deleted" {
		t.Fatalf("queue=%v", q)
	}
	// Restart: cursor persisted, nothing re-queued.
	st, _ = loadSyncState(db, testProj)
	if st.FeedAfter != 3 {
		t.Fatalf("feed_after=%d", st.FeedAfter)
	}
	syncChangeFeed(context.Background(), ctx, sc, db, testProj, st)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM index_queue`).Scan(&n)
	if n != 2 {
		t.Fatalf("queue rows after replay=%d", n)
	}

	cands, acks := drainIndexQueue(context.Background(), ctx, sc, db, testProj, indexerBatchSize, false)
	if len(cands) != 1 || cands[0].ID != 5 || acks[5].fileID != "5" {
		t.Fatalf("cands=%+v acks=%+v", cands, acks)
	}
	if m, _ := getMedia(db, testProj, "7"); m != nil {
		t.Fatal("tombstone did not remove media row 7")
	}
	// The candidate stays queued until processed and acknowledged.
	if q := listQueue(t, db); len(q) != 1 || q["5"] == "" {
		t.Fatalf("queue after drain=%v", q)
	}
	ackIndexWork(db, testProj, "5", acks[5].gen)
	if q := listQueue(t, db); len(q) != 0 {
		t.Fatalf("queue after ack=%v", q)
	}
}

// A re-enqueue while an attempt is in flight must survive that attempt's ack.
func TestIncremental_AckDoesNotDropNewerWork(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	if err := enqueueIndexWork(db, testProj, "9", 1, "changed", "event"); err != nil {
		t.Fatal(err)
	}
	var gen int64
	_ = db.QueryRow(`SELECT gen FROM index_queue WHERE file_id='9'`).Scan(&gen)
	if err := enqueueIndexWork(db, testProj, "9", 2, "changed", "feed"); err != nil {
		t.Fatal(err)
	}
	ackIndexWork(db, testProj, "9", gen)
	var rev int64
	if err := db.QueryRow(`SELECT revision FROM index_queue WHERE file_id='9'`).Scan(&rev); err != nil || rev != 2 {
		t.Fatalf("newer work dropped: rev=%d err=%v", rev, err)
	}
}

func TestIncremental_PrunedFeedRestartsSweep(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	inv := newFakeInventory(t, false)
	if _, err := db.Exec(`INSERT INTO index_sync_state(project_id, feed_mode, feed_after, sweep_active, sweep_completed_at) VALUES(?, 'feed', 2, 0, CURRENT_TIMESTAMP)`, testProj); err != nil {
		t.Fatal(err)
	}
	inv.pruned = 10
	st, _ := loadSyncState(db, testProj)
	syncChangeFeed(context.Background(), ctx, newStorageClient(), db, testProj, st)
	st, _ = loadSyncState(db, testProj)
	if !st.SweepActive || st.SweepCursor != 0 {
		t.Fatalf("gap did not restart sweep: %+v", st)
	}
}

// Legacy Storage: the feed is detected as unsupported, the sweep pages by
// offset and queues unindexed media, and a restart starts a new sweep.
func TestIncremental_LegacyStorageFallsBackToOffsetSweep(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	inv := newFakeInventory(t, true)
	inv.add(1, "a.mp3")
	inv.add(2, "notes.txt")
	inv.files[2] = StorageFile{ID: 2, Name: "notes.txt", Folder: "/", ContentType: "text/plain"}
	sc := newStorageClient()
	st, _ := loadSyncState(db, testProj)
	mode := syncChangeFeed(context.Background(), ctx, sc, db, testProj, st)
	if mode != feedModeLegacy {
		t.Fatalf("mode=%q", mode)
	}
	if reconcileStep(context.Background(), ctx, sc, db, testProj, st, mode, false) {
		t.Fatal("single-page sweep should finish")
	}
	if q := listQueue(t, db); len(q) != 1 || q["1"] != "changed" {
		t.Fatalf("queue=%v", q)
	}
	// Within the recheck period the unsupported feed is not probed again.
	inv.resetCalls()
	st, _ = loadSyncState(db, testProj)
	syncChangeFeed(context.Background(), ctx, sc, db, testProj, st)
	if c := inv.resetCalls(); c["changes-unsupported"] != 0 {
		t.Fatalf("legacy feed re-probed: %v", c)
	}
	restartLegacySweeps(db)
	st, _ = loadSyncState(db, testProj)
	if !st.SweepActive {
		t.Fatal("restart did not start a legacy sweep")
	}
}

// Orphan check: only an explicit 404 for the exact id deletes a row. A
// batch lookup that merely omits the id is inconclusive.
func TestIncremental_OrphanRequiresExplicitNotFound(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	inv := newFakeInventory(t, false)
	indexed(t, db, "1") // present
	indexed(t, db, "2") // omitted from batch but exists → keep
	indexed(t, db, "3") // gone → purge
	inv.add(1, "a.mp3")
	inv.add(2, "b.mp3")
	inv.hidden[2] = true
	st, _ := loadSyncState(db, testProj)
	st.OrphanCursor = ""
	if !reconcileOrphanSlice(ctx, newStorageClient(), db, testProj, st) {
		t.Fatal("orphan pass should finish in one slice")
	}
	for id, want := range map[string]bool{"1": true, "2": true, "3": false} {
		m, _ := getMedia(db, testProj, id)
		if (m != nil) != want {
			t.Fatalf("file %s present=%v want %v", id, m != nil, want)
		}
	}
}

func TestIncremental_EventCursorPersists(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	saveEventSeq(db, "", 42)
	saveEventSeq(db, testProj, 7)
	if got := loadEventSeq(db, ""); got != 42 {
		t.Fatalf("firehose seq=%d", got)
	}
	if got := loadEventSeq(db, testProj); got != 7 {
		t.Fatalf("project seq=%d", got)
	}

}
