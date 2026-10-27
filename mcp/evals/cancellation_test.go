package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"sync"
	"testing"
	"time"
)

func cancellationRun(t *testing.T) (store, *Run) {
	t.Helper()
	db := testStore(t)
	if err := db.saveSuite(&Suite{ID: "suite", Name: "Cancellation", RequiredPassRate: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	item := Case{ID: "case", SuiteID: "suite", Name: "Task", Prompt: "Wait", Enabled: true, Goals: []Goal{{Text: "Wait"}}}
	if err := db.saveCase(&item); err != nil {
		t.Fatal(err)
	}
	exp := &Experiment{ID: "exp", SuiteID: "suite", SuiteRevision: 1, Name: "Cancel", Targets: []Target{{AgentID: 1}}, Repetitions: 1, CreatedAt: time.Now()}
	if err := db.createExperiment(exp, []Case{item}); err != nil {
		t.Fatal(err)
	}
	run, err := db.claimRun()
	if err != nil || run == nil {
		t.Fatalf("claim=%+v err=%v", run, err)
	}
	return db, run
}

func TestCancellationIsDurableAndUnscoredAfterFinalization(t *testing.T) {
	db, run := cancellationRun(t)
	if err := db.cancelExperiment(run.ExperimentID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.getRun(run.ID)
	if err != nil || stored.CancelRequestedAt == nil || stored.Status != "running" || stored.Stage != "cancelling" || stored.FinishedAt != nil {
		t.Fatalf("cancellation prematurely complete: %+v err=%v", stored, err)
	}
	if err := db.updateRunProgress(run.ID, "judging", "new-runtime"); !errors.Is(err, errRunCancelled) {
		t.Fatalf("cancelled stage advanced: %v", err)
	}
	if retried, err := db.retryInvalidSimulation(run); err != nil || retried {
		t.Fatalf("cancelled run retried=%v err=%v", retried, err)
	}
	run.Status = "pass"
	score := 1.0
	run.OverallScore = &score
	if err := db.finishRun(run); !errors.Is(err, errRunCancelled) {
		t.Fatalf("cancelled run scored: %v", err)
	}
	if err := db.finishCancelled(run.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ = db.getRun(run.ID)
	if stored.Status != "cancelled" || stored.OverallScore != nil || stored.FinishedAt == nil {
		t.Fatalf("cancelled result=%+v", stored)
	}
	if _, err := db.retryRun(stored); !errors.Is(err, errRunCancelled) {
		t.Fatalf("explicit retry revived cancelled experiment: %v", err)
	}
}

func TestCancellationRacingAutomaticRequeueCannotReviveWork(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			db, run := cancellationRun(t)
			db.db.SetMaxOpenConns(1)
			start := make(chan struct{})
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			wg.Add(2)
			go func() { defer wg.Done(); <-start; _, e := db.retryInvalidSimulation(run); errs <- e }()
			go func() { defer wg.Done(); <-start; errs <- db.cancelExperiment(run.ExperimentID) }()
			close(start)
			wg.Wait()
			close(errs)
			for e := range errs {
				if e != nil {
					t.Fatal(e)
				}
			}
			if claimed, err := db.claimRun(); err != nil || claimed != nil {
				t.Fatalf("cancelled work reclaimed=%+v err=%v", claimed, err)
			}
			stored, _ := db.getRun(run.ID)
			if stored.CancelRequestedAt == nil {
				t.Fatal("durable cancellation lost")
			}
			if stored.Status != "cancelled" && stored.Stage != "cancelling" {
				t.Fatalf("cancellation overwritten: %+v", stored)
			}
		})
	}
}

func TestAttemptHistoryPreservesOriginalFailureAndCleanup(t *testing.T) {
	db, run := cancellationRun(t)
	run.EnvironmentRunID = "env-first"
	run.Error = "context_injection_incomplete"
	run.Status = "invalid_simulation"
	if err := db.saveAttempt(run, 0, "rt-first", []byte(`{"shutdown":{"status":"destroy_failed","error":"cleanup failed"}}`)); err != nil {
		t.Fatal(err)
	}
	run.Status = "running"
	run.Error = ""
	run.EnvironmentRunID = ""
	if err := db.saveAttempt(run, 0, "", nil); err != nil {
		t.Fatal(err)
	}
	run.SimulationAttempt = 1
	run.EnvironmentRunID = "env-second"
	run.Status = "cancelled"
	run.Error = ""
	if err := db.saveAttempt(run, 1, "rt-second", []byte(`{"shutdown":{"status":"complete"}}`)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.listAttempts(run.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("attempts=%+v err=%v", rows, err)
	}
	if rows[0].Attempt != 1 || rows[0].RuntimeID != "rt-first" || rows[0].EnvironmentRunID != "env-first" || rows[0].PrimaryError != "context_injection_incomplete" || !strings.Contains(string(rows[0].Cleanup), "cleanup failed") {
		t.Fatalf("original evidence overwritten: %+v", rows[0])
	}
	if rows[1].Attempt != 2 || rows[1].RuntimeID != "rt-second" || rows[1].Status != "cancelled" {
		t.Fatalf("second attempt lost: %+v", rows[1])
	}
}

func TestPersistedCancellationRecoversAfterRunnerRestart(t *testing.T) {
	db, run := cancellationRun(t)
	if err := db.updateRunProgress(run.ID, "agent_running", "environment-run-cancel"); err != nil {
		t.Fatal(err)
	}
	if err := db.cancelExperiment(run.ExperimentID); err != nil {
		t.Fatal(err)
	}
	platform := &cancellationPlatformStub{stopped: make(chan struct{}), stopCalls: make(chan string, 2)}
	svc := &service{db: db, ctx: sdk.NewAppCtxForTest(nil, db.db, nil, platform, nil)}
	if err := svc.recoverCancellations(); err != nil {
		t.Fatal(err)
	}
	stored, err := db.getRun(run.ID)
	if err != nil || stored.Status != "cancelled" || stored.Stage != "cancelled" || stored.OverallScore != nil || len(stored.Attempts) != 1 {
		t.Fatalf("recovered=%+v err=%v", stored, err)
	}
	if err := svc.recoverCancellations(); err != nil {
		t.Fatal(err)
	}
	if len(platform.stopCalls) != 1 {
		t.Fatal("recovery repeated a completed stop")
	}
}

type failedCleanupPlatform struct{ *cancellationPlatformStub }

func (p *failedCleanupPlatform) CallAppResult(_, tool string, _ map[string]any, out any) error {
	if tool == "environment_run_stop" {
		return errors.New("closing callback HTTP 404")
	}
	if tool == "environment_run_get" {
		raw := []byte(`{"run":{"id":"env","runtime_id":"rt"},"shutdown":{"status":"partial","error":"closing callback HTTP 404"},"voice_calls":[{"error":"context_injection_incomplete"}]}`)
		return json.Unmarshal(raw, out)
	}
	return fmt.Errorf("unexpected tool %s", tool)
}
func TestFailedCleanupPreventsRetryAndRetainsOriginalEvidence(t *testing.T) {
	db, run := cancellationRun(t)
	run.EnvironmentRunID = "env"
	p := &failedCleanupPlatform{cancellationPlatformStub: &cancellationPlatformStub{}}
	control := &evalRunControl{environment: "env"}
	svc := &service{db: db, ctx: sdk.NewAppCtxForTest(nil, db.db, nil, p, nil), controls: map[string]*evalRunControl{run.ID: control}}
	if err := svc.prepareRetry(run, []string{"original provider timeout"}); err == nil || !strings.Contains(err.Error(), "cleanup incomplete") {
		t.Fatalf("cleanup failure allowed retry: %v", err)
	}
	rows, err := db.listAttempts(run.ID)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].PrimaryError, "original provider timeout") || !strings.Contains(string(rows[0].Cleanup), "context_injection_incomplete") || !strings.Contains(string(rows[0].Cleanup), "HTTP 404") || rows[0].RuntimeID != "rt" {
		t.Fatalf("evidence=%+v err=%v", rows, err)
	}
	stored, _ := db.getRun(run.ID)
	if stored.SimulationAttempt != 0 {
		t.Fatal("cleanup failure advanced automatic retry")
	}
}

