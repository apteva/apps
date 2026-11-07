package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func listedAttributeDefs(t *testing.T, ctx *sdk.AppCtx, args map[string]any) []map[string]any {
	t.Helper()
	out, err := (&App{}).toolAttributeDefinitionsList(context.Background(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	defs := result["attribute_defs"].([]map[string]any)
	if result["count"] != len(defs) {
		t.Fatal("definition count mismatch")
	}
	return defs
}

func TestAttributeDiscoveryIsSelectOnlyEvenBeforeFirstContact(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	db := ctx.AppReadDB()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`PRAGMA query_only=OFF`)
	for i := 0; i < 2; i++ {
		defs := listedAttributeDefs(t, ctx, map[string]any{})
		if len(defs) != 10 {
			t.Fatalf("built-ins=%d", len(defs))
		}
		for _, def := range defs {
			if def["is_system"] != true || def["required"] != false {
				t.Fatalf("unexpected default: %v", def)
			}
			if _, ok := def["enum_values"].([]string); !ok {
				t.Fatalf("enum_values must always be an array: %v", def)
			}
		}
	}
	for _, table := range []string{"contact_attribute_defs", "contacts", "contact_activities", "crm_event_outbox"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("discovery changed %s: %d rows", table, count)
		}
	}
	if len(rec.EventsByTopic("contact.updated")) != 0 {
		t.Fatal("discovery emitted update")
	}
}

func TestAttributeDiscoveryProjectScopeTypesEnumsAndOverrides(t *testing.T) {
	ctx := newTestCtx(t)
	for _, row := range []struct {
		pid, key, typ string
		allowed       []any
		required      bool
		sort          int
	}{
		{"test-proj", "score", "number", nil, true, 5},
		{"test-proj", "seniority", "select", []any{"researcher", "partner"}, true, 30},
		{"test-proj", "topics", "multi_select", []any{"finance", "software"}, false, 31},
		{"foreign", "score", "text", nil, false, 0},
		{"foreign", "secret_stage", "select", []any{"private"}, false, 0},
	} {
		if _, err := dbDefineAttribute(ctx.AppDB(), row.pid, row.key, row.key, row.typ, row.allowed, row.required, row.sort); err != nil {
			t.Fatal(err)
		}
	}
	defs := listedAttributeDefs(t, ctx, map[string]any{"_project_id": "foreign"}) // project install ignores caller spoofing.
	if len(defs) != 12 || defs[0]["key"] != "score" {
		t.Fatalf("defaults/order: %v", defs)
	}
	byKey := map[string]map[string]any{}
	for _, def := range defs {
		if byKey[def["key"].(string)] != nil {
			t.Fatal("duplicate default override")
		}
		byKey[def["key"].(string)] = def
	}
	if byKey["score"]["type"] != "number" || byKey["score"]["required"] != true || byKey["secret_stage"] != nil {
		t.Fatal("foreign definitions leaked")
	}
	if !reflect.DeepEqual(byKey["seniority"]["enum_values"], []string{"researcher", "partner"}) || byKey["seniority"]["required"] != true || byKey["seniority"]["is_system"] != false {
		t.Fatal("project override lost")
	}
	if !reflect.DeepEqual(byKey["topics"]["enum_values"], []string{"finance", "software"}) {
		t.Fatal("multi-select values lost")
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	if _, err := (&App{}).toolAttributeDefinitionsList(context.Background(), ctx, map[string]any{}); err == nil {
		t.Fatal("global scope accepted missing project")
	}
	foreign := listedAttributeDefs(t, ctx, map[string]any{"_project_id": "foreign"})
	for _, def := range foreign {
		if def["key"] == "score" && def["type"] != "text" {
			t.Fatal("global project scope crossed")
		}
	}
}

func TestAttributeDiscoveryCancellationAndInvalidMetadata(t *testing.T) {
	ctx := newTestCtx(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&App{}).toolAttributeDefinitionsList(cancelled, ctx, map[string]any{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "stage", "Stage", "select", []any{"ready"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_attribute_defs SET enum_values='{invalid' WHERE project_id='test-proj' AND key='stage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolAttributeDefinitionsList(context.Background(), ctx, map[string]any{}); err == nil || !strings.Contains(err.Error(), "invalid allowed values") {
		t.Fatalf("invalid enum metadata silently accepted: %v", err)
	}
}

func TestUpdateAttributeContractRejectsMalformedPatchesAtomically(t *testing.T) {
	for _, row := range []struct {
		name       string
		attributes any
	}{
		{"dictionary", map[string]any{"lead_score": 88}},
		{"missing value", []any{map[string]any{"key": "lead_score"}}},
		{"string number", []any{map[string]any{"key": "lead_score", "value": "88"}}},
		{"string bool", []any{map[string]any{"key": "do_not_contact", "value": "false"}}},
		{"invalid allowed value", []any{map[string]any{"key": "seniority", "value": "not-allowed"}}},
		{"later invalid", []any{map[string]any{"key": "lead_score", "value": 88}, map[string]any{"key": "do_not_contact", "value": "false"}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			rec := tk.NewEmitRecorder()
			ctx := newTestCtx(t, tk.WithEmitter(rec))
			c := mustCreate(t, ctx, map[string]any{"first_name": "Keep", "attributes": []any{map[string]any{"key": "lead_score", "value": 42}, map[string]any{"key": "do_not_contact", "value": true}}})
			before := readAttributeContact(t, ctx, c.ID)
			rec.Reset()
			_, err := (&App{}).toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"first_name": "Lost", "attributes": row.attributes}, "source": "agent:research"})
			if err == nil {
				t.Fatal("invalid patch accepted")
			}
			if !reflect.DeepEqual(before, readAttributeContact(t, ctx, c.ID)) {
				t.Fatal("rejected patch partially updated contact")
			}
			if len(rec.EventsByTopic("contact.updated")) != 0 {
				t.Fatal("rejected patch emitted update")
			}
		})
	}
}

