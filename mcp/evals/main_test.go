package main

import (
	"database/sql"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) store {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/evals.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
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
	item := &Case{ID: "case-one", SuiteID: suite.ID, Name: "Create record", Prompt: "Create it", Goals: []string{"Record exists"}, Assertions: []Assertion{{Name: "record", Type: "app_state", App: "crm", Tool: "contacts_get"}}, Enabled: true}
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
	if got == nil || len(got.Runs) != 2 || got.Runs[0].CaseSnapshot.Prompt != "Create it" {
		t.Fatalf("experiment=%#v", got)
	}

	claimed, err := db.claimRun()
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
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

func TestParseJudgeToleratesSurroundingTextAndBoundsScore(t *testing.T) {
	verdict, err := parseJudge("result:\n```json\n{\"passed\":false,\"score\":120,\"reasoning\":\"missing tool call\",\"per_goal\":[]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Passed || verdict.Score != 100 || verdict.Reasoning == "" {
		t.Fatalf("verdict=%#v", verdict)
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
	if manifest.Name != "evals" || manifest.Version != "0.1.1" || !reflect.DeepEqual(provided, runtime) {
		t.Fatalf("manifest tools=%v runtime tools=%v", provided, runtime)
	}
}
