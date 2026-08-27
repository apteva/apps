package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const (
	testProject = "project-builder"
	testAgent   = int64(41)
	testThread  = "conversation-builder"
)

func TestManifestMatchesRuntimeContract(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	runtime := (&App{}).Manifest()
	if runtime.Name != "builder" || runtime.Version != "0.2.0" {
		t.Fatalf("runtime identity = %s@%s", runtime.Name, runtime.Version)
	}
	if runtime.Name != onDisk.Name || runtime.Version != onDisk.Version {
		t.Fatalf("embedded manifest drifted from apteva.yaml")
	}
	if !containsPermission(runtime.Requires.Permissions, sdk.PermMCPAttach) {
		t.Fatal("platform.mcp.attach permission missing")
	}
	if len(runtime.Requires.Apps) != 1 || runtime.Requires.Apps[0].Name != "conversations" || runtime.Requires.Apps[0].Optional {
		t.Fatalf("requires.apps = %+v, want required conversations", runtime.Requires.Apps)
	}
	if len(runtime.Provides.UIPanels) != 1 || runtime.Provides.UIPanels[0].Entry != "/ui/BuilderPanel.mjs" {
		t.Fatalf("Builder panel missing: %+v", runtime.Provides.UIPanels)
	}
	if len(runtime.Provides.UIComponents) != 1 || runtime.Provides.UIComponents[0].Name != "builder-workspace" {
		t.Fatalf("Builder workspace contribution missing: %+v", runtime.Provides.UIComponents)
	}
	wantTools := []string{"goal_start", "goal_list", "goal_get", "validation_set", "plan_set", "step_update", "resource_upsert", "check_record", "event_record", "goal_update"}
	gotTools := make([]string, 0, len(runtime.Provides.MCPTools))
	for _, tool := range runtime.Provides.MCPTools {
		gotTools = append(gotTools, tool.Name)
	}
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Fatalf("manifest tools = %v, want %v", gotTools, wantTools)
	}
	app := &App{}
	runtimeTools := app.MCPTools()
	gotRuntimeTools := make([]string, 0, len(runtimeTools))
	for _, tool := range runtimeTools {
		gotRuntimeTools = append(gotRuntimeTools, tool.Name)
	}
	if !reflect.DeepEqual(gotRuntimeTools, wantTools) {
		t.Fatalf("runtime tools = %v, want %v", gotRuntimeTools, wantTools)
	}
	if len(runtime.Provides.Skills) != 1 || runtime.Provides.Skills[0].Body == "" || runtime.Provides.Skills[0].BodyFile != "" {
		t.Fatalf("runtime skill was not embedded: %+v", runtime.Provides.Skills)
	}
	if len(app.Workers()) != 0 {
		t.Fatal("Builder must not run a heartbeat worker")
	}
}

