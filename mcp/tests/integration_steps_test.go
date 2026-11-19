package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func simulatedTarget() *TargetConfig {
	return &TargetConfig{Mode: "isolated", Targets: map[string]TargetEndpoint{"api": {App: "telephony", Path: "/api"}},
		Environment: &GeneratedEnvironment{Apps: []string{"telephony", "deploy"},
			Integrations: []SimulatedIntegration{{App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true,
				Seed: map[string][]map[string]any{"phone_numbers": {{"phone_number": "+33100000001"}}}}}}}
}

func triggerStep() Step {
	return Step{ID: "incoming", Kind: "integration_trigger", Slug: "telnyx", Trigger: "incoming_call", TimeoutMS: 15000,
		Input:      map[string]any{"to": "+33100000001", "from": "{{caller}}"},
		Capture:    map[string]string{"call_id": "/result/call_control_id"},
		Assertions: []Assertion{{Path: "/deliveries/0/status", Op: "equals", Value: float64(204)}}}
}

func stateStep() Step {
	return Step{ID: "answered", Kind: "integration_state", Slug: "telnyx", TimeoutMS: 5000, PollEveryMS: 100,
		Assertions: []Assertion{{Path: "/collections/calls/0/state", Op: "equals", Value: "answered"}}}
}

func TestIntegrationStepValidation(t *testing.T) {
	target := simulatedTarget()
	if err := target.validate(); err != nil {
		t.Fatal(err)
	}
	suite := Suite{Target: target}
	check := Check{SuiteID: 1, Name: "ring", Kind: "scenario", Definition: Definition{Steps: []Step{triggerStep(), stateStep()}}}
	if err := validateCheck(&check); err != nil {
		t.Fatal(err)
	}
	if err := validateScenarioBinding(check, suite); err != nil {
		t.Fatal(err)
	}
	// Generated environments bind only simulated integrations to declared apps.
	spec, err := generatedSpec(target, map[string]int64{"telephony": 4, "deploy": 5})
	if err != nil {
		t.Fatal(err)
	}
	bindings := encode(spec["integration_bindings"])
	if !strings.Contains(bindings, `"simulate":true`) || !strings.Contains(bindings, `"role":"carrier"`) || !strings.Contains(bindings, "+33100000001") {
		t.Fatal(bindings)
	}
	for name, mutate := range map[string]func(*GeneratedEnvironment){
		"live binding": func(e *GeneratedEnvironment) { e.Integrations[0].Simulate = false },
		"unknown app":  func(e *GeneratedEnvironment) { e.Integrations[0].App = "crm" },
		"no role":      func(e *GeneratedEnvironment) { e.Integrations[0].Role = "" },
	} {
		bad := simulatedTarget()
		mutate(bad.Environment)
		if err := bad.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	for name, steps := range map[string][]Step{
		"parallel trigger": {{ID: "group", Parallel: []Step{triggerStep(), stateStep()}}},
		"polling trigger":  {func() Step { s := triggerStep(); s.PollEveryMS = 200; return s }()},
		"no trigger":       {func() Step { s := triggerStep(); s.Trigger = ""; return s }()},
		"state with input": {func() Step { s := stateStep(); s.Input = map[string]any{"x": 1}; return s }()},
		"no slug":          {func() Step { s := stateStep(); s.Slug = ""; return s }()},
		"trigger with url": {func() Step { s := triggerStep(); s.Path = "https://example.com"; return s }()},
	} {
		c := Check{SuiteID: 1, Name: "x", Kind: "scenario", Definition: Definition{Steps: steps}}
		if err := validateCheck(&c); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	bindingCases := map[string]Suite{
		"standalone":        {},
		"existing target":   {Target: &TargetConfig{Mode: "existing", Targets: map[string]TargetEndpoint{"api": {BaseURL: "https://x.example"}}}},
		"not simulated":     {Target: &TargetConfig{Mode: "isolated", Environment: &GeneratedEnvironment{Apps: []string{"telephony"}}}},
		"other app binding": {Target: target},
	}
	for name, s := range bindingCases {
		c := Check{SuiteID: 1, Name: "x", Kind: "scenario", Definition: Definition{Steps: []Step{triggerStep()}}}
		if name == "other app binding" {
			c.Definition.Steps[0].App = "crm"
		}
		validateCheck(&c)
		if err := validateScenarioBinding(c, s); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Hand-written release suites cannot be checked at save time; the run reports a missing binding.
	c := Check{SuiteID: 1, Name: "x", Kind: "scenario", Definition: Definition{Steps: []Step{triggerStep()}}}
	validateCheck(&c)
	if err := validateScenarioBinding(c, Suite{Release: &ReleaseConfig{}}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationStepsRun(t *testing.T) {
	a, app, _ := fixture(t)
	reads := 0
	var triggerArgs map[string]any
	fail := ""
	platform := &scenarioPlatform{call: func(_ context.Context, target, tool string, args map[string]any, out any) error {
		if target != "environments" || args["run_id"] != "run_test" || args["slug"] != "telnyx" {
			t.Fatalf("unexpected call %s/%s %v", target, tool, args)
		}
		switch tool {
		case "environment_integration_trigger":
			if fail != "" {
				return errors.New(fail)
			}
			triggerArgs = args
			status := 204
			if args["trigger"] == "caller_hangup" {
				status = 500
			}
			return json.Unmarshal([]byte(`{"result":{"call_control_id":"v3:abc"},"deliveries":[{"id":"d1","event":"call.initiated","app":"telephony","target":"/webhooks/telnyx","state":"delivered","status":`+itoaTest(status)+`,"attempts":1}]}`), out)
		case "environment_integration_state":
			reads++
			state := "ringing"
			if reads >= 2 {
				state = "answered"
			}
			return json.Unmarshal([]byte(`{"collections":{"calls":[{"id":"v3:abc","state":"`+state+`"}]},"deliveries":[]}`), out)
		}
		return errors.New("unknown tool")
	}}
	run := &appRunContext{ctx: app, release: &releaseSession{id: "run_test", env: environmentClient{platform: platform}}}

	result, captures := a.runStep(context.Background(), triggerStep(), map[string]any{"caller": "+33600000000"}, run)
	if result.Status != "passed" || captures["call_id"] != "v3:abc" || len(result.Deliveries) != 1 || result.Deliveries[0].Event != "call.initiated" {
		t.Fatalf("%+v %v", result, captures)
	}
	input := triggerArgs["input"].(map[string]any)
	if input["from"] != "+33600000000" || triggerArgs["wait"] != true || triggerArgs["timeout_ms"] != 14000 {
		t.Fatalf("%v", triggerArgs)
	}
	if len(result.Assertions) != 1 || result.Assertions[0].Actual != float64(204) {
		t.Fatalf("assertion evidence dropped: %+v", result.Assertions)
	}

	result, _ = a.runStep(context.Background(), stateStep(), nil, run)
	if result.Status != "passed" || result.Attempts != 2 {
		t.Fatalf("%+v", result)
	}

	// A failed webhook names the app's answer instead of only "assertion failed".
	hangup := triggerStep()
	hangup.ID, hangup.Trigger, hangup.Capture = "hangup", "caller_hangup", nil
	result, _ = a.runStep(context.Background(), hangup, map[string]any{"caller": "+33600000000"}, run)
	if result.Status != "failed" || result.Error != "telephony returned 500 to call.initiated" {
		t.Fatalf("%+v", result)
	}

	for reason, want := range map[string]string{
		"simulated provider 422: number +33100000001 is not provisioned": "refused by the simulated provider",
		"telnyx simulation: 501 dial is not implemented":                 "does not support this",
		"unknown tool environment_integration_trigger":                   "needs Environments 0.12.0",
	} {
		fail = reason
		result, _ = a.runStep(context.Background(), triggerStep(), map[string]any{"caller": "x"}, run)
		if result.Status != "error" || !strings.Contains(result.Error, want) || !strings.Contains(result.Error, "incoming_call") {
			t.Fatalf("%q → %+v", reason, result)
		}
	}

	// Without an isolated run there is nothing to trigger.
	result, _ = a.runStep(context.Background(), triggerStep(), map[string]any{"caller": "x"}, &appRunContext{ctx: app})
	if result.Status != "error" || !strings.Contains(result.Error, "isolated release runs") {
		t.Fatalf("%+v", result)
	}
}

func itoaTest(n int) string { return strings.TrimSpace(encode(n)) }
