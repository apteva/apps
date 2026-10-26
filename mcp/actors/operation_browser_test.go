package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOperationBrowserIsolationAndExplicitInputs(t *testing.T) {
	d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Browser: actorBrowser{ContextID: "production-context", Backend: "browserbase", Persist: true}, Defaults: map[string]any{"onboarding_context_id": "default-context"}, Presets: map[string]map[string]any{"production": {"onboarding_context_id": "preset-context"}}, Operations: map[string]actorOperation{
		"signup":   {Browser: &actorBrowser{ContextID: "{{onboarding_context_id}}", Persist: true}, RequiredExplicitInputs: []string{"onboarding_context_id"}, Steps: []actorStep{{Action: "goto", URL: "https://example.com/signup"}}},
		"existing": {Steps: []actorStep{{Action: "goto", URL: "https://example.com/"}}},
	}}
	if err := validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	selected, err := selectOperation(d, "signup")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Browser.Backend != "" || selected.Browser.ContextID != "{{onboarding_context_id}}" {
		t.Fatal("operation browser must fully replace parent browser")
	}
	old, err := selectOperation(d, "existing")
	if err != nil || old.Browser.ContextID != "production-context" {
		t.Fatal("existing browser changed")
	}
	ctx, app := newTestCtx(t, newFakePlatform())
	raw, _ := json.Marshal(selected)
	for _, input := range []map[string]any{nil, {"onboarding_context_id": ""}, {"onboarding_context_id": "  "}, {"onboarding_context_id": nil}} {
		in, _ := json.Marshal(map[string]any{"preset": "production", "schedule_overrides": map[string]any{"onboarding_context_id": "schedule-context"}, "input": input})
		_, err := newActorExecution(context.Background(), app, ctx, &actorQueuedRun{DefinitionSnapshot: string(raw), InputJSON: string(in)})
		if err == nil || !strings.Contains(err.Error(), "explicit input") {
			t.Fatalf("inherited or empty context accepted: %v %v", input, err)
		}
	}
	in, _ := json.Marshal(map[string]any{"preset": "production", "input": map[string]any{"onboarding_context_id": "new-test-context"}})
	e, err := newActorExecution(context.Background(), app, ctx, &actorQueuedRun{DefinitionSnapshot: string(raw), InputJSON: string(in)})
	if err != nil {
		t.Fatal(err)
	}
	if e.definition.Browser.ContextID != "new-test-context" || e.session != nil {
		t.Fatal("context not bound before browser allocation")
	}
	req := actorOperationInputs(selected)
	if !strings.Contains(stringMustJSON(req["required_inputs"]), "onboarding_context_id") || !strings.Contains(stringMustJSON(req["required_inputs_by_preset"]), "onboarding_context_id") {
		t.Fatal("introspection hides required explicit input")
	}
}

func TestOperationBrowserRetrievalAndValidation(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def := map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "browser": map[string]any{"context_id": "production-context"}, "operations": map[string]any{"signup": map[string]any{"browser": map[string]any{"context_id": "{{onboarding_context_id}}"}, "required_explicit_inputs": []string{"onboarding_context_id"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/"}}}}}
	rec := saveFixtureActor(t, ctx, app, def)
	got, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "signup"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"browser":{"context_id":"{{onboarding_context_id}}"}`) {
		t.Fatal("retrieval did not show effective browser")
	}
	var d actorDefinition
	raw, _ := json.Marshal(def)
	json.Unmarshal(raw, &d)
	op := d.Operations["signup"]
	op.RequiredExplicitInputs = []string{"bad key"}
	d.Operations["signup"] = op
	if validateActorDefinition(d) == nil {
		t.Fatal("invalid explicit input accepted")
	}
	op.RequiredExplicitInputs = []string{"context", "context"}
	d.Operations["signup"] = op
	if validateActorDefinition(d) == nil {
		t.Fatal("duplicate explicit input accepted")
	}
	op.RequiredExplicitInputs = nil
	op.Browser = &actorBrowser{Backend: "invalid"}
	d.Operations["signup"] = op
	if validateActorDefinition(d) == nil {
		t.Fatal("invalid operation browser accepted")
	}
}
