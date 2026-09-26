package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tick advances the oldest run needing work. Execution itself belongs to Evals;
// this worker only starts experiments and scores what comes back.
func (s *service) tick(ctx context.Context) error {
	run, err := s.db.nextActiveRun()
	if err != nil || run == nil {
		return err
	}
	switch run.Status {
	case RunStatusQueued:
		return s.startRun(ctx, run)
	case RunStatusRunning:
		return s.collectRun(ctx, run)
	}
	return nil
}

func (s *service) createRun(packID, name string, targets []Target, trials int) (*Run, error) {
	pack, err := s.db.getPack(packID)
	if err != nil {
		return nil, err
	}
	if pack == nil {
		return nil, errors.New("pack not found")
	}
	// Only sealed packs run. A draft could change between two runs, which
	// would silently make their scores incomparable.
	if pack.State != PackStateSealed {
		return nil, errors.New("only a sealed pack can be run: seal it first to freeze its definition")
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one target is required")
	}
	if trials <= 0 {
		trials = 1
	}
	if name == "" {
		name = fmt.Sprintf("%s v%s · %d target(s) × %d trial(s)", pack.Name, pack.Version, len(targets), trials)
	}

	now := time.Now().UTC()
	run := &Run{
		ID: newID("run"), PackID: pack.ID, PackName: pack.Name, PackVersion: pack.Version,
		PackDigest: pack.Digest, ScoringVersion: pack.ScoringVersion, Name: name,
		Targets: targets, Trials: trials, Status: RunStatusQueued,
		Provenance: s.captureProvenance(pack), CreatedAt: now,
	}
	if err := s.db.saveRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

// captureProvenance records what a third party needs to reproduce this number.
// SnapshotsVerified stays false until Environments can content-address a
// snapshot: today bench can name the world it pinned but cannot prove two runs
// saw the same one.
func (s *service) captureProvenance(pack *Pack) Provenance {
	provenance := Provenance{
		ScoringVersion: pack.ScoringVersion, PackDigest: pack.Digest, PackVersion: pack.Version,
		ScenarioDigests: scenarioDigests(pack.Scenarios),
		SnapshotIDs:     map[string]string{}, EnvironmentIDs: map[string]string{},
		SnapshotsVerified: false, CapturedAt: time.Now().UTC(),
	}
	for _, scenario := range pack.Scenarios {
		if scenario.SnapshotID != "" {
			provenance.SnapshotIDs[scenario.ID] = scenario.SnapshotID
		}
		if scenario.EnvironmentID != "" {
			provenance.EnvironmentIDs[scenario.ID] = scenario.EnvironmentID
		}
	}
	if info, err := s.ctx.PlatformInfo(); err == nil && info != nil {
		provenance.PlatformVersion = info.Version
	}
	return provenance
}

func (s *service) startRun(_ context.Context, run *Run) error {
	pack, err := s.db.getPack(run.PackID)
	if err != nil {
		return err
	}
	if pack == nil {
		return s.failRun(run, errors.New("pack disappeared before the run started"))
	}

	suite, err := s.ensureSuite(pack)
	if err != nil {
		return s.failRun(run, fmt.Errorf("materialize suite: %w", err))
	}

	var experiment evalExperiment
	input := map[string]any{
		"suite_id":    suite.SuiteID,
		"name":        run.Name,
		"targets":     run.Targets,
		"repetitions": run.Trials,
	}
	if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_experiment_create", input, &experiment); err != nil {
		return s.failRun(run, fmt.Errorf("queue experiment: %w", err))
	}
	if experiment.ID == "" {
		return s.failRun(run, errors.New("evals returned no experiment id"))
	}

	now := time.Now().UTC()
	run.SuiteID, run.ExperimentID = suite.SuiteID, experiment.ID
	run.Status, run.StartedAt = RunStatusRunning, &now
	if err := s.db.saveRun(run); err != nil {
		return err
	}
	s.ctx.Emit("bench.run.started", map[string]any{
		"run_id": run.ID, "pack_digest": run.PackDigest, "pack_version": run.PackVersion,
		"experiment_id": experiment.ID, "targets": len(run.Targets), "trials": run.Trials,
	})
	return nil
}

