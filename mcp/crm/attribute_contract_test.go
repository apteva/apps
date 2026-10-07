package main

import (
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func readAttributeContact(t *testing.T, ctx *sdk.AppCtx, id int64) *Contact {
	t.Helper()
	out, err := (&App{}).toolGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["contact"].(*Contact)
}

func TestMCPAttributeValuesRecoverLegacyScalarsWithoutChangingText(t *testing.T) {
	cases := []struct {
		name, typ   string
		value, want any
	}{
		{"ACC score", "number", "88", float64(88)},
		{"MAYU score", "number", "83", float64(83)},
		{"native score", "number", float64(89), float64(89)},
		{"zero", "number", "0", float64(0)},
		{"fraction", "number", "-0.125", -0.125},
		{"exponent", "number", "1.25e2", float64(125)},
		{"whitespace", "number", " 88\n", float64(88)},
		{"integer", "number", int64(88), float64(88)},
		{"legacy true", "bool", "true", true},
		{"legacy false", "bool", "false", false},
		{"native false", "bool", false, false},
		{"numeric text", "text", "00123", "00123"},
		{"boolean text", "text", "false", "false"},
		{"null text", "text", "null", "null"},
		{"select", "select", "88", "88"},
		{"date", "date", "2026-10-07", "2026-10-07"},
		{"URL", "url", "https://example.test/88", "https://example.test/88"},
		{"multi select", "multi_select", []any{"88", "false"}, []any{"88", "false"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec := tk.NewEmitRecorder()
			ctx := newTestCtx(t, tk.WithEmitter(rec))
			app := &App{}
			c := mustCreate(t, ctx, map[string]any{"display_name": tt.name})
			var enum []any
			if tt.typ == "select" || tt.typ == "multi_select" {
				enum = []any{"88", "false"}
			}
			if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "custom", "Custom", tt.typ, enum, false, 0); err != nil {
				t.Fatal(err)
			}
			rec.Reset()
			if _, err := app.toolSetAttribute(ctx, map[string]any{"contact_id": c.ID, "key": "custom", "value": tt.value, "source": "agent:score"}); err != nil {
				t.Fatal(err)
			}
			out, err := app.toolGet(ctx, map[string]any{"id": c.ID})
			if err != nil {
				t.Fatal(err)
			}
			attrs := out.(map[string]any)["contact"].(*Contact).Attributes
			if len(attrs) != 1 || !reflect.DeepEqual(attrs[0].Value, tt.want) || attrs[0].Source != "agent:score" {
				t.Fatalf("readback=%+v; want %T(%v) and preserved provenance", attrs, tt.want, tt.want)
			}
			if len(rec.EventsByTopic("contact.updated")) != 1 {
				t.Fatal("successful write did not emit exactly one update")
			}
		})
	}
}

func TestMCPAttributeInvalidScalarsPreserveExistingData(t *testing.T) {
	cases := []struct {
		name, typ string
		value     any
	}{
		{"number junk", "number", "88 points"},
		{"empty number", "number", ""},
		{"leading zero", "number", "00123"},
		{"plus sign", "number", "+88"},
		{"number null string", "number", "null"},
		{"quoted number", "number", `"88"`},
		{"number boolean", "number", "true"},
		{"number array", "number", "[88]"},
		{"number object", "number", `{"value":88}`},
		{"overflow", "number", "1e999"},
		{"NaN string", "number", "NaN"},
		{"infinity string", "number", "Infinity"},
		{"native NaN", "number", math.NaN()},
		{"native infinity", "number", math.Inf(1)},
		{"native boolean for number", "number", true},
		{"boolean yes", "bool", "yes"},
		{"boolean one", "bool", "1"},
		{"boolean uppercase", "bool", "TRUE"},
		{"boolean null string", "bool", "null"},
		{"native number for boolean", "bool", float64(1)},
		{"native number for text", "text", float64(88)},
		{"invalid select", "select", "bad"},
		{"string array", "multi_select", `["88"]`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rec := tk.NewEmitRecorder()
			ctx := newTestCtx(t, tk.WithEmitter(rec))
			app := &App{}
			c := mustCreate(t, ctx, map[string]any{"display_name": "Keep me", "tags": []any{"draft"}})
			var enum []any
			if tt.typ == "select" || tt.typ == "multi_select" {
				enum = []any{"88"}
			}
			if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "custom", "Custom", tt.typ, enum, false, 0); err != nil {
				t.Fatal(err)
			}
			initial := map[string]any{"number": float64(89), "bool": true, "text": "keep", "select": "88", "multi_select": []any{"88"}}[tt.typ]
			if err := dbSetAttribute(ctx.AppDB(), "test-proj", c.ID, "custom", initial, "original"); err != nil {
				t.Fatal(err)
			}
			before := readAttributeContact(t, ctx, c.ID)
			rec.Reset()
			if _, err := app.toolSetAttribute(ctx, map[string]any{"contact_id": c.ID, "key": "custom", "value": tt.value, "source": "replacement"}); err == nil {
				t.Fatal("invalid value was accepted")
			}
			after := readAttributeContact(t, ctx, c.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected write changed contact: before=%+v after=%+v", before, after)
			}
			if len(rec.EventsByTopic("contact.updated")) != 0 {
				t.Fatal("rejected write emitted an update")
			}
		})
	}
}

