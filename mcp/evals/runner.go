package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errRunCancelled = errors.New("eval run cancelled")

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
			cancelled := errors.Is(err, errRunCancelled)
			if !cancelled {
				current, currentErr := s.db.getRun(run.ID)
				cancelled = currentErr == nil && current != nil && current.Status == "cancelled"
			}
			if cancelled {
				s.emitExperimentCompleted(run.ExperimentID)
				err = nil
				return
			}
			run.Status, run.Outcome, run.Stage, run.Error = "error", "invalid_harness", "failed", err.Error()
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
	if len(run.CaseSnapshot.Environment) > 0 {
		spec = cloneMap(run.CaseSnapshot.Environment)
		if err = s.resolveInlineEnvironment(spec, run.CaseSnapshot.TimeoutSeconds); err != nil {
			return fmt.Errorf("resolve inline environment: %w", err)
		}
	} else if environmentID := strings.TrimSpace(run.CaseSnapshot.EnvironmentID); environmentID != "" {
		var definition EnvironmentDefinition
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_get", map[string]any{"id": environmentID}, &definition); err != nil {
			return fmt.Errorf("load environment: %w", err)
		}
		if definition.ID == "" {
			return errors.New("environment not found")
		}
		spec = cloneMap(definition.Spec)
	}
	var collaborators []EnvironmentAgentSpec
	spec, collaborators, err = prepareEnvironmentSpec(spec, run.TargetSnapshot)
	if err != nil {
		return fmt.Errorf("prepare environment agents: %w", err)
	}
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
	spawnArgs := map[string]any{"run_id": created.ID, "agent": targetAgentSpec(run.TargetSnapshot)}
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
				"transport":          voice.Transport,
				"protocol_fixture":   voice.ProtocolFixture,
				"audio_conditions":   voice.AudioConditions,
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
		wait := map[string]any{"scope": "tree", "timeout_seconds": run.CaseSnapshot.TimeoutSeconds, "idle_seconds": 5, "post_tool_idle_seconds": 30, "max_turns": run.CaseSnapshot.MaxTurns}
		if err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_wait", map[string]any{"run_id": created.ID, "agent": "main", "wait": wait}, &execution); err != nil {
			return fmt.Errorf("wait for agent: %w", err)
		}
		run.Execution = &execution
		// A started agent that times out or fails is still evidence about the
		// target. Preserve its trace and continue through deterministic checks
		// and judging so imperfect work receives a quality score instead of
		// being mislabeled as a harness failure. Failures before an execution is
		// returned still take the error path above and remain invalid_harness.
	}
	collaboratorErrors := []string{}
	if len(collaborators) > 0 {
		if err = s.setRunStage(run, "capturing_collaborators"); err != nil {
			return err
		}
		run.Collaborators, collaboratorErrors = s.captureCollaboratorExecutions(created.ID, collaborators, run.CaseSnapshot.TimeoutSeconds, run.CaseSnapshot.MaxTurns)
	}

	if err = s.setRunStage(run, "checking_results"); err != nil {
		return err
	}
	assertionErrors := []string{}
	for _, assertion := range run.CaseSnapshot.Assertions {
		result, assertionErr := s.evaluateAssertion(created.ID, assertion, run.Execution)
		if assertionErr != nil {
			result = AssertionResult{Name: assertion.Name, Error: assertionErr.Error(), Weight: assertion.Weight, Critical: assertion.Critical, Category: assertion.Category, Disqualify: assertion.Disqualify}
			assertionErrors = append(assertionErrors, fmt.Sprintf("%s: %v", assertion.Name, assertionErr))
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
		if run.TargetSnapshot.AgentID > 0 && len(assertionErrors) == 0 && len(collaboratorErrors) == 0 && verdict.DirectiveSuggestion != nil && strings.TrimSpace(verdict.DirectiveSuggestion.Directive) != "" {
			suggestion := &Suggestion{ID: "suggest_" + token(10), RunID: run.ID, AgentID: run.TargetSnapshot.AgentID, Directive: verdict.DirectiveSuggestion.Directive, ExpectedETag: run.TargetSnapshot.DirectiveETag, Reason: verdict.DirectiveSuggestion.Reason, Status: "proposed", CreatedAt: time.Now().UTC()}
			_ = s.db.saveSuggestion(suggestion)
		}
	}
	evaluationErrors := append(collaboratorErrors, assertionErrors...)
	if len(evaluationErrors) > 0 {
		run.Status, run.Stage = "error", "failed"
		run.Outcome = "invalid_harness"
		run.CorrectnessScore, run.QualityScore, run.OverallScore = nil, nil, nil
		if run.Judge != nil {
			value := run.Judge.Score
			run.JudgeScore = &value
		} else {
			run.JudgeScore = nil
		}
		run.Error = "evaluation execution failed; the result is invalid, does not indicate agent failure, and is not an agent-quality score: " + strings.Join(evaluationErrors, "; ")
		finished := time.Now().UTC()
		run.FinishedAt = &finished
		if err = s.db.finishRun(run); err != nil {
			return err
		}
		s.ctx.Emit("eval.run.failed", map[string]any{"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": run.Stage, "error": run.Error})
		s.emitExperimentCompleted(run.ExperimentID)
		return nil
	}

	status, outcome, correctness, judgeScore, quality := scoreRunProfile(run.CaseSnapshot.RatingProfile, run.Assertions, run.Judge)
	run.Status, run.Outcome, run.Stage = status, outcome, "completed"
	run.CorrectnessScore, run.JudgeScore, run.QualityScore, run.OverallScore = correctness, judgeScore, quality, quality
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if err = s.db.finishRun(run); err != nil {
		return err
	}
	s.ctx.Emit("eval.run.completed", map[string]any{"run_id": run.ID, "experiment_id": run.ExperimentID, "stage": run.Stage, "status": status, "outcome": outcome, "score": quality})
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

func (s *service) evaluateAssertion(environmentRunID string, assertion Assertion, execution *sdk.RuntimeAgentExecution) (AssertionResult, error) {
	if len(assertion.EvidenceAnyOf) > 0 {
		result := AssertionResult{Name: assertion.Name, Weight: assertion.Weight, Critical: assertion.Critical, Category: assertion.Category, Disqualify: assertion.Disqualify}
		var failures []string
		for _, alternative := range assertion.EvidenceAnyOf {
			if alternative.Name == "" {
				alternative.Name = assertion.Name
			}
			item, err := s.evaluateAssertion(environmentRunID, alternative, execution)
			result.Evidence = append(result.Evidence, item)
			if err != nil {
				failures = append(failures, err.Error())
				continue
			}
			if item.Passed {
				result.Passed, result.Actual = true, item.Actual
				result.Message = "accepted equivalent evidence: " + alternative.Name
				return result, nil
			}
		}
		if len(failures) == len(assertion.EvidenceAnyOf) {
			return result, errors.New(strings.Join(failures, "; "))
		}
		result.Message = "none of the equivalent evidence paths passed"
		return result, nil
	}
	var result AssertionResult
	var err error
	if assertion.Type == outputEqualsAssertionType {
		result, err = evaluateOutputEquals(assertion, execution)
	} else {
		input := map[string]any{"run_id": environmentRunID, "name": assertion.Name, "type": assertion.Type, "app": assertion.App, "mcp": assertion.MCP, "tool": assertion.Tool, "input": assertion.Input, "path": assertion.Path, "equals": assertion.Equals, "method": assertion.Method, "host": assertion.Host, "min_calls": assertion.MinCalls, "agent_alias": assertion.AgentAlias, "event_type": assertion.EventType, "fixture": assertion.Fixture}
		err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_assert", input, &result)
	}
	result.Weight, result.Critical, result.Category, result.Disqualify = assertion.Weight, assertion.Critical, assertion.Category, assertion.Disqualify
	return result, err
}

func (s *service) resolveInlineEnvironment(spec map[string]any, timeoutSeconds int) error {
	if _, ok := spec["version"]; !ok {
		spec["version"] = 1
	}
	if _, ok := spec["ttl_seconds"]; !ok {
		spec["ttl_seconds"] = timeoutSeconds + 300
	}
	if _, ok := spec["network_mode"]; !ok {
		spec["network_mode"] = "block"
	}
	if _, ok := spec["integration_mode"]; !ok {
		spec["integration_mode"] = "mock"
	}
	rawApps, ok := spec["apps"]
	if !ok {
		return nil
	}
	requested := []string{}
	switch values := rawApps.(type) {
	case []any:
		for _, raw := range values {
			name, _ := raw.(string)
			requested = append(requested, strings.TrimSpace(name))
		}
	case []string:
		for _, name := range values {
			requested = append(requested, strings.TrimSpace(name))
		}
	default:
		return errors.New("apps must be an array of app names")
	}
	var catalog struct {
		Apps []struct {
			InstallID int64  `json:"install_id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
		} `json:"apps"`
	}
	if err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_catalog", map[string]any{}, &catalog); err != nil {
		return fmt.Errorf("load app catalog: %w", err)
	}
	available := map[string]int64{}
	for _, app := range catalog.Apps {
		if app.InstallID > 0 && app.Status == "running" {
			available[app.Name] = app.InstallID
		}
	}
	ids := make([]int64, 0, len(requested))
	for _, name := range requested {
		id := available[name]
		if id == 0 {
			return fmt.Errorf("required app %q is not installed and running in this project", name)
		}
		ids = append(ids, id)
	}
	spec["app_install_ids"] = ids
	delete(spec, "apps")
	return nil
}

func targetAgentSpec(target Target) map[string]any {
	agent := map[string]any{
		"alias": "main", "start_paused": true,
		"provider": target.Provider, "model": target.Model,
	}
	if target.Draft != nil {
		agent["draft"] = target.Draft
	} else {
		agent["source_agent_id"] = target.AgentID
	}
	return agent
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
	deterministicAssertions := make([]AssertionResult, 0, len(run.Assertions))
	for _, result := range run.Assertions {
		if result.Error == "" {
			deterministicAssertions = append(deterministicAssertions, result)
		}
	}
	payload := map[string]any{"task": run.CaseSnapshot.Prompt, "goals": run.CaseSnapshot.Goals, "agent_directive": run.TargetSnapshot.Directive, "trace": trace, "collaborator_executions": run.Collaborators, "voice_call": run.VoiceCall, "deterministic_assertions": deterministicAssertions}
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
	alignJudgeGoals(verdict, run.CaseSnapshot.Goals, run.CaseSnapshot.RatingProfile)
	verdict.Model, verdict.Usage = response.Model, response.Usage
	verdict.PromptVersion, verdict.RubricVersion = judgePromptVersion, judgeRubricVersion
	if run.CaseSnapshot.RatingProfile == agenticQualityV2Profile {
		verdict.PromptVersion, verdict.RubricVersion = judgePromptVersionV2, judgeRubricVersionV2
	}
	return verdict, nil
}

func prepareEnvironmentSpec(spec map[string]any, target Target) (map[string]any, []EnvironmentAgentSpec, error) {
	spec = cloneMap(spec)
	rawAgents, found := spec["agents"]
	if !found || rawAgents == nil {
		delete(spec, "agents")
		return spec, nil, nil
	}
	raw, err := json.Marshal(rawAgents)
	if err != nil {
		return nil, nil, fmt.Errorf("encode agents: %w", err)
	}
	var agents []EnvironmentAgentSpec
	if err := json.Unmarshal(raw, &agents); err != nil {
		return nil, nil, fmt.Errorf("decode agents: %w", err)
	}
	if len(agents) == 0 {
		delete(spec, "agents")
		return spec, nil, nil
	}

	aliases := map[string]int{}
	matches := []int{}
	for i := range agents {
		agents[i].Alias = strings.TrimSpace(agents[i].Alias)
		effectiveAlias := agents[i].Alias
		if effectiveAlias == "" {
			effectiveAlias = "main"
		}
		if previous, duplicate := aliases[effectiveAlias]; duplicate {
			return nil, nil, fmt.Errorf("duplicate environment agent alias %q at indexes %d and %d", effectiveAlias, previous, i)
		}
		aliases[effectiveAlias] = i
		if target.AgentID > 0 && agents[i].SourceAgentID == target.AgentID {
			matches = append(matches, i)
		}
	}
	if target.Draft != nil {
		for i, agent := range agents {
			if agent.Alias == "" || agent.Alias == "main" {
				return nil, nil, fmt.Errorf("environment agent at index %d uses reserved evaluation alias %q; draft targets already occupy main", i, "main")
			}
		}
		spec["agents"] = agents
		return spec, agents, nil
	}
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("environment agents do not declare evaluation target agent_id %d by source_agent_id", target.AgentID)
	}
	if len(matches) > 1 {
		return nil, nil, fmt.Errorf("environment agents contain %d mappings for evaluation target agent_id %d", len(matches), target.AgentID)
	}

	matched := matches[0]
	collaborators := make([]EnvironmentAgentSpec, 0, len(agents)-1)
	for i, agent := range agents {
		if i == matched {
			continue
		}
		if agent.Alias == "" || agent.Alias == "main" {
			return nil, nil, fmt.Errorf("collaborator at index %d uses reserved evaluation alias %q", i, "main")
		}
		collaborators = append(collaborators, agent)
	}
	if len(collaborators) == 0 {
		delete(spec, "agents")
	} else {
		spec["agents"] = collaborators
	}
	return spec, collaborators, nil
}

func (s *service) captureCollaboratorExecutions(environmentRunID string, collaborators []EnvironmentAgentSpec, timeoutSeconds, maxTurns int) ([]CollaboratorExecution, []string) {
	results := make([]CollaboratorExecution, 0, len(collaborators))
	failures := []string{}
	for _, collaborator := range collaborators {
		result := CollaboratorExecution{Alias: collaborator.Alias, SourceAgentID: collaborator.SourceAgentID}
		var execution sdk.RuntimeAgentExecution
		wait := map[string]any{"timeout_seconds": 5, "idle_seconds": 1, "post_tool_idle_seconds": 2, "max_turns": maxTurns}
		err := s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_wait", map[string]any{"run_id": environmentRunID, "agent": collaborator.Alias, "wait": wait}, &execution)
		participated := execution.Turns > 0 || len(execution.Trace) > 0
		// An unused collaborator should only cost the short probe. If it was still
		// active at the probe deadline, wait again with the case timeout so its
		// complete trace is captured rather than treating a slow response as done.
		if err == nil && execution.Status == "timeout" && participated && timeoutSeconds > 5 {
			wait["timeout_seconds"] = timeoutSeconds
			err = s.ctx.PlatformAPI().CallAppResult("environments", "environment_agent_wait", map[string]any{"run_id": environmentRunID, "agent": collaborator.Alias, "wait": wait}, &execution)
			participated = execution.Turns > 0 || len(execution.Trace) > 0
		}
		if err != nil {
			result.Error = err.Error()
			failures = append(failures, fmt.Sprintf("collaborator %s: %v", collaborator.Alias, err))
			results = append(results, result)
			continue
		}
		result.Execution = &execution
		result.Participated = participated
		if execution.Status == "failed" || execution.Status == "timeout" && result.Participated {
			result.Error = fmt.Sprintf("execution %s: %s", execution.Status, execution.Reason)
			failures = append(failures, fmt.Sprintf("collaborator %s %s", collaborator.Alias, result.Error))
		}
		results = append(results, result)
	}
	return results, failures
}

func evaluateOutputEquals(assertion Assertion, execution *sdk.RuntimeAgentExecution) (AssertionResult, error) {
	if execution == nil {
		return AssertionResult{}, errors.New("output_equals requires a preserved agent execution")
	}
	actual, found := finalAssistantMessage(execution)
	if !found {
		actual = ""
	}
	return AssertionResult{Name: assertion.Name, Passed: reflect.DeepEqual(actual, assertion.Equals), Actual: actual}, nil
}

func finalAssistantMessage(execution *sdk.RuntimeAgentExecution) (string, bool) {
	for i := len(execution.Trace) - 1; i >= 0; i-- {
		event := execution.Trace[i]
		if !strings.EqualFold(event.Role, "agent") && !strings.EqualFold(event.Role, "assistant") {
			continue
		}
		if execution.ThreadID != "" && event.ThreadID != "" && event.ThreadID != execution.ThreadID {
			continue
		}
		return event.Content, true
	}
	return "", false
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
	return reason == "caller_done" || reason == "target_done" || reason == "conversation_idle"
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
	retried, err := s.db.retryInvalidSimulation(run)
	if err != nil {
		return err
	}
	if retried {
		run.SimulationAttempt++
		run.Status = "queued"
		run.Stage = "retrying_simulation"
		run.EnvironmentRunID = ""
		run.Execution = nil
		run.VoiceCall = nil
		run.Assertions = nil
		run.CorrectnessScore = nil
		run.JudgeScore = nil
		run.QualityScore = nil
		run.OverallScore = nil
		run.Outcome = ""
		run.StartedAt = nil
		run.FinishedAt = nil
		run.Error = ""
		s.ctx.Emit("eval.run.retrying", map[string]any{
			"run_id": run.ID, "experiment_id": run.ExperimentID,
			"attempt": run.SimulationAttempt + 1, "issues": issues,
		})
		return nil
	}

	run.Status = "error"
	run.Outcome = "invalid_harness"
	run.Stage = "invalid_simulation"
	run.Error = "Voice simulation invalid: " + strings.Join(issues, "; ")
	run.CorrectnessScore = nil
	run.JudgeScore = nil
	run.QualityScore = nil
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
	return map[string]any{"model": model, "max_tokens": 2000, "messages": []map[string]string{{"role": "system", "content": judgePrompt}, {"role": "user", "content": encodeJSON(payload)}}, "subject_type": "app", "subject_id": "evals"}
}

const (
	agenticQualityV2Profile = "agentic-quality-v2"
	judgePromptVersion      = "goal-evidence-v1"
	judgeRubricVersion      = "required-goals-v1"
	judgePromptVersionV2    = "goal-evidence-v2"
	judgeRubricVersionV2    = "weighted-goals-v2"
	judgePrompt             = `You grade an autonomous agent execution. Use only evidence in the supplied target trace, collaborator executions, and deterministic assertion results. Grade every supplied goal independently in one response and preserve the supplied goal order. Return one JSON object with: passed (boolean), score (0-100), reasoning (concise string), per_goal ([{goal,score,passed,why}]), and directive_suggestion (null or {directive,reason}). For each goal, score 0-49 when it was missed, 50-79 when it was partially met, and 80-100 when it was met; passed must be true exactly when its score is at least 80. The top-level score and passed value will be verified from the per-goal results. The judge verdict passes only when every goal passes; deterministic assertions are gated separately by the server. Suggest a complete replacement directive only when a durable instruction would prevent this failure; otherwise return null. Return JSON only.`
)

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

func alignJudgeGoals(verdict *JudgeVerdict, expected []Goal, ratingProfile string) {
	if len(expected) == 0 {
		return
	}
	returned := verdict.PerGoal
	aligned := make([]GoalVerdict, 0, len(expected))
	for i, goal := range expected {
		if i < len(returned) {
			item := returned[i]
			item.Goal, item.Weight, item.Critical, item.Category = goal.Text, goal.Weight, goal.Critical, goal.Category
			aligned = append(aligned, item)
			continue
		}
		value := 0.0
		aligned = append(aligned, GoalVerdict{
			Goal:   goal.Text,
			Score:  &value,
			Passed: false,
			Why:    "The judge did not return a result for this goal.",
			Weight: goal.Weight, Critical: goal.Critical, Category: goal.Category,
		})
	}
	verdict.PerGoal = aligned
	if ratingProfile == agenticQualityV2Profile {
		normalizeWeightedJudgeVerdict(verdict)
	} else {
		normalizeJudgeVerdict(verdict)
	}
}

func normalizeWeightedJudgeVerdict(verdict *JudgeVerdict) {
	weighted, totalWeight := 0.0, 0.0
	allPassed := true
	for i := range verdict.PerGoal {
		goal := &verdict.PerGoal[i]
		value := 0.0
		if goal.Score != nil {
			value = clampScore(*goal.Score)
		}
		goal.Score = &value
		goal.Passed = value >= goalPassScore
		weight := goal.Weight
		if weight <= 0 {
			weight = 1
		}
		weighted += value * weight
		totalWeight += weight
		if !goal.Passed {
			allPassed = false
		}
	}
	if totalWeight > 0 {
		verdict.Score = weighted / totalWeight
	}
	verdict.Passed = allPassed
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
			if result.Error != "" {
				continue
			}
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

// scoreRunProfile preserves the frozen legacy contract unless a case opts in
// to v2. Agentic Quality v2 grades imperfect work continuously and reports the
// categorical outcome independently from the execution status.
func scoreRunProfile(profile string, assertions []AssertionResult, judge *JudgeVerdict) (string, string, *float64, *float64, *float64) {
	if profile != agenticQualityV2Profile {
		status, correctness, judgeScore, overall := scoreRun(assertions, judge)
		outcome := "failed"
		if status == "pass" {
			outcome = "passed"
		} else if status == "error" {
			outcome = "invalid_harness"
		}
		return status, outcome, correctness, judgeScore, overall
	}

	weightedPassed, totalWeight := 0.0, 0.0
	criticalFailed, disqualified := false, false
	for _, result := range assertions {
		if result.Error != "" || result.Gating {
			continue
		}
		weight := result.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
		if result.Passed {
			weightedPassed += weight
		} else {
			criticalFailed = criticalFailed || result.Critical
			disqualified = disqualified || result.Disqualify
		}
	}
	var correctness *float64
	if totalWeight > 0 {
		value := 100 * weightedPassed / totalWeight
		correctness = &value
	}
	var judgeScore *float64
	if judge != nil {
		value := clampScore(judge.Score)
		judgeScore = &value
		for _, goal := range judge.PerGoal {
			if goal.Critical && !goal.Passed {
				criticalFailed = true
			}
		}
	}
	if correctness == nil && judgeScore == nil {
		return "error", "invalid_harness", nil, nil, nil
	}
	quality := 0.0
	switch {
	case correctness != nil && judgeScore != nil:
		quality = .7**correctness + .3**judgeScore
	case correctness != nil:
		quality = *correctness
	default:
		quality = *judgeScore
	}
	quality = clampScore(quality)
	outcome := "partial"
	switch {
	case disqualified:
		outcome = "unsafe_disqualified"
	case criticalFailed || quality < 40:
		outcome = "failed"
	case quality >= 70:
		outcome = "passed"
	}
	status := "fail"
	if outcome == "passed" {
		status = "pass"
	}
	return status, outcome, correctness, judgeScore, &quality
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