func TestCancelUnownedRunnerPreservesTerminalAttempt(t *testing.T) {
	db, run := cancellationRun(t)
	if err := db.updateRunProgress(run.ID, "agent_running", "environment-run-cancel"); err != nil {
		t.Fatal(err)
	}
	platform := &cancellationPlatformStub{stopped: make(chan struct{}), stopCalls: make(chan string, 2)}
	svc := &service{db: db, ctx: sdk.NewAppCtxForTest(nil, db.db, nil, platform, nil)}
	if err := svc.cancelExperiment(run.ExperimentID); err != nil {
		t.Fatal(err)
	}
	stored, err := db.getRun(run.ID)
	if err != nil || stored.Status != "cancelled" || stored.Outcome != "cancelled" || len(stored.Attempts) != 1 || stored.Attempts[0].Status != "cancelled" || stored.Attempts[0].FinishedAt == nil {
		t.Fatalf("cancelled orphan=%+v err=%v", stored, err)
	}
}

func TestDirectEnvironmentCancellationCannotBecomeSimulationRetry(t *testing.T) {
	db, run := cancellationRun(t)
	svc := &service{db: db}
	call := &EnvironmentVoiceCall{Status: "cancelled", CancellationReason: "environment_stopped"}
	if err := svc.acceptVoiceCancellation(run, call); !errors.Is(err, errRunCancelled) {
		t.Fatalf("environment cancellation ignored: %v", err)
	}
	if retried, err := db.retryInvalidSimulation(run); err != nil || retried {
		t.Fatalf("cancelled environment retried=%v err=%v", retried, err)
	}
	if err := db.finishCancelled(run.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.getRun(run.ID)
	if stored.Status != "cancelled" || stored.OverallScore != nil {
		t.Fatalf("environment cancellation scored: %+v", stored)
	}
}
