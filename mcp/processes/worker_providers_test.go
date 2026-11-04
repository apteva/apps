package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func providerSetup(t *testing.T, d Definition, configure func(*AssignmentConfig)) (*App, *directPlatform, *Process, Run) {
	t.Helper()
	a, f, _ := directSetup(t)
	p := create(t, a, d)
	x := p.Assignments[0]
	c := x.AssignmentConfig
	configure(&c)
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "provider", "")
	if err != nil {
		t.Fatal(err)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}

func workerRequest(t *testing.T, f *directPlatform, thread string) sdk.ThreadSpawnRequest {
	t.Helper()
	for _, request := range f.threads {
		if request.ThreadID == thread {
			return request
		}
	}
	t.Fatalf("no spawn request for %s in %+v", thread, f.threads)
	return sdk.ThreadSpawnRequest{}
}

func TestNoProviderLeavesWorkersOnTheirAgent(t *testing.T) {
	a, f, _, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) {})
	s := stepBy(t, a, r, "research")
	if got := workerRequest(t, f, s.ThreadID).Provider; got != "" {
		t.Fatalf("unset assignment pinned %q", got)
	}
	if s.Provider != nil {
		t.Fatalf("unpinned step reported %+v", s.Provider)
	}
	raw, err := json.Marshal(r.Binding)
	if err != nil || strings.Contains(string(raw), "provider") {
		t.Fatalf("unset assignment stored provider fields: %s %v", raw, err)
	}
}

func TestAssignmentProviderPinsEveryWorkerAndReportsIt(t *testing.T) {
	a, f, p, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) {
		c.WorkerContinuity, c.Provider = "isolated", "opencode-go"
	})
	one := stepBy(t, a, r, "research")
	if got := workerRequest(t, f, one.ThreadID).Provider; got != "opencode-go" {
		t.Fatalf("first worker pinned %q", got)
	}
	if one.Provider == nil || one.Provider.Pin != "opencode-go" || one.Provider.Active != "opencode-go" || one.Provider.Missing {
		t.Fatalf("step status %+v", one.Provider)
	}
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	two := stepBy(t, a, r, "write")
	if two.ThreadID == one.ThreadID || workerRequest(t, f, two.ThreadID).Provider != "opencode-go" {
		t.Fatalf("second worker %+v", workerRequest(t, f, two.ThreadID))
	}
}

func TestStepProviderOverridesAssignmentAndSplitsAutoWorker(t *testing.T) {
	a, f, p, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) {
		c.Provider = "openai-codex"
		c.StepProviders = map[string]string{"write": "opencode-go"}
	})
	one := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	two := stepBy(t, a, r, "write")
	if two.ThreadID == "" || two.ThreadID == one.ThreadID {
		t.Fatalf("differing providers shared a worker: %q %q", one.ThreadID, two.ThreadID)
	}
	if workerRequest(t, f, one.ThreadID).Provider != "openai-codex" || workerRequest(t, f, two.ThreadID).Provider != "opencode-go" {
		t.Fatalf("workers %+v", f.threads)
	}
}

func TestOverrideOnOneStepLeavesOthersOnTheAgent(t *testing.T) {
	a, f, p, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) {
		c.StepProviders = map[string]string{"write": "opencode-go"}
	})
	one := stepBy(t, a, r, "research")
	if got := workerRequest(t, f, one.ThreadID).Provider; got != "" {
		t.Fatalf("step with no override pinned %q", got)
	}
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	if got := workerRequest(t, f, stepBy(t, a, r, "write").ThreadID).Provider; got != "opencode-go" {
		t.Fatalf("overridden step pinned %q", got)
	}
}

func TestSameProviderKeepsAutoWorkerShared(t *testing.T) {
	a, f, p, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) { c.Provider = "opencode-go" })
	one := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	two := stepBy(t, a, r, "write")
	if two.ThreadID != one.ThreadID || len(f.threads) != 1 {
		t.Fatalf("one provider should reuse the worker: %q %q threads=%d", one.ThreadID, two.ThreadID, len(f.threads))
	}
	if two.Provider == nil || two.Provider.Pin != "opencode-go" {
		t.Fatalf("reused worker lost its status: %+v", two.Provider)
	}
}

