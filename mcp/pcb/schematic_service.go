package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// SchematicView is the read model for pcb_schematic_get and the panel: the
// stored spec plus fully resolved per-sheet geometry and the ERC report.
type SchematicView struct {
	Schema     string               `json:"schema"`
	DesignID   int64                `json:"design_id"`
	RevisionID int64                `json:"revision_id"`
	Present    bool                 `json:"present"`
	Schematic  *SchematicSpec       `json:"schematic"`
	Sheets     []SchematicSheetView `json:"sheets"`
	ERC        SchematicERC         `json:"erc"`
	SVGPath    string               `json:"svg_path,omitempty"`
}

type SchematicSheetView struct {
	SchematicSheet
	SVGPath   string                `json:"svg_path"`
	Symbols   []SchematicSymbolView `json:"symbols"`
	Wires     []SchematicWire       `json:"wires"`
	Labels    []SchematicLabelView  `json:"labels"`
	Junctions []SchematicJunction   `json:"junctions"`
}

type SchematicSymbolView struct {
	ComponentID string             `json:"component_id"`
	Designator  string             `json:"designator"`
	Name        string             `json:"name,omitempty"`
	Value       string             `json:"value,omitempty"`
	SymbolID    string             `json:"symbol_id"`
	Kind        string             `json:"kind"`
	XNM         int64              `json:"x_nm"`
	YNM         int64              `json:"y_nm"`
	Rotation    int                `json:"rotation_deg"`
	Mirror      string             `json:"mirror,omitempty"`
	Unit        int                `json:"unit"`
	Body        SchematicRect      `json:"body"`
	Bounds      SchematicRect      `json:"bounds"`
	Pins        []SchematicPinView `json:"pins"`
}

type SchematicPinView struct {
	Number         string `json:"number"`
	Name           string `json:"name,omitempty"`
	PinID          string `json:"pin_id,omitempty"`
	ElectricalType string `json:"electrical_type,omitempty"`
	XNM            int64  `json:"x_nm"`
	YNM            int64  `json:"y_nm"`
	EndXNM         int64  `json:"end_x_nm"`
	EndYNM         int64  `json:"end_y_nm"`
	NetID          string `json:"net_id,omitempty"`
	SchematicNet   string `json:"schematic_net,omitempty"`
}

type SchematicLabelView struct {
	SchematicLabel
	ResolvedNetID string `json:"resolved_net_id,omitempty"`
}

func schematicSVGPath(revisionID int64) string {
	return fmt.Sprintf("/api/revisions/%d/schematic.svg", revisionID)
}

