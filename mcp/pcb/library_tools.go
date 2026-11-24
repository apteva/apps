package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) libraryTools(id map[string]any) []sdk.Tool {
	str := map[string]any{"type": "string"}
	nm := map[string]any{"type": "integer"}
	return []sdk.Tool{
		{Name: "pcb_library_list", Description: "List parts in the footprint/part library: built-in parts with datasheet-accurate land patterns plus this project's imported KiCad footprints. Filter by free-text query (name, MPN, LCSC, keyword) and category.", InputSchema: schemaObject(map[string]any{"query": str, "category": str, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, nil), HandlerCtx: a.toolLibraryList},
		{Name: "pcb_library_get", Description: "Return one library part: pads, courtyard, body, pins and functions, MPN/LCSC, datasheet, electrical model hints and provenance.", InputSchema: schemaObject(map[string]any{"part_id": str}, []string{"part_id"}), HandlerCtx: a.toolLibraryGet},
		{Name: "pcb_footprint_import_kicad", Description: "Import a KiCad 6-10 .kicad_mod footprint (pads smd/thru_hole/np_thru_hole, rect/roundrect/circle/oval, oval drills, rotation, courtyard, fab body, 3D model path) into the project library as a reusable part.", InputSchema: schemaObject(map[string]any{"content": str, "name": str, "category": str, "description": str, "mpn": str, "manufacturer": str, "lcsc": str, "datasheet_url": str, "designator_prefix": str}, []string{"content"}), HandlerCtx: a.toolFootprintImportKiCad},
		{Name: "pcb_component_place_from_library", Description: "Instantiate a library part into a design as a component with real pads, pins, frozen footprint contract and MPN, at x/y (nm), rotation and side; commits a component.add_from_library revision. Designator and id are auto-assigned when omitted.", InputSchema: schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "part_id": str, "x_nm": nm, "y_nm": nm, "rotation_udeg": nm, "side": map[string]any{"type": "string", "enum": []string{"front", "back"}}, "designator": str, "component_id": str, "value": str, "mpn": str, "name": str, "note": str}, []string{"design_id", "part_id", "x_nm", "y_nm"}), HandlerCtx: a.toolComponentPlaceFromLibrary},
	}
}

func (a *App) toolLibraryList(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	project := ""
	store := (*Store)(nil)
	if s, err := a.service(app); err == nil {
		project, store = s.project, s.store
	}
	parts, err := store.ListLibraryParts(project, stringArg(args, "query"), stringArg(args, "category"), intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"schema": librarySchema, "parts": parts, "count": len(parts), "categories": libraryCategories()}, nil
}

func (a *App) toolLibraryGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	partID := stringArg(args, "part_id")
	if partID == "" {
		return nil, errors.New("part_id required")
	}
	if part, ok := builtinLibraryPart(partID); ok {
		return map[string]any{"part": part}, nil
	}
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	part, err := s.store.GetLibraryPart(s.project, partID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"part": part}, nil
}

func (a *App) toolFootprintImportKiCad(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	content := stringArg(args, "content")
	if content == "" {
		return nil, errors.New("content required")
	}
	result, err := s.ImportKiCadFootprint([]byte(content), LibraryImportOptions{Name: stringArg(args, "name"), Category: stringArg(args, "category"), Description: stringArg(args, "description"), MPN: stringArg(args, "mpn"), Manufacturer: stringArg(args, "manufacturer"), LCSC: stringArg(args, "lcsc"), DatasheetURL: stringArg(args, "datasheet_url"), Designator: stringArg(args, "designator_prefix")}, callerName(ctx))
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) toolComponentPlaceFromLibrary(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	request := LibraryPlaceRequest{
		DesignID: int64Arg(args, "design_id"), ExpectedParentID: int64Arg(args, "expected_parent_id"), PartID: stringArg(args, "part_id"),
		Position:  Position{XNM: int64Arg(args, "x_nm"), YNM: int64Arg(args, "y_nm"), RotationUdeg: int64Arg(args, "rotation_udeg"), Side: stringArg(args, "side")},
		Component: Component{ID: stringArg(args, "component_id"), Designator: stringArg(args, "designator"), Value: stringArg(args, "value"), MPN: stringArg(args, "mpn"), Name: stringArg(args, "name")},
		Note:      stringArg(args, "note"),
	}
	revision, component, err := s.PlaceFromLibrary(request, callerName(ctx))
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision": revision, "component": component}, nil
}

// LibraryImportOptions overrides metadata the .kicad_mod file cannot carry.
type LibraryImportOptions struct {
	Name, Category, Description, MPN, Manufacturer, LCSC, DatasheetURL, Designator string
}

// ImportKiCadFootprint parses and stores a footprint in the project library.
func (s *Service) ImportKiCadFootprint(content []byte, options LibraryImportOptions, author string) (*KiCadFootprintImport, error) {
	result, err := importKiCadFootprint(content)
	if err != nil {
		return nil, err
	}
	part := result.Part
	if len(options.Name) > 160 {
		return nil, errors.New("name exceeds 160 characters")
	}
	if options.Name != "" {
		part.Name = options.Name
	}
	if options.Category != "" {
		part.Category = strings.ToLower(options.Category)
	}
	if options.Description != "" {
		part.Description = options.Description
	}
	if options.MPN != "" {
		part.MPN = options.MPN
	}
	if options.Manufacturer != "" {
		part.Manufacturer = options.Manufacturer
	}
	if options.LCSC != "" {
		part.LCSC = options.LCSC
	}
	if options.DatasheetURL != "" {
		part.DatasheetURL = options.DatasheetURL
	}
	if options.Designator != "" {
		part.Designator = strings.ToUpper(options.Designator)
	} else {
		part.Designator = designatorPrefixFor(part)
	}
	part.Provenance.ImportedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.store.UpsertLibraryPart(s.project, part, author); err != nil {
		return nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.library.part_imported", map[string]any{"part_id": part.ID, "name": part.Name, "pads": len(part.Footprint.Pads), "source_sha256": part.Provenance.SourceSHA256})
	}
	return result, nil
}

// LibraryPlaceRequest places one library part into a design revision.
type LibraryPlaceRequest struct {
	DesignID         int64
	ExpectedParentID int64
	PartID           string
	Position         Position
	Component        Component
	Note             string
}

// PlaceFromLibrary resolves the part (built-in or project), snapshots it
// into a component.add_from_library operation, and commits a revision.
func (s *Service) PlaceFromLibrary(request LibraryPlaceRequest, author string) (*Revision, *Component, error) {
	if request.PartID == "" {
		return nil, nil, errors.New("part_id required")
	}
	design, parent, def, err := s.revision(request.DesignID, request.ExpectedParentID)
	if err != nil {
		return nil, nil, err
	}
	part, err := s.store.GetLibraryPart(s.project, request.PartID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", request.PartID, err)
	}
	position := request.Position
	component := request.Component
	op := Operation{Type: "component.add_from_library", LibraryPartID: part.ID, LibraryPart: part, Position: &position, Component: &component}
	canonical, next, hash, err := applyOperations(def, []Operation{op})
	if err != nil {
		return nil, nil, err
	}
	operations, _ := json.Marshal([]Operation{op})
	note := request.Note
	if note == "" {
		note = "Place " + part.Name + " from library"
	}
	revision, err := s.store.CreateRevision(s.project, design.ID, parent.ID, canonical, operations, hash, note, author)
	if err != nil {
		return nil, nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
	}
	for _, candidate := range next.Components {
		if findComponent(def.Components, candidate.ID) < 0 {
			placed := candidate
			return revision, &placed, nil
		}
	}
	return revision, nil, nil
}
