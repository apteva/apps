package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLegacySuspendedRuntimeHoldRepairsAndReleasesWithoutReplay(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	if err := a.writeStep(s, "running", 40, "accepted-operation-123; reconcile before retry", "", "agent:7:"+s.ThreadID); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	if err := a.holdWorkerExecutionError(s, "provider quota exhausted"); err != nil {
		t.Fatal(err)
	}
	saved := stepBy(t, a, r, "research")
	// Reproduce the production legacy row: a running label overwrote the hold,
	// while its runtime suspension and immutable failure event remained intact.
	if _, err := a.db.Exec(`UPDATE process_step_runs SET state='running',updated_by=? WHERE id=?`, "agent:7:"+s.ThreadID, s.ID); err != nil {
		t.Fatal(err)
	}
	before := len(f.events)
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	repaired := stepBy(t, a, r, "research")
	if repaired.State != "blocked" || repaired.UpdatedBy != "runtime" || !repaired.DeliverySuspended || repaired.Output != saved.Output || repaired.Error != saved.Error {
		t.Fatal("runtime repair lost original checkpoint", repaired)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != 1 || len(f.events) != before {
		t.Fatal("repair replayed work or retained worker")
	}
	parent, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || parent.State != "blocked" || !parent.DeliverySuspended {
		t.Fatal("parent not repaired", parent, err)
	}
	// Original history is still recoverable; only an explicit operator can resume.
	var count int
	if err = a.db.QueryRow(`SELECT count(*) FROM process_step_events WHERE step_id=? AND actor='runtime' AND error=?`, s.ID, saved.Error).Scan(&count); err != nil || count < 2 {
		t.Fatal("repair missing audit event", count, err)
	}
}

func TestLegacyRuntimeRepairPreservesRetainedDependenciesAndNeedsRuntimeProof(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			a, f, p, r := sequentialSetup(t)
			s := stepBy(t, a, r, "research")
			if retained {
				if err := a.writeStep(s, "waiting", 20, "approval-123", "pending approval", "agent:7:"+s.ThreadID); err != nil {
					t.Fatal(err)
				}
				s = stepBy(t, a, r, "research")
				if err := a.holdWorkerExecutionError(s, "provider error"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.writeStep(s, "running", 20, "saved evidence", "Runtime execution error: executor-supplied text", "agent:7:"+s.ThreadID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.db.Exec(`UPDATE process_step_runs SET state='running',delivery_suspended=1,updated_by=? WHERE id=?`, "agent:7:"+s.ThreadID, s.ID); err != nil {
				t.Fatal(err)
			}
			if err := a.tickDirect(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
				t.Fatal("retired a dependency or trusted arbitrary error text", err)
			}
			if stepBy(t, a, r, "research").State != "running" {
				t.Fatal("unproven hold was changed")
			}
			_ = p
		})
	}
}

func TestLegacyRevalidationRechecksMissingCheckpointWithBoundedStableIDs(t *testing.T) {
	a, f, p, _ := legacyWaitBatch(t, []int64{7})
	before := len(f.events)
	now := time.Now()
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	first := f.events[before].SourceEventID
	f.lose = true
	if err := a.tickDirect(context.Background(), now.Add(16*time.Minute)); err == nil {
		t.Fatal("lost retry acknowledgement ignored")
	}
	retry := f.events[len(f.events)-1].SourceEventID
	if retry == first {
		t.Fatal("settled inspection reused old execution identity")
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), now.Add(17*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.events[len(f.events)-1].SourceEventID != retry {
		t.Fatal("ambiguous recheck changed identity on restart")
	}
	if err := a.tickDirect(context.Background(), now.Add(33*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.events[len(f.events)-1].SourceEventID == retry {
		t.Fatal("third inspection reused settled identity")
	}
	seen := len(f.events)
	if err := a.tickDirect(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != seen || len(f.kills) != 0 {
		t.Fatal("bounded handshake polled indefinitely or treated silence as retirement consent")
	}
	_ = p
}
