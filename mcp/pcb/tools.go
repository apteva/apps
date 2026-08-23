package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	definition := map[string]any{"type": "object", "description": "Canonical apteva-pcb/v1 definition."}
	operation := map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"type": "string"}}, "required": []string{"type"}}
	return []sdk.Tool{
		{Name: "pcb_examples", Description: "Return the native PCB schema example, units, operations, and compatibility boundary.", InputSchema: schemaObject(nil, nil), HandlerCtx: a.toolExamples},
		{Name: "pcb_designs_create", Description: "Create a project PCB design and immutable first revision.", InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}, "definition": definition, "note": map[string]any{"type": "string"}}, []string{"name"}), HandlerCtx: a.toolDesignCreate},
		{Name: "pcb_designs_list", Description: "List PCB designs in the current project.", InputSchema: schemaObject(map[string]any{"q": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"active", "archived", "all"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, nil), HandlerCtx: a.toolDesignList},
		{Name: "pcb_designs_get", Description: "Fetch a PCB design with current revision, validation, and artifacts.", InputSchema: schemaObject(map[string]any{"id": id}, []string{"id"}), HandlerCtx: a.toolDesignGet},
		{Name: "pcb_designs_archive", Description: "Archive or restore a PCB design.", InputSchema: schemaObject(map[string]any{"id": id, "archived": map[string]any{"type": "boolean", "default": true}}, []string{"id"}), HandlerCtx: a.toolDesignArchive},
		{Name: "pcb_revisions_create", Description: "Create a full-definition immutable child revision with optimistic concurrency.", InputSchema: schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "definition": definition, "note": map[string]any{"type": "string"}}, []string{"design_id", "expected_parent_id", "definition"}), HandlerCtx: a.toolRevisionCreate},
		{Name: "pcb_revisions_get", Description: "Fetch one immutable PCB revision.", InputSchema: schemaObject(map[string]any{"id": id}, []string{"id"}), HandlerCtx: a.toolRevisionGet},
		{Name: "pcb_revisions_diff", Description: "Return a semantic native-object diff between revisions.", InputSchema: schemaObject(map[string]any{"from_revision_id": id, "to_revision_id": id}, []string{"from_revision_id", "to_revision_id"}), HandlerCtx: a.toolRevisionDiff},
		{Name: "pcb_operations_apply", Description: "Atomically apply typed native operations and create an immutable revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "operations": map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": operation}, "note": map[string]any{"type": "string"}}, []string{"design_id", "expected_parent_id", "operations"}), HandlerCtx: a.toolOperationsApply},
		{Name: "pcb_validate", Description: "Run the native electrical-rule and design-rule validator.", InputSchema: designActionSchema(id), HandlerCtx: a.toolValidate},
		{Name: "pcb_render", Description: "Persist a deterministic SVG board preview.", InputSchema: designActionSchema(id), HandlerCtx: a.toolRender},
		{Name: "pcb_bom_generate", Description: "Persist a deterministic grouped BOM CSV.", InputSchema: designActionSchema(id), HandlerCtx: a.toolBOM},
		{Name: "pcb_artifacts_list", Description: "List generated PCB artifacts.", InputSchema: designActionSchema(id), HandlerCtx: a.toolArtifacts},
		{Name: "pcb_release_create", Description: "Validate and create a traceable native release ZIP; errors block release.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolRelease},
		{Name: "pcb_components_search", Description: "Search the optional component-data integration without coupling the PCB model to it.", InputSchema: schemaObject(map[string]any{"query": map[string]any{"type": "string"}, "country": map[string]any{"type": "string"}, "currency": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, []string{"query"}), HandlerCtx: a.toolComponentsSearch},
		{Name: "pcb_bom_source", Description: "Match up to twenty revision MPNs through the optional component-data integration.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "country": map[string]any{"type": "string"}, "currency": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolBOMSource},
		{Name: "pcb_providers_status", Description: "Report native app and provider integration bindings and the safe capabilities enabled by this version.", InputSchema: schemaObject(nil, nil), HandlerCtx: a.toolProvidersStatus},
	}
}

func schemaObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func designActionSchema(id map[string]any) map[string]any {
	return schemaObject(map[string]any{"design_id": id, "revision_id": id}, []string{"design_id"})
}
func (a *App) toolExamples(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return pcbExamples(), nil
}

func (a *App) toolDesignCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	name := stringArg(args, "name")
	raw := []byte(nil)
	if _, ok := args["definition"]; ok {
		raw, err = marshalRequired(args, "definition")
		if err != nil {
			return nil, err
		}
	}
	canonical, _, hash, err := normalizeDefinition(raw, name)
	if err != nil {
		return nil, err
	}
	d, err := s.store.CreateDesign(s.project, name, canonical, nil, hash, stringArg(args, "note"), callerName(ctx))
	if err == nil {
		app.Emit("pcb.design.created", map[string]any{"design_id": d.ID, "revision_id": d.CurrentRevisionID, "name": d.Name})
	}
	return map[string]any{"design": d}, err
}
func (a *App) toolDesignList(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	d, err := s.store.ListDesigns(s.project, stringArg(args, "q"), stringArg(args, "status"), intArg(args, "limit"))
	return map[string]any{"designs": d}, err
}
func (a *App) toolDesignGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	d, err := s.store.GetDesign(s.project, int64Arg(args, "id"))
	return map[string]any{"design": d}, err
}
func (a *App) toolDesignArchive(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	archived := true
	if v, ok := args["archived"].(bool); ok {
		archived = v
	}
	d, err := s.store.ArchiveDesign(s.project, int64Arg(args, "id"), archived)
	return map[string]any{"design": d}, err
}
func (a *App) toolRevisionCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	designID, parentID := int64Arg(args, "design_id"), int64Arg(args, "expected_parent_id")
	parent, err := s.store.GetRevision(s.project, parentID)
	if err != nil {
		return nil, err
	}
	if parent.DesignID != designID {
		return nil, errors.New("expected parent does not belong to design")
	}
	raw, err := marshalRequired(args, "definition")
	if err != nil {
		return nil, err
	}
	canonical, _, hash, err := normalizeDefinition(raw, "")
	if err != nil {
		return nil, err
	}
	revision, err := s.store.CreateRevision(s.project, designID, parentID, canonical, nil, hash, stringArg(args, "note"), callerName(ctx))
	if err == nil {
		app.Emit("pcb.revision.created", revisionEvent(revision))
	}
	return map[string]any{"revision": revision}, err
}
func (a *App) toolRevisionGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	r, err := s.store.GetRevision(s.project, int64Arg(args, "id"))
	return map[string]any{"revision": r}, err
}
func (a *App) toolRevisionDiff(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	from, err := s.store.GetRevision(s.project, int64Arg(args, "from_revision_id"))
	if err != nil {
		return nil, err
	}
	to, err := s.store.GetRevision(s.project, int64Arg(args, "to_revision_id"))
	if err != nil {
		return nil, err
	}
	if from.DesignID != to.DesignID {
		return nil, errors.New("revisions belong to different designs")
	}
	aDef, err := decodeDefinition(from.Definition)
	if err != nil {
		return nil, err
	}
	bDef, err := decodeDefinition(to.Definition)
	if err != nil {
		return nil, err
	}
	return semanticDiff(from.ID, to.ID, aDef, bDef, from.SourceSHA256, to.SourceSHA256), nil
}
func (a *App) toolOperationsApply(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	designID, parentID := int64Arg(args, "design_id"), int64Arg(args, "expected_parent_id")
	parent, err := s.store.GetRevision(s.project, parentID)
	if err != nil {
		return nil, err
	}
	if parent.DesignID != designID {
		return nil, errors.New("expected parent does not belong to design")
	}
	base, err := decodeDefinition(parent.Definition)
	if err != nil {
		return nil, err
	}
	opsRaw, err := marshalRequired(args, "operations")
	if err != nil {
		return nil, err
	}
	var ops []Operation
	if err = json.Unmarshal(opsRaw, &ops); err != nil {
		return nil, fmt.Errorf("invalid operations: %w", err)
	}
	canonical, _, hash, err := applyOperations(base, ops)
	if err != nil {
		return nil, err
	}
	revision, err := s.store.CreateRevision(s.project, designID, parentID, canonical, opsRaw, hash, stringArg(args, "note"), callerName(ctx))
	if err == nil {
		app.Emit("pcb.revision.created", revisionEvent(revision))
	}
	return map[string]any{"revision": revision}, err
}
func (a *App) toolValidate(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.Validate(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}
func (a *App) toolRender(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.Render(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}
func (a *App) toolBOM(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.BOM(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}
func (a *App) toolArtifacts(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListArtifacts(s.project, int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	return map[string]any{"artifacts": items}, err
}
func (a *App) toolRelease(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.Release(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), stringArg(args, "note"))
}

func (a *App) toolComponentsSearch(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	bound := app.IntegrationFor("component_data")
	if bound == nil {
		return nil, errors.New("no component-data provider is bound; connect Nexar/Octopart or continue with local component fields")
	}
	input := map[string]any{"query": stringArg(args, "query"), "country": configOrArg(app, args, "country", "default_country", "US"), "currency": configOrArg(app, args, "currency", "default_currency", "USD"), "authorizedOnly": true}
	if n := intArg(args, "limit"); n > 0 {
		input["limit"] = n
	}
	return executeProvider(app, bound, bound.ToolFor("components.search"), input)
}
func (a *App) toolBOMSource(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	_, revision, def, err := s.revision(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	if err != nil {
		return nil, err
	}
	bound := app.IntegrationFor("component_data")
	if bound == nil {
		return nil, errors.New("no component-data provider is bound")
	}
	seen := map[string]bool{}
	parts := []map[string]any{}
	missing := []string{}
	for _, c := range def.Components {
		mpn := strings.TrimSpace(c.MPN)
		if mpn == "" {
			missing = append(missing, c.Designator)
			continue
		}
		if !seen[mpn] && len(parts) < 20 {
			seen[mpn] = true
			parts = append(parts, map[string]any{"mpn": mpn})
		}
	}
	if len(parts) == 0 {
		return nil, errors.New("revision BOM has no manufacturer part numbers")
	}
	result, err := executeProvider(app, bound, bound.ToolFor("components.match_bom"), map[string]any{"parts": parts, "country": configOrArg(app, args, "country", "default_country", "US"), "currency": configOrArg(app, args, "currency", "default_currency", "USD"), "authorizedOnly": true})
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision_id": revision.ID, "matched_mpns": len(parts), "missing_mpn_designators": missing, "provider": bound.AppSlug, "result": result}, nil
}
func (a *App) toolProvidersStatus(_ context.Context, app *sdk.AppCtx, _ map[string]any) (any, error) {
	storage := app.IntegrationFor("storage")
	component, fab := app.IntegrationFor("component_data"), app.IntegrationFor("pcb_fabricator")
	return map[string]any{
		"native_engine":  map[string]any{"schema": pcbSchema, "engine": engineVersion, "external_engine_dependency": false},
		"storage":        bindingStatus(storage, true, "native PCB artifacts are persisted through the selected Storage app binding"),
		"component_data": bindingStatus(component, true, "available through the selected component-data connection"),
		"pcb_fabricator": bindingStatus(fab, false, "discovery only in v0.1; quote/order disabled"),
	}, nil
}

func executeProvider(app *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, input map[string]any) (any, error) {
	if tool == "" {
		return nil, errors.New("bound provider does not map the required capability")
	}
	res, err := app.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("provider request failed (status %d)", func() int {
			if res == nil {
				return 0
			}
			return res.Status
		}())
	}
	var parsed any
	if err = json.Unmarshal(res.Data, &parsed); err != nil {
		return map[string]any{"provider": bound.AppSlug, "raw": string(res.Data)}, nil
	}
	return map[string]any{"provider": bound.AppSlug, "data": parsed}, nil
}
func bindingStatus(bound *sdk.BoundIntegration, executable bool, note string) map[string]any {
	if bound == nil {
		return map[string]any{"bound": false, "executable": false}
	}
	out := map[string]any{"bound": true, "kind": bound.Kind, "executable": executable, "note": note}
	if bound.Kind == "app" {
		out["app_name"] = bound.AppName
		out["install_id"] = bound.InstallID
	} else {
		out["provider"] = bound.AppSlug
		out["connection_id"] = bound.ConnectionID
	}
	return out
}
func configOrArg(app *sdk.AppCtx, args map[string]any, arg, config, fallback string) string {
	if v := stringArg(args, arg); v != "" {
		return v
	}
	if v := strings.TrimSpace(app.Config().Get(config)); v != "" {
		return v
	}
	return fallback
}
func revisionEvent(r *Revision) map[string]any {
	return map[string]any{"design_id": r.DesignID, "revision_id": r.ID, "parent_revision_id": r.ParentID, "revision_number": r.Number}
}
func marshalRequired(args map[string]any, key string) ([]byte, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, fmt.Errorf("%s required", key)
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", key, err)
	}
	return body, nil
}
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}
func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func intArg(args map[string]any, key string) int { return int(int64Arg(args, key)) }
func callerName(ctx context.Context) string {
	if c := sdk.CallerFrom(ctx); c != nil && c.AgentID > 0 {
		return fmt.Sprintf("agent:%d", c.AgentID)
	}
	return "agent"
}
