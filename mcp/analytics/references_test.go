package main

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestReferenceSetMustExistBeforeSpecAndFailedCreateIsAtomic(t *testing.T) {
	db := testDashboardDB(t)
	_, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "patreon", Topic: "mrr.current", ValidationMode: "reject",
		Properties: []EventPropertySpec{{Key: "props.site_id", Type: "string", Required: true, ReferenceSet: "patreon.sites"}},
	}, true)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing set error=%v", err)
	}
	if _, err := getEventSpec(db, "p1", "patreon", "mrr.current"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed create left a partial spec: %v", err)
	}
}

func TestReferenceSetValidationRejectsUnknownAndInactiveValues(t *testing.T) {
	db := testDashboardDB(t)
	set, err := upsertReferenceSet(db, ReferenceSet{ProjectID: "p1", Key: "patreon.sites", Label: "Patreon sites"})
	if err != nil {
		t.Fatal(err)
	}
	if set.Key != "patreon.sites" {
		t.Fatalf("set=%#v", set)
	}
	if _, err := upsertReferenceValue(db, "p1", set.Key, ReferenceValue{Value: "site-a", Label: "Site A", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertReferenceValue(db, "p1", set.Key, ReferenceValue{Value: "site-old", Label: "Old site", Status: "inactive"}); err != nil {
		t.Fatal(err)
	}
	spec, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "patreon", Topic: "mrr.current", Status: "active", ValidationMode: "reject",
		Properties: []EventPropertySpec{{Key: "props.site_id", Type: "string", Required: true, ReferenceSet: set.Key}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Properties) != 1 || len(spec.Properties[0].AllowedValues) != 1 || spec.Properties[0].AllowedValues[0].Value != "site-a" {
		t.Fatalf("discovered properties=%#v", spec.Properties)
	}

	if _, err := insertEvent(db, EventInsert{App: "patreon", Topic: "mrr.current", ProjectID: "p1", Source: "track", Props: `{"site_id":"site-a"}`}); err != nil {
		t.Fatalf("active value rejected: %v", err)
	}
	for _, value := range []string{"site-missing", "site-old"} {
		event := EventInsert{App: "patreon", Topic: "mrr.current", ProjectID: "p1", Source: "track", Props: `{"site_id":"` + value + `"}`}
		validation, err := validateEventAgainstSpecs(db, event)
		if err != nil || !validation.Reject || len(validation.Violations) != 1 || validation.Violations[0].ViolationType != "reference_not_found" {
			t.Fatalf("value %q validation=%#v err=%v, want reference_not_found rejection", value, validation, err)
		}
		if _, err := insertEvent(db, event); err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("value %q tracking error=%v", value, err)
		}
	}
}

func TestDeactivatingReferenceDoesNotChangeStoredEvents(t *testing.T) {
	db := testDashboardDB(t)
	if _, err := upsertReferenceSet(db, ReferenceSet{ProjectID: "p1", Key: "patreon.sites"}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertReferenceValue(db, "p1", "patreon.sites", ReferenceValue{Value: "site-a", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertEventSpec(db, EventSpec{
		ProjectID: "p1", App: "patreon", Topic: "members.current", ValidationMode: "reject",
		Properties: []EventPropertySpec{{Key: "props.site_id", Type: "string", Required: true, ReferenceSet: "patreon.sites"}},
	}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := insertEvent(db, EventInsert{App: "patreon", Topic: "members.current", ProjectID: "p1", Source: "track", Props: `{"site_id":"site-a"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertReferenceValue(db, "p1", "patreon.sites", ReferenceValue{Value: "site-a", Status: "inactive"}); err != nil {
		t.Fatal(err)
	}
	count, err := countEvents(db, Filter{ProjectID: "p1", App: "patreon", Topic: "members.current"})
	if err != nil || count != 1 {
		t.Fatalf("historical event count=%d err=%v, want 1", count, err)
	}
	if _, err := insertEvent(db, EventInsert{App: "patreon", Topic: "members.current", ProjectID: "p1", Source: "track", Props: `{"site_id":"site-a"}`}); err == nil {
		t.Fatal("inactive site accepted for a new write")
	}
}

func TestReferenceSetsAreProjectScoped(t *testing.T) {
	db := testDashboardDB(t)
	for _, projectID := range []string{"p1", "p2"} {
		if _, err := upsertReferenceSet(db, ReferenceSet{ProjectID: projectID, Key: "patreon.sites"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := upsertReferenceValue(db, "p1", "patreon.sites", ReferenceValue{Value: "site-a", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	found, err := activeReferenceValueExists(db, "p2", "patreon.sites", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("reference value leaked across projects")
	}
}

func TestReferenceToolsDiscoverActiveValuesAndRejectProjectSpoofing(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}
	if _, err := app.toolReferenceSetUpsert(ctx, map[string]any{"key": "patreon.sites", "label": "Patreon sites"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []map[string]any{
		{"reference_set": "patreon.sites", "value": "site-a", "label": "Site A", "status": "active"},
		{"reference_set": "patreon.sites", "value": "site-old", "label": "Old", "status": "inactive"},
	} {
		if _, err := app.toolReferenceValueUpsert(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	got, err := app.toolReferenceValuesList(ctx, map[string]any{"reference_set": "patreon.sites"})
	if err != nil {
		t.Fatal(err)
	}
	values := got.(map[string]any)["values"].([]ReferenceValue)
	if len(values) != 1 || values[0].Value != "site-a" {
		t.Fatalf("active values=%#v", values)
	}
	_, err = app.toolReferenceValuesList(ctx, map[string]any{"project_id": "other", "reference_set": "patreon.sites"})
	if err == nil || !strings.Contains(err.Error(), "project_id is assigned by the platform") {
		t.Fatalf("spoofing error=%v", err)
	}
}

func TestValidateAndTrackBothRejectUnknownReferenceValues(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}
	if _, err := app.toolReferenceSetUpsert(ctx, map[string]any{"key": "patreon.sites", "label": "Patreon sites"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolReferenceValueUpsert(ctx, map[string]any{
		"reference_set": "patreon.sites", "value": "site-a", "label": "Site A", "status": "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app": "patreon", "topic": "mrr.current", "validation_mode": "reject",
		"properties": []any{map[string]any{"key": "props.site_id", "type": "string", "required": true, "reference_set": "patreon.sites"}},
	}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"app": "patreon", "event": "mrr.current", "props": map[string]any{"site_id": "site-typo"}}
	validated, err := app.toolEventValidate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if got := validated.(map[string]any); got["valid"] != false || got["reject"] != true || got["summary"].(map[string]any)["error"] != "reference_not_found" {
		t.Fatalf("validation result=%#v", got)
	}
	tracked, err := app.toolTrack(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if got := tracked.(map[string]any); got["reject"] != true || got["error"] != "reference_not_found" {
		t.Fatalf("track result=%#v", got)
	}
	count, err := countEvents(ctx.AppDB(), Filter{ProjectID: "h-sites", App: "patreon", Topic: "mrr.current"})
	if err != nil || count != 0 {
		t.Fatalf("rejected event count=%d err=%v", count, err)
	}
}

func TestEventSpecToolPatchPreservesPropertiesAndPolicies(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("h-sites"))
	app := &App{}
	if _, err := app.toolReferenceSetUpsert(ctx, map[string]any{"key": "patreon.sites", "label": "Patreon sites"}); err != nil {
		t.Fatal(err)
	}
	created, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app": "patreon", "topic": "mrr.current", "validation_mode": "reject", "ingest_mode": "upsert",
		"upsert_policy": map[string]any{"bucket": "none", "operation": "replace", "dimensions": []any{"props.site_id"}},
		"properties":    []any{map[string]any{"key": "props.site_id", "type": "string", "required": true, "reference_set": "patreon.sites"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := created.(map[string]any)["spec"].(*EventSpec)
	patched, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app": "patreon", "topic": "mrr.current", "description": "Canonical current MRR",
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := patched.(map[string]any)["spec"].(*EventSpec)
	if spec.ID != original.ID || spec.Description != "Canonical current MRR" || spec.UpsertPolicy == nil || len(spec.Properties) != 1 || spec.Properties[0].ReferenceSet != "patreon.sites" {
		t.Fatalf("unsafe patch result=%#v", spec)
	}
	if _, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app": "patreon", "topic": "mrr.current", "properties": []any{},
	}); err == nil || !strings.Contains(err.Error(), "clear_properties=true") {
		t.Fatalf("empty replacement error=%v", err)
	}
	cleared, err := app.toolEventSpecUpsert(ctx, map[string]any{
		"app": "patreon", "topic": "mrr.current", "clear_properties": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if props := cleared.(map[string]any)["spec"].(*EventSpec).Properties; len(props) != 0 {
		t.Fatalf("properties not explicitly cleared: %#v", props)
	}
}
