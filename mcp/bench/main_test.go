package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	suiteInput map[string]any
	caseInputs []map[string]any
}

func (f *fakePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	f.calls = append(f.calls, app+"."+tool)
	var payload any
	switch tool {
	case "eval_suite_create":
		f.suiteInput = input
		payload = evalSuite{ID: f.suiteID, Name: "suite"}
	case "eval_catalog":
		payload = map[string]any{"models": []map[string]any{
			{"provider": "openai-codex", "model_id": "gpt-5.6-sol", "gateway_model": "openai-codex/gpt-5.6-sol"},
		}}
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

func TestJudgedPackPinsCodexModelAndPreservesVerdictEvidence(t *testing.T) {
	platform := &fakePlatform{suiteID: "suite-judge"}
	svc, _ := newTestService(t, platform)
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	scenario := crmScenario()
	scenario.Goals = []string{"Create and update the requested contact correctly"}
	draft, err := svc.savePack(&Pack{
		Name: "Coding judged", JudgeModel: "gpt-5.6-sol", Scenarios: []Scenario{scenario},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.JudgeModel != "openai-codex/gpt-5.6-sol" || sealed.ScoringVersion != judgedV1().Version {
		t.Fatalf("sealed judge/profile = %q / %q", sealed.JudgeModel, sealed.ScoringVersion)
	}
	run, err := svc.createRun(sealed.ID, "", []Target{{AgentID: 1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.JudgeModel != sealed.JudgeModel || run.Provenance.JudgePrompt != JudgePromptVersion || run.Provenance.JudgeRubric != JudgeRubricVersion {
		t.Fatalf("run lost judge provenance: %+v", run)
	}
	if _, err := svc.ensureSuite(sealed); err != nil {
		t.Fatal(err)
	}
	if platform.suiteInput["judge_model"] != "openai-codex/gpt-5.6-sol" {
		t.Fatalf("suite judge = %#v", platform.suiteInput["judge_model"])
	}
	judgeScore := 90.0
	verdict := &JudgeVerdict{
		Passed: true, Score: judgeScore, Model: "gpt-5.6-sol",
		Usage:               map[string]any{"tokens_in": float64(123)},
		DirectiveSuggestion: &DirectiveSuggestion{Directive: "Always verify the saved record.", Reason: "Prevents incomplete updates."},
		PromptVersion:       JudgePromptVersion, RubricVersion: JudgeRubricVersion,
	}
	profile, err := svc.resolveProfile(sealed.ProfileDigest)
	if err != nil {
		t.Fatal(err)
	}
	result := svc.scoreOne(run, scenario, evalRun{
		ID: "eval-judge", Status: "pass", Execution: execution(1_000, 1, 100, 50, 0, 0.01),
		Judge: verdict, JudgeScore: &judgeScore,
	}, profile)
	if result.Score.JudgePoints != 27 || result.Evaluation.Judge == nil || result.Evaluation.Judge.Model != "gpt-5.6-sol" {
		t.Fatalf("judge scoring/evidence = %+v / %+v", result.Score, result.Evaluation)
	}
	if err := svc.db.saveResult(&result); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.db.getResult(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Evaluation.Judge == nil || stored.Evaluation.Judge.DirectiveSuggestion == nil ||
		stored.Evaluation.Judge.DirectiveSuggestion.Directive != "Always verify the saved record." ||
		stored.Evaluation.Judge.PromptVersion != JudgePromptVersion || stored.Evaluation.Judge.RubricVersion != JudgeRubricVersion ||
		stored.Evaluation.Judge.Usage["tokens_in"] != float64(123) {
		t.Fatalf("stored judge evidence = %#v", stored)
	}
}

func TestSealRejectsUnavailableJudgeModel(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	scenario := crmScenario()
	scenario.Goals = []string{"Complete the requested contact operation"}
	draft, err := svc.savePack(&Pack{Name: "Unavailable judge", JudgeModel: "missing-model", Scenarios: []Scenario{scenario}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.seal(draft.ID, "1.0.0"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("seal error = %v", err)
	}
}

func TestScoringProfilesAreSealedPinnedAndReproducible(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	profiles, err := svc.db.listProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != len(builtinProfiles()) {
		t.Fatalf("installed profiles = %d, want %d", len(profiles), len(builtinProfiles()))
	}

	draftProfile, err := svc.saveProfile(&Profile{
		Name: "Latency first", OnFailure: OnFailureZero,
		Components: []ProfileComponent{
			{Key: "success", Kind: KindGate, Weight: 80},
			{Key: "duration", Kind: KindBudget, Metric: "duration_ms", Curve: CurveRatio, Weight: 20},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealedProfile, err := svc.sealProfile(draftProfile.ID, "2026-09.latency-first")
	if err != nil {
		t.Fatal(err)
	}
	if sealedProfile.Digest == "" || sealedProfile.State != ProfileStateSealed {
		t.Fatalf("profile was not sealed: %+v", sealedProfile)
	}
	if _, err := svc.saveProfile(&Profile{ID: sealedProfile.ID, Name: "mutated"}, false); err == nil {
		t.Fatal("sealed scoring profiles must be immutable")
	}

	draftPack, err := svc.savePack(&Pack{
		Name: "Profile pin", ProfileDigest: sealedProfile.Digest, Scenarios: []Scenario{crmScenario()},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealedPack, err := svc.seal(draftPack.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if sealedPack.ProfileDigest != sealedProfile.Digest || sealedPack.ScoringVersion != sealedProfile.Version {
		t.Fatalf("pack did not pin the selected profile: %+v", sealedPack)
	}
	run, err := svc.createRun(sealedPack.ID, "", []Target{{AgentID: 1, Model: "gpt-5.5"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.ScoringProfileDigest != sealedProfile.Digest {
		t.Fatalf("run profile = %q, want %q", run.ScoringProfileDigest, sealedProfile.Digest)
	}
	score := scoreWithProfile(true, Metrics{DurationMS: 150_000}, crmScenario().Budget, sealedProfile)
	if score.Score != 90 || score.ProfileDigest != sealedProfile.Digest {
		t.Fatalf("custom profile score is not reproducible: %+v", score)
	}
	bundle, err := svc.evidence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle["scoring_profile"].(*Profile).Digest != sealedProfile.Digest {
		t.Fatal("evidence did not carry the pinned custom profile")
	}
	if bundle["scoring_formula"] != nil || bundle["scoring_weights"] != nil {
		t.Fatal("custom-profile evidence must not claim the legacy verified-v1 contract")
	}
}

// ---- admission ----

func TestAdmissionWithholdsRunsWhereTheAgentNeverStarted(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	run := &Run{ID: "run-1", Targets: []Target{{Provider: "opencode-go", Model: "kimi-k3"}}}

	result := svc.scoreOne(run, crmScenario(), evalRun{
		ID: "eval-1", Status: "error", Execution: nil,
		Error: "provider configuration failed before the agent started",
	}, verifiedV1())

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
	}, verifiedV1())

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
	}, verifiedV1())

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

func TestCategoriesAndTagsAreNormalizedSealedAndSnapshotted(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	scenario := crmScenario()
	scenario.Tags = []string{"TypeScript", "bug fix", "typescript", "  Backend  "}
	draft, err := svc.savePack(&Pack{
		Name: "Coding Core", Category: " Coding & Software ", Scenarios: []Scenario{scenario},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Category != "coding-software" {
		t.Fatalf("category = %q, want coding-software", draft.Category)
	}
	draft, err = svc.savePack(&Pack{ID: draft.ID, Name: draft.Name, Description: "updated by an older client"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Category != "coding-software" {
		t.Fatal("an update that omitted the v0.6 category erased it")
	}
	replacement := draft.Scenarios[0]
	replacement.Tags = nil
	draft, err = svc.putScenario(draft.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Scenarios[0].Tags) != 3 {
		t.Fatal("an older scenario update that omitted tags erased them")
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	wantTags := []string{"backend", "bug-fix", "typescript"}
	if got := sealed.Scenarios[0].Tags; fmt.Sprint(got) != fmt.Sprint(wantTags) {
		t.Fatalf("tags = %#v, want %#v", got, wantTags)
	}

	run, err := svc.createRun(sealed.ID, "", []Target{{AgentID: 1}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if run.PackCategory != "coding-software" || run.Provenance.Category != "coding-software" {
		t.Fatalf("run did not snapshot category: %+v", run)
	}
	if got := run.Provenance.ScenarioTags[scenario.ID]; fmt.Sprint(got) != fmt.Sprint(wantTags) {
		t.Fatalf("provenance tags = %#v, want %#v", got, wantTags)
	}
	result := svc.scoreOne(run, sealed.Scenarios[0], evalRun{
		ID: "eval-1", TargetIndex: 0, Repetition: 1, Status: "pass", Execution: execution(1000, 1, 20, 10, 0, 0.01),
	}, verifiedV1())
	if err := svc.db.saveResult(&result); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.db.getResult(result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored.ScenarioTags) != fmt.Sprint(wantTags) {
		t.Fatalf("stored result tags = %#v, want %#v", stored.ScenarioTags, wantTags)
	}

	forked, err := svc.fork(sealed.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if forked.Category != sealed.Category || forked.ProfileDigest != sealed.ProfileDigest {
		t.Fatalf("fork lost sealed metadata: %+v", forked)
	}
}

func TestEmptyTaxonomyPreservesLegacyPackDigest(t *testing.T) {
	pack := &Pack{
		Name: "Legacy", Description: "Existing definition", ScoringVersion: ScoringVersion,
		Scenarios: []Scenario{crmScenario()},
	}
	legacy, err := canonicalDigest(struct {
		Name           string     `json:"name"`
		Description    string     `json:"description"`
		ScoringVersion string     `json:"scoring_version"`
		Scenarios      []Scenario `json:"scenarios"`
	}{pack.Name, pack.Description, pack.ScoringVersion, normalizeScenarios(pack.Scenarios)})
	if err != nil {
		t.Fatal(err)
	}
	current, err := packDigest(pack)
	if err != nil {
		t.Fatal(err)
	}
	if current != legacy {
		t.Fatalf("empty taxonomy changed the legacy digest: current=%s legacy=%s", current, legacy)
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
	if _, err := svc.createRun(draft.ID, "", []Target{{AgentID: 1, Model: "gpt-5.5"}}, 1); err == nil {
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
		if _, err := svc.createRun(sealed.ID, "", []Target{{AgentID: 1, Model: "gpt-5.5"}}, 1); err != nil {
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

	strong := Target{AgentID: 1, Provider: "openai-codex", Model: "gpt-5.5"}
	weak := Target{AgentID: 2, Provider: "opencode-go", Model: "kimi-k3"}
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

func TestMultipleHiddenSetupsStayDistinctOnTheSameModel(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, _ := svc.savePack(&Pack{Name: "Draft candidates", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	targets := []Target{
		{Draft: &sdk.RuntimeAgentDraft{Name: "Concise", Directive: "Use tools and answer concisely."}, Model: "openai/gpt-test"},
		{Draft: &sdk.RuntimeAgentDraft{Name: "Thorough", Directive: "Use tools and explain the result thoroughly."}, Model: "openai/gpt-test"},
	}
	run, err := svc.createRun(sealed.ID, "Compare hidden setups", targets, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Targets) != 2 || run.Targets[0].Draft.Mode != "autonomous" || run.Targets[0].Draft.Config != "{}" {
		t.Fatalf("normalized targets=%#v", run.Targets)
	}
	if run.Targets[0].key() == run.Targets[1].key() {
		t.Fatal("different directives collapsed to one target identity")
	}
	rows := aggregate([]resultWithPack{
		{Result: Result{Target: run.Targets[0], ScenarioID: "one", Passed: true, Score: Score{Score: 100}}},
		{Result: Result{Target: run.Targets[1], ScenarioID: "one", Passed: true, Score: Score{Score: 90}}},
	})
	if len(rows) != 2 || rows[0].Label != "Concise · openai/gpt-test" || rows[1].Label != "Thorough · openai/gpt-test" {
		t.Fatalf("leaderboard rows=%#v", rows)
	}
}

func TestBaselineComparisonDetectsRegressionAndRecord(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	draft, _ := svc.savePack(&Pack{Name: "Core", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target := Target{AgentID: 1, Provider: "openai-codex", Model: "gpt-5.5"}

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
	run, err := svc.createRun(sealed.ID, "", []Target{{AgentID: 1, Model: "gpt-5.5"}}, 1)
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

// ---- complete read surface ----

func TestDataExportIncludesEveryPersistedBenchRecordType(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	draft, err := svc.savePack(&Pack{Name: "Readable", Scenarios: []Scenario{crmScenario()}}, true)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target := Target{AgentID: 1, Provider: "openai-codex", Model: "gpt-5.5"}
	run, err := svc.createRun(sealed.ID, "export me", []Target{target}, 1)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunStatusCompleted
	if err := svc.db.saveRun(run); err != nil {
		t.Fatal(err)
	}
	result := Result{
		ID: newID("result"), BenchRunID: run.ID, ScenarioID: sealed.Scenarios[0].ID,
		ScenarioName: sealed.Scenarios[0].Name, Target: target, Passed: true,
		Admission: AdmissionVerified, Score: Score{Score: 100}, CreatedAt: time.Now().UTC(),
	}
	if err := svc.db.saveResult(&result); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.setBaseline(run.ID, result.ScenarioID, 0, "release baseline"); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.savePackSuite(sealed.Digest, packSuite{SuiteID: "suite-1", CaseMap: map[string]string{"case-1": result.ScenarioID}}); err != nil {
		t.Fatal(err)
	}

	exported, err := svc.dataExport()
	if err != nil {
		t.Fatal(err)
	}
	counts := exported["counts"].(map[string]int)
	for name, minimum := range map[string]int{
		"packs": 2, "profiles": 2, "runs": 1, "results": 1, "baselines": 1, "pack_suites": 1,
	} {
		if counts[name] < minimum {
			t.Errorf("export count %s = %d, want at least %d", name, counts[name], minimum)
		}
	}
	if exported["scoring"] == nil {
		t.Fatal("complete export omitted the scoring contract")
	}

	bundle, err := svc.evidence(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := bundle["scoring_profile"].(*Profile)
	if !ok || profile.Digest != run.ScoringProfileDigest {
		t.Fatalf("evidence did not include the exact scoring profile: %#v", bundle["scoring_profile"])
	}
}

func TestReadSearchesFilterAndPaginateWithoutDroppingResults(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	draft, _ := svc.savePack(&Pack{Name: "Searchable", Scenarios: []Scenario{crmScenario()}}, true)
	sealed, err := svc.seal(draft.ID, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	target := Target{AgentID: 1, Provider: "opencode-go", Model: "kimi-k3"}
	for i := 0; i < 3; i++ {
		run, err := svc.createRun(sealed.ID, fmt.Sprintf("run-%d", i), []Target{target}, 1)
		if err != nil {
			t.Fatal(err)
		}
		run.Status = RunStatusCompleted
		if err := svc.db.saveRun(run); err != nil {
			t.Fatal(err)
		}
		result := Result{
			ID: newID("result"), BenchRunID: run.ID, ScenarioID: sealed.Scenarios[0].ID,
			ScenarioName: sealed.Scenarios[0].Name, Target: target, Passed: i != 1,
			Admission: AdmissionVerified, CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if err := svc.db.saveResult(&result); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := svc.db.searchRuns(runQuery{PackDigest: sealed.Digest, Status: RunStatusCompleted, Limit: 1, Offset: 1, IncludeResults: true})
	if err != nil {
		t.Fatal(err)
	}
	if runs.Page.Total != 3 || !runs.Page.HasMore || len(runs.Runs) != 1 || len(runs.Runs[0].Results) != 1 {
		t.Fatalf("unexpected paginated runs: %+v", runs)
	}
	failed := false
	results, err := svc.db.searchResults(resultQuery{PackID: sealed.ID, Provider: target.Provider, Model: target.Model, Passed: &failed, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if results.Page.Total != 1 || len(results.Results) != 1 || results.Results[0].Passed {
		t.Fatalf("unexpected filtered results: %+v", results)
	}
}

func TestCategoryAndTagFiltersScopeDiscoveryResultsAndLeaderboards(t *testing.T) {
	svc, _ := newTestService(t, &fakePlatform{})
	if err := svc.ensureBuiltinProfiles(); err != nil {
		t.Fatal(err)
	}
	target := Target{AgentID: 1, Provider: "openai-codex", Model: "gpt-test"}
	makePack := func(name, category, tag string) (*Pack, *Run) {
		t.Helper()
		scenario := crmScenario()
		scenario.ID = slugify(name)
		scenario.Name = name
		scenario.Tags = []string{tag}
		draft, err := svc.savePack(&Pack{Name: name, Category: category, Scenarios: []Scenario{scenario}}, true)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := svc.seal(draft.ID, "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.createRun(sealed.ID, name+" run", []Target{target}, 1)
		if err != nil {
			t.Fatal(err)
		}
		run.Status = RunStatusCompleted
		if err := svc.db.saveRun(run); err != nil {
			t.Fatal(err)
		}
		result := Result{
			ID: newID("result"), BenchRunID: run.ID, ScenarioID: scenario.ID, ScenarioName: scenario.Name,
			ScenarioTags: normalizeTaxonomyValues(scenario.Tags), Target: target, Trial: 1, Passed: true,
			Admission: AdmissionVerified, Score: Score{Score: 100}, CreatedAt: time.Now().UTC(),
		}
		if err := svc.db.saveResult(&result); err != nil {
			t.Fatal(err)
		}
		return sealed, run
	}
	coding, _ := makePack("Coding Fix", "Coding", "Bug Fix")
	makePack("Research Sources", "Research", "Citation")

	runs, err := svc.db.searchRuns(runQuery{Category: "CODING"})
	if err != nil {
		t.Fatal(err)
	}
	if runs.Page.Total != 1 || runs.Runs[0].PackCategory != "coding" {
		t.Fatalf("category run filter returned %#v", runs)
	}
	results, err := svc.db.searchResults(resultQuery{Category: "coding", Tag: "bug fix"})
	if err != nil {
		t.Fatal(err)
	}
	if results.Page.Total != 1 || results.Results[0].ScenarioID != coding.Scenarios[0].ID {
		t.Fatalf("category/tag result filter returned %#v", results)
	}
	board, err := svc.globalLeaderboard("", "Coding")
	if err != nil {
		t.Fatal(err)
	}
	if board["category"] != "coding" || len(board["packs"].([]map[string]any)) != 1 {
		t.Fatalf("category leaderboard was not scoped: %#v", board)
	}

	app := &App{svc: svc}
	categories, err := app.toolListCategories(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := categories.([]categorySummary)
	if len(items) != 2 || items[0].Category != "coding" || fmt.Sprint(items[0].Tags) != "[bug-fix]" {
		t.Fatalf("category discovery returned %#v", items)
	}
}

func TestReadToolsUseExplicitDiscoverableSchemas(t *testing.T) {
	app := &App{}
	wanted := map[string]bool{
		"bench_run_search": false, "bench_result_list": false, "bench_result_get": false,
		"bench_baseline_list": false, "bench_baseline_get": false,
		"bench_profile_list": false, "bench_profile_get": false, "bench_scoring_get": false,
		"bench_pack_suite_list": false, "bench_data_export": false,
	}
	for _, tool := range app.MCPTools() {
		if _, ok := wanted[tool.Name]; !ok {
			continue
		}
		wanted[tool.Name] = true
		if additional, ok := tool.InputSchema["additionalProperties"].(bool); !ok || additional {
			t.Errorf("read tool %s does not reject undocumented inputs", tool.Name)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("missing read tool %s", name)
		}
	}
	for _, tool := range app.MCPTools() {
		if tool.Name == "bench_run_create" {
			properties := tool.InputSchema["properties"].(map[string]any)
			targets := properties["targets"].(map[string]any)
			target := targets["items"].(map[string]any)
			targetProperties := target["properties"].(map[string]any)
			if targetProperties["agent_id"] == nil || targetProperties["draft"] == nil {
				t.Fatalf("bench_run_create does not expose both target modes: %#v", target)
			}
			if choices, ok := target["oneOf"].([]any); !ok || len(choices) != 2 {
				t.Fatalf("bench_run_create does not require exactly one target mode: %#v", target)
			}
		}
		if tool.Name != "bench_leaderboard_global" {
			continue
		}
		properties := tool.InputSchema["properties"].(map[string]any)
		if properties["profile_digest"] == nil || properties["scoring_version"] != nil {
			t.Fatalf("global leaderboard schema must select the scoring contract by profile_digest: %#v", properties)
		}
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
	target := Target{AgentID: 1, Provider: "opencode-go", Model: "kimi-k3"}
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
	wide := Target{AgentID: 1, Provider: "opencode-go", Model: "kimi-k3"}
	narrow := Target{AgentID: 1, Provider: "opencode-go", Model: "deepseek-flash"}

	packA, runA := sealedPackWithRun(t, svc, "Core", wide)
	packB, runB := sealedPackWithRun(t, svc, "Extras", wide)
	full := Score{Score: 90, SuccessPoints: 70, DurationPoints: 10, TurnPoints: 5, ToolErrorPoints: 5, CostBasis: "cost_usd"}
	seedResult(t, svc, runA.ID, packA.Scenarios[0].ID, wide, true, full, Metrics{DurationMS: 1000})
	seedResult(t, svc, runB.ID, packB.Scenarios[0].ID, wide, true, full, Metrics{DurationMS: 1000})
	// deepseek only ever faced one of the two packs.
	seedResult(t, svc, runA.ID, packA.Scenarios[0].ID, narrow, true, full, Metrics{DurationMS: 1000})

	board, err := svc.globalLeaderboard("", "")
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
	a := Target{AgentID: 1, Provider: "opencode-go", Model: "kimi-k3"}
	b := Target{AgentID: 1, Provider: "opencode-go", Model: "deepseek-flash"}
	pack, run := sealedPackWithRun(t, svc, "Core", a)
	full := Score{Score: 90, SuccessPoints: 70, CostBasis: "cost_usd"}
	seedResult(t, svc, run.ID, pack.Scenarios[0].ID, a, true, full, Metrics{DurationMS: 1000})
	seedResult(t, svc, run.ID, pack.Scenarios[0].ID, b, false, Score{CostBasis: "cost_usd"}, Metrics{DurationMS: 1000})

	board, err := svc.globalLeaderboard("", "")
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
