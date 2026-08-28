package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) store {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/evals.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, path := range []string{"migrations/001_init.sql", "migrations/002_voice_cases.sql", "migrations/003_run_progress.sql", "migrations/004_simulation_retry.sql"} {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return store{db: db}
}

func TestSuiteCaseExperimentPersistence(t *testing.T) {
	db := testStore(t)
	db.db.SetMaxOpenConns(1)
	suite := &Suite{ID: "suite-one", Name: "One", EnvironmentID: "env-one", JudgeModel: "openai/gpt-4.1", ContinuousTargets: []Target{{AgentID: 7, Model: "openai/gpt-4.1"}}, ScheduleMinutes: 60, RequiredPassRate: .9, Enabled: true}
	if err := db.saveSuite(suite); err != nil {
		t.Fatal(err)
	}
	item := &Case{
		ID: "case-one", SuiteID: suite.ID, Name: "Answer a caller", Mode: "voice",
		Prompt: "Help the caller book an appointment", Goals: []string{"The appointment is booked"},
		Assertions: []Assertion{{Name: "record", Type: "app_state", App: "crm", Tool: "contacts_get"}},
		Voice: &VoiceCase{
			CallerGoal: "Book an appointment for tomorrow", CallerPersona: "A concise customer",
			MaxFirstResponseMS: 2000, Transport: "carrier", ProtocolFixture: "telephony-carrier",
			AudioConditions: &VoiceAudioConditions{Preset: "cafe", Intensity: "moderate", Codec: "none", Seed: 42},
		},
		Enabled: true,
	}
	if err := db.saveCase(item); err != nil {
		t.Fatal(err)
	}
	rows, err := db.listSuites()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Cases) != 1 || rows[0].ContinuousTargets[0].AgentID != 7 {
		t.Fatalf("suites=%#v", rows)
	}

	exp := &Experiment{ID: "exp-one", SuiteID: suite.ID, SuiteRevision: rows[0].Revision, Name: "Run", TriggerType: "manual", Targets: []Target{{AgentID: 7, AgentName: "Agent", Model: "gpt-4.1"}}, Repetitions: 2, JudgeModel: suite.JudgeModel, CreatedAt: time.Now().UTC()}
	if err := db.createExperiment(exp, rows[0].Cases); err != nil {
		t.Fatal(err)
	}
	got, err := db.getExperiment(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Runs) != 2 || got.Runs[0].CaseSnapshot.Mode != "voice" || got.Runs[0].CaseSnapshot.Voice == nil || got.Runs[0].CaseSnapshot.Voice.CallerGoal != "Book an appointment for tomorrow" || got.Runs[0].CaseSnapshot.Voice.Transport != "carrier" || got.Runs[0].CaseSnapshot.Voice.AudioConditions == nil || got.Runs[0].CaseSnapshot.Voice.AudioConditions.Preset != "cafe" {
		t.Fatalf("experiment=%#v", got)
	}

	claimed, err := db.claimRun()
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if claimed.Stage != "starting" {
		t.Fatalf("claimed stage=%q", claimed.Stage)
	}
	if err := db.updateRunProgress(claimed.ID, "agent_running", "env-run-one"); err != nil {
		t.Fatal(err)
	}
	progress, err := db.getRun(claimed.ID)
	if err != nil || progress.Stage != "agent_running" || progress.EnvironmentRunID != "env-run-one" {
		t.Fatalf("progress=%#v err=%v", progress, err)
	}
	claimed.Stage, claimed.EnvironmentRunID = progress.Stage, progress.EnvironmentRunID
	value := 100.0
	finished := time.Now().UTC()
	claimed.Status, claimed.OverallScore, claimed.FinishedAt = "pass", &value, &finished
	claimed.Execution = &sdk.RuntimeAgentExecution{Metrics: sdk.RuntimeAgentMetrics{TokensIn: 10, TokensOut: 5, CostUSD: .01}}
	if err := db.finishRun(claimed); err != nil {
		t.Fatal(err)
	}
	got, err = db.getExperiment(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Passed != 1 || got.Summary.Queued != 1 || got.Summary.Targets[0].AverageTokens != 15 {
		t.Fatalf("summary=%#v", got.Summary)
	}
}

func TestInvalidSimulationRetriesSameLogicalRunOnce(t *testing.T) {
	db := testStore(t)
	suite := &Suite{ID: "suite-retry", Name: "Retry", RequiredPassRate: 1, Enabled: true}
	if err := db.saveSuite(suite); err != nil {
		t.Fatal(err)
	}
	item := Case{
		ID: "case-retry", SuiteID: suite.ID, Name: "Voice", Mode: "voice",
		Prompt: "Complete the call", Goals: []string{"Finish naturally"},
		Voice: &VoiceCase{CallerGoal: "Complete the call"}, Enabled: true,
	}
	if err := db.saveCase(&item); err != nil {
		t.Fatal(err)
	}
	experiment := &Experiment{
		ID: "exp-retry", SuiteID: suite.ID, SuiteRevision: 1, Name: "Retry",
		TriggerType: "manual", Targets: []Target{{AgentID: 1}}, Repetitions: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.createExperiment(experiment, []Case{item}); err != nil {
		t.Fatal(err)
	}
	run, err := db.claimRun()
	if err != nil || run == nil {
		t.Fatalf("claim run: run=%#v err=%v", run, err)
	}
	run.EnvironmentRunID = "env-first"
	run.VoiceCall = &EnvironmentVoiceCall{ID: "call-first"}
	run.Assertions = []AssertionResult{{Name: "valid", Passed: false}}

	retried, err := db.retryInvalidSimulation(run)
	if err != nil || !retried {
		t.Fatalf("retry invalid simulation: retried=%v err=%v", retried, err)
	}
	requeued, err := db.getRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Status != "queued" || requeued.Stage != "retrying_simulation" ||
		requeued.SimulationAttempt != 1 || requeued.EnvironmentRunID != "" ||
		requeued.VoiceCall != nil || len(requeued.Assertions) != 0 {
		t.Fatalf("requeued run=%#v", requeued)
	}
	requeued, err = db.claimRun()
	if err != nil || requeued == nil {
		t.Fatalf("claim retry: run=%#v err=%v", requeued, err)
	}
	retried, err = db.retryInvalidSimulation(requeued)
	if err != nil || retried {
		t.Fatalf("second retry: retried=%v err=%v", retried, err)
	}
}

func TestScoreRunGatesOnDeterministicFailure(t *testing.T) {
	status, correctness, judgeScore, overall := scoreRun([]AssertionResult{{Passed: true}, {Passed: false}}, &JudgeVerdict{Passed: true, Score: 100})
	if status != "fail" || *correctness != 50 || *judgeScore != 100 || *overall > 49 {
		t.Fatalf("status=%s correctness=%v judge=%v overall=%v", status, *correctness, *judgeScore, *overall)
	}
	status, _, _, overall = scoreRun(nil, &JudgeVerdict{Passed: true, Score: 82})
	if status != "pass" || *overall != 82 {
		t.Fatalf("judge-only status=%s score=%v", status, *overall)
	}
}

func TestScoreRunDoesNotCountVoiceValidityAsCorrectness(t *testing.T) {
	status, correctness, judgeScore, overall := scoreRun(
		[]AssertionResult{
			{Name: "Valid two-sided voice simulation", Passed: true, Gating: true},
			{Name: "No realtime audio errors", Passed: true, Gating: true},
		},
		&JudgeVerdict{Passed: false, Score: 36.875},
	)
	if status != "fail" || correctness != nil || judgeScore == nil || *judgeScore != 36.875 || overall == nil || *overall != 36.875 {
		t.Fatalf("status=%s correctness=%v judge=%v overall=%v", status, correctness, judgeScore, overall)
	}
}

func TestVoiceSimulationIssuesRejectsOneSidedCall(t *testing.T) {
	call := &EnvironmentVoiceCall{
		Status:     "invalid_simulation",
		Validity:   VoiceCallValidity{Status: "invalid", Reasons: []string{"caller produced no audio", "transcript has no caller turn"}},
		Transcript: []VoiceTranscriptTurn{{Speaker: "receptionist", Text: "Hello"}},
		Metrics:    VoiceCallMetrics{EndedBy: "audio_disconnected", ReceptionistAudioS: 1},
	}
	issues := voiceSimulationIssues(call)
	if len(issues) != 2 || !strings.Contains(strings.Join(issues, " "), "caller produced no audio") {
		t.Fatalf("issues=%v", issues)
	}
	results := voiceAssertionResults(&VoiceCase{}, call)
	if len(results) < 4 || results[0].Passed || !results[0].Gating {
		t.Fatalf("results=%#v", results)
	}
}

func TestVoiceSimulationIssuesAcceptsValidConversation(t *testing.T) {
	call := &EnvironmentVoiceCall{
		Status:   "completed",
		Validity: VoiceCallValidity{Status: "valid"},
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "Hello"},
			{Speaker: "caller", Text: "Please call me tomorrow"},
		},
		Metrics: VoiceCallMetrics{EndedBy: "caller_done", ReceptionistAudioS: 1, CallerAudioS: 1},
	}
	if issues := voiceSimulationIssues(call); len(issues) != 0 {
		t.Fatalf("issues=%v", issues)
	}
	for _, result := range voiceAssertionResults(&VoiceCase{}, call) {
		if result.Gating && !result.Passed {
			t.Fatalf("result=%#v", result)
		}
	}
}

