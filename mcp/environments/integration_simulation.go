package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Simulated integrations (0.12.0). An integration binding with simulate:true
// runs the integration's catalog simulation inside the runtime instead of a
// dead fake connection. The platform owns the simulation: it validates the
// binding at runtime creation, delivers provider webhooks to the runtime's
// apps, and serves trigger and state reads. Environments only checks the
// shape of a spec, drives triggers, and asserts on the simulated state.

const (
	integrationAssertMaxTimeout = 60 * time.Second
	integrationAssertPoll       = 250 * time.Millisecond
	integrationTriggerMaxMS     = 60000
	integrationAssertSampleSize = 5
)

var errIntegrationSimulationUnavailable = errors.New("runtime integration simulation is unavailable on this platform; it needs an Apteva server with runtime integration simulation")

func (s *service) integrationSimulation() (sdk.RuntimeIntegrationSimulationClient, error) {
	client, ok := s.runtime().(sdk.RuntimeIntegrationSimulationClient)
	if !ok || client == nil {
		return nil, errIntegrationSimulationUnavailable
	}
	return client, nil
}

// validateIntegrationBindings checks what Environments can know without the
// catalog. Whether the integration has a simulation, and whether seeded
// collections exist, is decided by the platform when the runtime starts.
func validateIntegrationBindings(spec EnvironmentSpec) error {
	carrierTargets := map[string]bool{}
	for _, raw := range spec.ProtocolFixtures {
		fixture := normalizeProtocolFixtureSpec(raw)
		if fixture.Pack == protocolPackTelephonyCarrier {
			carrierTargets[fixture.TargetApp] = true
		}
	}
	type key struct{ slug, app, role string }
	simulated := map[key]bool{}
	for i, binding := range spec.IntegrationBindings {
		slug := strings.TrimSpace(binding.Slug)
		if len(binding.Seed) > 0 && !binding.Simulate {
			return fmt.Errorf("integration binding %d: seed requires simulate", i)
		}
		if !binding.Simulate {
			continue
		}
		if slug == "" {
			return fmt.Errorf("integration binding %d: simulate requires slug", i)
		}
		for collection := range binding.Seed {
			if strings.TrimSpace(collection) == "" {
				return fmt.Errorf("integration binding %d: seed collection name required", i)
			}
		}
		app, role := strings.TrimSpace(binding.App), strings.TrimSpace(binding.Role)
		k := key{slug, app, role}
		if simulated[k] {
			return fmt.Errorf("integration binding %d: %s is already simulated for app %q role %q", i, slug, app, role)
		}
		simulated[k] = true
		// The deprecated telephony-carrier fixture rewrites the carrier
		// binding of its target app to its own Twilio fake. Mixing it with a
		// simulated binding on that role would silently change the slug.
		if role == "carrier" && carrierTargets[app] {
			return fmt.Errorf("integration binding %d: simulated carrier for %q conflicts with the telephony-carrier protocol fixture; use one or the other", i, app)
		}
	}
	return nil
}