func buildSchematicView(designID, revisionID int64, def *Definition) *SchematicView {
	view := &SchematicView{Schema: schematicViewSchema, DesignID: designID, RevisionID: revisionID, Schematic: def.Schematic, Sheets: []SchematicSheetView{}, ERC: runSchematicERC(def)}
	if def.Schematic == nil {
		return view
	}
	view.Present = true
	view.SVGPath = schematicSVGPath(revisionID)
	spec := def.Schematic
	conn := buildSchematicConnectivity(def)
	components := map[string]Component{}
	for _, c := range def.Components {
		components[c.ID] = c
	}
	nodeNet := map[string]string{}
	for _, net := range def.Nets {
		for _, n := range net.Nodes {
			if _, ok := nodeNet[nodeKey(n.ComponentID, n.PinID)]; !ok {
				nodeNet[nodeKey(n.ComponentID, n.PinID)] = net.ID
			}
		}
	}
	// Each schematic group maps to the board net most of its pins belong to.
	groupNet := map[int]string{}
	for g := range conn.Groups {
		counts := map[string]int{}
		best := ""
		for _, n := range conn.groupNodes(g) {
			if id := nodeNet[nodeKey(n.ComponentID, n.PinID)]; id != "" {
				counts[id]++
				if best == "" || counts[id] > counts[best] || counts[id] == counts[best] && id < best {
					best = id
				}
			}
		}
		groupNet[g] = best
	}
	pinGroup := map[string]int{}
	for pi, pin := range conn.Pins {
		pinGroup[fmt.Sprintf("%d\x00%s", pin.Symbol, pin.Number)] = conn.PinGroup[pi]
	}
	for _, sheet := range spec.Sheets {
		sv := SchematicSheetView{SchematicSheet: sheet, SVGPath: schematicSVGPath(revisionID) + "?sheet_id=" + sheet.ID, Symbols: []SchematicSymbolView{}, Wires: []SchematicWire{}, Labels: []SchematicLabelView{}, Junctions: []SchematicJunction{}}
		for si, sym := range spec.Symbols {
			if sym.SheetID != sheet.ID || !conn.ValidSymbolMask[si] {
				continue
			}
			symDef := conn.SymbolDefs[si]
			c := components[sym.ComponentID]
			x1, y1, x2, y2 := transformedBounds(sym, symDef.Body)
			item := SchematicSymbolView{ComponentID: sym.ComponentID, Designator: c.Designator, Name: c.Name, Value: c.Value, SymbolID: sym.SymbolID, Kind: symDef.Kind, XNM: sym.XNM, YNM: sym.YNM, Rotation: sym.Rotation, Mirror: sym.Mirror, Unit: sym.Unit, Body: symDef.Body, Bounds: SchematicRect{X1NM: x1, Y1NM: y1, X2NM: x2, Y2NM: y2}, Pins: []SchematicPinView{}}
			_, pinIDs, _ := resolveSchematicSymbol(spec, sym.SymbolID, &c)
			for _, pin := range symDef.Pins {
				if !symbolPinActive(pin, sym.Unit) {
					continue
				}
				dx, dy := pinDirection(pin.AngleDeg)
				ax, ay := schematicTransform(sym, pin.XNM, pin.YNM)
				ex, ey := schematicTransform(sym, pin.XNM+dx*pin.LengthNM, pin.YNM+dy*pin.LengthNM)
				pv := SchematicPinView{Number: pin.Number, Name: pin.Name, PinID: pinIDs[pin.Number], ElectricalType: pin.ElectricalType, XNM: ax, YNM: ay, EndXNM: ex, EndYNM: ey}
				if pv.PinID != "" {
					pv.NetID = nodeNet[nodeKey(sym.ComponentID, pv.PinID)]
				}
				if g, ok := pinGroup[fmt.Sprintf("%d\x00%s", si, pin.Number)]; ok {
					pv.SchematicNet = conn.Groups[g].Name
				}
				item.Pins = append(item.Pins, pv)
			}
			x1, y1, x2, y2 = item.Bounds.X1NM, item.Bounds.Y1NM, item.Bounds.X2NM, item.Bounds.Y2NM
			for _, p := range item.Pins {
				x1, y1, x2, y2 = min64(x1, p.XNM), min64(y1, p.YNM), max64(x2, p.XNM), max64(y2, p.YNM)
			}
			item.Bounds = SchematicRect{X1NM: x1, Y1NM: y1, X2NM: x2, Y2NM: y2}
			sv.Symbols = append(sv.Symbols, item)
		}
		sort.SliceStable(sv.Symbols, func(i, j int) bool { return designatorLess(sv.Symbols[i].Designator, sv.Symbols[j].Designator) })
		for _, w := range spec.Wires {
			if w.SheetID == sheet.ID {
				sv.Wires = append(sv.Wires, w)
			}
		}
		for li, l := range spec.Labels {
			if l.SheetID != sheet.ID {
				continue
			}
			lv := SchematicLabelView{SchematicLabel: l}
			if g := conn.LabelGroup[li]; g >= 0 {
				lv.ResolvedNetID = groupNet[g]
			}
			sv.Labels = append(sv.Labels, lv)
		}
		for _, j := range spec.Junctions {
			if j.SheetID == sheet.ID {
				sv.Junctions = append(sv.Junctions, j)
			}
		}
		view.Sheets = append(view.Sheets, sv)
	}
	return view
}

func (s *Service) SchematicGet(designID, revisionID int64) (*SchematicView, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	return buildSchematicView(design.ID, revision.ID, def), nil
}

func (s *Service) schematicRevision(revisionID int64) (*Revision, *Definition, error) {
	revision, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, nil, err
	}
	def, err := decodeDefinition(revision.Definition)
	return revision, def, err
}

