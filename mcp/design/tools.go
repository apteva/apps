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
	definition := map[string]any{"type": "object", "description": "Safe apteva-design/v1 operation graph."}
	parameters := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "number"}}
	formats := map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"step", "stl", "3mf", "glb", "mesh-json"}}}
	return []sdk.Tool{
		{Name: "design_examples", Description: "Return the operation vocabulary, expression syntax, output formats, and complete canonical examples.", InputSchema: schemaObject(nil, nil), HandlerCtx: a.toolExamples},
		{Name: "designs_create", Description: "Create a project design and immutable first revision. Geometry is a safe declarative operation graph, not executable code.", InputSchema: schemaObject(map[string]any{
			"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"kind":       map[string]any{"type": "string", "enum": []string{"parametric", "sketch2d"}},
			"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"definition": definition, "parameters": parameters, "note": map[string]any{"type": "string"},
		}, []string{"name", "definition"}), HandlerCtx: a.toolDesignCreate},
		{Name: "designs_list", Description: "List designs in the current project.", InputSchema: schemaObject(map[string]any{
			"q": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"},
			"status": map[string]any{"type": "string", "enum": []string{"active", "archived", "all"}},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		}, nil), HandlerCtx: a.toolDesignList},
		{Name: "designs_get", Description: "Fetch a design with its current revision, latest build, validation, measurements, and artifacts.", InputSchema: schemaObject(map[string]any{"id": id}, []string{"id"}), HandlerCtx: a.toolDesignGet},
		{Name: "designs_archive", Description: "Archive or restore a design.", InputSchema: schemaObject(map[string]any{"id": id, "archived": map[string]any{"type": "boolean", "default": true}}, []string{"id"}), HandlerCtx: a.toolDesignArchive},
		{Name: "revisions_create", Description: "Create an immutable child revision. expected_parent_id prevents silently overwriting concurrent work. Omitted definition or parameters are inherited.", InputSchema: schemaObject(map[string]any{
			"design_id": id, "expected_parent_id": id, "definition": definition, "parameters": parameters,
			"note": map[string]any{"type": "string"},
		}, []string{"design_id", "expected_parent_id"}), HandlerCtx: a.toolRevisionCreate},
		{Name: "revisions_get", Description: "Fetch one immutable revision.", InputSchema: schemaObject(map[string]any{"id": id}, []string{"id"}), HandlerCtx: a.toolRevisionGet},
		{Name: "revisions_diff", Description: "Compare definition, parameters, source hashes, and latest geometry reports for two revisions.", InputSchema: schemaObject(map[string]any{"from_revision_id": id, "to_revision_id": id}, []string{"from_revision_id", "to_revision_id"}), HandlerCtx: a.toolRevisionDiff},
		{Name: "design_validate", Description: "Build the exact B-rep and evaluate geometry and caller-defined checks. Returns dimensions, volume, area, topology, and checks.", InputSchema: designBuildSchema(id, nil), HandlerCtx: a.toolValidate},
		{Name: "design_render", Description: "Build and persist a browser-ready triangle mesh preview.", InputSchema: designBuildSchema(id, nil), HandlerCtx: a.toolRender},
		{Name: "design_export", Description: "Build and persist neutral CAD and print artifacts. STEP retains exact B-rep geometry; STL/3MF are manufacturing meshes; GLB is for preview.", InputSchema: designBuildSchema(id, formats), HandlerCtx: a.toolExport},
		{Name: "design_artifacts", Description: "List generated artifacts for a design, optionally limited to one revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id}, []string{"design_id"}), HandlerCtx: a.toolArtifacts},
		{Name: "manufacturing_package_create", Description: "Validate and create a traceable ZIP with source, resolved parameters, measurements, checks, hashes, STEP, STL, 3MF, and GLB. Refuses failed revisions.", InputSchema: designBuildSchema(id, nil), HandlerCtx: a.toolManufacturingPackage},
	}
}

func schemaObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func designBuildSchema(id, formats map[string]any) map[string]any {
	properties := map[string]any{"design_id": id, "revision_id": id}
	if formats != nil {
		properties["formats"] = formats
	}
	return schemaObject(properties, []string{"design_id"})
}

func (a *App) toolExamples(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return designExamples(), nil
}

func (a *App) toolDesignCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	definitionRaw, err := marshalRequired(args, "definition")
	if err != nil {
		return nil, err
	}
	canonical, definition, err := normalizeDefinition(definitionRaw, service.maxOperations)
	if err != nil {
		return nil, err
	}
	parametersRaw, err := marshalOptional(args, "parameters", []byte("{}"))
	if err != nil {
		return nil, err
	}
	parametersRaw, err = normalizeParameters(parametersRaw, definition)
	if err != nil {
		return nil, err
	}
	design, err := service.store.CreateDesign(service.project, CreateDesignInput{
		Name: stringArg(args, "name"), Description: stringArg(args, "description"), Kind: stringArg(args, "kind"),
		Tags: stringSliceArg(args, "tags"), Definition: canonical, Parameters: parametersRaw,
		Note: stringArg(args, "note"), Author: callerName(ctx),
	})
	if err != nil {
		return nil, err
	}
	app.Emit("design.created", map[string]any{"design_id": design.ID, "revision_id": design.CurrentRevisionID, "name": design.Name})
	return map[string]any{"design": design}, nil
}