func TestUpdateAttributeContractNativeTypesMergeProvenanceAndClear(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "topics", "Topics", "multi_select", []any{"finance", "software"}, false, 0); err != nil {
		t.Fatal(err)
	}
	c := mustCreate(t, ctx, map[string]any{"attributes": []any{map[string]any{"key": "industry", "value": "Keep"}}})
	// Round-trip JSON to exercise exactly the documented wire format/types.
	var args map[string]any
	if err := json.Unmarshal([]byte(`{"patch":{"attributes":[{"key":"lead_score","value":88},{"key":"do_not_contact","value":false},{"key":"seniority","value":"founder","source":"human"},{"key":"topics","value":["finance"]}]},"source":"agent:research"}`), &args); err != nil {
		t.Fatal(err)
	}
	args["id"] = c.ID
	if _, err := app.toolUpdate(ctx, args); err != nil {
		t.Fatal(err)
	}
	got := readAttributeContact(t, ctx, c.ID)
	values := map[string]Attribute{}
	for _, attr := range got.Attributes {
		values[attr.Key] = attr
	}
	if values["lead_score"].Value != float64(88) || values["do_not_contact"].Value != false || values["industry"].Value != "Keep" || values["seniority"].Value != "founder" || !reflect.DeepEqual(values["topics"].Value, []any{"finance"}) {
		t.Fatalf("wrong readback: %v", values)
	}
	if values["lead_score"].Source != "agent:research" || values["seniority"].Source != "human" {
		t.Fatal("provenance lost")
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"attributes": []any{}}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Attributes, readAttributeContact(t, ctx, c.ID).Attributes) {
		t.Fatal("empty array cleared values")
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"attributes": []any{map[string]any{"key": "lead_score", "value": nil}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"company": "Clear me"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"company": nil}}); err != nil {
		t.Fatal(err)
	}
	if readAttributeContact(t, ctx, c.ID).Company != "" {
		t.Fatal("nullable core field did not clear")
	}
	for _, attr := range readAttributeContact(t, ctx, c.ID).Attributes {
		if attr.Key == "lead_score" && attr.Value != nil {
			t.Fatal("explicit null did not clear")
		}
	}
}

func TestUpdateRejectsMisplacedTopLevelAttributesWithoutWrites(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Keep", "attributes": []any{map[string]any{"key": "lead_score", "value": 42}}})
	before := readAttributeContact(t, ctx, c.ID)
	_, err := (&App{}).toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"first_name": "Lost"}, "attributes": []any{map[string]any{"key": "lead_score", "value": 88}}})
	if err == nil || !strings.Contains(err.Error(), "patch.attributes") {
		t.Fatalf("misplaced attributes=%v", err)
	}
	if !reflect.DeepEqual(before, readAttributeContact(t, ctx, c.ID)) {
		t.Fatal("misplaced attributes caused partial update")
	}
}

func TestAttributeDiscoveryAndPatchSchemasAreExplicit(t *testing.T) {
	found := 0
	for _, tool := range (&App{}).MCPTools() {
		switch tool.Name {
		case "contacts_attribute_definitions_list":
			found++
			if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false || tool.HandlerCtx == nil {
				t.Fatal("missing read-only contract")
			}
			for _, word := range []string{"enum_values", "required", "contacts_update", "contacts_set_attribute", "READ ONLY"} {
				if !strings.Contains(tool.Description, word) {
					t.Errorf("missing %s", word)
				}
			}
		case "contacts_update":
			found++
			patch := tool.InputSchema["properties"].(map[string]any)["patch"].(map[string]any)
			if !reflect.DeepEqual(patch["properties"].(map[string]any)["company"].(map[string]any)["type"], []string{"string", "null"}) {
				t.Fatal("existing nullable core patch contract lost")
			}
			attrs := patch["properties"].(map[string]any)["attributes"].(map[string]any)
			item := attrs["items"].(map[string]any)
			if attrs["type"] != "array" || !reflect.DeepEqual(item["required"], []string{"key", "value"}) || patch["additionalProperties"] != false {
				t.Fatalf("missing nested attribute schema: %v", patch)
			}
			value := item["properties"].(map[string]any)["value"].(map[string]any)
			if len(value["anyOf"].([]any)) != 5 || strings.Contains(value["description"].(string), "accept legacy") {
				t.Fatal("incorrect native value contract")
			}
			for _, word := range []string{"patch.attributes", "NOT a key/value object", "contacts_attribute_definitions_list", "missing value", "contacts_get", `"value":88`} {
				if !strings.Contains(tool.Description, word) {
					t.Errorf("missing %s", word)
				}
			}
		}
	}
	if found != 2 {
		t.Fatal("tools missing")
	}
}
