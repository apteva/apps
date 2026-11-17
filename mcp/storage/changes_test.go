package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func getChanges(t *testing.T, after int64, limit int) fileChangesPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/files/changes?project_id=test-proj&after=%d&limit=%d", after, limit), nil)
	rec := httptest.NewRecorder()
	(&App{}).httpFileChanges(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page fileChangesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestChangeFeed_RecordsAddMoveAndDeletesInOrder(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	a := mustUpload(t, ctx, "a.mp4", "/", "A")
	b := mustUpload(t, ctx, "b.mp4", "/", "B")
	if _, err := app.toolMove(ctx, map[string]any{"id": a.ID, "folder": "/clips/"}); err != nil {
		t.Fatal(err)
	}
	// Hard delete leaves no row; the feed must still carry a tombstone.
	if _, err := app.toolDelete(ctx, map[string]any{"id": b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDelete(ctx, map[string]any{"id": a.ID, "keep_record": true}); err != nil {
		t.Fatal(err)
	}

	page := getChanges(t, 0, 100)
	want := []struct {
		id int64
		op string
	}{{a.ID, "added"}, {b.ID, "added"}, {a.ID, "updated"}, {b.ID, "deleted"}, {a.ID, "deleted"}}
	if len(page.Changes) != len(want) {
		t.Fatalf("changes=%+v", page.Changes)
	}
	for i, w := range want {
		c := page.Changes[i]
		if c.FileID != w.id || c.Op != w.op {
			t.Fatalf("change %d = %+v, want %+v", i, c, w)
		}
		if i > 0 && c.Seq <= page.Changes[i-1].Seq {
			t.Fatalf("seq not increasing: %+v", page.Changes)
		}
	}
	if page.HasMore || page.NextAfter != page.HeadSeq {
		t.Fatalf("cursor: %+v", page)
	}
	// Resuming from the cursor yields nothing new.
	if again := getChanges(t, page.NextAfter, 100); len(again.Changes) != 0 || again.NextAfter != page.NextAfter {
		t.Fatalf("resume: %+v", again)
	}
}

func TestChangeFeed_IgnoresBookkeepingUpdates(t *testing.T) {
	ctx := newTestCtx(t)
	f := mustUpload(t, ctx, "a.mp4", "/", "A")
	head := getChanges(t, 0, 100).NextAfter
	if _, err := ctx.AppDB().Exec(`UPDATE files SET checksum_status='verified', checksum_error='', updated_at=CURRENT_TIMESTAMP WHERE id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	if page := getChanges(t, head, 100); len(page.Changes) != 0 {
		t.Fatalf("bookkeeping update entered the feed: %+v", page.Changes)
	}
}

func TestChangeFeed_PaginatesAndScopesByProject(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 0; i < 5; i++ {
		mustUpload(t, ctx, fmt.Sprintf("f%d.mp4", i), "/", fmt.Sprintf("body-%d", i))
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO files(project_id,name,folder,storage_key) VALUES('other-proj','x.mp4','/','other-key')`); err != nil {
		t.Fatal(err)
	}
	var seen int
	after := int64(0)
	for i := 0; i < 10; i++ {
		page := getChanges(t, after, 2)
		seen += len(page.Changes)
		after = page.NextAfter
		if !page.HasMore {
			if after != page.HeadSeq {
				t.Fatalf("idle cursor should reach head: %+v", page)
			}
			break
		}
	}
	if seen != 5 {
		t.Fatalf("saw %d changes for test-proj, want 5", seen)
	}
}

func TestChangeFeed_PruneAdvancesWatermark(t *testing.T) {
	ctx := newTestCtx(t)
	mustUpload(t, ctx, "a.mp4", "/", "A")
	mustUpload(t, ctx, "b.mp4", "/", "B")
	first := getChanges(t, 0, 100).Changes[0].Seq
	old := time.Now().Add(-changeFeedRetention - time.Hour).Unix()
	if _, err := ctx.AppDB().Exec(`UPDATE file_changes SET changed_at=? WHERE seq=?`, old, first); err != nil {
		t.Fatal(err)
	}
	pruneFileChanges(ctx, time.Now())
	page := getChanges(t, 0, 100)
	if page.PrunedThrough != first || len(page.Changes) != 1 {
		t.Fatalf("after prune: %+v", page)
	}
}

func TestDiscoveryPage_KeysetIsStableUnderUpdates(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, mustUpload(t, ctx, fmt.Sprintf("f%d.mp4", i), "/", fmt.Sprintf("body-%d", i)).ID)
	}
	fetch := func(after int64) (files []DiscoveryFile, next int64, more bool) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/files?project_id=test-proj&after_id=%d&limit=2&fields=discovery", after), nil)
		rec := httptest.NewRecorder()
		app.httpListOrSearch(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Files       []DiscoveryFile `json:"files"`
			NextAfterID int64           `json:"next_after_id"`
			HasMore     bool            `json:"has_more"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if raw := rec.Body.String(); strings.Contains(raw, `"url"`) || strings.Contains(raw, `"storage_key"`) {
			t.Fatalf("discovery rows leaked heavy fields: %s", raw)
		}
		return body.Files, body.NextAfterID, body.HasMore
	}
	var got []int64
	after := int64(0)
	for {
		files, next, more := fetch(after)
		for _, f := range files {
			got = append(got, f.ID)
		}
		// Touch an unseen row mid-sweep: with updated_at ordering + OFFSET
		// this shifted the window and skipped a file.
		if len(got) == 2 {
			if _, err := app.toolMove(ctx, map[string]any{"id": ids[4], "folder": "/moved/"}); err != nil {
				t.Fatal(err)
			}
		}
		after = next
		if !more {
			break
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(ids) {
		t.Fatalf("sweep ids=%v want %v", got, ids)
	}
}
