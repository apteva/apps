package main

// Tier 1 — renders DB CRUD + state machine. In-memory SQLite via
// testkit, no spawned binary. Runs in milliseconds.

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestInsertRender_RoundTrip(t *testing.T) {
	ctx := newTestCtx(t)
	id, err := insertRender(ctx.AppDB(), testProj, "trim", []string{"42"},
		map[string]any{"start_ms": int64(1000), "end_ms": int64(3000)},
		"clip.mp4", "agent:1")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero render_id")
	}
	got, err := getRender(ctx.AppDB(), testProj, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Operation != "trim" {
		t.Errorf("operation=%q", got.Operation)
	}
	if got.Status != "pending" {
		t.Errorf("expected pending on insert, got %q", got.Status)
	}
	if len(got.SourceFileIDs) != 1 || got.SourceFileIDs[0] != "42" {
		t.Errorf("source_file_ids=%v", got.SourceFileIDs)
	}
	if got.OutputName != "clip.mp4" {
		t.Errorf("output_name=%q", got.OutputName)
	}
	if got.RequestedBy != "agent:1" {
		t.Errorf("requested_by=%q", got.RequestedBy)
	}
}

func TestInsertRender_Validation(t *testing.T) {
	ctx := newTestCtx(t)
	cases := []struct {
		name      string
		project   string
		op        string
		sources   []string
		expectErr string
	}{
		{"empty project", "", "trim", []string{"1"}, "project_id required"},
		{"empty op", testProj, "", []string{"1"}, "operation required"},
		{"no sources", testProj, "trim", nil, "at least one source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := insertRender(ctx.AppDB(), c.project, c.op, c.sources, nil, "", "")
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestClaimNextPending_PicksOldest(t *testing.T) {
	ctx := newTestCtx(t)
	// Insert two pending rows in order; claim should return the first.
	id1, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	id2, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"2"}, nil, "", "")
	got, err := claimNextPending(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id1 {
		t.Errorf("expected to claim id=%d (oldest), got %d", id1, got.ID)
	}
	if got.Status != "running" {
		t.Errorf("claimed row status=%q want running", got.Status)
	}
	if got.StartedAt == "" {
		t.Error("started_at not set on claim")
	}
	// Second claim returns the other row.
	got2, err := claimNextPending(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if got2.ID != id2 {
		t.Errorf("second claim id=%d want %d", got2.ID, id2)
	}
}

func TestClaimNextPending_EmptyQueue(t *testing.T) {
	ctx := newTestCtx(t)
	_, err := claimNextPending(ctx.AppDB())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows on empty queue, got %v", err)
	}
}

func TestClaimNextPending_AtomicUnderRace(t *testing.T) {
	// Same row should never be claimed twice. Insert one row, fire N
	// goroutines each calling claim, count successes.
	ctx := newTestCtx(t)
	insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")

	const workers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			row, err := claimNextPending(ctx.AppDB())
			if err == nil && row != nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Errorf("expected exactly 1 successful claim across %d workers, got %d", workers, successes)
	}
}

func TestRenderMarkOk_FinalisesRow(t *testing.T) {
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	if _, err := claimNextPending(ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	if err := renderMarkOk(ctx.AppDB(), id, "99"); err != nil {
		t.Fatal(err)
	}
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "ok" {
		t.Errorf("status=%q", got.Status)
	}
	if got.OutputFileID != "99" {
		t.Errorf("output_file_id=%q", got.OutputFileID)
	}
	if got.ProgressPct != 100 {
		t.Errorf("progress_pct=%d want 100", got.ProgressPct)
	}
	if got.CompletedAt == "" {
		t.Error("completed_at not set")
	}
}

func TestRenderMarkOk_GuardsAgainstNonRunning(t *testing.T) {
	// Marking a still-pending row as ok should not flip it (the
	// WHERE status='running' guard catches the bug where a worker
	// completes a row it never claimed).
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	_ = renderMarkOk(ctx.AppDB(), id, "99") // no-op: still pending
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "pending" {
		t.Errorf("guard breached: status=%q want pending", got.Status)
	}
}

func TestRenderMarkFailed_FromPending(t *testing.T) {
	// Pre-validation failures fail rows before they're claimed.
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	if err := renderMarkFailed(ctx.AppDB(), id, "bad params"); err != nil {
		t.Fatal(err)
	}
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "failed" {
		t.Errorf("status=%q", got.Status)
	}
	if got.Error != "bad params" {
		t.Errorf("error=%q", got.Error)
	}
}

func TestRenderMarkCancelled_Pending(t *testing.T) {
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	if err := renderMarkCancelled(ctx.AppDB(), id); err != nil {
		t.Fatal(err)
	}
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "cancelled" {
		t.Errorf("status=%q", got.Status)
	}
}

func TestRenderMarkCancelled_NoOpOnTerminal(t *testing.T) {
	// Cancelling an already-failed row must not overwrite the status.
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	_ = renderMarkFailed(ctx.AppDB(), id, "boom")
	_ = renderMarkCancelled(ctx.AppDB(), id) // should be no-op
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.Status != "failed" {
		t.Errorf("cancellation overwrote terminal: status=%q", got.Status)
	}
}

func TestRenderUpdateProgress_OnlyWhileRunning(t *testing.T) {
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	// Pending → no update.
	_ = renderUpdateProgress(ctx.AppDB(), id, 42)
	got, _ := getRender(ctx.AppDB(), testProj, id)
	if got.ProgressPct != 0 {
		t.Errorf("progress updated while pending: %d", got.ProgressPct)
	}
	// Claim → running → update lands.
	_, _ = claimNextPending(ctx.AppDB())
	_ = renderUpdateProgress(ctx.AppDB(), id, 42)
	got, _ = getRender(ctx.AppDB(), testProj, id)
	if got.ProgressPct != 42 {
		t.Errorf("progress=%d want 42", got.ProgressPct)
	}
	// Out-of-range gets clamped.
	_ = renderUpdateProgress(ctx.AppDB(), id, 200)
	got, _ = getRender(ctx.AppDB(), testProj, id)
	if got.ProgressPct != 100 {
		t.Errorf("progress=%d want clamped to 100", got.ProgressPct)
	}
}

func TestListRenders_FiltersAndOrder(t *testing.T) {
	ctx := newTestCtx(t)
	// Mix statuses + ops; ensure newest-first ordering and filters work.
	id1, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"1"}, nil, "", "")
	id2, _ := insertRender(ctx.AppDB(), testProj, "resize", []string{"1"}, nil, "", "")
	id3, _ := insertRender(ctx.AppDB(), testProj, "trim", []string{"2"}, nil, "", "")
	_, _ = claimNextPending(ctx.AppDB()) // id1 → running
	_ = renderMarkOk(ctx.AppDB(), id1, "100")

	all, err := listRenders(ctx.AppDB(), testProj, RenderFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(all))
	}
	if all[0].ID != id3 {
		t.Errorf("expected newest-first, got %d first", all[0].ID)
	}

	trims, _ := listRenders(ctx.AppDB(), testProj, RenderFilters{Operation: "trim"})
	if len(trims) != 2 {
		t.Errorf("trim filter returned %d rows", len(trims))
	}

	pending, _ := listRenders(ctx.AppDB(), testProj, RenderFilters{Status: "pending"})
	if len(pending) != 2 {
		t.Errorf("pending filter returned %d rows", len(pending))
	}
	for _, r := range pending {
		if r.ID == id1 {
			t.Errorf("pending filter returned the ok row id=%d", r.ID)
		}
	}
	_ = id2
}

func TestGetRender_ProjectScoped(t *testing.T) {
	// A render in another project must not be returned. Tenants
	// should never see each other's renders.
	ctx := newTestCtx(t)
	id, _ := insertRender(ctx.AppDB(), "other-proj", "trim", []string{"1"}, nil, "", "")
	_, err := getRender(ctx.AppDB(), testProj, id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cross-project get should return ErrNoRows, got %v", err)
	}
}
