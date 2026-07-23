package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (s *service) executeRun(ctx context.Context, run *Run) (err error) {
	started := time.Now().UTC()
	run.StartedAt = &started
	var environmentRun *EnvironmentRun
	defer func() {
		if environmentRun != nil {
			var ignored map[string]any
			_ = s.ctx.PlatformAPI().CallAppResult("environments", "environment_run_stop", map[string]any{"id": environmentRun.ID}, &ignored)
		}
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("eval runner panic: %v", recovered)
		}
		if err != nil {
			run.Status, run.Stage, run.Error = "error", "failed", err.Error()
			finished := time.Now().UTC()
			run.FinishedAt = &finished
			_ = s.db.finishRun(run)
			s.ctx.Emit("eval.run.failed", map[string]any{"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": run.Stage, "error": err.Error()})
			s.emitExperimentCompleted(run.ExperimentID)
		}
	}()

	if err = s.setRunStage(run, "preparing_environment"); err != nil {
		return err
	}
	spec := map[string]any{"version": 1, "ttl_seconds": run.CaseSnapshot.TimeoutSeconds + 300, "network_mode": "block", "integration_mode": "mock"}
	if environmentID := strings.TrimSpace(run.CaseSnapshot.EnvironmentID); environmentID != "" {
		var definition EnvironmentDefinition
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_get", map[string]any{"id": environmentID}, &definition); err != nil {
			return fmt.Errorf("load environment: %w", err)
		}
		if definition.ID == "" {
			return errors.New("environment not found")
		}
		spec = cloneMap(definition.Spec)
	}
	delete(spec, "agents")
	var created EnvironmentRun
	if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_run_create", map[string]any{"kind": "eval", "spec": spec}, &created); err != nil {
		return fmt.Errorf("start environment: %w", err)
	}
	environmentRun = &created
	run.EnvironmentRunID = created.ID

	if err = s.setRunStage(run, "spawning_agent"); err != nil {
		return err
	}
	var spawned sdk.RuntimeAgent
	spawnArgs := map[string]any{"run_id": created.ID, "agent": map[string]any{"source_agent_id": run.TargetSnapshot.AgentID, "alias": "main", "start_paused": true, "provider": run.TargetSnapshot.Provider, "model": run.TargetSnapshot.Model}}
	if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_spawn", spawnArgs, &spawned); err != nil {
		return fmt.Errorf("spawn agent: %w", err)
	}
	if run.CaseSnapshot.Mode == "voice" {
		if run.CaseSnapshot.Voice == nil {
			return errors.New("voice case has no caller settings")
		}
		voice := run.CaseSnapshot.Voice
		input := map[string]any{
			"run_id": created.ID,
			"voice": map[string]any{
				"target_agent":       "main",
				"target_directive":   run.TargetSnapshot.Directive,
				"caller_name":        voice.CallerName,
				"caller_persona":     voice.CallerPersona,
				"caller_goal":        voice.CallerGoal,
				"caller_behavior":    voice.CallerBehavior,
				"provider":           voice.Provider,
				"voice":              voice.Voice,
				"caller_provider":    voice.CallerProvider,
				"caller_voice":       voice.CallerVoice,
				"greeting":           voice.Greeting,
				"timeout_seconds":    run.CaseSnapshot.TimeoutSeconds,
				"disconnect_on_done": true,
			},
		}
		if err = s.setRunStage(run, "connecting_voice_call"); err != nil {
			return err
		}
		var call EnvironmentVoiceCall
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_voice_call", input, &call); err != nil {
			return fmt.Errorf("run voice call: %w", err)
		}
		run.VoiceCall = &call
		run.Execution = call.Execution
		run.Assertions = append(run.Assertions, voiceAssertionResults(voice, &call)...)
		if issues := voiceSimulationIssues(&call); len(issues) > 0 {
			return s.finishInvalidSimulation(run, issues)
		}
	} else {
		if err = s.setRunStage(run, "sending_task"); err != nil {
			return err
		}
		var accepted map[string]any
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_send", map[string]any{"run_id": created.ID, "agent": "main", "thread_id": "main", "message": environmentTaskMessage(run.CaseSnapshot.Prompt, created.WebFixtures)}, &accepted); err != nil {
			return fmt.Errorf("send task: %w", err)
		}
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_control", map[string]any{"run_id": created.ID, "agent": "main", "action": "run"}, &accepted); err != nil {
			return fmt.Errorf("start agent: %w", err)
		}

		if err = s.setRunStage(run, "agent_running"); err != nil {
			return err
		}
		var execution sdk.RuntimeAgentExecution
		wait := map[string]any{"timeout_seconds": run.CaseSnapshot.TimeoutSeconds, "idle_seconds": 5, "post_tool_idle_seconds": 30, "max_turns": run.CaseSnapshot.MaxTurns}
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_wait", map[string]any{"run_id": created.ID, "agent": "main", "wait": wait}, &execution); err != nil {
			return fmt.Errorf("wait for agent: %w", err)
		}
		run.Execution = &execution
		if execution.Status == "failed" || execution.Status == "timeout" {
			return fmt.Errorf("agent execution %s: %s", execution.Status, execution.Reason)
		}
	}

	if err = s.setRunStage(run, "checking_results"); err != nil {
		return err
	}
	for _, assertion := range run.CaseSnapshot.Assertions {
		input := map[string]any{"run_id": created.ID, "name": assertion.Name, "type": assertion.Type, "app": assertion.App, "tool": assertion.Tool, "input": assertion.Input, "path": assertion.Path, "equals": assertion.Equals, "method": assertion.Method, "host": assertion.Host, "min_calls": assertion.MinCalls, "agent_alias": assertion.AgentAlias, "event_type": assertion.EventType, "fixture": assertion.Fixture}
		var result AssertionResult
		if callErr := s.ctx.PlatformAPI().CallAppResult("environments", "environment_assert", input, &result); callErr != nil {
			result = AssertionResult{Name: assertion.Name, Passed: false, Message: callErr.Error()}
		}
		if result.Name == "" {
			result.Name = assertion.Name
		}
		run.Assertions = append(run.Assertions, result)
	}

	experiment, err := s.db.getExperiment(run.ExperimentID)
	if err != nil || experiment == nil {
		return errors.New("experiment disappeared")
	}
	if experiment.JudgeModel != "" && len(run.CaseSnapshot.Goals) > 0 {
		if err = s.setRunStage(run, "judging"); err != nil {
			return err
		}
		verdict, judgeErr := s.judge(ctx, experiment.JudgeModel, run)
		if judgeErr != nil {
			return fmt.Errorf("judge: %w", judgeErr)
		}
		run.Judge = verdict
		if verdict.DirectiveSuggestion != nil && strings.TrimSpace(verdict.DirectiveSuggestion.Directive) != "" {
			suggestion := &Suggestion{ID: "suggest_" + token(10), RunID: run.ID, AgentID: run.TargetSnapshot.AgentID, Directive: verdict.DirectiveSuggestion.Directive, ExpectedETag: run.TargetSnapshot.DirectiveETag, Reason: verdict.DirectiveSuggestion.Reason, Status: "proposed", CreatedAt: time.Now().UTC()}
			_ = s.db.saveSuggestion(suggestion)
		}
	}

	status, correctness, judgeScore, overall := scoreRun(run.Assertions, run.Judge)
	run.Status, run.Stage, run.CorrectnessScore, run.JudgeScore, run.OverallScore = status, "completed", correctness, judgeScore, overall
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if err = s.db.finishRun(run); err != nil {
		return err
	}
	s.ctx.Emit("eval.run.completed", map[string]any{"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": run.Stage, "status": status, "score": overall})
	s.emitExperimentCompleted(run.ExperimentID)
	if experiment.TriggerType == "schedule" {
		updated, _ := s.db.getExperiment(run.ExperimentID)
		if updated != nil && updated.Status == "completed" && updated.Summary != nil {
			suite, _ := s.db.getSuite(updated.SuiteID)
			if suite != nil && updated.Summary.PassRate < suite.RequiredPassRate {
				s.ctx.Emit("eval.regression.detected", map[string]any{"suite_id": suite.ID, "experiment_id": updated.ID, "pass_rate": updated.Summary.PassRate, "required_pass_rate": suite.RequiredPassRate})
			}
		}
	}
	return nil
}