// ensureSuite materializes one Evals suite per sealed digest and reuses it for
// every later run of that digest, so Evals does not accumulate a suite per run.
func (s *service) ensureSuite(pack *Pack) (*packSuite, error) {
	existing, err := s.db.getPackSuite(pack.Digest)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.SuiteID != "" {
		return existing, nil
	}

	var suite evalSuite
	suiteInput := map[string]any{
		"name":        fmt.Sprintf("bench: %s v%s", pack.Name, pack.Version),
		"description": fmt.Sprintf("Materialized by Bench from sealed pack %s (digest %s).", pack.ID, short(pack.Digest)),
	}
	if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_suite_create", suiteInput, &suite); err != nil {
		return nil, err
	}
	if suite.ID == "" {
		return nil, errors.New("evals returned no suite id")
	}

	created := packSuite{SuiteID: suite.ID, CaseMap: map[string]string{}}
	for _, scenario := range pack.Scenarios {
		var createdCase evalCase
		caseInput := map[string]any{
			"suite_id":        suite.ID,
			"name":            scenario.Name,
			"prompt":          scenario.Prompt,
			"goals":           scenario.Goals,
			"assertions":      checksToAssertions(scenario.Checks),
			"environment_id":  scenario.EnvironmentID,
			"weight":          scenario.Weight,
			"timeout_seconds": scenario.TimeoutSeconds,
			"max_turns":       scenario.MaxTurns,
			"enabled":         true,
		}
		if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_case_create", caseInput, &createdCase); err != nil {
			return nil, fmt.Errorf("scenario %q: %w", scenario.ID, err)
		}
		if createdCase.ID == "" {
			return nil, fmt.Errorf("scenario %q: evals returned no case id", scenario.ID)
		}
		created.CaseMap[createdCase.ID] = scenario.ID
	}

	if err := s.db.savePackSuite(pack.Digest, created); err != nil {
		return nil, err
	}
	return &created, nil
}

// checksToAssertions fans each bench check out to the Evals assertion shape.
// Evals compares a single path to a single value, so a check is already
// one-to-one with an assertion; the mapping exists to keep bench's vocabulary
// independent of the Evals schema.
func checksToAssertions(checks []Check) []map[string]any {
	assertions := make([]map[string]any, 0, len(checks))
	for _, check := range checks {
		// Environments names these types app_state / mcp_state and errors on
		// anything else, so a check authored without a type — which is the
		// common case from the panel — must land on app_state rather than on
		// bench's own shorthand.
		kind := check.Type
		switch kind {
		case "", "app":
			kind = "app_state"
		case "mcp":
			kind = "mcp_state"
		}
		assertion := map[string]any{"name": check.Name, "type": kind}
		if check.App != "" {
			assertion["app"] = check.App
		}
		if check.MCP != "" {
			assertion["mcp"] = check.MCP
		}
		if check.Tool != "" {
			assertion["tool"] = check.Tool
		}
		if check.Input != nil {
			assertion["input"] = check.Input
		}
		if check.Path != "" {
			assertion["path"] = check.Path
		}
		if check.Equals != nil {
			assertion["equals"] = check.Equals
		}
		assertions = append(assertions, assertion)
	}
	return assertions
}

