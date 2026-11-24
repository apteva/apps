package main

import (
	"context"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) v011Tools() []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	str := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	sections := map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{"type": "string", "enum": append([]string{"all"}, templateSections...)}}
	return []sdk.Tool{
		{Name: "pcb_designs_copy", Description: "Create a new design from another design's revision. include picks sections (board, mechanical, fixed_components, components, keepouts, zones, routing, schematic, firmware, simulation, wiring, or all; default all). Copy only board+mechanical+fixed_components to start a block from a shared template. template=true marks the copy as a template.", InputSchema: schemaObject(map[string]any{"source_design_id": id, "revision_id": id, "name": str, "include": sections, "template": boolean, "note": str}, []string{"source_design_id", "name"}), HandlerCtx: a.toolDesignsCopy},
		{Name: "pcb_templates_list", Description: "List designs marked as templates (template.set operation or pcb_designs_copy template=true).", InputSchema: schemaObject(nil, nil), HandlerCtx: a.toolTemplatesList},
		{Name: "pcb_template_apply", Description: "Replace sections of a design with a template's (default board, mechanical, fixed_components: shared outline, holes and connector positions) as a new revision, and record the link for pcb_template_sync.", InputSchema: schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "template_design_id": id, "template_revision_id": id, "include": sections, "note": str}, []string{"design_id", "expected_parent_id", "template_design_id"}), HandlerCtx: a.toolTemplateApply},
		{Name: "pcb_template_sync", Description: "Push a template's current revision to every design copied from it or linked with pcb_template_apply (board, mechanical, fixed components, keepouts only). dry_run lists what would change.", InputSchema: schemaObject(map[string]any{"template_design_id": id, "dry_run": boolean}, []string{"template_design_id"}), HandlerCtx: a.toolTemplateSync},
		{Name: "pcb_i2c_bus_check", Description: "Check I2C addresses across blocks that share one bus (e.g. chained over Qwiic) and within each block. Devices come from components with electrical_model.i2c_address.", InputSchema: schemaObject(map[string]any{"design_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": id}}, []string{"design_ids"}), HandlerCtx: a.toolI2CBusCheck},
		{Name: "pcb_lcsc_search", Description: "Search the LCSC/JLCPCB assembly parts catalog by MPN, value or keyword. Returns LCSC number, package, stock, basic/extended and price breaks (USD).", InputSchema: schemaObject(map[string]any{"query": str, "package": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}, []string{"query"}), HandlerCtx: a.toolLCSCSearch},
		{Name: "pcb_lcsc_match", Description: "Look up every placed BOM line in the LCSC catalog (by MPN, else value+footprint) with stock and price at build_quantity. Chip resistors/capacitors without an MPN hit get a stocked same-value, same-size part (basis parametric). apply=true writes exact MPN matches (plus parametric ones with accept_parametric) to each component's lcsc field as a new revision (needs expected_parent_id); keyword hits are never applied.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "expected_parent_id": id, "build_quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 100000}, "apply": boolean, "overwrite": boolean, "accept_parametric": boolean}, []string{"design_id"}), HandlerCtx: a.toolLCSCMatch},
	}
}

func stringsArg(args map[string]any, key string) []string {
	out := []string{}
	if list, ok := args[key].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func int64sArg(args map[string]any, key string) []int64 {
	out := []int64{}
	if list, ok := args[key].([]any); ok {
		for _, item := range list {
			if v, ok := numberField(item); ok && v > 0 {
				out = append(out, int64(v))
			}
		}
	}
	return out
}

func (a *App) toolDesignsCopy(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	design, sections, err := s.CopyDesign(int64Arg(args, "source_design_id"), int64Arg(args, "revision_id"), stringArg(args, "name"), stringsArg(args, "include"), boolArg(args, "template"), stringArg(args, "note"), callerName(ctx))
	return map[string]any{"design": design, "include": sections}, err
}

func (a *App) toolTemplatesList(_ context.Context, app *sdk.AppCtx, _ map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	templates, err := s.Templates()
	return map[string]any{"templates": templates}, err
}

func (a *App) toolTemplateApply(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	revision, report, err := s.TemplateApply(int64Arg(args, "design_id"), int64Arg(args, "expected_parent_id"), int64Arg(args, "template_design_id"), int64Arg(args, "template_revision_id"), stringsArg(args, "include"), stringArg(args, "note"), callerName(ctx))
	return map[string]any{"revision": revision, "validation": report}, err
}

func (a *App) toolTemplateSync(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	results, err := s.TemplateSync(int64Arg(args, "template_design_id"), boolArg(args, "dry_run"), callerName(ctx))
	return map[string]any{"designs": results}, err
}

func (a *App) toolI2CBusCheck(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.I2CBusCheck(int64sArg(args, "design_ids"))
}

func (a *App) toolLCSCSearch(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	parts, err := s.LCSCSearch(app, stringArg(args, "query"), stringArg(args, "package"), intArg(args, "limit"))
	return map[string]any{"parts": parts}, err
}

func (a *App) toolLCSCMatch(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.LCSCMatch(app, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), int64Arg(args, "expected_parent_id"), intArg(args, "build_quantity"), boolArg(args, "apply"), boolArg(args, "overwrite"), boolArg(args, "accept_parametric"), callerName(ctx))
}