func TestGoalLifecycleAndCompletionGate(t *testing.T) {
	app, appCtx := newTestApp(t)
	call := callerContext(testAgent, testThread, testProject, "call-create")
	createdRaw, err := app.toolGoalStart(call, appCtx, map[string]any{
		"title": "Launch research workspace", "objective": "Create a verified research-agent workspace",
		"success_criteria": []any{"Research agent is running", "Conversations can reach the agent"},
		"constraints":      []any{"Do not publish externally"}, "idempotency_key": "research-workspace",
	})
	if err != nil {
		t.Fatalf("goal_start: %v", err)
	}
	created := createdRaw.(map[string]any)
	goal := created["goal"].(*Goal)
	if created["created"] != true || goal.ProjectID != testProject || goal.OwnerAgentID != testAgent {
		t.Fatalf("created goal = %+v, receipt=%+v", goal, created)
	}

	repeatRaw, err := app.toolGoalStart(callerContext(testAgent, testThread, testProject, "call-create-repeat"), appCtx, map[string]any{
		"title": "Ignored retry title", "objective": "Ignored retry objective",
		"success_criteria": []string{"Ignored"}, "idempotency_key": "research-workspace",
	})
	if err != nil {
		t.Fatalf("idempotent goal_start: %v", err)
	}
	repeat := repeatRaw.(map[string]any)
	if repeat["created"] != false || repeat["goal"].(*Goal).ID != goal.ID {
		t.Fatalf("idempotent receipt = %+v", repeat)
	}
	if _, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "call-before-plan"), appCtx, map[string]any{"goal_id": goal.ID, "status": "completed"}); err == nil {
		t.Fatal("goal completed before a plan existed")
	}

	planRaw, err := app.toolPlanSet(callerContext(testAgent, testThread, testProject, "call-plan"), appCtx, map[string]any{
		"goal_id": goal.ID,
		"steps": []any{
			map[string]any{"title": "Inspect project", "detail": "Inventory current agents and apps"},
			map[string]any{"title": "Create research agent", "detail": "Create and configure the agent", "requires_approval": true},
		},
		"checks": []any{
			map[string]any{"key": "agent-running", "name": "Research agent is running"},
			map[string]any{"key": "conversation-ready", "name": "Conversation is ready"},
		},
	})
	if err != nil {
		t.Fatalf("plan_set: %v", err)
	}
	plan := planRaw.(*GoalBundle)
	if len(plan.Steps) != 2 || len(plan.Checks) != 2 || plan.Goal.Status != goalActive {
		t.Fatalf("plan bundle = %+v", plan)
	}
	completed := goalCompleted
	if _, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "call-too-early"), appCtx, map[string]any{"goal_id": goal.ID, "status": completed}); err == nil {
		t.Fatal("goal completed before steps/checks passed")
	}

	stepOne := plan.Steps[0]
	for index, status := range []string{"active", "completed"} {
		_, err = app.toolStepUpdate(callerContext(testAgent, testThread, testProject, "step-one-"+status), appCtx, map[string]any{
			"goal_id": goal.ID, "step_id": stepOne.ID, "status": status, "note": "phase one " + status,
		})
		if err != nil {
			t.Fatalf("step one update %d: %v", index, err)
		}
	}

	stepTwo := plan.Steps[1]
	waitingRaw, err := app.toolStepUpdate(callerContext(testAgent, testThread, testProject, "step-two-approval"), appCtx, map[string]any{
		"goal_id": goal.ID, "step_id": stepTwo.ID, "status": "waiting_approval", "note": "Approval requested in Conversation",
	})
	if err != nil {
		t.Fatalf("step waiting approval: %v", err)
	}
	waiting := waitingRaw.(*GoalBundle)
	if waiting.Goal.Status != goalWaitingApproval || waiting.Steps[1].ApprovalState != "requested" {
		t.Fatalf("approval state = goal %s step %+v", waiting.Goal.Status, waiting.Steps[1])
	}
	if _, err := app.toolStepUpdate(callerContext(testAgent, testThread, testProject, "step-two-bypass"), appCtx, map[string]any{
		"goal_id": goal.ID, "step_id": stepTwo.ID, "status": "completed", "note": "Tried to bypass approval",
	}); err == nil {
		t.Fatal("approval-gated step completed before approval")
	}
	approved := "approved"
	active := "active"
	if _, err := app.store.UpdateStep(GoalIdentity{ProjectID: testProject, OwnerAgentID: testAgent}, goal.ID, stepTwo.ID, UpdateStepInput{
		Status: &active, ApprovalState: &approved, Note: "Operator approved", ActorAgentID: testAgent, ActorThreadID: testThread, EventKey: "approved",
	}); err != nil {
		t.Fatalf("approve step: %v", err)
	}
	if _, err := app.toolStepUpdate(callerContext(testAgent, testThread, testProject, "step-two-complete"), appCtx, map[string]any{
		"goal_id": goal.ID, "step_id": stepTwo.ID, "status": "completed", "note": "Agent created and configured",
	}); err != nil {
		t.Fatalf("complete step: %v", err)
	}

	resourceRaw, err := app.toolResourceUpsert(callerContext(testAgent, testThread, testProject, "resource-agent"), appCtx, map[string]any{
		"goal_id": goal.ID, "key": "agent:research", "kind": "agent", "name": "Research Agent", "external_id": "agent-77", "status": "ready",
		"desired_state": map[string]any{"mode": "handsfree"}, "observed_state": map[string]any{"status": "running"},
	})
	if err != nil {
		t.Fatalf("resource_upsert: %v", err)
	}
	if resourceRaw.(map[string]any)["resource"].(*ManagedResource).ExternalID != "agent-77" {
		t.Fatalf("resource = %+v", resourceRaw)
	}
	resourceUpdateRaw, err := app.toolResourceUpsert(callerContext(testAgent, testThread, testProject, "resource-agent-drift"), appCtx, map[string]any{
		"goal_id": goal.ID, "key": "agent:research", "kind": "agent", "name": "Research Agent", "status": "drifted",
	})
	if err != nil {
		t.Fatalf("partial resource_upsert: %v", err)
	}
	resourceUpdate := resourceUpdateRaw.(map[string]any)["resource"].(*ManagedResource)
	if resourceUpdate.ExternalID != "agent-77" || resourceUpdate.DesiredState["mode"] != "handsfree" || resourceUpdate.ObservedState["status"] != "running" {
		t.Fatalf("partial resource update lost state: %+v", resourceUpdate)
	}

	if _, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "call-before-checks"), appCtx, map[string]any{"goal_id": goal.ID, "status": completed}); err == nil {
		t.Fatal("goal completed while checks were pending")
	}
	for _, check := range plan.Checks {
		if _, err := app.toolCheckRecord(callerContext(testAgent, testThread, testProject, "check-"+check.Key), appCtx, map[string]any{
			"goal_id": goal.ID, "key": check.Key, "name": check.Name, "status": "passing", "result": "Verified from platform state",
			"evidence": map[string]any{"agent_id": 77, "status": "running"},
		}); err != nil {
			t.Fatalf("check %s: %v", check.Key, err)
		}
	}

	finalRaw, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "call-complete"), appCtx, map[string]any{
		"goal_id": goal.ID, "status": "completed", "summary": "Research workspace is ready and verified",
	})
	if err != nil {
		t.Fatalf("goal complete: %v", err)
	}
	final := finalRaw.(*GoalBundle)
	if final.Goal.Status != goalCompleted || final.Goal.CompletedAt == "" || !final.Completion.Ready || len(final.Resources) != 1 {
		t.Fatalf("final bundle = %+v", final)
	}
}