func (s *service) collectRun(_ context.Context, run *Run) error {
	var experiment evalExperiment
	if err := s.ctx.PlatformAPI().CallAppResult("evals", "eval_experiment_get",
		map[string]any{"id": run.ExperimentID}, &experiment); err != nil {
		// A transient read failure is not a benchmark failure; retry next tick.
		s.ctx.Logger().Warn("bench: experiment read failed", "run_id", run.ID, "error", err)
		return nil
	}
	switch experiment.Status {
	case "queued", "running", "":
		return nil
	}

	pack, err := s.db.getPack(run.PackID)
	if err != nil {
		return err
	}
	if pack == nil {
		return s.failRun(run, errors.New("pack disappeared before scoring"))
	}
	suite, err := s.db.getPackSuite(run.PackDigest)
	if err != nil {
		return err
	}
	if suite == nil {
		return s.failRun(run, errors.New("suite mapping missing; cannot attribute runs to scenarios"))
	}

	results := make([]Result, 0, len(experiment.Runs))
	rejected := 0
	for _, evaluated := range experiment.Runs {
		scenarioID := suite.CaseMap[evaluated.CaseID]
		scenario := pack.scenario(scenarioID)
		if scenario == nil {
			// A case we cannot attribute is not evidence about anything.
			continue
		}
		result := s.scoreOne(run, *scenario, evaluated)
		if result.Admission == AdmissionInvalid {
			rejected++
			s.ctx.Emit("bench.result.rejected", map[string]any{
				"run_id": run.ID, "scenario_id": scenario.ID,
				"target": result.Target.label(), "reason": result.InvalidReason,
			})
		}
		if err := s.db.saveResult(&result); err != nil {
			return err
		}
		results = append(results, result)
	}

	now := time.Now().UTC()
	run.Summary = summarize(results, run.Targets)
	run.Status, run.FinishedAt = RunStatusCompleted, &now
	if experiment.Status == "failed" && len(results) == 0 {
		run.Status, run.Error = RunStatusFailed, experiment.Error
	}
	if err := s.db.saveRun(run); err != nil {
		return err
	}

	if run.Status == RunStatusFailed {
		s.ctx.Emit("bench.run.failed", map[string]any{"run_id": run.ID, "error": run.Error})
		return nil
	}
	s.ctx.Emit("bench.run.completed", map[string]any{
		"run_id": run.ID, "pack_digest": run.PackDigest, "pack_version": run.PackVersion,
		"scoring_version": run.ScoringVersion, "total": run.Summary.Total,
		"verified": run.Summary.Verified, "invalid": run.Summary.Invalid,
		"pass_rate": run.Summary.PassRate, "average_score": run.Summary.AverageScore,
	})
	s.emitBaselineMovement(run)
	return nil
}

// scoreOne turns one Evals run into a scored bench result. The admission
// decision comes first: a run whose agent never executed says nothing about the
// target and is withheld rather than scored zero.
func (s *service) scoreOne(run *Run, scenario Scenario, evaluated evalRun) Result {
	result := Result{
		ID:           newID("result"),
		BenchRunID:   run.ID,
		ScenarioID:   scenario.ID,
		ScenarioName: scenario.Name,
		TargetIndex:  evaluated.TargetIndex,
		Target:       evaluated.TargetSnap,
		Trial:        evaluated.Repetition,
		EvalRunID:    evaluated.ID,
		Error:        evaluated.Error,
		CreatedAt:    time.Now().UTC(),
	}
	if result.Trial <= 0 {
		result.Trial = 1
	}
	if result.TargetIndex < len(run.Targets) {
		// Prefer the requested target: the snapshot may omit fields the
		// caller specified, such as an explicit provider override.
		requested := run.Targets[result.TargetIndex]
		if result.Target.Provider == "" {
			result.Target.Provider = requested.Provider
		}
		if result.Target.Model == "" {
			result.Target.Model = requested.Model
		}
		if result.Target.AgentName == "" {
			result.Target.AgentName = requested.AgentName
		}
	}

	if evaluated.Execution == nil {
		result.Admission = AdmissionInvalid
		result.InvalidReason = firstNonEmpty(evaluated.Error, "the agent never started, so the run is not evidence about this target")
		return result
	}

	result.Metrics = metricsFrom(evaluated)
	result.Passed = evaluated.Status == "pass"

	switch {
	case len(scenario.Checks) == 0:
		// Without deterministic checks the outcome rests on a judge, which is
		// diagnosis, not evidence. Score it, but mark it uncomparable.
		result.Admission = AdmissionDiagnostic
	case evaluated.Status == "error" && !assertionsEvaluated(evaluated):
		result.Admission = AdmissionInvalid
		result.InvalidReason = firstNonEmpty(evaluated.Error, "final-state checks could not be evaluated")
		return result
	default:
		result.Admission = AdmissionVerified
	}

	result.Score = computeScore(result.Passed, result.Metrics, scenario.Budget)
	return result
}

// assertionsEvaluated reports whether the harness got far enough to judge the
// task. An errored run that still produced a finished execution is a genuine
// task failure; one that did not is a harness failure.
func assertionsEvaluated(evaluated evalRun) bool {
	return evaluated.Execution != nil && !evaluated.Execution.FinishedAt.IsZero()
}