func (s *service) setRunStage(run *Run, stage string) error {
	run.Stage = stage
	if err := s.db.updateRunProgress(run.ID, stage, run.EnvironmentRunID); err != nil {
		return err
	}
	s.ctx.Emit("eval.run.stage.changed", map[string]any{
		"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": stage,
		"environment_run_id": run.EnvironmentRunID,
	})
	return nil
}

func (s *service) emitExperimentCompleted(id string) {
	experiment, err := s.db.getExperiment(id)
	if err != nil || experiment == nil || experiment.Status != "completed" {
		return
	}
	s.ctx.Emit("eval.experiment.completed", map[string]any{
		"experiment_id": experiment.ID, "suite_id": experiment.SuiteID, "summary": experiment.Summary,
	})
}

func environmentTaskMessage(task string, fixtures []EnvironmentWebFixture) string {
	if len(fixtures) == 0 {
		return task
	}
	lines := []string{"Test environment:"}
	for _, fixture := range fixtures {
		if strings.TrimSpace(fixture.TestURL) == "" {
			continue
		}
		name := fixture.Pack
		if name == "" {
			name = fixture.ID
		}
		lines = append(lines, fmt.Sprintf("- The simulated %s website is available at %s", name, fixture.TestURL))
	}
	if len(lines) == 1 {
		return task
	}
	lines = append(lines, "Use the Computer app for website tasks. The site and all actions inside it are simulated.", "", "Task:", task)
	return strings.Join(lines, "\n")
}