func (a *App) toolDesignList(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	designs, err := service.store.ListDesigns(service.project, stringArg(args, "q"), stringArg(args, "kind"), stringArg(args, "status"), intArg(args, "limit"))
	return map[string]any{"designs": designs}, err
}

func (a *App) toolDesignGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	design, err := service.store.GetDesign(service.project, int64Arg(args, "id"))
	return map[string]any{"design": design}, err
}

func (a *App) toolDesignArchive(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	archived := true
	if value, ok := args["archived"].(bool); ok {
		archived = value
	}
	design, err := service.store.ArchiveDesign(service.project, int64Arg(args, "id"), archived)
	return map[string]any{"design": design}, err
}

func (a *App) toolRevisionCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	designID := int64Arg(args, "design_id")
	parentID := int64Arg(args, "expected_parent_id")
	parent, err := service.store.GetRevision(service.project, parentID)
	if err != nil {
		return nil, err
	}
	if parent.DesignID != designID {
		return nil, errors.New("expected parent does not belong to design")
	}
	definitionRaw := []byte(parent.Definition)
	if _, ok := args["definition"]; ok {
		definitionRaw, err = marshalRequired(args, "definition")
		if err != nil {
			return nil, err
		}
	}
	canonical, definition, err := normalizeDefinition(definitionRaw, service.maxOperations)
	if err != nil {
		return nil, err
	}
	parametersRaw := []byte(parent.Parameters)
	if _, ok := args["parameters"]; ok {
		parametersRaw, err = marshalRequired(args, "parameters")
		if err != nil {
			return nil, err
		}
	}
	parametersRaw, err = normalizeParameters(parametersRaw, definition)
	if err != nil {
		return nil, err
	}
	revision, err := service.store.CreateRevision(service.project, CreateRevisionInput{
		DesignID: designID, ExpectedParent: parentID, Definition: canonical, Parameters: parametersRaw,
		Note: stringArg(args, "note"), Author: callerName(ctx),
	})
	if err != nil {
		return nil, err
	}
	app.Emit("design.revision.created", map[string]any{"design_id": designID, "revision_id": revision.ID, "parent_revision_id": parentID, "revision_number": revision.RevisionNumber})
	return map[string]any{"revision": revision}, nil
}

func (a *App) toolRevisionGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	revision, err := service.store.GetRevision(service.project, int64Arg(args, "id"))
	return map[string]any{"revision": revision}, err
}

func (a *App) toolRevisionDiff(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	from, err := service.store.GetRevision(service.project, int64Arg(args, "from_revision_id"))
	if err != nil {
		return nil, err
	}
	to, err := service.store.GetRevision(service.project, int64Arg(args, "to_revision_id"))
	if err != nil {
		return nil, err
	}
	if from.DesignID != to.DesignID {
		return nil, errors.New("revisions belong to different designs")
	}
	fromBuild, _ := service.store.LatestBuild(from.DesignID, from.ID)
	toBuild, _ := service.store.LatestBuild(to.DesignID, to.ID)
	return map[string]any{
		"from": from, "to": to, "definition": diffJSON(from.Definition, to.Definition),
		"parameters": diffJSON(from.Parameters, to.Parameters), "geometry": map[string]any{"from": fromBuild, "to": toBuild},
	}, nil
}

func (a *App) toolValidate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return service.Build(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), []string{"mesh-json"})
}

func (a *App) toolRender(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return service.Build(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), []string{"mesh-json", "glb"})
}

func (a *App) toolExport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	formats := stringSliceArg(args, "formats")
	if len(formats) == 0 {
		formats = []string{"step", "stl", "3mf"}
	}
	return service.Build(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), formats)
}

func (a *App) toolArtifacts(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	artifacts, err := service.store.ListArtifacts(service.project, int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	return map[string]any{"artifacts": artifacts}, err
}

func (a *App) toolManufacturingPackage(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	service, err := a.service(app)
	if err != nil {
		return nil, err
	}
	artifact, err := service.ManufacturingPackage(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	return map[string]any{"artifact": artifact}, err
}

func marshalRequired(args map[string]any, key string) ([]byte, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, fmt.Errorf("%s required", key)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", key, err)
	}
	return body, nil
}

func marshalOptional(args map[string]any, key string, fallback []byte) ([]byte, error) {
	if _, ok := args[key]; !ok {
		return fallback, nil
	}
	return marshalRequired(args, key)
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func int64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case float64:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case json.Number:
		number, _ := value.Int64()
		return number
	default:
		return 0
	}
}

func intArg(args map[string]any, key string) int { return int(int64Arg(args, key)) }

func stringSliceArg(args map[string]any, key string) []string {
	value, ok := args[key]
	if !ok {
		return nil
	}
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		output := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok && strings.TrimSpace(item) != "" {
				output = append(output, strings.TrimSpace(item))
			}
		}
		return output
	default:
		return nil
	}
}

func callerName(ctx context.Context) string {
	if caller := sdk.CallerFrom(ctx); caller != nil && caller.AgentID > 0 {
		return fmt.Sprintf("agent:%d", caller.AgentID)
	}
	return "agent"
}