type integrationTriggerArgs struct {
	RunID     string         `json:"run_id"`
	Slug      string         `json:"slug"`
	Trigger   string         `json:"trigger"`
	App       string         `json:"app,omitempty"`
	Role      string         `json:"role,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Wait      bool           `json:"wait,omitempty"`
	TimeoutMS int            `json:"timeout_ms,omitempty"`
}

func (a *App) toolIntegrationTrigger(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var in integrationTriggerArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	in.Slug, in.Trigger = strings.TrimSpace(in.Slug), strings.TrimSpace(in.Trigger)
	if in.Slug == "" || in.Trigger == "" {
		return nil, errors.New("slug and trigger are required")
	}
	if in.TimeoutMS < 0 || in.TimeoutMS > integrationTriggerMaxMS {
		return nil, fmt.Errorf("timeout_ms must be between 0 and %d", integrationTriggerMaxMS)
	}
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	client, err := a.svc.integrationSimulation()
	if err != nil {
		return nil, err
	}
	return client.TriggerRuntimeIntegration(r.RuntimeID, in.Slug, in.Trigger, sdk.RuntimeIntegrationTriggerRequest{
		RuntimeIntegrationSelector: sdk.RuntimeIntegrationSelector{App: strings.TrimSpace(in.App), Role: strings.TrimSpace(in.Role)},
		Input:                      in.Input,
		Wait:                       in.Wait,
		TimeoutMS:                  in.TimeoutMS,
	})
}

func (a *App) toolIntegrationState(_ *sdk.AppCtx, args map[string]any) (any, error) {
	slug := strings.TrimSpace(str(args, "slug"))
	if slug == "" {
		return nil, errors.New("slug is required")
	}
	r, err := a.runFor(args)
	if err != nil {
		return nil, err
	}
	client, err := a.svc.integrationSimulation()
	if err != nil {
		return nil, err
	}
	return client.GetRuntimeIntegrationState(r.RuntimeID, slug, sdk.RuntimeIntegrationSelector{App: strings.TrimSpace(str(args, "app")), Role: strings.TrimSpace(str(args, "role"))})
}

// assertIntegrationState checks records in one simulated collection, or the
// webhook deliveries. where selects records, require is the condition each
// selected record is checked against, and match decides how the counts pass:
//
//	any  (default) at least max(min_count,1) selected records satisfy require
//	all  at least max(min_count,1) records are selected and all satisfy require
//	none no selected record satisfies require
//
// With timeout_ms the state is polled until the assertion passes or the
// timeout ends; the last observation is returned either way.
func (s *service) assertIntegrationState(runtimeID string, a Assertion) (AssertionResult, error) {
	if strings.TrimSpace(a.Slug) == "" {
		return AssertionResult{}, errors.New("integration_state assertion requires slug")
	}
	collection := strings.TrimSpace(a.Collection)
	if (collection == "") == !a.Deliveries {
		return AssertionResult{}, errors.New("integration_state assertion requires exactly one of collection or deliveries")
	}
	match := a.Match
	if match == "" {
		match = "any"
	}
	if match != "any" && match != "all" && match != "none" {
		return AssertionResult{}, errors.New("match must be any, all or none")
	}
	if a.TimeoutMS < 0 || time.Duration(a.TimeoutMS)*time.Millisecond > integrationAssertMaxTimeout {
		return AssertionResult{}, fmt.Errorf("timeout_ms must be between 0 and %d", integrationAssertMaxTimeout.Milliseconds())
	}
	if a.MinCount < 0 {
		return AssertionResult{}, errors.New("min_count must not be negative")
	}
	if err := validateRecordConditions(a.Where); err != nil {
		return AssertionResult{}, fmt.Errorf("where: %w", err)
	}
	if err := validateRecordConditions(a.Require); err != nil {
		return AssertionResult{}, fmt.Errorf("require: %w", err)
	}
	client, err := s.integrationSimulation()
	if err != nil {
		return AssertionResult{}, err
	}
	selector := sdk.RuntimeIntegrationSelector{App: strings.TrimSpace(a.App), Role: strings.TrimSpace(a.Role)}
	deadline := time.Now().Add(time.Duration(a.TimeoutMS) * time.Millisecond)
	for {
		state, err := client.GetRuntimeIntegrationState(runtimeID, strings.TrimSpace(a.Slug), selector)
		if err != nil {
			return AssertionResult{}, err
		}
		var records []map[string]any
		if a.Deliveries {
			records, err = deliveryRecords(state.Deliveries)
			if err != nil {
				return AssertionResult{}, err
			}
		} else {
			records = state.Collections[collection]
		}
		result := evaluateRecords(records, a.Where, a.Require, match, a.MinCount)
		if result.Passed || !time.Now().Before(deadline) {
			return result, nil
		}
		time.Sleep(integrationAssertPoll)
	}
}

func deliveryRecords(deliveries []sdk.RuntimeIntegrationDelivery) ([]map[string]any, error) {
	b, err := json.Marshal(deliveries)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func evaluateRecords(records []map[string]any, where, require map[string]any, match string, minCount int) AssertionResult {
	need := minCount
	if need < 1 {
		need = 1
	}
	selected, satisfied := 0, 0
	var samples []map[string]any
	for _, record := range records {
		if !recordMatches(record, where) {
			continue
		}
		selected++
		ok := recordMatches(record, require)
		if ok {
			satisfied++
		}
		// Samples show what made the assertion fail or pass: violators for
		// all/none, otherwise the selected records.
		show := match == "any" || (match == "all" && !ok) || (match == "none" && ok)
		if show && len(samples) < integrationAssertSampleSize {
			samples = append(samples, record)
		}
	}
	var passed bool
	var message string
	switch match {
	case "all":
		passed = selected >= need && satisfied == selected
		message = fmt.Sprintf("%d of %d selected records satisfy require (need all, at least %d)", satisfied, selected, need)
	case "none":
		passed = satisfied == 0
		message = fmt.Sprintf("%d of %d selected records satisfy require (need none)", satisfied, selected)
	default:
		passed = satisfied >= need
		message = fmt.Sprintf("%d of %d selected records satisfy require (need at least %d)", satisfied, selected, need)
	}
	return AssertionResult{
		Passed:  passed,
		Message: message,
		Actual:  map[string]any{"total": len(records), "selected": selected, "satisfied": satisfied, "samples": samples},
	}
}

var recordConditionOps = map[string]bool{"eq": true, "ne": true, "in": true, "gt": true, "gte": true, "lt": true, "lte": true, "exists": true, "contains": true}

// A condition value is a literal (equality) or an operator object such as
// {"gte": 200, "lt": 300}. An object whose keys are not all operators is a
// literal object compared for equality.
func conditionOps(expected any) (map[string]any, bool) {
	m, ok := expected.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, false
	}
	for k := range m {
		if !recordConditionOps[k] {
			return nil, false
		}
	}
	return m, true
}

func validateRecordConditions(conditions map[string]any) error {
	for path, expected := range conditions {
		if strings.Trim(path, ". ") == "" {
			return errors.New("condition path required")
		}
		ops, ok := conditionOps(expected)
		if !ok {
			continue
		}
		for op, v := range ops {
			switch op {
			case "gt", "gte", "lt", "lte":
				if _, ok := toFloat(v); !ok {
					return fmt.Errorf("%s: %s needs a number", path, op)
				}
			case "in":
				if _, ok := v.([]any); !ok {
					return fmt.Errorf("%s: in needs a list", path)
				}
			case "exists":
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("%s: exists needs true or false", path)
				}
			case "contains":
				if _, ok := v.(string); !ok {
					return fmt.Errorf("%s: contains needs a string", path)
				}
			}
		}
	}
	return nil
}

func recordMatches(record map[string]any, conditions map[string]any) bool {
	for path, expected := range conditions {
		got, found := jsonPathLookup(record, path)
		ops, isOps := conditionOps(expected)
		if !isOps {
			if !found || !jsonEqual(got, expected) {
				return false
			}
			continue
		}
		for op, v := range ops {
			if !applyConditionOp(op, got, found, v) {
				return false
			}
		}
	}
	return true
}

func applyConditionOp(op string, got any, found bool, v any) bool {
	switch op {
	case "exists":
		want, _ := v.(bool)
		return found == want
	case "eq":
		return found && jsonEqual(got, v)
	case "ne":
		return !found || !jsonEqual(got, v)
	case "in":
		list, _ := v.([]any)
		if !found {
			return false
		}
		for _, item := range list {
			if jsonEqual(got, item) {
				return true
			}
		}
		return false
	case "contains":
		s, ok := got.(string)
		sub, _ := v.(string)
		return found && ok && strings.Contains(s, sub)
	case "gt", "gte", "lt", "lte":
		g, ok := toFloat(got)
		w, ok2 := toFloat(v)
		if !found || !ok || !ok2 {
			return false
		}
		switch op {
		case "gt":
			return g > w
		case "gte":
			return g >= w
		case "lt":
			return g < w
		default:
			return g <= w
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// jsonEqual compares after a JSON round trip so 200 and 200.0, or typed and
// generic maps, compare equal.
func jsonEqual(a, b any) bool {
	return reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b))
}

func normalizeJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

var integrationTriggerSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"run_id":     map[string]any{"type": "string"},
		"slug":       map[string]any{"type": "string", "description": "Integration slug of a simulate:true binding, for example telnyx."},
		"trigger":    map[string]any{"type": "string", "description": "Trigger defined by the integration's simulation, for example incoming_call."},
		"app":        map[string]any{"type": "string", "description": "Binding app; needed only when the run simulates this integration for more than one binding."},
		"role":       map[string]any{"type": "string", "description": "Binding role; needed only when the run simulates this integration for more than one binding."},
		"input":      objectSchema,
		"wait":       map[string]any{"type": "boolean", "description": "Wait until the webhooks this trigger emitted are delivered or failed. Webhooks caused by the apps' reactions are not awaited; poll environment_integration_state or use an integration_state assertion with timeout_ms."},
		"timeout_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": integrationTriggerMaxMS},
	},
	"required":             []string{"run_id", "slug", "trigger"},
	"additionalProperties": true,
}

var integrationStateSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"run_id": map[string]any{"type": "string"},
		"slug":   map[string]any{"type": "string"},
		"app":    map[string]any{"type": "string"},
		"role":   map[string]any{"type": "string"},
	},
	"required":             []string{"run_id", "slug"},
	"additionalProperties": true,
}
