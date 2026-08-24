package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	definition := map[string]any{"type": "object", "description": "Canonical apteva-pcb/v1 definition."}
	operation := map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"type": "string"}}, "required": []string{"type"}}
	routeOptions := map[string]any{"type": "object", "description": "Native deterministic routing options including net_ids, layers, grid_nm, clearance_nm, widths, via geometry, and replace_existing."}
	simulationOptions := map[string]any{"type": "object", "description": "Simulation duration, step, sources, probes, and fault injection."}
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
		{Name: "pcb_route_analyze", Description: "Analyze routability and return a deterministic reviewable route plan without changing the design.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions}, []string{"design_id"}), HandlerCtx: a.toolRouteSuggest},
		{Name: "pcb_route_suggest", Description: "Suggest clearance-safe traces and vias as typed operations without changing the design.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions}, []string{"design_id"}), HandlerCtx: a.toolRouteSuggest},
		{Name: "pcb_route_apply", Description: "Route selected or unrouted nets and create an immutable revision after review.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions, "allow_partial": map[string]any{"type": "boolean"}, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolRouteApply},
		{Name: "pcb_route_selected", Description: "Route the net_ids in options and create an immutable revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions, "allow_partial": map[string]any{"type": "boolean"}, "note": map[string]any{"type": "string"}}, []string{"design_id", "options"}), HandlerCtx: a.toolRouteApply},
		{Name: "pcb_route_all", Description: "Route every incomplete net and create an immutable revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions, "allow_partial": map[string]any{"type": "boolean"}, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolRouteApply},
		{Name: "pcb_route_optimize", Description: "Replace existing routing for selected nets with a deterministic optimized route revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": routeOptions, "allow_partial": map[string]any{"type": "boolean"}, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolRouteOptimize},
		{Name: "pcb_route_remove", Description: "Remove PCB Studio-generated autoroutes for selected nets and create a revision.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "net_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolRouteRemove},
		{Name: "pcb_simulation_create", Description: "Save revisioned native simulation sources, probes, and virtual-device configuration.", InputSchema: schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "simulation": map[string]any{"type": "object"}, "note": map[string]any{"type": "string"}}, []string{"design_id", "expected_parent_id", "simulation"}), HandlerCtx: a.toolSimulationCreate},
		{Name: "pcb_simulation_run", Description: "Run deterministic native DC, transient RC, and event-driven digital simulation and persist the result.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": simulationOptions}, []string{"design_id"}), HandlerCtx: a.toolSimulationRun},
		{Name: "pcb_simulation_probe", Description: "Run the electrical model with temporary probes and persist waveforms.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": simulationOptions}, []string{"design_id"}), HandlerCtx: a.toolSimulationRun},
		{Name: "pcb_simulation_fault_set", Description: "Run simulation with explicit open-component or short-net faults.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": simulationOptions}, []string{"design_id", "options"}), HandlerCtx: a.toolSimulationRun},
		{Name: "pcb_simulation_results_get", Description: "Load a persisted native simulation result artifact.", InputSchema: schemaObject(map[string]any{"artifact_id": id}, []string{"artifact_id"}), HandlerCtx: a.toolSimulationResultGet},
		{Name: "pcb_simulation_compare", Description: "Compare final voltages and digital states from two simulation artifacts.", InputSchema: schemaObject(map[string]any{"from_artifact_id": id, "to_artifact_id": id}, []string{"from_artifact_id", "to_artifact_id"}), HandlerCtx: a.toolSimulationCompare},
		{Name: "pcb_firmware_run", Description: "Run an Arduino-compatible sketch against PCB Studio virtual GPIO, serial, I2C, and sensor models; optionally invoke a bound Functions compiler sandbox.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "source": map[string]any{"type": "string"}, "language": map[string]any{"type": "string"}, "board": map[string]any{"type": "string"}, "iterations": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "executor_function": map[string]any{"type": "string"}, "sensor_values": map[string]any{"type": "object"}}, []string{"design_id", "source"}), HandlerCtx: a.toolFirmwareRun},
		{Name: "pcb_validate", Description: "Run the native electrical-rule and design-rule validator.", InputSchema: designActionSchema(id), HandlerCtx: a.toolValidate},
		{Name: "pcb_render", Description: "Persist a deterministic SVG board preview.", InputSchema: designActionSchema(id), HandlerCtx: a.toolRender},
		{Name: "pcb_bom_generate", Description: "Persist a deterministic grouped BOM CSV.", InputSchema: designActionSchema(id), HandlerCtx: a.toolBOM},
		{Name: "pcb_manufacturing_generate", Description: "Validate and persist a deterministic Gerber X2 plus Excellon manufacturing ZIP.", InputSchema: designActionSchema(id), HandlerCtx: a.toolManufacturing},
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

func (a *App) toolRouteSuggest(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options, err := routeOptionsArg(args)
	if err != nil {
		return nil, err
	}
	plan, err := s.RouteSuggest(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
	return map[string]any{"plan": plan}, err
}

func (a *App) toolRouteApply(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options, err := routeOptionsArg(args)
	if err != nil {
		return nil, err
	}
	revision, plan, err := s.RouteApply(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options, stringArg(args, "note"), callerName(ctx), boolArg(args, "allow_partial"))
	return map[string]any{"revision": revision, "plan": plan}, err
}

func (a *App) toolRouteOptimize(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	options, err := routeOptionsArg(args)
	if err != nil {
		return nil, err
	}
	options.ReplaceExisting = true
	copy := cloneArgs(args)
	body, _ := json.Marshal(options)
	var generic map[string]any
	_ = json.Unmarshal(body, &generic)
	copy["options"] = generic
	return a.toolRouteApply(ctx, app, copy)
}

func (a *App) toolRouteRemove(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	var netIDs []string
	if err := decodeOptionalArg(args, "net_ids", &netIDs); err != nil {
		return nil, err
	}
	revision, err := s.RouteRemove(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), netIDs, stringArg(args, "note"), callerName(ctx))
	return map[string]any{"revision": revision}, err
}

func (a *App) toolSimulationCreate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	var simulation SimulationSpec
	if err := decodeOptionalArg(args, "simulation", &simulation); err != nil {
		return nil, err
	}
	copy := cloneArgs(args)
	copy["operations"] = []any{map[string]any{"type": "simulation.set", "simulation": simulation}}
	return a.toolOperationsApply(ctx, app, copy)
}

