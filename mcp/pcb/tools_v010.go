package main

import (
	"context"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) v010Tools() []sdk.Tool {
	id := map[string]any{"type": "integer", "minimum": 1}
	str := map[string]any{"type": "string"}
	measurement := map[string]any{"type": "object", "properties": map[string]any{"check_id": str, "value": map[string]any{"type": "number"}, "passed": map[string]any{"type": "boolean"}, "note": str}, "required": []string{"check_id"}, "additionalProperties": false}
	return []sdk.Tool{
		{Name: "pcb_fab_profiles", Description: "List built-in fab rule profiles (reference values) plus the design's own profiles and the active one. Select one with the fab_profile.select operation.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id}, nil), HandlerCtx: a.toolFabProfiles},
		{Name: "pcb_assembly_generate", Description: "Persist the assembly package: pick-and-place (centroid) CSV and assembly BOM, verified against the design.", InputSchema: designActionSchema(id), HandlerCtx: a.toolAssemblyGenerate},
		{Name: "pcb_firmware_header", Description: "Generate board_pins.h mapping net names to MCU GPIO numbers (PIN_<NET>). The firmware runtime defines the same constants automatically.", InputSchema: designActionSchema(id), HandlerCtx: a.toolFirmwareHeader},
		{Name: "pcb_bringup_plan", Description: "Return the revision's measurable bring-up checks (inspection, no-short resistance, rail voltages, firmware) with expected ranges.", InputSchema: designActionSchema(id), HandlerCtx: a.toolBringUpStatus},
		{Name: "pcb_bringup_record", Description: "Record bring-up measurements for one manufactured board. Numeric checks take value; inspection and firmware checks take passed.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "board_serial": str, "measurements": map[string]any{"type": "array", "minItems": 1, "maxItems": 200, "items": measurement}}, []string{"design_id", "board_serial", "measurements"}), HandlerCtx: a.toolBringUpRecord},
		{Name: "pcb_bringup_status", Description: "Return per-board bring-up results and the physical-validation status that feeds the readiness gate.", InputSchema: designActionSchema(id), HandlerCtx: a.toolBringUpStatus},
		{Name: "pcb_order_prepare", Description: "Validate and package a fabrication order (Gerbers, drills, optional assembly files, order.json) and track it as prepared. Placing the order happens at the fab; record the result with pcb_order_update.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "fabricator": str, "quantity": map[string]any{"type": "integer", "minimum": 1, "maximum": 100000}, "assembly": map[string]any{"type": "boolean"}, "surface_finish": str, "solder_mask": str, "copper_weight_oz": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "note": str}, []string{"design_id", "fabricator", "quantity"}), HandlerCtx: a.toolOrderPrepare},
		{Name: "pcb_order_update", Description: "Record a fab quote, order number, status change (quoted, placed, in_production, shipped, delivered, cancelled) or tracking for an order.", InputSchema: schemaObject(map[string]any{"order_id": id, "status": map[string]any{"type": "string", "enum": []string{"quoted", "placed", "in_production", "shipped", "delivered", "cancelled"}}, "external_order_id": str, "quote_amount": map[string]any{"type": "number", "minimum": 0}, "quote_currency": str, "tracking": str, "note": str}, []string{"order_id"}), HandlerCtx: a.toolOrderUpdate},
		{Name: "pcb_orders_list", Description: "List fabrication orders for a design, newest first.", InputSchema: schemaObject(map[string]any{"design_id": id}, []string{"design_id"}), HandlerCtx: a.toolOrdersList},
		{Name: "pcb_panelize", Description: "Panelize a revision for production: rows x columns copies with spacing, rails (default 5 mm top/bottom) carrying 3 fiducials and 4 NPTH tooling holes, separated by V-score lines (gerbers/V_Score.gbr) or routed tabs with mouse-bite NPTH perforations. Stores a verified panel ZIP (all Gerbers, PTH/NPTH drills, gbrjob, panel.json) as an artifact; the design revision is not changed.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "rows": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "columns": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "spacing_nm": map[string]any{"type": "integer", "minimum": 0, "maximum": 20_000_000}, "rail_width_nm": map[string]any{"type": "integer", "minimum": 0, "maximum": 30_000_000}, "rail_sides": map[string]any{"type": "string", "enum": []string{"top_bottom", "left_right", "all"}}, "separation": map[string]any{"type": "string", "enum": []string{"vscore", "tabs"}}, "tab_width_nm": map[string]any{"type": "integer", "minimum": 0, "maximum": 20_000_000}}, []string{"design_id", "rows", "columns"}), HandlerCtx: a.toolPanelize},
		{Name: "pcb_release_overview", Description: "Return everything the Release workspace shows: readiness gates, package contents, sourcing, bring-up, fab profile and orders.", InputSchema: designActionSchema(id), HandlerCtx: a.toolReleaseOverview},
	}
}

func (a *App) toolFabProfiles(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	out := map[string]any{"builtin": builtinFabProfiles()}
	if designID := int64Arg(args, "design_id"); designID > 0 {
		s, err := a.service(app)
		if err != nil {
			return nil, err
		}
		_, revision, def, err := s.revision(designID, int64Arg(args, "revision_id"))
		if err != nil {
			return nil, err
		}
		out["revision_id"] = revision.ID
		out["design_profiles"] = def.FabProfiles
		out["active_fab_profile_id"] = def.ActiveFabProfileID
		out["effective_rules"] = effectiveRules(def)
	}
	return out, nil
}

func (a *App) toolAssemblyGenerate(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	artifact, checks, err := s.AssemblyGenerate(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	return map[string]any{"artifact": artifact, "checks": checks}, err
}

func (a *App) toolFirmwareHeader(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	artifact, pins, err := s.FirmwareHeader(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
	return map[string]any{"artifact": artifact, "pins": pins}, err
}

func (a *App) toolBringUpStatus(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.BringUpStatus(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}

func (a *App) toolBringUpRecord(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	var inputs []BringUpInput
	if err := decodeOptionalArg(args, "measurements", &inputs); err != nil {
		return nil, err
	}
	status, recorded, err := s.BringUpRecord(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), stringArg(args, "board_serial"), inputs, callerName(ctx))
	return map[string]any{"status": status, "recorded": recorded}, err
}

func (a *App) toolOrderPrepare(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options := OrderOptions{Fabricator: stringArg(args, "fabricator"), Quantity: intArg(args, "quantity"), Assembly: boolArg(args, "assembly"), SurfaceFinish: stringArg(args, "surface_finish"), SolderMask: stringArg(args, "solder_mask"), CopperWeightOz: intArg(args, "copper_weight_oz"), Note: stringArg(args, "note")}
	order, artifact, err := s.OrderPrepare(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
	out := map[string]any{"order": order, "artifact": artifact}
	if page, ok := fabricatorUploadPages[options.Fabricator]; ok {
		out["upload_page"] = page
	}
	return out, err
}

func (a *App) toolOrderUpdate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	update := OrderUpdate{Status: stringArg(args, "status"), ExternalOrderID: stringArg(args, "external_order_id"), QuoteCurrency: stringArg(args, "quote_currency"), Tracking: stringArg(args, "tracking"), Note: stringArg(args, "note")}
	if value, ok := numberField(args["quote_amount"]); ok && args["quote_amount"] != nil {
		update.QuoteAmount = &value
	}
	order, err := s.OrderUpdate(int64Arg(args, "order_id"), update, callerName(ctx))
	return map[string]any{"order": order}, err
}

func (a *App) toolOrdersList(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	orders, err := s.Orders(int64Arg(args, "design_id"))
	return map[string]any{"orders": orders}, err
}

func (a *App) toolReleaseOverview(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.ReleaseOverview(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}

func (a *App) toolPanelize(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options := PanelOptions{Rows: intArg(args, "rows"), Columns: intArg(args, "columns"), SpacingNM: int64Arg(args, "spacing_nm"), RailWidthNM: int64Arg(args, "rail_width_nm"), RailSides: stringArg(args, "rail_sides"), Separation: stringArg(args, "separation"), TabWidthNM: int64Arg(args, "tab_width_nm")}
	artifact, report, err := s.Panelize(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
	return map[string]any{"artifact": artifact, "panel": report}, err
}