// parentForWrite loads expected_parent_id for an optimistic-concurrency write.
func (s *Service) parentForWrite(designID, expectedParentID int64) (*Revision, *Definition, error) {
	if designID <= 0 || expectedParentID <= 0 {
		return nil, nil, errors.New("design_id and expected_parent_id are required")
	}
	parent, err := s.store.GetRevision(s.project, expectedParentID)
	if err != nil {
		return nil, nil, err
	}
	if parent.DesignID != designID {
		return nil, nil, errors.New("expected parent does not belong to design")
	}
	def, err := decodeDefinition(parent.Definition)
	return parent, def, err
}

func (s *Service) commitOperations(designID int64, parent *Revision, base *Definition, ops []Operation, note, author string) (*Revision, *Definition, error) {
	canonical, next, hash, err := applyOperations(base, ops)
	if err != nil {
		return nil, nil, err
	}
	opsJSON, _ := json.Marshal(ops)
	revision, err := s.store.CreateRevision(s.project, designID, parent.ID, canonical, opsJSON, hash, strings.TrimSpace(note), author)
	if err != nil {
		return nil, nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
	}
	return revision, next, nil
}

func (s *Service) SchematicGenerate(designID, expectedParentID int64, replaceExisting bool, note, author string) (*Revision, *SchematicView, error) {
	parent, def, err := s.parentForWrite(designID, expectedParentID)
	if err != nil {
		return nil, nil, err
	}
	if def.Schematic != nil && !replaceExisting {
		return nil, nil, errors.New("revision already has a schematic; set replace_existing to regenerate it from the net list")
	}
	spec := generateSchematic(def)
	if strings.TrimSpace(note) == "" {
		note = "Generate schematic from net list"
	}
	revision, next, err := s.commitOperations(designID, parent, def, []Operation{{Type: "schematic.set", Schematic: spec}}, note, author)
	if err != nil {
		return nil, nil, err
	}
	return revision, buildSchematicView(designID, revision.ID, next), nil
}

func (s *Service) SchematicRender(designID, revisionID int64, sheetID string) (*Artifact, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	body, err := renderSchematicSVG(def, sheetID)
	if err != nil {
		return nil, err
	}
	kind := "schematic"
	if sheetID != "" && def.Schematic != nil && len(def.Schematic.Sheets) > 0 && def.Schematic.Sheets[0].ID != sheetID {
		kind = "schematic-" + safeFilename(sheetID)
	}
	erc := runSchematicERC(def)
	return s.persistArtifact(design, revision, kind, "svg", "image/svg+xml", body, map[string]any{"engine": engineVersion, "schema": schematicSchema, "sheet_id": firstNonEmpty(sheetID, def.Schematic.Sheets[0].ID), "erc_status": erc.Status})
}

func (s *Service) KiCadSchematicImport(designID, expectedParentID int64, content []byte, replaceNets bool, note, author string) (*Revision, *KiCadSchematicImportReport, *SchematicERC, error) {
	if len(strings.TrimSpace(string(content))) == 0 {
		return nil, nil, nil, errors.New("KiCad schematic content is required")
	}
	if len(content) > maxDefinitionBytes {
		return nil, nil, nil, fmt.Errorf("KiCad schematic exceeds %d bytes", maxDefinitionBytes)
	}
	parent, def, err := s.parentForWrite(designID, expectedParentID)
	if err != nil {
		return nil, nil, nil, err
	}
	ops, report, err := importKiCadSchematic(def, content, replaceNets)
	if err != nil {
		return nil, nil, nil, err
	}
	if strings.TrimSpace(note) == "" {
		note = "Import KiCad schematic"
	}
	revision, next, err := s.commitOperations(designID, parent, def, ops, note, author)
	if err != nil {
		return nil, report, nil, err
	}
	erc := runSchematicERC(next)
	if s.ctx != nil {
		s.ctx.Emit("pcb.schematic.imported", map[string]any{"design_id": designID, "revision_id": revision.ID, "status": report.Status, "nets_created": report.NetsCreated, "erc_status": erc.Status})
	}
	return revision, report, &erc, nil
}

// ---- MCP tools -------------------------------------------------------------

func (a *App) toolSchematicGet(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	return s.SchematicGet(int64Arg(args, "design_id"), int64Arg(args, "revision_id"))
}