func (s *service) judge(ctx context.Context, model string, run *Run) (*JudgeVerdict, error) {
	var trace []sdk.RuntimeTraceEvent
	if run.Execution != nil {
		trace = run.Execution.Trace
	}
	payload := map[string]any{"task": run.CaseSnapshot.Prompt, "goals": run.CaseSnapshot.Goals, "agent_directive": run.TargetSnapshot.Directive, "trace": trace, "voice_call": run.VoiceCall, "deterministic_assertions": run.Assertions}
	request := judgeRequest(model, payload)
	var response struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := s.ctx.PlatformAPI().CallAppResult("llm", "llm_chat_complete", request, &response); err != nil {
		return nil, err
	}
	if len(response.Choices) == 0 {
		return nil, errors.New("judge returned no choices")
	}
	verdict, err := parseJudge(response.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	alignJudgeGoals(verdict, run.CaseSnapshot.Goals)
	verdict.Model, verdict.Usage = response.Model, response.Usage
	return verdict, nil
}

func voiceAssertionResults(spec *VoiceCase, call *EnvironmentVoiceCall) []AssertionResult {
	if call == nil {
		return []AssertionResult{
			{Name: "Valid two-sided voice simulation", Passed: false, Message: "no voice call result", Gating: true},
			{Name: "Call ended normally", Passed: false, Message: "no voice call result", Gating: true},
			{Name: "Both participants produced audio", Passed: false, Message: "no voice call result", Gating: true},
			{Name: "No realtime audio errors", Passed: false, Message: "no voice call result", Gating: true},
		}
	}
	validityIssues := voiceSimulationIssues(call)
	receptionistTurns, callerTurns, transitions := voiceTranscriptCounts(call.Transcript)
	results := []AssertionResult{
		{Name: "Valid two-sided voice simulation", Passed: len(validityIssues) == 0, Actual: map[string]any{"receptionist_turns": receptionistTurns, "caller_turns": callerTurns, "speaker_transitions": transitions}, Message: strings.Join(validityIssues, "; "), Gating: true},
		{Name: "Call ended normally", Passed: voiceCallEndedNormally(call.Metrics.EndedBy), Actual: call.Metrics.EndedBy, Gating: true},
		{Name: "Both participants produced audio", Passed: call.Metrics.ReceptionistAudioS > 0 && call.Metrics.CallerAudioS > 0, Actual: map[string]float64{"receptionist_seconds": call.Metrics.ReceptionistAudioS, "caller_seconds": call.Metrics.CallerAudioS}, Gating: true},
		{Name: "No realtime audio errors", Passed: call.Metrics.RealtimeErrors == 0, Actual: call.Metrics.RealtimeErrors, Gating: true},
	}
	if spec != nil && spec.MaxFirstResponseMS > 0 {
		actual := call.Metrics.FirstResponseMS
		results = append(results, AssertionResult{Name: "First response latency", Passed: actual > 0 && actual <= spec.MaxFirstResponseMS, Actual: actual, Message: fmt.Sprintf("maximum %d ms", spec.MaxFirstResponseMS)})
	}
	if spec != nil && spec.MaxAverageResponseMS > 0 {
		actual := call.Metrics.AverageResponseMS
		results = append(results, AssertionResult{Name: "Average response latency", Passed: actual > 0 && actual <= spec.MaxAverageResponseMS, Actual: actual, Message: fmt.Sprintf("maximum %d ms", spec.MaxAverageResponseMS)})
	}
	return results
}

func voiceSimulationIssues(call *EnvironmentVoiceCall) []string {
	if call == nil {
		return []string{"voice call result is missing"}
	}
	if call.Validity.Status == "invalid" {
		if len(call.Validity.Reasons) > 0 {
			return append([]string(nil), call.Validity.Reasons...)
		}
		return []string{"environment marked the voice simulation invalid"}
	}
	if call.Validity.Status == "valid" {
		return nil
	}

	issues := []string{}
	if call.Status != "completed" {
		issues = append(issues, "voice call status is "+fallbackString(call.Status, "unknown"))
	}
	if !voiceCallEndedNormally(call.Metrics.EndedBy) {
		issues = append(issues, "call ended unexpectedly: "+fallbackString(call.Metrics.EndedBy, "unknown"))
	}
	if call.Metrics.ReceptionistAudioS <= 0 {
		issues = append(issues, "receptionist produced no audio")
	}
	if call.Metrics.CallerAudioS <= 0 {
		issues = append(issues, "caller produced no audio")
	}
	receptionistTurns, callerTurns, transitions := voiceTranscriptCounts(call.Transcript)
	if receptionistTurns == 0 {
		issues = append(issues, "transcript has no receptionist turn")
	}
	if callerTurns == 0 {
		issues = append(issues, "transcript has no caller turn")
	}
	if transitions == 0 {
		issues = append(issues, "conversation has no speaker turn-taking")
	}
	if call.Metrics.RealtimeErrors > 0 {
		issues = append(issues, fmt.Sprintf("realtime participants reported %d errors", call.Metrics.RealtimeErrors))
	}
	return issues
}

func voiceCallEndedNormally(reason string) bool {
	return reason == "caller_done" || reason == "conversation_idle"
}

func fallbackString(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func voiceTranscriptCounts(transcript []VoiceTranscriptTurn) (receptionist, caller, transitions int) {
	previous := ""
	for _, turn := range transcript {
		speaker := strings.TrimSpace(turn.Speaker)
		switch speaker {
		case "receptionist":
			receptionist++
		case "caller":
			caller++
		default:
			continue
		}
		if previous != "" && previous != speaker {
			transitions++
		}
		previous = speaker
	}
	return receptionist, caller, transitions
}

func (s *service) finishInvalidSimulation(run *Run, issues []string) error {
	run.Status = "error"
	run.Stage = "invalid_simulation"
	run.Error = "Voice simulation invalid: " + strings.Join(issues, "; ")
	run.CorrectnessScore = nil
	run.JudgeScore = nil
	run.OverallScore = nil
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if err := s.db.finishRun(run); err != nil {
		return err
	}
	s.ctx.Emit("eval.run.invalid", map[string]any{
		"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": run.Stage,
		"error": run.Error, "issues": issues,
	})
	s.emitExperimentCompleted(run.ExperimentID)
	return nil
}

func judgeRequest(model string, payload map[string]any) map[string]any {
	request := map[string]any{"model": model, "max_tokens": 2000, "messages": []map[string]string{{"role": "system", "content": judgePrompt}, {"role": "user", "content": encodeJSON(payload)}}, "subject_type": "app", "subject_id": "evals"}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "openai-codex/") {
		request["temperature"] = 0
	}
	return request
}

const judgePrompt = `You grade an autonomous agent execution. Use only evidence in the supplied trace and deterministic assertion results. Grade every supplied goal independently in one response and preserve the supplied goal order. Return one JSON object with: passed (boolean), score (0-100), reasoning (concise string), per_goal ([{goal,score,passed,why}]), and directive_suggestion (null or {directive,reason}). For each goal, score 0-49 when it was missed, 50-79 when it was partially met, and 80-100 when it was met; passed must be true exactly when its score is at least 80. The top-level score and passed value will be verified from the per-goal results. The judge verdict passes only when every goal passes; deterministic assertions are gated separately by the server. Suggest a complete replacement directive only when a durable instruction would prevent this failure; otherwise return null. Return JSON only.`

func parseJudge(raw string) (*JudgeVerdict, error) {
	raw = strings.TrimSpace(raw)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return nil, errors.New("judge did not return JSON")
	}
	var verdict JudgeVerdict
	if err := json.Unmarshal([]byte(raw[start:end+1]), &verdict); err != nil {
		return nil, err
	}
	normalizeJudgeVerdict(&verdict)
	return &verdict, nil
}

