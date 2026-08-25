package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type fleetTemplatePlatform struct {
	tk.BasePlatformClient
	templates     []sdk.ProjectTemplate
	listProjectID string
	includeSystem bool
	getProjectID  string
	getTemplateID string
}

func (p *fleetTemplatePlatform) ListProjectTemplates(projectID string, options sdk.ProjectTemplateListOptions) ([]sdk.ProjectTemplate, error) {
	p.listProjectID = projectID
	p.includeSystem = options.IncludeSystem
	return p.templates, nil
}

func (p *fleetTemplatePlatform) GetProjectTemplate(projectID, templateID string) (*sdk.ProjectTemplate, error) {
	p.getProjectID, p.getTemplateID = projectID, templateID
	for i := range p.templates {
		if p.templates[i].ID == templateID {
			copy := p.templates[i]
			return &copy, nil
		}
	}
	return nil, errTestTemplateNotFound
}

var errTestTemplateNotFound = &templateTestError{"template not found"}

type templateTestError struct{ message string }

func (e *templateTestError) Error() string { return e.message }

func testProjectTemplate() sdk.ProjectTemplate {
	return sdk.ProjectTemplate{
		ID: "tpl-parent", Kind: sdk.ProjectSetupTemplateKind,
		Name: "Client operations", Description: "Operate the client workspace",
		Source: "user", OwnerProjectID: "parent-project", SchemaVersion: 2, Revision: 7,
		Definition: json.RawMessage(`{
			"category":"business",
			"agents":[{"key":"operator","name":"Operator","directive":"Run {{description}}","mode":"cautious","apps":["tasks"]}],
			"dashboard_layout":[{"id":"tasks","component":"tasks:task-overview","size":"half"}]
		}`),
	}
}

func TestTenantTemplateListUsesProjectTemplateSDK(t *testing.T) {
	platform := &fleetTemplatePlatform{templates: []sdk.ProjectTemplate{testProjectTemplate()}}
	app, ctx := newTestApp(t, tk.WithProjectID("parent-project"), tk.WithPlatform(platform))

	result, err := app.toolTenantTemplateList(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["project_id"] != "parent-project" || platform.listProjectID != "parent-project" {
		t.Fatalf("project scope result=%v platform=%q", out["project_id"], platform.listProjectID)
	}
	if !platform.includeSystem {
		t.Fatal("built-in templates should be included by default")
	}
	if got := out["templates"].([]sdk.ProjectTemplate); len(got) != 1 || got[0].ID != "tpl-parent" {
		t.Fatalf("templates=%+v", got)
	}
}

func TestTenantApplyTemplateImportsSnapshotThenApplies(t *testing.T) {
	platform := &fleetTemplatePlatform{templates: []sdk.ProjectTemplate{testProjectTemplate()}}
	app, ctx := newTestApp(t, tk.WithProjectID("parent-project"), tk.WithPlatform(platform))

	var imported, applied bool
	tenantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-fake" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_ = json.NewEncoder(w).Encode([]tenantProject{{ID: "child-project", Name: "Default"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/child-project/templates":
			_ = json.NewEncoder(w).Encode(map[string]any{"templates": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/child-project/templates":
			var body struct {
				Kind          string          `json:"kind"`
				SchemaVersion int             `json:"schema_version"`
				Name          string          `json:"name"`
				Definition    json.RawMessage `json:"definition"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Kind != sdk.ProjectSetupTemplateKind || body.SchemaVersion != 2 || body.Name != "Client operations" || !json.Valid(body.Definition) {
				t.Fatalf("import body=%+v definition=%s", body, body.Definition)
			}
			imported = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "tpl-child"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/projects/child-project/setup/apply":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["preset_id"] != "tpl-child" || body["description"] != "A bespoke client operation" {
				t.Fatalf("apply body=%+v", body)
			}
			applied = true
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "applied", "warnings": []string{}})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer tenantServer.Close()

	enc, _ := app.keys.seal([]byte("sk-fake"))
	tenant := &Tenant{Slug: "templated", Kind: KindRemote, BaseURL: tenantServer.URL, OwnerEmail: "owner@example.com", Status: StatusActive}
	if err := app.store.insert(tenant, enc, nil); err != nil {
		t.Fatal(err)
	}

	result, err := app.toolTenantApplyTemplate(ctx, map[string]any{
		"tenant_id": tenant.ID, "template_id": "tpl-parent",
		"project_description": "A bespoke client operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(*tenantTemplateApplyResult)
	if !imported || !applied || !got.Imported || got.TargetTemplateID != "tpl-child" || got.TargetProjectID != "child-project" {
		t.Fatalf("imported=%v applied=%v result=%+v", imported, applied, got)
	}
	if platform.getProjectID != "parent-project" || platform.getTemplateID != "tpl-parent" {
		t.Fatalf("SDK get project=%q template=%q", platform.getProjectID, platform.getTemplateID)
	}
	events, _ := app.store.recentEvents(tenant.ID, 10)
	if len(events) == 0 || events[0].Kind != "template_applied" {
		t.Fatalf("events=%+v", events)
	}
}

func TestPendingTemplateSurvivesSetupFallbackAndClearsAfterApply(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "pending-template", StatusSetupPending)
	pending := pendingTenantTemplate{
		TenantID: id, SourceProjectID: "parent-project",
		Template: testProjectTemplate(), Description: "Client setup",
	}
	if err := app.store.savePendingTemplate(pending); err != nil {
		t.Fatal(err)
	}
	loaded, err := app.store.getPendingTemplate(id)
	if err != nil || loaded == nil {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if loaded.Template.ID != pending.Template.ID || loaded.Description != pending.Description || !sameJSON(loaded.Template.Definition, pending.Template.Definition) {
		t.Fatalf("pending template changed: %+v", loaded)
	}
	if err := app.store.deletePendingTemplate(id); err != nil {
		t.Fatal(err)
	}
	loaded, err = app.store.getPendingTemplate(id)
	if err != nil || loaded != nil {
		t.Fatalf("pending template not cleared: loaded=%+v err=%v", loaded, err)
	}
}

func TestSameJSONIgnoresFormattingAndObjectOrder(t *testing.T) {
	if !sameJSON(json.RawMessage(`{"a":1,"b":[2]}`), json.RawMessage("{\n  \"b\": [2], \"a\": 1\n}")) {
		t.Fatal("semantically equal template definitions should match")
	}
	if sameJSON(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`)) {
		t.Fatal("different template definitions should not match")
	}
}

func TestTemplateErrorsMentionPermissionRefresh(t *testing.T) {
	app, ctx := newTestApp(t, tk.WithProjectID("parent-project"))
	_, err := app.toolTenantTemplateList(ctx, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "platform.templates.read") {
		t.Fatalf("error=%v", err)
	}
}