func (a *App) toolSchematicGenerate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	revision, view, err := s.SchematicGenerate(int64Arg(args, "design_id"), int64Arg(args, "expected_parent_id"), boolArg(args, "replace_existing"), stringArg(args, "note"), callerName(ctx))
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision": revision, "schematic": view}, nil
}

func (a *App) toolSchematicRender(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	artifact, err := s.SchematicRender(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), stringArg(args, "sheet_id"))
	return map[string]any{"artifact": artifact}, err
}

func (a *App) toolKiCadSchematicImport(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	content, _ := args["content"].(string)
	revision, report, erc, err := s.KiCadSchematicImport(int64Arg(args, "design_id"), int64Arg(args, "expected_parent_id"), []byte(content), boolArg(args, "replace_nets"), stringArg(args, "note"), callerName(ctx))
	if err != nil {
		if report != nil {
			return map[string]any{"report": report}, err
		}
		return nil, err
	}
	return map[string]any{"revision": revision, "report": report, "erc": erc}, nil
}

func schematicToolSchemas(id map[string]any) map[string]map[string]any {
	return map[string]map[string]any{
		"get":      schemaObject(map[string]any{"design_id": id, "revision_id": id}, []string{"design_id"}),
		"generate": schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "replace_existing": map[string]any{"type": "boolean"}, "note": map[string]any{"type": "string"}}, []string{"design_id", "expected_parent_id"}),
		"render":   schemaObject(map[string]any{"design_id": id, "revision_id": id, "sheet_id": map[string]any{"type": "string"}}, []string{"design_id"}),
		"import":   schemaObject(map[string]any{"design_id": id, "expected_parent_id": id, "content": map[string]any{"type": "string", "description": "Complete .kicad_sch file content (single sheet)."}, "replace_nets": map[string]any{"type": "boolean", "description": "Rebuild the net list from schematic connectivity; nets with identical node sets keep their IDs."}, "note": map[string]any{"type": "string"}}, []string{"design_id", "expected_parent_id", "content"}),
	}
}

// ---- HTTP ------------------------------------------------------------------

// handleRevisionSchematic serves GET /api/revisions/{id}/schematic (JSON view)
// and GET /api/revisions/{id}/schematic.svg?sheet_id= (inline SVG).
func (a *App) handleRevisionSchematic(w http.ResponseWriter, r *http.Request, s *Service, revisionID int64, resource string) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	revision, def, err := s.schematicRevision(revisionID)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	switch resource {
	case "schematic":
		writeJSON(w, 200, buildSchematicView(revision.DesignID, revision.ID, def))
	case "schematic.svg":
		if def.Schematic == nil {
			writeJSONError(w, 404, "revision has no schematic")
			return
		}
		body, err := renderSchematicSVG(def, r.URL.Query().Get("sheet_id"))
		if err != nil {
			writeJSONError(w, 404, err.Error())
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.WriteHeader(200)
		_, _ = w.Write(body)
	default:
		writeJSONError(w, 404, "not found")
	}
}

// handleDesignSchematic serves the design-scoped schematic actions:
// GET schematic, POST schematic-generate, schematic-render, kicad-schematic-import.
func (a *App) handleDesignSchematic(w http.ResponseWriter, r *http.Request, app *sdk.AppCtx, s *Service, designID int64, action string) {
	switch action {
	case "schematic":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		revisionID, _ := strconv.ParseInt(r.URL.Query().Get("revision_id"), 10, 64)
		view, err := s.SchematicGet(designID, revisionID)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, view)
	case "schematic-generate", "schematic-render", "kicad-schematic-import":
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w, http.MethodPost)
			return
		}
		args := map[string]any{}
		if err := decodeBody(r, &args); err != nil {
			writeHTTPError(w, err)
			return
		}
		args["design_id"] = float64(designID)
		var result any
		var err error
		switch action {
		case "schematic-generate":
			result, err = a.toolSchematicGenerate(r.Context(), app, args)
		case "schematic-render":
			result, err = a.toolSchematicRender(r.Context(), app, args)
		default:
			result, err = a.toolKiCadSchematicImport(r.Context(), app, args)
		}
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, result)
	default:
		writeJSONError(w, 404, "not found")
	}
}
