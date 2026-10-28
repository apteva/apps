package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

// ---- fakes ----

// fakePlatform answers the Evals calls bench delegates to. Fixtures are already
// envelope-stripped, so a single Unmarshal into out matches what the real
// CallAppResult hands back.
type fakePlatform struct {
	testkit.BasePlatformClient
	suiteID    string
	caseSeq    int
	experiment evalExperiment
	calls      []string
	caseInputs []map[string]any
}

func (f *fakePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	f.calls = append(f.calls, app+"."+tool)
	var payload any
	switch tool {
	case "eval_suite_create":
		payload = evalSuite{ID: f.suiteID, Name: "suite"}
	case "eval_case_create":
		f.caseSeq++
		f.caseInputs = append(f.caseInputs, input)
		payload = evalCase{ID: fmt.Sprintf("case-%d", f.caseSeq), SuiteID: f.suiteID}
	case "eval_experiment_create":
		payload = evalExperiment{ID: f.experiment.ID, SuiteID: f.suiteID, Status: "queued"}
	case "eval_experiment_get":
		payload = f.experiment
	case "eval_experiment_cancel":
		payload = map[string]bool{"ok": true}
	case "environment_catalog", "environment_list", "environment_snapshot_list":
		payload = map[string]any{}
	default:
		return fmt.Errorf("unexpected call %s.%s", app, tool)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newTestService(t *testing.T, platform sdk.PlatformClient) (*service, *testkit.EmitRecorder) {
	t.Helper()
	recorder := testkit.NewEmitRecorder()
	ctx := testkit.NewAppCtx(t, "apteva.yaml",
		testkit.WithPlatform(platform), testkit.WithEmitter(recorder))
	return &service{ctx: ctx, db: store{db: ctx.AppDB()}}, recorder
}

func execution(durationMS int64, turns, tokensIn, tokensOut, toolErrors int, cost float64) *sdk.RuntimeAgentExecution {
	start := time.Now().UTC().Add(-time.Duration(durationMS) * time.Millisecond)
	return &sdk.RuntimeAgentExecution{
		Status: "completed", Turns: turns, StartedAt: start,
		FinishedAt: start.Add(time.Duration(durationMS) * time.Millisecond),
		Metrics: sdk.RuntimeAgentMetrics{
			Provider: "openai-codex", Model: "gpt-5.5",
			TokensIn: tokensIn, TokensOut: tokensOut, CostUSD: cost,
			ToolCalls: 5, Errors: toolErrors,
		},
	}
}

func crmScenario() Scenario {
	return Scenario{
		ID: "crm-create-contact", Name: "Create then update a contact",
		Prompt: "Create a contact for Elena, then update her record.", EnvironmentID: "env-crm",
		Checks: []Check{{Name: "lifecycle", Type: "app", App: "crm", Tool: "crm_contact_get",
			Input: map[string]any{"email": "elena@example.com"}, Path: "lifecycle", Equals: "prospect"}},
		Budget:   Budget{DurationMS: 75_000, CostUSD: 0.75, TokensTotal: 60_000, Turns: 8},
		MaxTurns: 8, Weight: 1,
	}
}

// ---- scoring contract ----

func TestVerifiedV1AwardsFullMarksInsideEveryBudget(t *testing.T) {
	metrics := Metrics{DurationMS: 72_986, TurnsUsed: 6, TokensTotal: 58_627, CostUSD: 0, Errors: 0}
	score := computeScore(true, metrics, crmScenario().Budget)

	if score.Score != 100 {
		t.Fatalf("score = %v, want 100 (%+v)", score.Score, score)
	}
	if score.SuccessPoints != 70 || score.DurationPoints != 10 || score.CostPoints != 10 ||
		score.TurnPoints != 5 || score.ToolErrorPoints != 5 {
		t.Fatalf("unexpected breakdown: %+v", score)
	}
	// Cost was never reported, so efficiency must fall back to tokens and say so.
	if score.CostBasis != "tokens_total" {
		t.Fatalf("cost basis = %q, want tokens_total", score.CostBasis)
	}
}

func TestVerifiedV1DecaysLinearlyPastBudget(t *testing.T) {
	budget := crmScenario().Budget
	// Every metric at 1.5x budget: half of each efficiency allowance survives.
	metrics := Metrics{DurationMS: 112_500, TurnsUsed: 12, TokensTotal: 90_000, Errors: 2}
	score := computeScore(true, metrics, budget)

	if score.DurationPoints != 5 {
		t.Errorf("duration points = %v, want 5", score.DurationPoints)
	}
	if score.CostPoints != 5 {
		t.Errorf("cost points = %v, want 5", score.CostPoints)
	}
	if score.TurnPoints != 2.5 {
		t.Errorf("turn points = %v, want 2.5", score.TurnPoints)
	}
	if score.ToolErrorPoints != 0 {
		t.Errorf("tool error points = %v, want 0 with 2 errors", score.ToolErrorPoints)
	}
	if score.Score != 82.5 {
		t.Fatalf("score = %v, want 82.5", score.Score)
	}
}

func TestVerifiedV1ZeroesEfficiencyAtTwiceBudget(t *testing.T) {
	budget := crmScenario().Budget
	metrics := Metrics{DurationMS: 150_000, TurnsUsed: 16, TokensTotal: 120_000, Errors: 0}
	score := computeScore(true, metrics, budget)

	if score.DurationPoints != 0 || score.CostPoints != 0 || score.TurnPoints != 0 {
		t.Fatalf("efficiency should be exhausted at 2x budget: %+v", score)
	}
	// Success and a clean tool record still stand.
	if score.Score != 75 {
		t.Fatalf("score = %v, want 75", score.Score)
	}
}

func TestFailedRunScoresZeroRegardlessOfEfficiency(t *testing.T) {
	metrics := Metrics{DurationMS: 1_000, TurnsUsed: 1, TokensTotal: 100, Errors: 0}
	score := computeScore(false, metrics, crmScenario().Budget)
	if score.Score != 0 {
		t.Fatalf("score = %v, want 0 for a failed run", score.Score)
	}
	if score.SuccessPoints != 0 || score.DurationPoints != 0 {
		t.Fatalf("a failed run must earn nothing: %+v", score)
	}
}

func TestCostBasisPrefersRealCostWhenReported(t *testing.T) {
	budget := crmScenario().Budget
	score := computeScore(true, Metrics{DurationMS: 1_000, TurnsUsed: 1, TokensTotal: 1_000, CostUSD: 0.5}, budget)
	if score.CostBasis != "cost_usd" {
		t.Fatalf("cost basis = %q, want cost_usd", score.CostBasis)
	}
}

// ---- admission ----

func TestAdmissionWithholdsRunsWhereTheAgentNeverStarted(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	run := &Run{ID: "run-1", Targets: []Target{{Provider: "opencode-go", Model: "kimi-k3"}}}

	result := svc.scoreOne(run, crmScenario(), evalRun{
		ID: "eval-1", Status: "error", Execution: nil,
		Error: "provider configuration failed before the agent started",
	})

	if result.Admission != AdmissionInvalid {
		t.Fatalf("admission = %q, want invalid", result.Admission)
	}
	if result.Score.Score != 0 || result.Passed {
		t.Fatalf("an invalid result carries no score: %+v", result.Score)
	}
	if result.InvalidReason == "" {
		t.Fatal("an invalid result must say why it was withheld")
	}
}

func TestAdmissionScoresGenuineTaskFailuresAsZero(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	run := &Run{ID: "run-1", Targets: []Target{{Provider: "openai-codex", Model: "gpt-5.5"}}}

	// The agent ran to completion and simply got the task wrong. That is
	// evidence about the target, so it is admitted and scores zero.
	result := svc.scoreOne(run, crmScenario(), evalRun{
		ID: "eval-2", Status: "fail", Execution: execution(40_000, 5, 10_000, 5_000, 0, 0.2),
	})

	if result.Admission != AdmissionVerified {
		t.Fatalf("admission = %q, want verified", result.Admission)
	}
	if result.Passed || result.Score.Score != 0 {
		t.Fatalf("a real task failure scores zero: passed=%v score=%v", result.Passed, result.Score.Score)
	}
}

func TestAdmissionMarksChecklessScenariosDiagnostic(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	scenario := crmScenario()
	scenario.Checks = nil

	result := svc.scoreOne(&Run{ID: "run-1"}, scenario, evalRun{
		ID: "eval-3", Status: "pass", Execution: execution(10_000, 2, 1_000, 500, 0, 0.1),
	})

	if result.Admission != AdmissionDiagnostic {
		t.Fatalf("admission = %q, want diagnostic without deterministic checks", result.Admission)
	}
}

func TestInvalidResultsDoNotMovePassRate(t *testing.T) {
	targets := []Target{{Provider: "openai-codex", Model: "gpt-5.5"}}
	results := []Result{
		{TargetIndex: 0, Target: targets[0], Admission: AdmissionVerified, Passed: true, Score: Score{Score: 100}},
		{TargetIndex: 0, Target: targets[0], Admission: AdmissionInvalid},
		{TargetIndex: 0, Target: targets[0], Admission: AdmissionInvalid},
	}
	summary := summarize(results, targets)

	if summary.Total != 3 || summary.Verified != 1 || summary.Invalid != 2 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	// Two harness failures must not turn a perfect record into a third of one.
	if summary.PassRate != 1 {
		t.Fatalf("pass rate = %v, want 1", summary.PassRate)
	}
	if summary.AverageScore != 100 {
		t.Fatalf("average score = %v, want 100", summary.AverageScore)
	}
}

// ---- sealing ----

func TestSealDigestIgnoresScenarioAuthoringOrder(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	first := crmScenario()
	second := crmScenario()
	second.ID, second.Name = "zz-second", "Second"

	packA, err := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{first, second}}, true)
	if err != nil {
		t.Fatal(err)
	}
	packB, err := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{second, first}}, true)
	if err != nil {
		t.Fatal(err)
	}

	sealedA, err := svc.seal(packA.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	sealedB, err := svc.seal(packB.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if sealedA.Digest != sealedB.Digest {
		t.Fatalf("same content hashed differently:\n%s\n%s", sealedA.Digest, sealedB.Digest)
	}
	// Identical content is the same benchmark, so it must resolve to one row.
	if sealedA.ID != sealedB.ID {
		t.Fatalf("identical packs sealed to two rows: %s vs %s", sealedA.ID, sealedB.ID)
	}
}

func TestSealedPacksRejectEdits(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, err := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.savePack(&Pack{ID: sealed.ID, Name: "Renamed"}, false); err == nil {
		t.Fatal("expected a sealed pack to refuse renaming")
	}
	if _, err := svc.putScenario(sealed.ID, crmScenario()); err == nil {
		t.Fatal("expected a sealed pack to refuse scenario edits")
	}
	// The draft it came from stays editable — that is where the next version
	// is authored.
	if _, err := svc.putScenario(draft.ID, crmScenario()); err != nil {
		t.Fatalf("draft should remain editable: %v", err)
	}
}

func TestSealRefusesScenariosThatCannotBeReproducedOrScored(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})

	unpinned := crmScenario()
	unpinned.EnvironmentID, unpinned.SnapshotID = "", ""
	draft, err := svc.savePack(&Pack{Name: "Unpinned", Scenarios: []Scenario{unpinned}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.seal(draft.ID, ""); err == nil {
		t.Fatal("expected seal to refuse a scenario with no pinned world")
	}

	unbudgeted := crmScenario()
	unbudgeted.Budget = Budget{}
	draft2, err := svc.savePack(&Pack{Name: "Unbudgeted", Scenarios: []Scenario{unbudgeted}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.seal(draft2.ID, ""); err == nil {
		t.Fatal("expected seal to refuse a scenario with no budgets")
	}
}

func TestRunsRequireASealedPack(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, err := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.createRun(draft.ID, "", []Target{{Model: "gpt-5.5"}}, 1); err == nil {
		t.Fatal("expected a draft pack to be unrunnable")
	}
}

// ---- lifecycle ----

func TestRunDelegatesToEvalsThenScoresWhatComesBack(t *testing.T) {
	platform := &fakePlatform{suiteID: "suite-1", experiment: evalExperiment{ID: "exp-1"}}
	svc, recorder := newTestService(t, platform)

	draft, err := svc.savePack(&Pack{Name: "Apteva Core", Scenarios: []Scenario{crmScenario()}}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	targets := []Target{{AgentID: 1, Provider: "openai-codex", Model: "gpt-5.5"}}
	run, err := svc.createRun(sealed.ID, "", targets, 2)
	if err != nil {
		t.Fatal(err)
	}
	if run.Provenance.PackDigest != sealed.Digest || run.Provenance.ScoringVersion != ScoringVersion {
		t.Fatalf("provenance did not pin the sealed definition: %+v", run.Provenance)
	}
	// Snapshots are not content-addressed yet; the bundle must not imply they are.
	if run.Provenance.SnapshotsVerified {
		t.Fatal("SnapshotsVerified must stay false until Environments can digest a snapshot")
	}

	// First tick materializes the suite and queues the experiment.
	if err := svc.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, err := svc.db.getRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != RunStatusRunning || started.ExperimentID != "exp-1" {
		t.Fatalf("run did not start: status=%s experiment=%s", started.Status, started.ExperimentID)
	}
	if len(platform.caseInputs) != 1 {
		t.Fatalf("expected one Evals case per scenario, got %d", len(platform.caseInputs))
	}
	if got := platform.caseInputs[0]["environment_id"]; got != "env-crm" {
		t.Fatalf("case did not carry the scenario environment: %v", got)
	}

	// Evals finishes: one clean pass, one harness failure.
	platform.experiment = evalExperiment{
		ID: "exp-1", SuiteID: "suite-1", Status: "completed",
		Runs: []evalRun{
			{ID: "eval-1", CaseID: "case-1", TargetIndex: 0, Repetition: 1, Status: "pass",
				TargetSnap: targets[0], Execution: execution(72_986, 6, 40_000, 18_627, 0, 0)},
			{ID: "eval-2", CaseID: "case-1", TargetIndex: 0, Repetition: 2, Status: "error",
				TargetSnap: targets[0], Error: "environment seed failed"},
		},
	}

	if err := svc.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished, err := svc.db.getRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != RunStatusCompleted {
		t.Fatalf("status = %s, want completed (%s)", finished.Status, finished.Error)
	}
	if len(finished.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(finished.Results))
	}
	if finished.Summary.Verified != 1 || finished.Summary.Invalid != 1 {
		t.Fatalf("unexpected admission split: %+v", finished.Summary)
	}
	// The seeding failure is not evidence about gpt-5.5, so the pass rate holds.
	if finished.Summary.PassRate != 1 {
		t.Fatalf("pass rate = %v, want 1", finished.Summary.PassRate)
	}
	if finished.Summary.AverageScore != 100 {
		t.Fatalf("average score = %v, want 100", finished.Summary.AverageScore)
	}
	if len(recorder.EventsByTopic("bench.result.rejected")) != 1 {
		t.Fatal("withholding a result should be announced")
	}
	if len(recorder.EventsByTopic("bench.run.completed")) != 1 {
		t.Fatal("expected a completion event")
	}
}

func TestSuiteIsMaterializedOncePerDigest(t *testing.T) {
	platform := &fakePlatform{suiteID: "suite-1", experiment: evalExperiment{ID: "exp-1"}}
	svc, _ := newTestService(t, platform)

	draft, _ := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.createRun(sealed.ID, "", []Target{{Model: "gpt-5.5"}}, 1); err != nil {
			t.Fatal(err)
		}
		if err := svc.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	suiteCreates := 0
	for _, call := range platform.calls {
		if call == "evals.eval_suite_create" {
			suiteCreates++
		}
	}
	if suiteCreates != 1 {
		t.Fatalf("suite created %d times; one per sealed digest is enough", suiteCreates)
	}
}

func TestLeaderboardRanksByPassRateAcrossRuns(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, _ := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	strong := Target{Provider: "openai-codex", Model: "gpt-5.5"}
	weak := Target{Provider: "opencode-go", Model: "kimi-k3"}
	run, err := svc.createRun(sealed.ID, "", []Target{strong, weak}, 2)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunStatusCompleted
	if err := svc.db.saveRun(run); err != nil {
		t.Fatal(err)
	}

	seed := []Result{
		{TargetIndex: 0, Target: strong, Passed: true, Admission: AdmissionVerified, Score: Score{Score: 100, CostBasis: "cost_usd"}},
		{TargetIndex: 0, Target: strong, Passed: true, Admission: AdmissionVerified, Score: Score{Score: 90, CostBasis: "cost_usd"}},
		{TargetIndex: 1, Target: weak, Passed: false, Admission: AdmissionVerified, Score: Score{Score: 0, CostBasis: "tokens_total"}},
		{TargetIndex: 1, Target: weak, Passed: true, Admission: AdmissionVerified, Score: Score{Score: 80, CostBasis: "cost_usd"}},
		// An invalid row must be excluded from the board entirely.
		{TargetIndex: 1, Target: weak, Admission: AdmissionInvalid},
	}
	for i := range seed {
		seed[i].ID, seed[i].BenchRunID = newID(fmt.Sprintf("result%d", i)), run.ID
		seed[i].ScenarioID, seed[i].Trial, seed[i].CreatedAt = "crm-create-contact", i+1, time.Now().UTC()
		if err := svc.db.saveResult(&seed[i]); err != nil {
			t.Fatal(err)
		}
	}

	board, err := svc.leaderboard(sealed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	rows := board["rows"].([]leaderboardRow)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Label != "openai-codex/gpt-5.5" {
		t.Fatalf("leader = %q, want openai-codex/gpt-5.5", rows[0].Label)
	}
	if rows[0].PassRate != 1 || rows[1].PassRate != 0.5 {
		t.Fatalf("pass rates = %v, %v", rows[0].PassRate, rows[1].PassRate)
	}
	if rows[1].Runs != 2 {
		t.Fatalf("invalid rows must not reach the board: runs = %d", rows[1].Runs)
	}
	// gpt-5.5 priced every run; kimi-k3 fell back to tokens once, which makes
	// its cost column not comparable with the leader's.
	if rows[0].MixedCostBasis {
		t.Error("leader should not be flagged as mixed basis")
	}
	if !rows[1].MixedCostBasis {
		t.Error("expected a mixed cost basis flag on the runner-up")
	}
}

func TestBaselineComparisonDetectsRegressionAndRecord(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, _ := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Provider: "openai-codex", Model: "gpt-5.5"}

	baselineRun, _ := svc.createRun(sealed.ID, "baseline", []Target{target}, 1)
	baselineRun.Status = RunStatusCompleted
	_ = svc.db.saveRun(baselineRun)
	first := Result{ID: newID("r"), BenchRunID: baselineRun.ID, ScenarioID: "crm-create-contact",
		Target: target, Trial: 1, Passed: true, Admission: AdmissionVerified,
		Score: Score{Score: 80}, CreatedAt: time.Now().UTC()}
	if err := svc.db.saveResult(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setBaseline(baselineRun.ID, "crm-create-contact", 0, "gpt-5.5 @ 1.0.0"); err != nil {
		t.Fatal(err)
	}

	laterRun, _ := svc.createRun(sealed.ID, "later", []Target{target}, 1)
	laterRun.Status = RunStatusCompleted
	_ = svc.db.saveRun(laterRun)
	second := Result{ID: newID("r"), BenchRunID: laterRun.ID, ScenarioID: "crm-create-contact",
		Target: target, Trial: 1, Passed: true, Admission: AdmissionVerified,
		Score: Score{Score: 95}, CreatedAt: time.Now().UTC()}
	if err := svc.db.saveResult(&second); err != nil {
		t.Fatal(err)
	}

	comparison, err := svc.compareToBaselines(laterRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	deltas := comparison["deltas"].([]baselineDelta)
	if len(deltas) != 1 {
		t.Fatalf("expected one delta, got %d", len(deltas))
	}
	if deltas[0].Verdict != "ahead" || deltas[0].ScoreDelta != 15 {
		t.Fatalf("unexpected delta: %+v", deltas[0])
	}
}

func TestEvidenceBundleStatesTheSnapshotCaveat(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, _ := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.createRun(sealed.ID, "", []Target{{Model: "gpt-5.5"}}, 1)
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := svc.evidence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	caveats, ok := bundle["caveats"].([]string)
	if !ok || len(caveats) == 0 {
		t.Fatal("an unverifiable world must be disclosed in the evidence bundle")
	}
	if bundle["pack"] == nil {
		t.Fatal("the bundle must carry the sealed definition it scored against")
	}
}

// ---- manifest consistency ----

func TestManifestToolsMatchImplementation(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()

	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
		implemented[tool.Name] = true
	}

	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but the app does not implement it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("app implements %q but the manifest does not declare it", name)
		}
	}
}

func TestManifestDeclaresItsDependenciesAndPermissions(t *testing.T) {
	manifest := (&App{}).Manifest()

	// Bench delegates execution rather than running agents itself, so the
	// app-to-app call permission and both dependencies are load-bearing.
	permissions := map[string]bool{}
	for _, permission := range manifest.Requires.Permissions {
		permissions[string(permission)] = true
	}
	if !permissions["platform.apps.call"] {
		t.Error("bench cannot reach evals or environments without platform.apps.call")
	}

	deps := map[string]bool{}
	for _, dep := range manifest.Requires.Apps {
		deps[dep.Name] = true
	}
	for _, required := range []string{"evals", "environments"} {
		if !deps[required] {
			t.Errorf("manifest does not require %q", required)
		}
	}
}

func TestChecksMapOntoEnvironmentsAssertionTypes(t *testing.T) {
	// Environments errors on an unrecognised assertion type, so an untyped
	// check — what the panel produces by default — must not reach it as
	// bench's own shorthand.
	assertions := checksToAssertions([]Check{
		{Name: "untyped", App: "crm", Tool: "crm_contact_get"},
		{Name: "shorthand", Type: "app", App: "crm"},
		{Name: "mcp shorthand", Type: "mcp", MCP: "server"},
		{Name: "explicit", Type: "telemetry"},
	})
	want := []string{"app_state", "app_state", "mcp_state", "telemetry"}
	for i, expected := range want {
		if got := assertions[i]["type"]; got != expected {
			t.Errorf("assertion %d type = %v, want %q", i, got, expected)
		}
	}
}

// ---- leaderboards ----

func seedResult(t *testing.T, svc *service, runID, scenarioID string, target Target, passed bool, score Score, m Metrics) {
	t.Helper()
	r := Result{
		ID: newID("r"), BenchRunID: runID, ScenarioID: scenarioID, ScenarioName: scenarioID,
		Target: target, Trial: 1, Passed: passed, Admission: AdmissionVerified,
		Score: score, Metrics: m, CreatedAt: time.Now().UTC(),
	}
	if err := svc.db.saveResult(&r); err != nil {
		t.Fatal(err)
	}
}

func sealedPackWithRun(t *testing.T, svc *service, name string, target Target) (*Pack, *Run) {
	t.Helper()
	scenario := crmScenario()
	scenario.ID, scenario.Name = "sc-"+slugify(name), name
	draft, err := svc.savePack(&Pack{Name: name, Scenarios: []Scenario{scenario}}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.createRun(sealed.ID, "", []Target{target}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunStatusCompleted
	if err := svc.db.saveRun(run); err != nil {
		t.Fatal(err)
	}
	return sealed, run
}

func TestLeaderboardReportsWhereThePointsWent(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	target := Target{Provider: "opencode-go", Model: "kimi-k3"}
	sealed, run := sealedPackWithRun(t, svc, "Core", target)

	// Two passes that differ only in the cost component — the real signature of
	// a model that is correct but verbose.
	seedResult(t, svc, run.ID, sealed.Scenarios[0].ID, target, true,
		Score{Score: 90, SuccessPoints: 70, DurationPoints: 10, CostPoints: 0, TurnPoints: 5, ToolErrorPoints: 5, CostBasis: "tokens_total"},
		Metrics{DurationMS: 50_000, TokensTotal: 120_000})
	seedResult(t, svc, run.ID, sealed.Scenarios[0].ID, target, true,
		Score{Score: 95, SuccessPoints: 70, DurationPoints: 10, CostPoints: 5, TurnPoints: 5, ToolErrorPoints: 5, CostBasis: "tokens_total"},
		Metrics{DurationMS: 44_000, TokensTotal: 90_000})

	board, err := svc.leaderboard(sealed.Digest)
	if err != nil {
		t.Fatal(err)
	}
	rows := board["rows"].([]leaderboardRow)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	c := rows[0].Components
	if c.Success != 70 || c.Duration != 10 || c.Turns != 5 || c.ToolErrors != 5 {
		t.Fatalf("unexpected components: %+v", c)
	}
	// The averaged cost component is what shows the budget is the binding
	// constraint rather than correctness.
	if c.Cost != 2.5 {
		t.Fatalf("cost component = %v, want 2.5", c.Cost)
	}
	if rows[0].Packs != 1 || rows[0].Scenarios != 1 {
		t.Fatalf("coverage = %d pack(s), %d scenario(s)", rows[0].Packs, rows[0].Scenarios)
	}
	if len(board["by_scenario"].([]scenarioRow)) != 1 {
		t.Fatal("expected a per-scenario breakdown")
	}
}

func TestGlobalLeaderboardFlagsUnequalCoverage(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	wide := Target{Provider: "opencode-go", Model: "kimi-k3"}
	narrow := Target{Provider: "opencode-go", Model: "deepseek-flash"}

	packA, runA := sealedPackWithRun(t, svc, "Core", wide)
	packB, runB := sealedPackWithRun(t, svc, "Extras", wide)
	full := Score{Score: 90, SuccessPoints: 70, DurationPoints: 10, TurnPoints: 5, ToolErrorPoints: 5, CostBasis: "cost_usd"}
	seedResult(t, svc, runA.ID, packA.Scenarios[0].ID, wide, true, full, Metrics{DurationMS: 1000})
	seedResult(t, svc, runB.ID, packB.Scenarios[0].ID, wide, true, full, Metrics{DurationMS: 1000})
	// deepseek only ever faced one of the two packs.
	seedResult(t, svc, runA.ID, packA.Scenarios[0].ID, narrow, true, full, Metrics{DurationMS: 1000})

	board, err := svc.globalLeaderboard("")
	if err != nil {
		t.Fatal(err)
	}
	if comparable := board["comparable"].(bool); comparable {
		t.Fatal("targets covering different packs must not be reported as comparable")
	}
	rows := board["rows"].([]leaderboardRow)
	byLabel := map[string]leaderboardRow{}
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	if byLabel["opencode-go/kimi-k3"].Packs != 2 {
		t.Errorf("kimi-k3 packs = %d, want 2", byLabel["opencode-go/kimi-k3"].Packs)
	}
	if byLabel["opencode-go/deepseek-flash"].Packs != 1 {
		t.Errorf("deepseek-flash packs = %d, want 1", byLabel["opencode-go/deepseek-flash"].Packs)
	}
	if len(board["packs"].([]map[string]any)) != 2 {
		t.Error("expected both sealed packs listed")
	}
}

func TestGlobalLeaderboardIsComparableWhenCoverageMatches(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	a := Target{Provider: "opencode-go", Model: "kimi-k3"}
	b := Target{Provider: "opencode-go", Model: "deepseek-flash"}
	pack, run := sealedPackWithRun(t, svc, "Core", a)
	full := Score{Score: 90, SuccessPoints: 70, CostBasis: "cost_usd"}
	seedResult(t, svc, run.ID, pack.Scenarios[0].ID, a, true, full, Metrics{DurationMS: 1000})
	seedResult(t, svc, run.ID, pack.Scenarios[0].ID, b, false, Score{CostBasis: "cost_usd"}, Metrics{DurationMS: 1000})

	board, err := svc.globalLeaderboard("")
	if err != nil {
		t.Fatal(err)
	}
	if !board["comparable"].(bool) {
		t.Fatal("equal coverage should be reported as comparable")
	}
	rows := board["rows"].([]leaderboardRow)
	if rows[0].Label != "opencode-go/kimi-k3" {
		t.Fatalf("leader = %q, want kimi-k3 on pass rate", rows[0].Label)
	}
}