func TestSameProcedureDifferentAssignments(t *testing.T) {
	a, f, p, _ := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) { c.Provider = "opencode-go" })
	c := p.Assignments[0].AssignmentConfig
	c.Name, c.Provider = "Default provider", ""
	x, err := a.saveAssignment(p.ProjectID, p.ID, "", 0, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.assignmentStatus(p.ProjectID, p.ID, x.ID, "active"); err != nil {
		t.Fatal(err)
	}
	raw, err := a.startAssignment(p.ProjectID, p.ID, x.ID, "second", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := stepBy(t, a, raw.(map[string]any)["run"].(Run), "research")
	if got := workerRequest(t, f, s.ThreadID).Provider; got != "" {
		t.Fatalf("default assignment pinned %q", got)
	}
}

func TestEditingAssignmentDoesNotMoveARunningRun(t *testing.T) {
	a, f, p, r := providerSetup(t, workflowDefinition(), func(c *AssignmentConfig) {
		c.WorkerContinuity, c.Provider = "isolated", "opencode-go"
	})
	x, err := a.assignmentStatus(p.ProjectID, p.ID, r.AssignmentID, "paused")
	if err != nil {
		t.Fatal(err)
	}
	c := x.AssignmentConfig
	c.Provider = "anthropic"
	if _, err = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	one := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	if got := workerRequest(t, f, stepBy(t, a, r, "write").ThreadID).Provider; got != "opencode-go" {
		t.Fatalf("running run moved to %q", got)
	}
}

func TestProviderValidation(t *testing.T) {
	d := Definition{Steps: []Step{{Key: "a", Role: "x"}, {Key: "b", Role: "x"}, {Key: "c", Role: "y"}, {Key: "h", Role: "human"}}}
	roles := map[string]Executor{"x": {Kind: "agent", AgentID: 7}, "y": {Kind: "agent", AgentID: 8}, "human": {Kind: "human"}}
	for _, test := range []struct {
		name string
		d    Definition
		c    AssignmentConfig
		want string
	}{
		{name: "nothing set", d: Definition{}, c: AssignmentConfig{}},
		{name: "no steps", d: Definition{}, c: AssignmentConfig{Provider: "anthropic"}, want: "structured steps"},
		{name: "unknown step", d: d, c: AssignmentConfig{Roles: roles, StepProviders: map[string]string{"gone": "anthropic"}}, want: "step gone"},
		{name: "human step", d: d, c: AssignmentConfig{Roles: roles, StepProviders: map[string]string{"h": "anthropic"}}, want: "a human performs"},
		{name: "auto mix allowed", d: d, c: AssignmentConfig{Roles: roles, StepProviders: map[string]string{"a": "anthropic"}}},
		{name: "isolated mix allowed", d: d, c: AssignmentConfig{Roles: roles, WorkerContinuity: "isolated", StepProviders: map[string]string{"a": "anthropic"}}},
		{name: "per executor mix refused", d: d, c: AssignmentConfig{Roles: roles, WorkerContinuity: "per_executor", Provider: "openai-codex", StepProviders: map[string]string{"a": "anthropic"}}, want: "steps a and b"},
		{name: "per executor same agent", d: d, c: AssignmentConfig{Roles: roles, WorkerContinuity: "per_executor", StepProviders: map[string]string{"a": "anthropic", "b": "anthropic"}}},
		{name: "per executor different agents", d: d, c: AssignmentConfig{Roles: roles, WorkerContinuity: "per_executor", StepProviders: map[string]string{"c": "anthropic"}}},
		{name: "per executor human ignored", d: d, c: AssignmentConfig{Roles: roles, WorkerContinuity: "per_executor", Provider: "anthropic"}},
		{name: "per executor unbound role uses owner", d: d, c: AssignmentConfig{OwnerAgentID: 7, Roles: map[string]Executor{"human": {Kind: "human"}}, WorkerContinuity: "per_executor", StepProviders: map[string]string{"a": "anthropic"}}, want: "steps a and b"},
	} {
		err := validateWorkerProviders(test.d, test.c)
		if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
			t.Errorf("%s: %v, want %q", test.name, err, test.want)
		}
	}
}

func TestUnknownProviderRefusedAtSave(t *testing.T) {
	a, _, _ := directSetup(t)
	p := create(t, a, workflowDefinition())
	x := p.Assignments[0]
	for _, change := range []func(*AssignmentConfig){
		func(c *AssignmentConfig) { c.Provider = "open-ai" },
		func(c *AssignmentConfig) { c.StepProviders = map[string]string{"write": "open-ai"} },
		func(c *AssignmentConfig) { c.StepProviders = map[string]string{"write": ""} },
	} {
		c := x.AssignmentConfig
		change(&c)
		if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err == nil || !strings.Contains(err.Error(), "unknown provider") {
			t.Fatalf("saved %+v: %v", c, err)
		}
	}
}