const goalPassScore = 80.0

func normalizeJudgeVerdict(verdict *JudgeVerdict) {
	verdict.Score = clampScore(verdict.Score)
	if len(verdict.PerGoal) == 0 {
		return
	}

	total := 0.0
	allScored, allPassed := true, true
	lowest := 100.0
	for i := range verdict.PerGoal {
		goal := &verdict.PerGoal[i]
		if goal.Score == nil {
			allScored = false
			if !goal.Passed {
				allPassed = false
			}
			continue
		}
		value := clampScore(*goal.Score)
		goal.Score = &value
		goal.Passed = value >= goalPassScore
		total += value
		lowest = math.Min(lowest, value)
		if !goal.Passed {
			allPassed = false
		}
	}
	if !allScored {
		return
	}

	verdict.Score = total / float64(len(verdict.PerGoal))
	verdict.Passed = allPassed
	// Required goals gate the scenario into the same visual band as its weakest goal.
	if lowest < 50 && verdict.Score >= 50 {
		verdict.Score = 49
	} else if lowest < goalPassScore && verdict.Score >= goalPassScore {
		verdict.Score = goalPassScore - 1
	}
}

func alignJudgeGoals(verdict *JudgeVerdict, expected []string) {
	if len(expected) == 0 {
		return
	}
	returned := verdict.PerGoal
	aligned := make([]GoalVerdict, 0, len(expected))
	for i, goal := range expected {
		if i < len(returned) {
			item := returned[i]
			item.Goal = goal
			aligned = append(aligned, item)
			continue
		}
		value := 0.0
		aligned = append(aligned, GoalVerdict{
			Goal:   goal,
			Score:  &value,
			Passed: false,
			Why:    "The judge did not return a result for this goal.",
		})
	}
	verdict.PerGoal = aligned
	normalizeJudgeVerdict(verdict)
}

