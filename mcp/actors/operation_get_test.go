package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func galleryGetFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("testdata/patreon_gallery_revision55.json")
	if err != nil {
		t.Fatal(err)
	}
	var op map[string]any
	if err = json.Unmarshal(raw, &op); err != nil {
		t.Fatal(err)
	}
	ops := map[string]any{"gallery_post": op}
	for i := 0; i < 61; i++ {
		ops[fmt.Sprintf("other_%02d", i)] = map[string]any{"steps": []any{map[string]any{"action": "goto", "url": "https://www.patreon.com/", "label": strings.Repeat("unrelated", 450)}}}
	}
	def := map[string]any{"schema_version": 1, "allowed_hosts": []any{"www.patreon.com", "patreon.com"}, "browser": map[string]any{"context_id": "{{context_id}}", "backend": "browserbase"}, "defaults": map[string]any{"context_id": "fixture", "creator_url": "https://www.patreon.com/fixture", "alias": "{{alias_source}}"}, "presets": map[string]any{"sample": map[string]any{"alias_source": "preset value", "creator_label": "Fixture Creator"}}, "limits": map[string]any{"max_pages": 21, "max_items": 500, "step_retries": 2}, "operations": ops}
	return def, op
}

func TestActorGetOperationExactRevisionAndCompleteness(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	def, op := galleryGetFixture(t)
	rec := saveFixtureActor(t, ctx, app, def)
	// Store revision 55 then change latest, without executing any operation.
	raw, _ := json.Marshal(def)
	if _, err := ctx.AppDB().Exec(`INSERT INTO actors_versions(project_id,actor_id,revision,definition_json) VALUES(?,?,55,?)`, projectID(ctx), rec.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	latestRaw, _ := json.Marshal(testActorDefinition())
	if _, err := ctx.AppDB().Exec(`UPDATE actors_definitions SET revision=56,definition_json=? WHERE id=?`, string(latestRaw), rec.ID); err != nil {
		t.Fatal(err)
	}
	got, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 55, "operation": "gallery_post"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	var out map[string]any
	json.Unmarshal(encoded, &out)
	if out["retrieved_revision"] != float64(55) || out["latest_revision"] != float64(56) || out["step_count"] != float64(45) || out["complete"] != true {
		t.Fatal("wrong revision/completeness metadata")
	}
	if !reflect.DeepEqual(out["operation"], op) {
		t.Fatal("operation fields or assertions were omitted/changed")
	}
	if strings.Contains(string(encoded), "other_00") || len(encoded) > maxActorOperationResponseBytes {
		t.Fatal("unrelated operations were included")
	}
	requirements := out["input_requirements"].(map[string]any)
	if !strings.Contains(stringMustJSON(requirements["required_inputs"]), "alias_source") || strings.Contains(stringMustJSON(requirements["required_inputs_by_preset"]), "alias_source") {
		t.Fatal("global template/preset requirements were not resolved")
	}
	for _, args := range []map[string]any{
		{"id": rec.ID, "revision": 54, "operation": "gallery_post"},
		{"id": rec.ID, "revision": 56, "operation": "gallery_post"},
		{"id": rec.ID, "revision": 55, "operation": "missing"},
		{"id": rec.ID, "revision": 0, "operation": "gallery_post"},
		{"id": rec.ID, "revision": 55.5, "operation": "gallery_post"},
		{"id": rec.ID, "revision": 55, "operation": ""},
		{"id": rec.ID, "revision": 55, "operation": true},
	} {
		if _, err = app.toolActorGet(ctx, args); err == nil {
			t.Fatalf("invalid request succeeded: %v", args)
		}
	}
	if _, err = app.toolActorGet(ctx.WithProject("other"), map[string]any{"id": rec.ID, "revision": 55, "operation": "gallery_post"}); err == nil {
		t.Fatal("cross-project revision was exposed")
	}
	full, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 55})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.(map[string]any)["actor"].(*actorRecord).Definition.Operations) != 62 {
		t.Fatal("legacy full retrieval changed")
	}
	if len(plat.calls) != 0 {
		t.Fatal("retrieval performed an external action")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_runs`).Scan(&count)
	if count != 0 {
		t.Fatal("retrieval queued execution")
	}
	t.Logf("gallery revision55: full operation=%d steps, compact response=%d bytes", 45, len(encoded))
}

func stringMustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestActorGetOperationPreservesRawAndRejectsOversize(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def, op := galleryGetFixture(t)
	rec := saveFixtureActor(t, ctx, app, def)
	for _, n := range []int{500, 40000} {
		op["future_metadata"] = strings.Repeat("x", n)
		op["explicit_false"] = false
		raw, _ := json.Marshal(def)
		if _, err := ctx.AppDB().Exec(`UPDATE actors_versions SET definition_json=? WHERE actor_id=? AND revision=1`, string(raw), rec.ID); err != nil {
			t.Fatal(err)
		}
		out, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1, "operation": "gallery_post"})
		if n == 40000 {
			if err == nil || !strings.Contains(err.Error(), "incomplete-response") {
				t.Fatal("oversized operation silently returned a partial definition")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		json.Unmarshal([]byte(stringMustJSON(out)), &result)
		if !reflect.DeepEqual(result["operation"], op) {
			t.Fatal("raw metadata or explicit false was dropped")
		}
	}
}

func TestActorGetOperationLimitsAndImplicitRun(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	rec := saveFixtureActor(t, ctx, app, testActorDefinition())
	out, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "run"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["step_count"].(int) != len(rec.Definition.Steps) {
		t.Fatal("implicit run omitted steps")
	}
	if _, err = app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "missing"}); err == nil {
		t.Fatal("missing operation became generic run")
	}
	limits := actorOperationLimits(actorLimits{MaxItems: 2, StepRetries: 0})
	if limits["max_pages"] != defaultActorMaxPages || limits["max_items"] != 2 || limits["step_retries"] != 0 {
		t.Fatal("operation limits merged with actor limits or lost zero retries")
	}
	templated := actorOperationLimits(actorLimits{MaxPages: "{{page_budget}}"})
	if templated["max_pages"].(map[string]any)["template"] != "{{page_budget}}" {
		t.Fatal("unresolved limit was presented as a fixed effective budget")
	}
}

func TestActorGetOperationMCPWire(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("operation-get-fixture"))
	def, op := galleryGetFixture(t)
	var saved map[string]any
	resp := sc.POST("/actors", map[string]any{"name": "Immutable gallery retrieval fixture", "definition": def}, &saved)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	id := saved["actor"].(map[string]any)["id"]
	// Use the real SDK MCP mux; the handler result must survive JSON-RPC framing.
	var wire map[string]any
	resp = sc.POST("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "actors_get", "arguments": map[string]any{"id": id, "revision": 1, "operation": "gallery_post"}}}, &wire)
	if resp.Status != 200 || wire["error"] != nil {
		t.Fatal(string(resp.Body))
	}
	result := wire["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		t.Fatal("MCP payload was truncated", err)
	}
	if decoded["step_count"] != float64(45) || !reflect.DeepEqual(decoded["operation"], op) {
		t.Fatal("MCP omitted operation steps/assertions")
	}
	var runs map[string]any
	sc.GET("/runs", &runs)
	if count, ok := runs["total"].(float64); !ok || count != 0 {
		t.Fatal("read-only transport test queued a run")
	}
	t.Logf("MCP text bytes=%d; all 45 gallery steps intact", len(content))
}

func TestActorGetOperationEffectiveCrawlLimits(t *testing.T) {
	def := actorDefinition{Crawl: &crawlDefinition{Frontier: crawlFrontier{MaxPages: 20, MaxItems: 200}}}
	got := actorOperationEffectiveLimits(def)
	if got["max_pages"] != 20 || got["max_items"] != 200 {
		t.Fatal("implicit crawl budgets were replaced by step defaults")
	}
	def.Limits = actorLimits{MaxPages: 5, MaxItems: "{{items}}"}
	got = actorOperationEffectiveLimits(def)
	if got["max_pages"] != 5 || got["max_items"].(map[string]any)["maximum"] != 200 {
		t.Fatal("crawl caps were lost")
	}
}

func TestActorGetOperationIgnoresUnrelatedSchemas(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def, _ := galleryGetFixture(t)
	rec := saveFixtureActor(t, ctx, app, def)
	def["operations"].(map[string]any)["other_00"] = map[string]any{"steps": "future unrelated schema"}
	raw, _ := json.Marshal(def)
	ctx.AppDB().Exec(`UPDATE actors_versions SET definition_json=? WHERE actor_id=? AND revision=1`, string(raw), rec.ID)
	ctx.AppDB().Exec(`UPDATE actors_definitions SET revision=2,definition_json='invalid latest payload' WHERE id=?`, rec.ID)
	out, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1, "operation": "gallery_post"})
	if err != nil || out.(map[string]any)["step_count"] != 45 {
		t.Fatal("unrelated/latest schema blocked immutable operation retrieval", err)
	}
}

func TestActorGetOperationUsesOperationOverride(t *testing.T) {
	ctx, app := newTestCtx(t, newFakePlatform())
	def, op := galleryGetFixture(t)
	op["limits"] = map[string]any{"max_items": 2, "step_retries": 0}
	def["read_only"] = true
	op["steps"] = []any{map[string]any{"action": "goto", "url": "https://www.patreon.com/library"}}
	// Root read_only is inherited, so use an actually read-only operation.
	def["operations"] = map[string]any{"gallery_post": op}
	rec := saveFixtureActor(t, ctx, app, def)
	out, err := app.toolActorGet(ctx, map[string]any{"name": rec.Name, "revision": 1, "operation": "gallery_post"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	limits := result["effective_limits"].(map[string]any)
	if limits["max_pages"] != defaultActorMaxPages || limits["max_items"] != 2 || limits["step_retries"] != 0 || result["execution"].(map[string]any)["read_only"] != true {
		t.Fatal("operation override or inherited read-only semantics changed")
	}
	if _, err = ctx.AppDB().Exec(`DELETE FROM actors_versions WHERE actor_id=? AND revision=1`, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1, "operation": "gallery_post"}); err == nil {
		t.Fatal("missing immutable latest revision silently used mutable definition")
	}
}