func metricsFrom(evaluated evalRun) Metrics {
	metrics := Metrics{}
	if evaluated.Execution == nil {
		return metrics
	}
	execution := evaluated.Execution
	m := execution.Metrics
	metrics.Provider, metrics.Model = m.Provider, m.Model
	metrics.TurnsUsed = execution.Turns
	metrics.TokensIn, metrics.TokensOut = m.TokensIn, m.TokensOut
	metrics.TokensTotal = m.TokensIn + m.TokensOut
	metrics.CostUSD = m.CostUSD
	metrics.LLMCalls, metrics.ToolCalls, metrics.Errors = m.LLMCalls, m.ToolCalls, m.Errors
	if !execution.StartedAt.IsZero() && !execution.FinishedAt.IsZero() {
		metrics.DurationMS = execution.FinishedAt.Sub(execution.StartedAt).Milliseconds()
	}
	if metrics.DurationMS <= 0 {
		metrics.DurationMS = int64(m.LLMDurationMS)
	}
	return metrics
}

// emitBaselineMovement compares a finished run to pinned baselines so a broken
// record or a regression is a platform event rather than something a human has
// to notice in a table.
func (s *service) emitBaselineMovement(run *Run) {
	comparison, err := s.compareToBaselines(run.ID)
	if err != nil {
		return
	}
	deltas, ok := comparison["deltas"].([]baselineDelta)
	if !ok {
		return
	}
	for _, delta := range deltas {
		switch delta.Verdict {
		case "ahead":
			s.ctx.Emit("bench.record.broken", map[string]any{
				"run_id": run.ID, "scenario_id": delta.ScenarioID, "label": delta.Label,
				"score": delta.Score, "baseline_score": delta.BaselineScore,
				"pass_rate": delta.PassRate, "baseline_pass_rate": delta.BaselinePass,
			})
		case "behind":
			s.ctx.Emit("bench.regression.detected", map[string]any{
				"run_id": run.ID, "scenario_id": delta.ScenarioID, "label": delta.Label,
				"score": delta.Score, "baseline_score": delta.BaselineScore,
				"pass_rate": delta.PassRate, "baseline_pass_rate": delta.BaselinePass,
			})
		}
	}
}

func (s *service) cancelRun(id string) (*Run, error) {
	run, err := s.db.getRun(id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	if run.Status != RunStatusQueued && run.Status != RunStatusRunning {
		return run, nil
	}
	if run.ExperimentID != "" {
		var ignored map[string]any
		_ = s.ctx.PlatformAPI().CallAppResult("evals", "eval_experiment_cancel",
			map[string]any{"id": run.ExperimentID}, &ignored)
	}
	now := time.Now().UTC()
	run.Status, run.FinishedAt = RunStatusCancelled, &now
	if err := s.db.saveRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *service) failRun(run *Run, cause error) error {
	now := time.Now().UTC()
	run.Status, run.Error, run.FinishedAt = RunStatusFailed, cause.Error(), &now
	if err := s.db.saveRun(run); err != nil {
		return err
	}
	s.ctx.Emit("bench.run.failed", map[string]any{"run_id": run.ID, "error": cause.Error()})
	return nil
}

// evidence is the exportable bundle: the sealed definition, the provenance, and
// every scored result. It is what someone else needs to check the number.
func (s *service) evidence(runID string) (map[string]any, error) {
	run, err := s.db.getRun(runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	pack, err := s.db.getPackByDigest(run.PackDigest)
	if err != nil {
		return nil, err
	}
	bundle := map[string]any{
		"run":             run,
		"provenance":      run.Provenance,
		"scoring_version": run.ScoringVersion,
		"scoring_formula": ScoringFormula,
		"scoring_weights": ScoreWeights,
		"exported_at":     time.Now().UTC(),
	}
	if pack != nil {
		bundle["pack"] = pack
	}
	// State this plainly in the bundle rather than leaving a reader to assume
	// the world was pinned by content.
	if !run.Provenance.SnapshotsVerified {
		bundle["caveats"] = []string{
			"Environment snapshots are referenced by id, not by content digest. " +
				"Two runs naming the same snapshot are asserted, not proven, to have seen the same world.",
		}
	}
	return bundle, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func short(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