func clampScore(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}

func scoreRun(assertions []AssertionResult, judge *JudgeVerdict) (string, *float64, *float64, *float64) {
	passed := true
	var correctness *float64
	if len(assertions) > 0 {
		count, total := 0, 0
		for _, result := range assertions {
			if !result.Passed {
				passed = false
			}
			if result.Gating {
				continue
			}
			total++
			if result.Passed {
				count++
			}
		}
		if total > 0 {
			value := 100 * float64(count) / float64(total)
			correctness = &value
		}
	}
	var judgeScore *float64
	if judge != nil {
		value := judge.Score
		judgeScore = &value
		if !judge.Passed {
			passed = false
		}
	}
	if correctness == nil && judgeScore == nil {
		value := 0.0
		return "error", nil, nil, &value
	}
	overall := 0.0
	switch {
	case correctness != nil && judgeScore != nil:
		overall = .7**correctness + .3**judgeScore
	case correctness != nil:
		overall = *correctness
	default:
		overall = *judgeScore
	}
	if correctness != nil && *correctness < 100 && overall > 49 {
		overall = 49
	}
	status := "fail"
	if passed {
		status = "pass"
	}
	return status, correctness, judgeScore, &overall
}

func cloneMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var cloned map[string]any
	_ = json.Unmarshal(raw, &cloned)
	if cloned == nil {
		cloned = map[string]any{}
	}
	return cloned
}
