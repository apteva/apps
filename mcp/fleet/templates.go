package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const tenantTemplateApplyTimeout = 5 * time.Minute

type pendingTenantTemplate struct {
	TenantID        string
	SourceProjectID string
	Template        sdk.ProjectTemplate
	Description     string
}

type tenantProject struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type tenantTemplateApplyResult struct {
	Status           string `json:"status"`
	SourceProjectID  string `json:"source_project_id"`
	SourceTemplateID string `json:"source_template_id"`
	SourceRevision   int    `json:"source_revision,omitempty"`
	TargetProjectID  string `json:"target_project_id,omitempty"`
	TargetTemplateID string `json:"target_template_id,omitempty"`
	Imported         bool   `json:"imported"`
	ApplyResult      any    `json:"apply_result,omitempty"`
	Error            string `json:"error,omitempty"`
}

func pendingTemplateStatus(p pendingTenantTemplate) tenantTemplateApplyResult {
	return tenantTemplateApplyResult{
		Status: "pending", SourceProjectID: p.SourceProjectID,
		SourceTemplateID: p.Template.ID, SourceRevision: p.Template.Revision,
	}
}

func (s *store) savePendingTemplate(p pendingTenantTemplate) error {
	raw, err := json.Marshal(p.Template)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO fleet_pending_templates
			(tenant_id, source_project_id, source_template_id, template_json, description)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id) DO UPDATE SET
			source_project_id=excluded.source_project_id,
			source_template_id=excluded.source_template_id,
			template_json=excluded.template_json,
			description=excluded.description,
			updated_at=CURRENT_TIMESTAMP`,
		p.TenantID, p.SourceProjectID, p.Template.ID, string(raw), p.Description)
	return err
}

func (s *store) getPendingTemplate(tenantID string) (*pendingTenantTemplate, error) {
	var sourceProjectID, sourceTemplateID, raw, description string
	err := s.db.QueryRow(`
		SELECT source_project_id, source_template_id, template_json, description
		FROM fleet_pending_templates WHERE tenant_id=?`, tenantID).
		Scan(&sourceProjectID, &sourceTemplateID, &raw, &description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var template sdk.ProjectTemplate
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		return nil, fmt.Errorf("decode pending template: %w", err)
	}
	if template.ID == "" {
		template.ID = sourceTemplateID
	}
	return &pendingTenantTemplate{
		TenantID: tenantID, SourceProjectID: sourceProjectID,
		Template: template, Description: description,
	}, nil
}

func (s *store) deletePendingTemplate(tenantID string) error {
	_, err := s.db.Exec(`DELETE FROM fleet_pending_templates WHERE tenant_id=?`, tenantID)
	return err
}

func projectTemplateAPI(ctx *sdk.AppCtx) (sdk.ProjectTemplateClient, error) {
	if ctx == nil {
		return nil, errors.New("project template context is unavailable")
	}
	api := ctx.ProjectTemplatesAPI()
	if api == nil {
		return nil, errors.New("platform project-template API is unavailable; Fleet requires app-sdk v0.67.0+ and platform.templates.read")
	}
	return api, nil
}

func sourceProjectID(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	projectID := strings.TrimSpace(getStr(args, "source_project_id"))
	if projectID == "" && ctx != nil {
		projectID = strings.TrimSpace(ctx.CurrentProject())
	}
	if projectID == "" {
		return "", errors.New("source_project_id is required outside a project-scoped call")
	}
	return projectID, nil
}

func (a *App) requestedParentTemplate(ctx *sdk.AppCtx, args map[string]any) (*pendingTenantTemplate, error) {
	templateID := strings.TrimSpace(getStr(args, "template_id"))
	if templateID == "" {
		return nil, nil
	}
	api, err := projectTemplateAPI(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := sourceProjectID(ctx, args)
	if err != nil {
		return nil, err
	}
	template, err := api.GetProjectTemplate(projectID, templateID)
	if err != nil {
		return nil, fmt.Errorf("read parent template %q: %w", templateID, err)
	}
	if template.Kind != sdk.ProjectSetupTemplateKind {
		return nil, fmt.Errorf("template %q has unsupported kind %q", template.ID, template.Kind)
	}
	if _, err := template.DecodeProjectSetup(); err != nil {
		return nil, fmt.Errorf("decode parent template %q: %w", template.ID, err)
	}
	description := strings.TrimSpace(getStr(args, "project_description"))
	if description == "" {
		description = strings.TrimSpace(template.Description)
	}
	if description == "" {
		description = template.Name
	}
	return &pendingTenantTemplate{
		SourceProjectID: projectID,
		Template:        *template,
		Description:     description,
	}, nil
}

func (a *App) toolTenantTemplateList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := projectTemplateAPI(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := sourceProjectID(ctx, args)
	if err != nil {
		return nil, err
	}
	includeSystem := true
	if _, specified := args["include_system"]; specified {
		includeSystem = boolArg(args, "include_system")
	}
	templates, err := api.ListProjectTemplates(projectID, sdk.ProjectTemplateListOptions{IncludeSystem: includeSystem})
	if err != nil {
		return nil, fmt.Errorf("list parent project templates: %w", err)
	}
	return map[string]any{"project_id": projectID, "templates": templates}, nil
}

func (a *App) toolTenantApplyTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := strings.TrimSpace(getStr(args, "tenant_id"))
	if tenantID == "" {
		return nil, errors.New("tenant_id is required")
	}
	pending, err := a.requestedParentTemplate(ctx, args)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, errors.New("template_id is required")
	}
	tenant, key, err := a.tenantControlAuth(tenantID)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(tenantID, "apply template")
	if err != nil {
		return nil, err
	}
	defer done()
	baseURL, err := a.internalTenantBaseURL(ctx, tenant)
	if err != nil {
		return nil, err
	}
	result, err := a.applyTemplateSnapshot(context.Background(), baseURL, key, *pending, getStr(args, "target_project_id"))
	if err != nil {
		_ = a.store.recordEvent(tenantID, "template_apply_failed", "user", map[string]any{
			"source_project_id": pending.SourceProjectID, "template_id": pending.Template.ID, "error": err.Error(),
		})
		return nil, err
	}
	_ = a.store.deletePendingTemplate(tenantID)
	_ = a.store.recordEvent(tenantID, "template_applied", "user", result)
	return result, nil
}

func (a *App) applyPendingTemplateBestEffort(ctx *sdk.AppCtx, tenant *Tenant, apiKey string) any {
	pending, err := a.store.getPendingTemplate(tenant.ID)
	if err != nil {
		return tenantTemplateApplyResult{Status: "failed", Error: err.Error()}
	}
	if pending == nil {
		return nil
	}
	baseURL, err := a.internalTenantBaseURL(ctx, tenant)
	if err != nil {
		return a.pendingTemplateFailure(tenant.ID, pending, err)
	}
	result, err := a.applyTemplateSnapshot(context.Background(), baseURL, apiKey, *pending, "")
	if err != nil {
		return a.pendingTemplateFailure(tenant.ID, pending, err)
	}
	_ = a.store.deletePendingTemplate(tenant.ID)
	_ = a.store.recordEvent(tenant.ID, "template_applied", "user", result)
	return result
}

func (a *App) pendingTemplateFailure(tenantID string, pending *pendingTenantTemplate, err error) tenantTemplateApplyResult {
	result := tenantTemplateApplyResult{
		Status: "failed", SourceProjectID: pending.SourceProjectID,
		SourceTemplateID: pending.Template.ID, SourceRevision: pending.Template.Revision,
		Error: err.Error(),
	}
	_ = a.store.recordEvent(tenantID, "template_apply_failed", "user", result)
	return result
}

func (a *App) applyTemplateSnapshot(ctx context.Context, baseURL, apiKey string, pending pendingTenantTemplate, requestedProjectID string) (*tenantTemplateApplyResult, error) {
	project, err := resolveTenantTemplateProject(ctx, baseURL, apiKey, requestedProjectID)
	if err != nil {
		return nil, err
	}
	targetTemplateID, imported, err := ensureTenantTemplate(ctx, baseURL, apiKey, project.ID, pending.Template)
	if err != nil {
		return nil, err
	}

	var applied any
	applyPath := "/api/projects/" + url.PathEscape(project.ID) + "/setup/apply"
	client := &http.Client{Timeout: tenantTemplateApplyTimeout}
	if err := tenantJSONWithClient(client, ctx, baseURL, apiKey, http.MethodPost, applyPath, map[string]any{
		"preset_id":   targetTemplateID,
		"description": pending.Description,
	}, &applied); err != nil {
		return nil, fmt.Errorf("apply template to tenant project %q: %w", project.ID, err)
	}
	return &tenantTemplateApplyResult{
		Status: "applied", SourceProjectID: pending.SourceProjectID,
		SourceTemplateID: pending.Template.ID, SourceRevision: pending.Template.Revision,
		TargetProjectID: project.ID, TargetTemplateID: targetTemplateID,
		Imported: imported, ApplyResult: applied,
	}, nil
}

func resolveTenantTemplateProject(ctx context.Context, baseURL, apiKey, requested string) (*tenantProject, error) {
	var projects []tenantProject
	if err := tenantJSON(ctx, baseURL, apiKey, http.MethodGet, "/api/projects", nil, &projects); err != nil {
		return nil, fmt.Errorf("list tenant projects: %w", err)
	}
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for i := range projects {
			if projects[i].ID == requested {
				return &projects[i], nil
			}
		}
		return nil, fmt.Errorf("target project %q not found on tenant", requested)
	}
	if len(projects) == 1 {
		return &projects[0], nil
	}
	for i := range projects {
		if strings.EqualFold(projects[i].Name, "Default") {
			return &projects[i], nil
		}
	}
	if len(projects) == 0 {
		return nil, errors.New("tenant has no project to receive the template")
	}
	return nil, errors.New("tenant has multiple projects; target_project_id is required")
}

func ensureTenantTemplate(ctx context.Context, baseURL, apiKey, projectID string, source sdk.ProjectTemplate) (string, bool, error) {
	path := "/api/projects/" + url.PathEscape(projectID) + "/templates"
	var catalog struct {
		Templates []sdk.ProjectTemplate `json:"templates"`
	}
	if err := tenantJSON(ctx, baseURL, apiKey, http.MethodGet, path, nil, &catalog); err != nil {
		return "", false, fmt.Errorf("list tenant project templates: %w", err)
	}
	for _, candidate := range catalog.Templates {
		if candidate.Kind == source.Kind && candidate.Name == source.Name && sameJSON(candidate.Definition, source.Definition) {
			return candidate.ID, false, nil
		}
	}
	var created sdk.ProjectTemplate
	if err := tenantJSON(ctx, baseURL, apiKey, http.MethodPost, path, map[string]any{
		"kind": sdk.ProjectSetupTemplateKind, "schema_version": 2,
		"name": source.Name, "description": source.Description,
		"definition": source.Definition,
	}, &created); err != nil {
		return "", false, fmt.Errorf("import template into tenant project: %w", err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", false, errors.New("tenant returned an empty imported template id")
	}
	return created.ID, true, nil
}

func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return bytes.Equal(leftRaw, rightRaw)
}