func (a *App) toolSimulationRun(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	var options SimulationOptions
	if err := decodeOptionalArg(args, "options", &options); err != nil {
		return nil, err
	}
	return s.Simulate(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
}

func (a *App) toolSimulationResultGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	result, artifact, err := loadSimulationArtifact(s, int64Arg(args, "artifact_id"))
	return map[string]any{"result": result, "artifact": artifact}, err
}

func (a *App) toolSimulationCompare(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	from, fromArtifact, err := loadSimulationArtifact(s, int64Arg(args, "from_artifact_id"))
	if err != nil {
		return nil, err
	}
	to, toArtifact, err := loadSimulationArtifact(s, int64Arg(args, "to_artifact_id"))
	if err != nil {
		return nil, err
	}
	nets := map[string]bool{}
	for id := range from.FinalVoltage {
		nets[id] = true
	}
	for id := range to.FinalVoltage {
		nets[id] = true
	}
	ids := make([]string, 0, len(nets))
	for id := range nets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	differences := []map[string]any{}
	for _, id := range ids {
		delta := to.FinalVoltage[id] - from.FinalVoltage[id]
		if absFloat(delta) > 1e-9 || from.FinalDigital[id] != to.FinalDigital[id] {
			differences = append(differences, map[string]any{"net_id": id, "from_voltage_v": from.FinalVoltage[id], "to_voltage_v": to.FinalVoltage[id], "delta_v": roundFloat(delta, 9), "from_digital": from.FinalDigital[id], "to_digital": to.FinalDigital[id]})
		}
	}
	return map[string]any{"from_artifact_id": fromArtifact.ID, "to_artifact_id": toArtifact.ID, "differences": differences}, nil
}

func (a *App) toolFirmwareRun(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options := FirmwareOptions{Source: stringArg(args, "source"), Language: stringArg(args, "language"), Board: stringArg(args, "board"), Iterations: intArg(args, "iterations"), ExecutorFunction: stringArg(args, "executor_function")}
	if err := decodeOptionalArg(args, "sensor_values", &options.SensorValues); err != nil {
		return nil, err
	}
	return s.Firmware(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
}

func routeOptionsArg(args map[string]any) (RouteOptions, error) {
	var options RouteOptions
	if err := decodeOptionalArg(args, "options", &options); err != nil {
		return options, err
	}
	return options, nil
}

func decodeOptionalArg(args map[string]any, key string, out any) error {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	if err = json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	return nil
}

func cloneArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range args {
		out[key] = value
	}
	return out
}
func boolArg(args map[string]any, key string) bool { value, _ := args[key].(bool); return value }
func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func loadSimulationArtifact(s *Service, artifactID int64) (*SimulationResult, *Artifact, error) {
	artifact, err := s.store.GetArtifact(s.project, artifactID)
	if err != nil {
		return nil, nil, err
	}
	if artifact.Kind != "simulation" || artifact.Format != "json" {
		return nil, artifact, fmt.Errorf("artifact %d is not a simulation result", artifactID)
	}
	body, err := os.ReadFile(artifact.LocalPath)
	if err != nil {
		return nil, artifact, err
	}
	var result SimulationResult
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, artifact, err
	}
	return &result, artifact, nil
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
func (a *App) toolManufacturing(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.Manufacturing(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
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
	executor := app.IntegrationFor("firmware_executor")
	return map[string]any{
		"native_engine":     map[string]any{"schema": pcbSchema, "engine": engineVersion, "routing": routingSchema, "simulation": simulationSchema, "firmware_runtime": "apteva-arduino-behavioral/0.3", "external_engine_dependency": false},
		"storage":           bindingStatus(storage, true, "native PCB artifacts are persisted through the selected Storage app binding"),
		"component_data":    bindingStatus(component, true, "available through the selected component-data connection"),
		"pcb_fabricator":    bindingStatus(fab, false, "provider discovery is available; quote/order remains approval-gated"),
		"firmware_executor": bindingStatus(executor, true, "optional Functions sandbox for full Arduino compiler/runtime adapters; native behavioral execution works without it"),
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
		if bound.Role == "storage" {
			out["app_name"] = "storage"
		} else {
			out["app_name"] = bound.AppName
		}
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