func TestGoalScopeIsHelperAndProjectBound(t *testing.T) {
	app, _ := newTestApp(t)
	goal, _, err := app.store.CreateGoal(CreateGoalInput{
		ProjectID: testProject, OwnerAgentID: testAgent, ThreadID: testThread,
		Title: "Scoped", Objective: "Remain scoped", SuccessCriteria: []string{"Scoped"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.GetGoal(GoalIdentity{ProjectID: "other-project", OwnerAgentID: testAgent}, goal.ID); err == nil {
		t.Fatal("goal crossed project boundary")
	}
	if _, err := app.store.GetGoal(GoalIdentity{ProjectID: testProject, OwnerAgentID: testAgent + 1}, goal.ID); err == nil {
		t.Fatal("goal crossed Helper boundary")
	}
	if _, _, err := builderIdentity(context.Background(), nil); err == nil {
		t.Fatal("missing trusted caller was accepted")
	}
}

func TestOptionalValidationPolicyAndCompletionGate(t *testing.T) {
	app, appCtx := newTestApp(t)
	createdRaw, err := app.toolGoalStart(callerContext(testAgent, testThread, testProject, "validation-create"), appCtx, map[string]any{
		"title": "Validate support workflow", "objective": "Build and validate a support workflow in a virtual world",
		"success_criteria":  []any{"The workflow passes its behavioral scenarios"},
		"validation_mode":   "simulated",
		"validation_policy": map[string]any{"max_runs": 8, "max_repair_attempts": 1, "auto_repair": true},
		"idempotency_key":   "validate-support-workflow",
	})
	if err != nil {
		t.Fatalf("goal_start with validation: %v", err)
	}
	goal := createdRaw.(map[string]any)["goal"].(*Goal)
	if goal.ValidationMode != validationSimulated || goal.ValidationPolicy.MaxRuns != 8 || goal.ValidationPolicy.MaxRepairAttempts != 1 || !goal.ValidationPolicy.InstallSafeApps {
		t.Fatalf("validation policy = mode %s policy %+v", goal.ValidationMode, goal.ValidationPolicy)
	}

	planRaw, err := app.toolPlanSet(callerContext(testAgent, testThread, testProject, "validation-plan"), appCtx, map[string]any{
		"goal_id": goal.ID,
		"steps":   []any{map[string]any{"title": "Build and test workflow"}},
		"checks":  []any{map[string]any{"key": "workflow-ready", "name": "Workflow is ready"}},
	})
	if err != nil {
		t.Fatalf("plan_set: %v", err)
	}
	plan := planRaw.(*GoalBundle)
	validationCheck := bundleCheck(plan, validationCheckKey)
	if len(plan.Checks) != 2 || validationCheck == nil || validationCheck.Status != "pending" {
		t.Fatalf("validation completion check = %+v", plan.Checks)
	}
	if _, err := app.toolStepUpdate(callerContext(testAgent, testThread, testProject, "validation-step"), appCtx, map[string]any{
		"goal_id": goal.ID, "step_id": plan.Steps[0].ID, "status": "completed", "note": "Build complete",
	}); err != nil {
		t.Fatalf("complete step: %v", err)
	}
	if _, err := app.toolCheckRecord(callerContext(testAgent, testThread, testProject, "validation-ready-check"), appCtx, map[string]any{
		"goal_id": goal.ID, "key": "workflow-ready", "name": "Workflow is ready", "status": "passing", "result": "Ready",
	}); err != nil {
		t.Fatalf("ordinary check: %v", err)
	}
	if _, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "validation-premature-complete"), appCtx, map[string]any{
		"goal_id": goal.ID, "status": "completed",
	}); err == nil {
		t.Fatal("simulated goal completed before virtual-world validation passed")
	}
	if _, err := app.toolCheckRecord(callerContext(testAgent, testThread, testProject, "validation-empty-evidence"), appCtx, map[string]any{
		"goal_id": goal.ID, "key": validationCheckKey, "name": "Virtual-world workflow validation", "status": "passing", "result": "Passed",
	}); err == nil {
		t.Fatal("validation passed without authoritative evidence")
	}
	if _, err := app.toolCheckRecord(callerContext(testAgent, testThread, testProject, "validation-passed"), appCtx, map[string]any{
		"goal_id": goal.ID, "key": validationCheckKey, "name": "Virtual-world workflow validation", "status": "passing", "result": "8/8 scenarios passed",
		"evidence": map[string]any{"environment_run_id": "env-run-1", "eval_experiment_id": "experiment-1", "run_count": 8, "pass_rate": 1.0, "agent_ids": []any{101}},
	}); err != nil {
		t.Fatalf("record validation: %v", err)
	}
	if _, err := app.toolGoalUpdate(callerContext(testAgent, testThread, testProject, "validation-complete"), appCtx, map[string]any{
		"goal_id": goal.ID, "status": "completed", "summary": "Workflow passed isolated validation",
	}); err != nil {
		t.Fatalf("complete validated goal: %v", err)
	}
}

func TestValidationCanBeEnabledOrDisabledBeforeCompletion(t *testing.T) {
	app, appCtx := newTestApp(t)
	goal, _, err := app.store.CreateGoal(CreateGoalInput{
		ProjectID: testProject, OwnerAgentID: testAgent, ThreadID: testThread,
		Title: "Optional validation", Objective: "Choose validation later", SuccessCriteria: []string{"Ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if goal.ValidationMode != validationBuildOnly || goal.ValidationPolicy.MaxRuns != 0 {
		t.Fatalf("default validation = %s %+v", goal.ValidationMode, goal.ValidationPolicy)
	}
	planRaw, err := app.toolPlanSet(callerContext(testAgent, testThread, testProject, "optional-plan"), appCtx, map[string]any{
		"goal_id": goal.ID, "steps": []any{map[string]any{"title": "Build"}}, "checks": []any{map[string]any{"key": "ready", "name": "Ready"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planRaw.(*GoalBundle).Checks) != 1 {
		t.Fatal("build-only plan unexpectedly required virtual-world validation")
	}
	enabledRaw, err := app.toolValidationSet(callerContext(testAgent, testThread, testProject, "optional-enable"), appCtx, map[string]any{
		"goal_id": goal.ID, "mode": "continuous", "policy": map[string]any{"max_runs": 12, "run_on_change": true},
	})
	if err != nil {
		t.Fatalf("enable validation: %v", err)
	}
	enabled := enabledRaw.(*GoalBundle)
	if enabled.Goal.ValidationMode != validationContinuous || len(enabled.Checks) != 2 || bundleCheck(enabled, validationCheckKey) == nil {
		t.Fatalf("enabled bundle = %+v", enabled)
	}
	disabledRaw, err := app.toolValidationSet(callerContext(testAgent, testThread, testProject, "optional-disable"), appCtx, map[string]any{
		"goal_id": goal.ID, "mode": "build_only",
	})
	if err != nil {
		t.Fatalf("disable validation: %v", err)
	}
	disabled := disabledRaw.(*GoalBundle)
	if disabled.Goal.ValidationMode != validationBuildOnly || len(disabled.Checks) != 1 {
		t.Fatalf("disabled bundle = %+v", disabled)
	}
	if _, err := app.toolValidationSet(callerContext(testAgent, testThread, testProject, "optional-invalid"), appCtx, map[string]any{
		"goal_id": goal.ID, "mode": "production",
	}); err == nil {
		t.Fatal("invalid validation mode accepted")
	}
}

func TestGoalHTTPReadModel(t *testing.T) {
	app, _ := newTestApp(t)
	goal, _, err := app.store.CreateGoal(CreateGoalInput{
		ProjectID: testProject, OwnerAgentID: testAgent, ThreadID: testThread,
		Title: "Visible project", Objective: "Show the durable Builder state", SuccessCriteria: []string{"Visible"},
	})
	if err != nil {
		t.Fatal(err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/goals?project_id="+testProject+"&owner_agent_id=41", nil)
	listRec := httptest.NewRecorder()
	app.handleGoals(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var list goalListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Goals) != 1 || list.Goals[0].ID != goal.ID {
		t.Fatalf("goals = %+v", list.Goals)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/goals/"+goal.ID+"?project_id="+testProject+"&owner_agent_id=41", nil)
	detailRec := httptest.NewRecorder()
	app.handleGoals(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}
	var bundle GoalBundle
	if err := json.Unmarshal(detailRec.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Goal == nil || bundle.Goal.ID != goal.ID || bundle.Steps == nil || bundle.Checks == nil || bundle.Resources == nil || bundle.Events == nil {
		t.Fatalf("bundle = %+v", bundle)
	}

	wrongScope := httptest.NewRecorder()
	app.handleGoals(wrongScope, httptest.NewRequest(http.MethodGet, "/goals/"+goal.ID+"?project_id="+testProject+"&owner_agent_id=42", nil))
	if wrongScope.Code != http.StatusNotFound {
		t.Fatalf("wrong owner status=%d body=%s", wrongScope.Code, wrongScope.Body.String())
	}
	missingScope := httptest.NewRecorder()
	app.handleGoals(missingScope, httptest.NewRequest(http.MethodGet, "/goals", nil))
	if missingScope.Code != http.StatusBadRequest {
		t.Fatalf("missing scope status=%d", missingScope.Code)
	}
}

type setupPlatform struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	requests []sdk.EnsureAppToolsRequest
	errors   []error
	result   *sdk.EnsureAppToolsResult
}

func (p *setupPlatform) EnsureAppToolsAttached(req sdk.EnsureAppToolsRequest) (*sdk.EnsureAppToolsResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
	if len(p.errors) > 0 {
		err := p.errors[0]
		p.errors = p.errors[1:]
		return nil, err
	}
	return p.result, nil
}

func TestStartupReconciliationRetriesUntilAttached(t *testing.T) {
	platform := &setupPlatform{
		errors: []error{&sdk.AgentToolsError{StatusCode: 409, Code: "caller_tools_not_ready", Message: "retry"}},
		result: &sdk.EnsureAppToolsResult{AgentID: 9, AttachedInstallIDs: []int64{11, 12}, MCPServerIDs: []int64{21, 22}, Changed: true, Applied: true},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	app := &App{retryDelays: []time.Duration{0, 0}}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := app.currentSetupStatus()
		if status.State == setupAttached {
			if status.Attempts != 2 || status.Result == nil || !status.Result.Applied {
				t.Fatalf("setup status = %+v", status)
			}
			platform.mu.Lock()
			requests := append([]sdk.EnsureAppToolsRequest(nil), platform.requests...)
			platform.mu.Unlock()
			if len(requests) != 2 || requests[1].AgentKind != sdk.AgentKindPlatformHelper || !reflect.DeepEqual(requests[1].IncludeRequiredApps, []string{"conversations"}) {
				t.Fatalf("requests = %+v", requests)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reconciliation did not attach: %+v", app.currentSetupStatus())
}

func TestSetupErrorClassification(t *testing.T) {
	tests := []struct {
		code      string
		retryable bool
		state     string
	}{
		{"caller_tools_not_ready", true, setupPending},
		{"required_app_not_bound", true, setupPending},
		{"target_agent_not_found", true, setupActionNeeded},
		{"permission_denied", false, setupFailed},
	}
	for _, test := range tests {
		err := &sdk.AgentToolsError{Code: test.code, Message: test.code}
		if retryableSetupError(err) != test.retryable || setupStateForError(err) != test.state {
			t.Fatalf("classification %s: retry=%v state=%s", test.code, retryableSetupError(err), setupStateForError(err))
		}
	}
	if !retryableSetupError(errors.New("network")) {
		t.Fatal("local transport error was not considered retryable")
	}
}

func newTestApp(t *testing.T) (*App, *sdk.AppCtx) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	return app, ctx
}

func callerContext(agentID int64, threadID, projectID, callID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AgentID: agentID, ThreadID: threadID, ProjectID: projectID, ToolCallID: callID,
	})
}

func containsPermission(values []sdk.Permission, wanted sdk.Permission) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func bundleCheck(bundle *GoalBundle, key string) *GoalCheck {
	for _, check := range bundle.Checks {
		if check.Key == key {
			return check
		}
	}
	return nil
}