func TestFollowLatestVersionWithoutOverriddenStepRefusesNewRun(t *testing.T) {
	d := workflowDefinition()
	a, _, p, _ := providerSetup(t, d, func(c *AssignmentConfig) {
		c.FollowLatest, c.StepProviders = true, map[string]string{"publish": "opencode-go"}
	})
	d.Steps = d.Steps[:3]
	p = status(t, a, p.ID, "paused")
	if _, err := a.save(p.ProjectID, p.ID, "operator", p.Version, d); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	_, err := a.start(p.ProjectID, p.ID, "after-edit", "")
	if err == nil || !strings.Contains(err.Error(), "step publish") {
		t.Fatalf("started a run with a stale override: %v", err)
	}
}

func TestUnavailableProviderWaitsVisiblyAndNeverDropsThePin(t *testing.T) {
	a, f, _ := directSetup(t)
	p := create(t, a, workflowDefinition())
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Provider = "opencode-go"
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	f.spawnErr = errors.Join(sdk.ErrThreadProviderUnavailable, errors.New("not configured for this user and project"))
	raw, err := a.start(p.ProjectID, p.ID, "waiting", "")
	if err != nil && !errors.Is(err, sdk.ErrThreadProviderUnavailable) {
		t.Fatal(err)
	}
	var r Run
	if raw != nil {
		r = raw.(map[string]any)["run"].(Run)
	} else {
		runs, listErr := a.dispatches(p.ID)
		if listErr != nil || len(runs) != 1 {
			t.Fatal(runs, listErr)
		}
		r = runs[0]
	}
	s := stepBy(t, a, r, "research")
	if len(f.threads) != 0 {
		t.Fatalf("a worker started without its provider: %+v", f.threads)
	}
	if !strings.Contains(s.DeliveryWarning, "provider opencode-go") || !strings.Contains(s.DeliveryWarning, "not configured") {
		t.Fatalf("warning %q", s.DeliveryWarning)
	}
	if s.DeliverySuspended || s.NextAttemptAt == "" || s.State != "ready" {
		t.Fatalf("provider wait should keep retrying: %+v", s)
	}

	// The provider comes back: the same frozen request runs, with its pin.
	f.spawnErr = nil
	if _, err = a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	all, err := a.steps(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	if err = a.deliverStep(p, r, &s, all); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	if got := workerRequest(t, f, s.ThreadID).Provider; got != "opencode-go" {
		t.Fatalf("recovered worker pinned %q", got)
	}
	if s.DeliveryWarning != "" || s.Provider == nil || s.Provider.Pin != "opencode-go" {
		t.Fatalf("recovered step %+v", s)
	}
}

func TestProviderConflictSuspendsDelivery(t *testing.T) {
	if !permanentDeliveryError(errors.Join(sdk.ErrThreadProviderConflict, errors.New("thread on anthropic"))) {
		t.Fatal("conflict can't clear by retrying")
	}
	for _, err := range []error{sdk.ErrThreadProviderUnavailable, sdk.ErrThreadProviderUnsupported} {
		if permanentDeliveryError(err) {
			t.Fatalf("%v can clear by itself and must keep retrying", err)
		}
	}
}

func TestMissingProviderIsReportedOnTheStep(t *testing.T) {
	a, f, _ := directSetup(t)
	f.missing = true
	p := create(t, a, workflowDefinition())
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Provider = "opencode-go"
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "missing", "")
	if err != nil {
		t.Fatal(err)
	}
	s := stepBy(t, a, raw.(map[string]any)["run"].(Run), "research")
	if s.Provider == nil || !s.Provider.Missing || s.Provider.Active != "openai-codex" || s.Provider.Pin != "opencode-go" {
		t.Fatalf("status %+v", s.Provider)
	}
}

func TestLostSpawnResponseRetriesTheSamePin(t *testing.T) {
	a, f, _ := directSetup(t)
	f.lose = true
	p := create(t, a, workflowDefinition())
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Provider = "opencode-go"
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	_, _ = a.start(p.ProjectID, p.ID, "lost", "")
	raw, err := a.start(p.ProjectID, p.ID, "lost", "")
	if err != nil {
		t.Fatal(err)
	}
	s := stepBy(t, a, raw.(map[string]any)["run"].(Run), "research")
	var saved sdk.ThreadSpawnRequest
	var body string
	if err = a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE event_id=?`, s.DeliveryEventID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(body), &saved); err != nil || saved.Provider != "opencode-go" {
		t.Fatalf("saved request %s %v", body, err)
	}
	for _, request := range f.threads {
		if request.Provider != "opencode-go" {
			t.Fatalf("a retry dropped the pin: %+v", f.threads)
		}
	}
}
