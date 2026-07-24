package main

import (
	"database/sql"
	"encoding/json"
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
	for _, path := range []string{"migrations/001_init.sql", "migrations/002_voice_cases.sql", "migrations/003_run_progress.sql"} {
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
		Voice:      &VoiceCase{CallerGoal: "Book an appointment for tomorrow", CallerPersona: "A concise customer", MaxFirstResponseMS: 2000, Transport: "carrier", ProtocolFixture: "telephony-carrier"},
		Enabled:    true,
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
	if got == nil || len(got.Runs) != 2 || got.Runs[0].CaseSnapshot.Mode != "voice" || got.Runs[0].CaseSnapshot.Voice == nil || got.Runs[0].CaseSnapshot.Voice.CallerGoal != "Book an appointment for tomorrow" || got.Runs[0].CaseSnapshot.Voice.Transport != "carrier" {
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

func TestJudgeRequestOmitsUnsupportedCodexTemperature(t *testing.T) {
	codex := judgeRequest("openai-codex/gpt-5.6-terra", map[string]any{"task": "test"})
	if _, ok := codex["temperature"]; ok {
		t.Fatalf("Codex request contains unsupported temperature: %#v", codex)
	}
	other := judgeRequest("opencode-go/glm-5.2", map[string]any{"task": "test"})
	if temperature, ok := other["temperature"]; !ok || temperature != 0 {
		t.Fatalf("non-Codex request temperature=%#v", temperature)
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
	if manifest.Name != "evals" || manifest.Version != "0.4.0" || !reflect.DeepEqual(provided, runtime) {
		t.Fatalf("manifest tools=%v runtime tools=%v", provided, runtime)
	}
	if manifest.Runtime.Source == nil || manifest.Runtime.Source.Ref != "evals/v"+manifest.Version {
		t.Fatalf("manifest runtime ref=%v version=%s", manifest.Runtime.Source, manifest.Version)
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

func (s *evalPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.calls = append(s.calls, recordedAppCall{app: app, tool: tool, input: input})
	var value any
	switch tool {
	case "environment_catalog":
		value = map[string]any{
			"apps":         []map[string]any{{"install_id": 12, "name": "crm"}},
			"connections":  []map[string]any{{"id": 4, "app_slug": "hubspot"}},
			"integrations": []map[string]any{{"slug": "hubspot", "name": "HubSpot"}},
			"agents":       []map[string]any{{"id": 7, "name": "Sales"}},
			"snapshots":    []map[string]any{},
		}
	case "environment_list":
		value = []EnvironmentDefinition{{ID: "env-one", Name: "Project clone"}}
	case "llm_models_list":
		value = LLMModels{Models: []map[string]any{{"gateway_model": "anthropic/claude-sonnet"}}}
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
	if len(catalog["apps"].([]any)) != 1 || len(catalog["environments"].([]EnvironmentDefinition)) != 1 || len(catalog["models"].([]map[string]any)) != 1 {
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