func TestVoiceSimulationIssuesAcceptsConversationIdle(t *testing.T) {
	call := &EnvironmentVoiceCall{
		Status:   "completed",
		Validity: VoiceCallValidity{Status: "valid"},
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "When should we call?"},
			{Speaker: "caller", Text: "Monday at four."},
			{Speaker: "receptionist", Text: "Your callback is booked. Goodbye."},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "conversation_idle", ReceptionistAudioS: 1, CallerAudioS: 1,
		},
	}
	if issues := voiceSimulationIssues(call); len(issues) != 0 {
		t.Fatalf("issues=%v", issues)
	}
	for _, result := range voiceAssertionResults(&VoiceCase{}, call) {
		if result.Gating && !result.Passed {
			t.Fatalf("result=%#v", result)
		}
	}
}

func TestVoiceSimulationIssuesAcceptsTargetCompletion(t *testing.T) {
	call := &EnvironmentVoiceCall{
		Status:   "completed",
		Validity: VoiceCallValidity{Status: "valid"},
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "When should we call?"},
			{Speaker: "caller", Text: "Monday at four."},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "target_done", ReceptionistAudioS: 1, CallerAudioS: 1,
		},
	}
	if issues := voiceSimulationIssues(call); len(issues) != 0 {
		t.Fatalf("issues=%v", issues)
	}
	for _, result := range voiceAssertionResults(&VoiceCase{}, call) {
		if result.Gating && !result.Passed {
			t.Fatalf("result=%#v", result)
		}
	}
}