func TestMCPAttributeMissingValueIsNotAClear(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"display_name": "Keep score"})
	if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "score", "Score", "number", nil, false, 0); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"contact_id": c.ID, "key": "score", "value": float64(89)}
	if _, err := app.toolSetAttribute(ctx, args); err != nil {
		t.Fatal(err)
	}
	delete(args, "value")
	if _, err := app.toolSetAttribute(ctx, args); err == nil || !strings.Contains(err.Error(), "value required") {
		t.Fatalf("missing-value error=%v", err)
	}
	got := readAttributeContact(t, ctx, c.ID)
	if len(got.Attributes) != 1 || got.Attributes[0].Value != float64(89) {
		t.Fatal("missing value cleared score")
	}
	args["value"] = nil
	if _, err := app.toolSetAttribute(ctx, args); err != nil {
		t.Fatal(err)
	}
	got = readAttributeContact(t, ctx, c.ID)
	if len(got.Attributes) != 1 || got.Attributes[0].Value != nil {
		t.Fatal("explicit null did not clear optional score")
	}
	if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "score", "Score", "number", nil, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSetAttribute(ctx, args); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("cleared required score: %v", err)
	}
}

func TestMCPAttributeConversionUsesOnlyCurrentProjectDefinition(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	local := mustCreate(t, ctx, map[string]any{"display_name": "Local"})
	foreign, err := dbCreate(ctx.AppDB(), "foreign", map[string]any{"display_name": "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	for pid, typ := range map[string]string{"test-proj": "text", "foreign": "number"} {
		if _, err := dbDefineAttribute(ctx.AppDB(), pid, "score", "Score", typ, nil, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.toolSetAttribute(ctx, map[string]any{"contact_id": local.ID, "key": "score", "value": "00123"}); err != nil {
		t.Fatal(err)
	}
	got := readAttributeContact(t, ctx, local.ID)
	if len(got.Attributes) != 1 || got.Attributes[0].Value != "00123" {
		t.Fatal("used another project's numeric definition")
	}
	if _, err := app.toolSetAttribute(ctx, map[string]any{"contact_id": foreign.ID, "key": "score", "value": "88"}); err == nil {
		t.Fatal("cross-project write accepted")
	}
	if err := dbSetAttribute(ctx.AppDB(), "foreign", foreign.ID, "score", "88", "strict"); err == nil {
		t.Fatal("database writes must continue rejecting string numbers")
	}
}

func TestMCPAttributeContractDocumentsTypesExamplesAndReadback(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "contacts_set_attribute" {
			continue
		}
		for _, guidance := range []string{"finite JSON number", "JSON boolean", "multi_select", "legacy", "contacts_get", "solution_ready", `"value":88`} {
			if !strings.Contains(tool.Description, guidance) {
				t.Errorf("missing guidance %q", guidance)
			}
		}
		value := tool.InputSchema["properties"].(map[string]any)["value"].(map[string]any)
		if len(value["anyOf"].([]any)) != 5 || len(value["examples"].([]any)) == 0 {
			t.Fatalf("missing polymorphic value contract: %v", value)
		}
		doc, err := os.ReadFile("skills/crm/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		for _, guidance := range []string{"opportunity_score", "contacts_get", "readback", "do not advance readiness"} {
			if !strings.Contains(string(doc), guidance) {
				t.Errorf("skill missing readiness guidance %q", guidance)
			}
		}
		return
	}
	t.Fatal("contacts_set_attribute not registered")
}
