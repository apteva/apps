package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SimulatedIntegration binds a catalog simulation (for example a fake Telnyx)
// to an app role in a generated isolated environment. Environments 0.12.0+.
type SimulatedIntegration struct {
	App      string                      `json:"app"`
	Role     string                      `json:"role"`
	Slug     string                      `json:"slug"`
	Simulate bool                        `json:"simulate"`
	Seed     map[string][]map[string]any `json:"seed,omitempty"`
}

const maxSimulatedIntegrations = 8

func (e *GeneratedEnvironment) validateIntegrations() error {
	if len(e.Integrations) > maxSimulatedIntegrations {
		return fmt.Errorf("at most %d simulated integrations", maxSimulatedIntegrations)
	}
	seen := map[string]bool{}
	for _, in := range e.Integrations {
		if !in.Simulate {
			return fmt.Errorf("integration %q: generated environments only take simulated integrations (simulate: true); live connections are never bound", in.Slug)
		}
		if !slugPattern.MatchString(in.Slug) || !slugPattern.MatchString(in.Role) || !containsString(e.Apps, in.App) {
			return fmt.Errorf("integration %q needs a slug, a role and an app from environment.apps", in.Slug)
		}
		key := in.App + "/" + in.Role
		if seen[key] {
			return fmt.Errorf("app %s role %s is bound twice", in.App, in.Role)
		}
		seen[key] = true
		if len(in.Seed) > 16 {
			return fmt.Errorf("integration %q seeds at most 16 collections", in.Slug)
		}
	}
	return nil
}

// simulates reports whether a step's slug (and app/role, when given) names a
// simulated binding of the environment.
func (e *GeneratedEnvironment) simulates(slug, app, role string) bool {
	for _, in := range e.Integrations {
		if in.Simulate && in.Slug == slug && (app == "" || in.App == app) && (role == "" || in.Role == role) {
			return true
		}
	}
	return false
}

func (e *GeneratedEnvironment) bindingSpec() []map[string]any {
	out := make([]map[string]any, 0, len(e.Integrations))
	for _, in := range e.Integrations {
		binding := map[string]any{"app": in.App, "role": in.Role, "slug": in.Slug, "simulate": true}
		if len(in.Seed) > 0 {
			binding["seed"] = in.Seed
		}
		out = append(out, binding)
	}
	return out
}

// deliverySummary is one simulated webhook as kept in step evidence.
type deliverySummary struct {
	Event    string `json:"event"`
	App      string `json:"app,omitempty"`
	Target   string `json:"target,omitempty"`
	State    string `json:"state"`
	Status   int    `json:"status,omitempty"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error,omitempty"`
}

type simulatedDelivery struct {
	deliverySummary
	ID string `json:"id"`
}

func (d deliverySummary) failed() bool {
	return d.State == "failed" || d.State == "undeliverable" || d.Status >= 400
}

func describeDelivery(d deliverySummary) string {
	target := d.App
	if target == "" {
		target = "the app"
	}
	switch {
	case d.Status > 0:
		return fmt.Sprintf("%s returned %d to %s", target, d.Status, d.Event)
	case d.Error != "":
		return fmt.Sprintf("%s was not delivered to %s: %s", d.Event, target, d.Error)
	default:
		return fmt.Sprintf("%s to %s is %s", d.Event, target, d.State)
	}
}

func integrationArgs(step Step, runID string) map[string]any {
	args := map[string]any{"run_id": runID, "slug": step.Slug}
	if step.App != "" {
		args["app"] = step.App
	}
	if step.Role != "" {
		args["role"] = step.Role
	}
	return args
}

// integrationStep fires a provider event or reads a simulation's state in the
// run's environment. Trigger output is {result, deliveries}; state output is
// {collections, deliveries}.
func integrationStep(ctx context.Context, step Step, values map[string]any, app *appRunContext, result *StepResult) (any, error) {
	if app.release == nil || app.release.id == "" {
		return nil, errors.New("simulated integrations exist only in isolated release runs")
	}
	args := integrationArgs(step, app.release.id)
	if step.Kind == "integration_state" {
		var state map[string]any
		if err := app.release.env.call(ctx, "environment_integration_state", args, &state); err != nil {
			return nil, integrationError(step, err)
		}
		return state, nil
	}
	input, err := expandValue(step.Input, values)
	if err != nil {
		return nil, err
	}
	if input != nil {
		args["input"] = input
	}
	args["trigger"], args["wait"] = step.Trigger, true
	// Leave the step time to read the answer after Environments stops waiting.
	wait := step.TimeoutMS - 1000
	if wait < 0 {
		wait = 0
	}
	args["timeout_ms"] = wait
	var out struct {
		Result     json.RawMessage     `json:"result"`
		Deliveries []simulatedDelivery `json:"deliveries"`
	}
	if err := app.release.env.call(ctx, "environment_integration_trigger", args, &out); err != nil {
		return nil, integrationError(step, err)
	}
	deliveries := make([]any, 0, len(out.Deliveries))
	for _, d := range out.Deliveries {
		result.Deliveries = append(result.Deliveries, d.deliverySummary)
		deliveries = append(deliveries, d)
	}
	var parsed any
	if len(out.Result) > 0 {
		_ = json.Unmarshal(out.Result, &parsed)
	}
	return normalizeJSON(map[string]any{"result": parsed, "deliveries": deliveries}), nil
}

func normalizeJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// integrationError keeps the simulation's own reason: a refused trigger
// (provider 422, e.g. an unprovisioned number) or a tool the simulation does
// not implement (501).
func integrationError(step Step, err error) error {
	reason := deployReason(err)
	what := "reading " + step.Slug + " state"
	if step.Kind == "integration_trigger" {
		what = "simulated " + step.Slug + " " + step.Trigger
	}
	switch {
	case strings.Contains(reason, "501") || strings.Contains(strings.ToLower(reason), "not implemented") || strings.Contains(strings.ToLower(reason), "unsupported"):
		return fmt.Errorf("%s: the simulation does not support this (%s)", what, reason)
	case strings.Contains(reason, "422"):
		return fmt.Errorf("%s was refused by the simulated provider (%s); check the environment's seed (numbers, routes)", what, reason)
	case strings.Contains(strings.ToLower(reason), "unknown tool") || strings.Contains(strings.ToLower(reason), "no such tool"):
		return fmt.Errorf("%s needs Environments 0.12.0 or newer: %s", what, reason)
	}
	return fmt.Errorf("%s failed: %s", what, reason)
}