func TestEnvironmentTaskMessageIncludesFixtureContext(t *testing.T) {
	task := "Join the creator's most affordable paid membership."
	message := environmentTaskMessage(task, []EnvironmentWebFixture{{ID: "patreon", Pack: "patreon", TestURL: "http://gateway.test/fixture"}})
	if !strings.Contains(message, "simulated patreon website") || !strings.Contains(message, "http://gateway.test/fixture") || !strings.HasSuffix(message, task) {
		t.Fatalf("message=%q", message)
	}
	if got := environmentTaskMessage(task, nil); got != task {
		t.Fatalf("plain message=%q", got)
	}
}

func TestParseJudgeToleratesSurroundingTextAndBoundsScore(t *testing.T) {
	verdict, err := parseJudge("result:\n```json\n{\"passed\":false,\"score\":120,\"reasoning\":\"missing tool call\",\"per_goal\":[]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Passed || verdict.Score != 100 || verdict.Reasoning == "" {
		t.Fatalf("verdict=%#v", verdict)
	}
}

func TestParseJudgeDerivesScenarioResultFromGoalScores(t *testing.T) {
	verdict, err := parseJudge(`{
		"passed": true,
		"score": 100,
		"reasoning": "one requirement was only partially met",
		"per_goal": [
			{"goal": "Greet the caller clearly", "score": 100, "passed": false, "why": "Clear greeting"},
			{"goal": "Confirm the requested callback", "score": 60, "passed": true, "why": "Time was repeated but not confirmed"}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Passed || verdict.Score != 79 {
		t.Fatalf("scenario verdict=%#v", verdict)
	}
	if !verdict.PerGoal[0].Passed || verdict.PerGoal[1].Passed {
		t.Fatalf("goal verdicts=%#v", verdict.PerGoal)
	}
	if verdict.PerGoal[0].Score == nil || *verdict.PerGoal[0].Score != 100 || verdict.PerGoal[1].Score == nil || *verdict.PerGoal[1].Score != 60 {
		t.Fatalf("goal scores=%#v", verdict.PerGoal)
	}
}

func TestParseJudgePreservesLegacyGoalVerdicts(t *testing.T) {
	verdict, err := parseJudge(`{
		"passed": false,
		"score": 72,
		"reasoning": "legacy verdict",
		"per_goal": [{"goal": "Complete the task", "passed": false, "why": "Incomplete"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Passed || verdict.Score != 72 || verdict.PerGoal[0].Score != nil || verdict.PerGoal[0].Passed {
		t.Fatalf("legacy verdict=%#v", verdict)
	}
}

func TestParseJudgeClampsGoalScores(t *testing.T) {
	verdict, err := parseJudge(`{
		"passed": true,
		"score": 100,
		"reasoning": "out of bounds",
		"per_goal": [
			{"goal": "First", "score": -5, "passed": true, "why": "Missed"},
			{"goal": "Second", "score": 120, "passed": false, "why": "Met"}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Score != 49 || verdict.Passed || *verdict.PerGoal[0].Score != 0 || *verdict.PerGoal[1].Score != 100 {
		t.Fatalf("verdict=%#v", verdict)
	}
}

func TestAlignJudgeGoalsUsesConfiguredGoalsAndFailsMissingResults(t *testing.T) {
	first := 90.0
	verdict := &JudgeVerdict{
		Passed:  true,
		Score:   90,
		PerGoal: []GoalVerdict{{Goal: "paraphrased goal", Score: &first, Passed: true, Why: "Met"}},
	}
	alignJudgeGoals(verdict, []string{"Greet the caller", "Confirm the callback"})
	if verdict.Passed || verdict.Score != 45 || len(verdict.PerGoal) != 2 {
		t.Fatalf("verdict=%#v", verdict)
	}
	if verdict.PerGoal[0].Goal != "Greet the caller" || verdict.PerGoal[1].Goal != "Confirm the callback" || *verdict.PerGoal[1].Score != 0 {
		t.Fatalf("goals=%#v", verdict.PerGoal)
	}
}

func TestJudgeRequestOmitsTemperatureForEveryModel(t *testing.T) {
	codex := judgeRequest("openai-codex/gpt-5.6-terra", map[string]any{"task": "test"})
	if _, ok := codex["temperature"]; ok {
		t.Fatalf("Codex request contains unsupported temperature: %#v", codex)
	}
	other := judgeRequest("opencode-go/glm-5.2", map[string]any{"task": "test"})
	if _, ok := other["temperature"]; ok {
		t.Fatalf("non-Codex request contains model-specific temperature: %#v", other)
	}
	claude := judgeRequest("anthropic/claude-fable-5", map[string]any{"task": "test"})
	if _, ok := claude["temperature"]; ok {
		t.Fatalf("Claude request contains deprecated temperature: %#v", claude)
	}
}

func TestManifestAndToolsStayAligned(t *testing.T) {
	manifest := (&App{}).Manifest()
	provided := make([]string, 0, len(manifest.Provides.MCPTools))
	for _, tool := range manifest.Provides.MCPTools {
		provided = append(provided, tool.Name)
	}
	runtime := make([]string, 0)
	for _, tool := range (&App{}).MCPTools() {
		runtime = append(runtime, tool.Name)
	}
	sort.Strings(provided)
	sort.Strings(runtime)
	if manifest.Name != "evals" || manifest.Version != "0.5.8" || !reflect.DeepEqual(provided, runtime) {
		t.Fatalf("manifest tools=%v runtime tools=%v", provided, runtime)
	}
	if manifest.Runtime.Source == nil || manifest.Runtime.Source.Ref != "evals/v"+manifest.Version {
		t.Fatalf("manifest runtime ref=%v version=%s", manifest.Runtime.Source, manifest.Version)
	}
}

func TestCreateToolSchemasExposeCanonicalWorkflow(t *testing.T) {
	tools := map[string]sdk.Tool{}
	for _, tool := range (&App{}).MCPTools() {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"eval_suite_create", "eval_case_create", "eval_experiment_create"} {
		schema := tools[name].InputSchema
		if schema == nil {
			t.Fatalf("tool %s has no schema", name)
		}
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("tool %s must reject unknown properties: %#v", name, schema)
		}
	}
	caseProperties, _ := tools["eval_case_create"].InputSchema["properties"].(map[string]any)
	for _, field := range []string{"suite_id", "name", "prompt", "goals", "assertions", "environment_id", "timeout_seconds", "max_turns"} {
		if _, found := caseProperties[field]; !found {
			t.Errorf("eval_case_create schema is missing %q", field)
		}
	}
	assertions, _ := caseProperties["assertions"].(map[string]any)
	assertionItems, _ := assertions["items"].(map[string]any)
	assertionProperties, _ := assertionItems["properties"].(map[string]any)
	for _, field := range []string{"name", "type", "path", "equals"} {
		if _, found := assertionProperties[field]; !found {
			t.Errorf("assertion schema is missing %q", field)
		}
	}
	experimentProperties, _ := tools["eval_experiment_create"].InputSchema["properties"].(map[string]any)
	targets, _ := experimentProperties["targets"].(map[string]any)
	targetItems, _ := targets["items"].(map[string]any)
	targetProperties, _ := targetItems["properties"].(map[string]any)
	for _, field := range []string{"agent_id", "provider", "model"} {
		if _, found := targetProperties[field]; !found {
			t.Errorf("target schema is missing %q", field)
		}
	}
	argsWithProject := map[string]any{"_project_id": "project-one", "suite_id": "suite-one", "targets": []any{map[string]any{"agent_id": 7.0}}}
	var decoded experimentInput
	if err := decodeStrictArgs(argsWithProject, &decoded); err != nil || decoded.SuiteID != "suite-one" || len(decoded.Targets) != 1 {
		t.Fatalf("trusted project context rejected: decoded=%#v err=%v", decoded, err)
	}
	if argsWithProject["_project_id"] != "project-one" {
		t.Fatal("strict decoder mutated caller arguments")
	}
	if _, err := (&App{}).toolCreateExperiment(nil, map[string]any{"suite_id": "suite-one", "targets": []any{}, "unexpected": true}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict experiment arguments error=%v", err)
	}
}

func TestPanelUsesProductionJSXRuntime(t *testing.T) {
	panel, err := os.ReadFile("ui/EvalsPanel.mjs")
	if err != nil {
		t.Fatal(err)
	}
	source := string(panel)
	if strings.Contains(source, "react/jsx-dev-runtime") || strings.Contains(source, "jsxDEV") {
		t.Fatal("EvalsPanel.mjs uses React's development JSX runtime")
	}
	if !strings.Contains(source, "react/jsx-runtime") {
		t.Fatal("EvalsPanel.mjs does not import React's production JSX runtime")
	}
}

type recordedAppCall struct {
	app   string
	tool  string
	input map[string]any
}

type evalPlatformStub struct {
	testkit.BasePlatformClient
	t     *testing.T
	calls []recordedAppCall
}

type evalCampaignPlatformStub struct {
	testkit.BasePlatformClient
	sdk.RuntimeClient
	t            *testing.T
	definitions  map[string]EnvironmentDefinition
	created      int
	spawned      int
	stopped      int
	asserted     int
	prompts      []string
	models       []LLMModel
	assertionErr error
	judgeCalls   int
}

func (s *evalCampaignPlatformStub) ListRuntimeCatalogAgents(string) ([]sdk.RuntimeCatalogAgent, error) {
	return []sdk.RuntimeCatalogAgent{{ID: 7, Name: "Test Agent", Directive: "Resolve support requests", DirectiveETag: "etag-one", ProjectID: "project-one"}}, nil
}

func (s *evalCampaignPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "llm" {
		var value any
		switch tool {
		case "llm_models_list":
			value = LLMModels{Models: s.models}
		case "llm_chat_complete":
			s.judgeCalls++
			value = map[string]any{
				"model":   "openai-codex/gpt-5.6-sol",
				"choices": []any{map[string]any{"message": map[string]any{"content": `{"passed":true,"score":100,"reasoning":"The goal was met.","per_goal":[{"goal":"Help the user","score":100,"passed":true,"why":"Met"}],"directive_suggestion":null}`}}},
			}
		default:
			s.t.Fatalf("unexpected app call %s/%s", app, tool)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	}
	if app != "environments" {
		s.t.Fatalf("unexpected app call %s/%s", app, tool)
	}
	var value any
	switch tool {
	case "environment_catalog":
		value = map[string]any{"assertion_types": []string{"app_state", "mcp_state", "mcp_tool_call", "edge_call", "telemetry", "web_state", "web_event", "protocol_event"}}
	case "environment_get":
		id, _ := input["id"].(string)
		value = s.definitions[id]
	case "environment_run_create":
		s.created++
		value = EnvironmentRun{ID: fmt.Sprintf("environment-run-%d", s.created), RuntimeID: fmt.Sprintf("runtime-%d", s.created), Status: "running"}
	case "environment_agent_spawn":
		s.spawned++
		value = sdk.RuntimeAgent{Alias: "main", Status: "paused"}
	case "environment_agent_send":
		message, _ := input["message"].(string)
		s.prompts = append(s.prompts, message)
		value = map[string]any{"ok": true}
	case "environment_agent_control":
		value = map[string]any{"ok": true}
	case "environment_agent_wait":
		value = sdk.RuntimeAgentExecution{
			Status:   "completed",
			ThreadID: "main",
			Turns:    1,
			Trace:    []sdk.RuntimeTraceEvent{{Index: 1, ThreadID: "main", Role: "assistant", Content: "support request resolved"}},
			Metrics:  sdk.RuntimeAgentMetrics{LLMCalls: 1, TokensIn: 8, TokensOut: 4},
		}
	case "environment_assert":
		s.asserted++
		if s.assertionErr != nil {
			return s.assertionErr
		}
		name, _ := input["name"].(string)
		value = AssertionResult{Name: name, Passed: true, Actual: "resolved"}
	case "environment_run_stop":
		s.stopped++
		value = map[string]any{"ok": true}
	default:
		s.t.Fatalf("unexpected app call %s/%s", app, tool)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestCaseRejectsUnsupportedAssertionBeforePersistence(t *testing.T) {
	platform := &evalCampaignPlatformStub{t: t, definitions: map[string]EnvironmentDefinition{}}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-assertions", Name: "Assertion validation"}, true); err != nil {
		t.Fatal(err)
	}
	_, err := svc.saveCase(&Case{
		ID: "case-unsupported", SuiteID: "suite-assertions", Name: "Unsupported", Prompt: "Help",
		Assertions: []Assertion{{Name: "Invented output check", Type: "invented_output_equals", Equals: "urgent"}},
	}, true)
	if err == nil || !strings.Contains(err.Error(), `unsupported assertion type "invented_output_equals"`) || !strings.Contains(err.Error(), "app_state") || !strings.Contains(err.Error(), "output_equals") || !strings.Contains(err.Error(), "For agent output, use goals") {
		t.Fatalf("unsupported assertion error=%v", err)
	}
	suite, err := svc.db.getSuite("suite-assertions")
	if err != nil || suite == nil || len(suite.Cases) != 0 {
		t.Fatalf("suite=%#v err=%v", suite, err)
	}
	_, err = svc.saveCase(&Case{
		ID: "case-invalid-output", SuiteID: "suite-assertions", Name: "Invalid output", Prompt: "Help",
		Assertions: []Assertion{{Name: "Exact output", Type: outputEqualsAssertionType, Equals: 7}},
	}, true)
	if err == nil || !strings.Contains(err.Error(), "output_equals requires equals to be a string") {
		t.Fatalf("output_equals type error=%v", err)
	}
}

func TestOutputEqualsUsesFinalAssistantMessageExactly(t *testing.T) {
	execution := &sdk.RuntimeAgentExecution{ThreadID: "main", Trace: []sdk.RuntimeTraceEvent{
		{Index: 1, ThreadID: "main", Role: "user", Content: "Classify this"},
		{Index: 2, ThreadID: "side", Role: "assistant", Content: "routine"},
		{Index: 3, ThreadID: "main", Role: "agent", Content: "urgent"},
	}}
	passed, err := evaluateOutputEquals(Assertion{Name: "Exact urgent", Type: outputEqualsAssertionType, Equals: "urgent"}, execution)
	if err != nil || !passed.Passed || passed.Actual != "urgent" {
		t.Fatalf("passed=%#v err=%v", passed, err)
	}
	failed, err := evaluateOutputEquals(Assertion{Name: "No punctuation", Type: outputEqualsAssertionType, Equals: "urgent."}, execution)
	if err != nil || failed.Passed || failed.Actual != "urgent" {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
}

func TestOutputEqualsRunIsEvaluatedInsideEvals(t *testing.T) {
	platform := &evalCampaignPlatformStub{t: t, definitions: map[string]EnvironmentDefinition{}}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-output", Name: "Native output"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveCase(&Case{
		ID: "case-output", SuiteID: "suite-output", Name: "Exact output", Prompt: "Resolve support request",
		Assertions: []Assertion{{Name: "Exact final message", Type: outputEqualsAssertionType, Equals: "support request resolved"}},
	}, true); err != nil {
		t.Fatal(err)
	}
	experiment, err := svc.createExperiment("suite-output", "", "manual", []Target{{AgentID: 7}}, 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.runNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed, err := svc.db.getExperiment(experiment.ID)
	if err != nil || completed == nil || len(completed.Runs) != 1 {
		t.Fatalf("experiment=%#v err=%v", completed, err)
	}
	run := completed.Runs[0]
	if run.Status != "pass" || run.CorrectnessScore == nil || *run.CorrectnessScore != 100 || run.OverallScore == nil || *run.OverallScore != 100 || len(run.Assertions) != 1 || !run.Assertions[0].Passed {
		t.Fatalf("run=%#v", run)
	}
	if platform.asserted != 0 || platform.stopped != 1 {
		t.Fatalf("environment assertions=%d stopped=%d", platform.asserted, platform.stopped)
	}
}

func TestAssertionExecutionErrorPreservesJudgeAndDoesNotScoreAgent(t *testing.T) {
	platform := &evalCampaignPlatformStub{
		t:            t,
		models:       []LLMModel{{Provider: "openai-codex", ModelID: "gpt-5.6-sol", GatewayModel: "openai-codex/gpt-5.6-sol"}},
		definitions:  map[string]EnvironmentDefinition{},
		assertionErr: errors.New("simulated fixture unavailable"),
	}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-assertion-error", Name: "Assertion error"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveCase(&Case{
		ID: "case-assertion-error", SuiteID: "suite-assertion-error", Name: "Case", Prompt: "Help", Goals: []string{"Help the user"},
		Assertions: []Assertion{{Name: "CRM state", Type: "app_state", App: "crm", Tool: "record_get", Path: "status", Equals: "done"}},
	}, true); err != nil {
		t.Fatal(err)
	}
	experiment, err := svc.createExperiment("suite-assertion-error", "", "manual", []Target{{AgentID: 7}}, 1, 0, "openai-codex/gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.runNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed, err := svc.db.getExperiment(experiment.ID)
	if err != nil || completed == nil || len(completed.Runs) != 1 {
		t.Fatalf("experiment=%#v err=%v", completed, err)
	}
	run := completed.Runs[0]
	if run.Status != "error" || run.Stage != "failed" || run.CorrectnessScore != nil || run.OverallScore != nil || run.Judge == nil || !run.Judge.Passed || run.JudgeScore == nil || *run.JudgeScore != 100 {
		t.Fatalf("run=%#v", run)
	}
	if len(run.Assertions) != 1 || !strings.Contains(run.Assertions[0].Error, "fixture unavailable") || !strings.Contains(run.Error, "does not indicate agent failure") {
		t.Fatalf("assertions=%#v run error=%q", run.Assertions, run.Error)
	}
	if platform.judgeCalls != 1 || platform.asserted != 1 || platform.stopped != 1 {
		t.Fatalf("judge=%d asserted=%d stopped=%d", platform.judgeCalls, platform.asserted, platform.stopped)
	}
}

func TestExperimentCanonicalizesJudgeModelBeforeQueueingRuns(t *testing.T) {
	platform := &evalCampaignPlatformStub{
		t: t,
		models: []LLMModel{
			{Provider: "openai-codex", ModelID: "gpt-5.6-sol", GatewayModel: "openai-codex/gpt-5.6-sol"},
		},
		definitions: map[string]EnvironmentDefinition{},
	}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-judge", Name: "Judge resolution"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveCase(&Case{ID: "case-judge", SuiteID: "suite-judge", Name: "Case", Prompt: "Help", Goals: []string{"Help the user"}}, true); err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.resolveJudgeModel("openai-codex/gpt-5.6-sol")
	if err != nil || resolved != "openai-codex/gpt-5.6-sol" {
		t.Fatalf("canonical judge model resolved=%q err=%v", resolved, err)
	}
	experiment, err := svc.createExperiment("suite-judge", "", "manual", []Target{{AgentID: 7}}, 1, 0, "gpt-5.6-sol")
	if err != nil {
		t.Fatal(err)
	}
	if experiment.JudgeModel != "openai-codex/gpt-5.6-sol" || len(experiment.Runs) != 1 {
		t.Fatalf("experiment=%#v", experiment)
	}
}

func TestExperimentRejectsUnknownOrAmbiguousJudgeModelBeforeQueueingRuns(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested string
		models    []LLMModel
		wantError string
	}{
		{name: "unknown", requested: "missing-model", models: []LLMModel{{Provider: "openai-codex", ModelID: "gpt-5.6-sol", GatewayModel: "openai-codex/gpt-5.6-sol"}}, wantError: "not available"},
		{name: "ambiguous", requested: "shared-model", models: []LLMModel{{Provider: "provider-a", ModelID: "shared-model", GatewayModel: "provider-a/shared-model"}, {Provider: "provider-b", ModelID: "shared-model", GatewayModel: "provider-b/shared-model"}}, wantError: "ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			platform := &evalCampaignPlatformStub{t: t, models: test.models, definitions: map[string]EnvironmentDefinition{}}
			ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
			svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
			if _, err := svc.saveSuite(&Suite{ID: "suite-reject", Name: "Judge rejection"}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.saveCase(&Case{ID: "case-reject", SuiteID: "suite-reject", Name: "Case", Prompt: "Help", Goals: []string{"Help the user"}}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.createExperiment("suite-reject", "", "manual", []Target{{AgentID: 7}}, 1, 0, test.requested); err == nil || !strings.Contains(err.Error(), test.wantError) || !strings.Contains(err.Error(), "eval_catalog.models[].gateway_model") {
				t.Fatalf("judge model error=%v", err)
			}
			experiments, err := svc.db.listExperiments(10)
			if err != nil || len(experiments) != 0 {
				t.Fatalf("experiments=%#v err=%v", experiments, err)
			}
		})
	}
}

func TestFourCaseCampaignRunsPromptsAssertionsAndStopsEnvironments(t *testing.T) {
	platform := &evalCampaignPlatformStub{
		t: t,
		definitions: map[string]EnvironmentDefinition{
			"env-one": {ID: "env-one", Name: "Support sandbox", Spec: map[string]any{"version": 1, "network_mode": "block", "integration_mode": "mock"}},
		},
	}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-four", Name: "Billing campaign", EnvironmentID: "env-one"}, true); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		_, err := svc.saveCase(&Case{
			ID:         fmt.Sprintf("billing-%d", i),
			SuiteID:    "suite-four",
			Name:       fmt.Sprintf("Billing case %d", i),
			Prompt:     fmt.Sprintf("support request %d", i),
			Assertions: []Assertion{{Name: "request resolved", Type: "app_state", App: "crm", Tool: "contact_get", Path: "status", Equals: "resolved"}},
		}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	experiment, err := svc.createExperiment("suite-four", "Four cases", "manual", []Target{{AgentID: 7, Provider: "openai", Model: "gpt-test"}}, 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(experiment.Runs) != 4 {
		t.Fatalf("created %d eval runs, want 4", len(experiment.Runs))
	}
	for range 4 {
		if err := svc.runNext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := svc.db.getExperiment(experiment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || len(completed.Runs) != 4 {
		t.Fatalf("experiment=%#v", completed)
	}
	for _, run := range completed.Runs {
		if run.Status != "pass" || run.Execution == nil || len(run.Execution.Trace) == 0 || len(run.Assertions) != 1 || !run.Assertions[0].Passed || run.EnvironmentRunID == "" {
			t.Errorf("incomplete persisted run=%#v", run)
		}
	}
	if platform.created != 4 || platform.spawned != 4 || len(platform.prompts) != 4 || platform.asserted != 4 || platform.stopped != 4 {
		t.Fatalf("orchestration created=%d spawned=%d prompts=%d asserted=%d stopped=%d", platform.created, platform.spawned, len(platform.prompts), platform.asserted, platform.stopped)
	}
	for _, prompt := range platform.prompts {
		if !strings.Contains(prompt, "support request") {
			t.Errorf("agent did not receive case prompt: %q", prompt)
		}
	}
}

func TestExperimentRejectsInvalidEnvironmentBeforeQueueingRuns(t *testing.T) {
	platform := &evalCampaignPlatformStub{t: t, definitions: map[string]EnvironmentDefinition{}}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}
	if _, err := svc.saveSuite(&Suite{ID: "suite-invalid-environment", Name: "Invalid environment", EnvironmentID: "env-missing"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveCase(&Case{ID: "case-one", SuiteID: "suite-invalid-environment", Name: "Case", Prompt: "Help", Assertions: []Assertion{{Name: "done", Type: "app_state"}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.createExperiment("suite-invalid-environment", "", "manual", []Target{{AgentID: 7}}, 1, 0, ""); err == nil || !strings.Contains(err.Error(), "not found or inaccessible") {
		t.Fatalf("invalid environment error=%v", err)
	}
	experiments, err := svc.db.listExperiments(10)
	if err != nil || len(experiments) != 0 {
		t.Fatalf("experiments=%#v err=%v", experiments, err)
	}
}

func (s *evalPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.calls = append(s.calls, recordedAppCall{app: app, tool: tool, input: input})
	var value any
	switch tool {
	case "environment_catalog":
		value = map[string]any{
			"assertion_types": []string{"app_state", "mcp_state", "mcp_tool_call", "edge_call", "telemetry", "web_state", "web_event", "protocol_event"},
			"apps":            []map[string]any{{"install_id": 12, "name": "crm"}},
			"connections":     []map[string]any{{"id": 4, "app_slug": "hubspot"}},
			"integrations":    []map[string]any{{"slug": "hubspot", "name": "HubSpot"}},
			"agents":          []map[string]any{{"id": 7, "name": "Sales"}},
			"snapshots":       []map[string]any{},
		}
	case "environment_list":
		value = []EnvironmentDefinition{{ID: "env-one", Name: "Project clone"}}
	case "llm_models_list":
		value = LLMModels{Models: []LLMModel{{Provider: "anthropic", ModelID: "claude-sonnet", GatewayModel: "anthropic/claude-sonnet"}}}
	case "environment_create":
		value = EnvironmentDefinition{ID: "env-created", Name: input["name"].(string), Spec: input["spec"].(map[string]any)}
	default:
		s.t.Fatalf("unexpected app call %s/%s", app, tool)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestCatalogAndEnvironmentCreationDelegateToEnvironments(t *testing.T) {
	platform := &evalPlatformStub{t: t}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("project-one"), testkit.WithPlatform(platform))
	svc := &service{ctx: ctx, db: store{db: ctx.AppDB()}}

	catalog, err := svc.catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog["apps"].([]any)) != 1 || len(catalog["environments"].([]EnvironmentDefinition)) != 1 || len(catalog["models"].([]LLMModel)) != 1 || len(catalog["assertion_types"].([]string)) != 9 {
		t.Fatalf("catalog=%#v", catalog)
	}

	input := map[string]any{"name": "Sales eval", "spec": map[string]any{"version": 1.0, "network_mode": "block"}}
	created, err := svc.createEnvironment(input)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "env-created" || len(platform.calls) != 4 || platform.calls[3].tool != "environment_create" {
		t.Fatalf("created=%#v calls=%#v", created, platform.calls)
	}
}
