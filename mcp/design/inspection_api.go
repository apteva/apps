package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
	"net/http"
)

func (s *Service) Measure(ctx context.Context, designID, revisionID int64, query map[string]any) (any, error) {
	d, err := s.store.GetDesign(s.project, designID)
	if err != nil {
		return nil, err
	}
	if revisionID == 0 {
		revisionID = d.CurrentRevisionID
	}
	r, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, err
	}
	if r.DesignID != d.ID {
		return nil, errors.New("revision does not belong to design")
	}
	canonical, _, err := normalizeDefinition(r.Definition, s.maxOperations)
	if err != nil {
		return nil, err
	}
	runtime, _, err := s.materializeComponentSources(r.ID, canonical)
	if err != nil {
		return nil, err
	}
	var def map[string]any
	_ = json.Unmarshal(runtime, &def)
	query["type"] = "measurement"
	query["id"] = "measurement"
	def["checks"] = []any{query}
	body, _ := json.Marshal(def)
	result, err := s.engine.Build(ctx, d.Name, body, r.Parameters, []string{})
	if err != nil {
		return nil, err
	}
	for _, c := range result.Report.ExactChecks {
		if c.ID == "measurement" {
			return map[string]any{"revision_id": r.ID, "source_sha256": r.SourceSHA256, "measurement": c}, nil
		}
	}
	return nil, errors.New("measurement result missing")
}
func (a *App) toolMeasure(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	q, ok := args["query"].(map[string]any)
	if !ok {
		return nil, errors.New("query required")
	}
	return s.Measure(ctx, int64Arg(args, "design_id"), int64Arg(args, "revision_id"), q)
}
func (a *App) handleMeasure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, "POST")
		return
	}
	app := a.ctx
	if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && app != nil {
		app = app.WithProject(p)
	}
	var args map[string]any
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	out, err := a.toolMeasure(r.Context(), app, args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) toolEnclosureFromAssembly(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	sourceID, revisionID := int64Arg(args, "assembly_design_id"), int64Arg(args, "assembly_revision_id")
	source, err := s.store.GetDesign(s.project, sourceID)
	if err != nil {
		return nil, err
	}
	if revisionID == 0 {
		revisionID = source.CurrentRevisionID
	}
	revision, err := s.store.GetRevision(s.project, revisionID)
	if err != nil {
		return nil, err
	}
	if revision.DesignID != sourceID {
		return nil, errors.New("assembly revision does not belong to source")
	}
	built, err := s.Build(ctx, sourceID, revisionID, []string{"mesh-json"})
	if err != nil {
		return nil, err
	}
	options, _ := args["options"].(map[string]any)
	definition, err := assemblyEnclosure(source, revision, built.Report, options)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(definition)
	canonical, _, err := normalizeDefinition(raw, s.maxOperations)
	if err != nil {
		return nil, err
	}
	name := stringArg(args, "name")
	if name == "" {
		name = source.Name + " enclosure"
	}
	design, err := s.store.CreateDesign(s.project, CreateDesignInput{Name: name, Definition: canonical, Parameters: []byte(`{}`), Note: "Assembly-driven enclosure template; requires fit and thermal testing"})
	return map[string]any{"design": design, "source_revision_id": revisionID}, err
}
func (a *App) handleAssemblyEnclosure(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, "POST")
		return
	}
	app := a.ctx
	if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && app != nil {
		app = app.WithProject(p)
	}
	var args map[string]any
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	out, err := a.toolEnclosureFromAssembly(r.Context(), app, args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func assemblyEnclosure(source *Design, r *Revision, report GeometryReport, opts map[string]any) (*DesignDefinition, error) {
	option := func(key string, fallback, min, max float64) (float64, error) {
		v := fallback
		if raw, ok := opts[key]; ok {
			n, ok := numberValue(raw)
			if !ok {
				return 0, fmt.Errorf("%s must be numeric", key)
			}
			v = n
		}
		if v < min || v > max {
			return 0, fmt.Errorf("%s must be between %g and %g", key, min, max)
		}
		return v, nil
	}
	wall, err := option("wall_mm", 2, .8, 10)
	if err != nil {
		return nil, err
	}
	gap, err := option("component_gap_mm", 2, .1, 30)
	if err != nil {
		return nil, err
	}
	floor, err := option("front_mm", 2, .8, 10)
	if err != nil {
		return nil, err
	}
	rear, err := option("rear_mm", 2, .8, 10)
	if err != nil {
		return nil, err
	}
	fit, err := option("lid_fit_mm", .25, .05, 2)
	if err != nil {
		return nil, err
	}
	min := report.Bounds.Min
	size := report.Bounds.Size
	if size[0] <= 0 || size[1] <= 0 || size[2] <= 0 {
		return nil, errors.New("assembly has no valid bounds")
	}
	params := map[string]ParameterSpec{}
	for k, v := range map[string]float64{"wall": wall, "component_gap": gap, "front": floor, "rear": rear, "lid_fit": fit, "width": size[0], "height": size[1], "depth": size[2], "x": min[0], "y": min[1], "z": min[2]} {
		params[k] = ParameterSpec{Type: "length", Default: v}
	}
	ops := []map[string]any{
		{"id": "outer", "type": "box", "origin": []any{"x-component_gap-wall", "y-component_gap-wall", "z-component_gap-front"}, "size": []any{"width+2*component_gap+2*wall", "height+2*component_gap+2*wall", "depth+2*component_gap+front"}},
		{"id": "cavity", "type": "box", "origin": []any{"x-component_gap", "y-component_gap", "z-component_gap"}, "size": []any{"width+2*component_gap", "height+2*component_gap", "depth+2*component_gap+1"}},
		{"id": "shell_hollow", "type": "cut", "inputs": []any{"outer", "cavity"}},
		{"id": "cover", "type": "box", "origin": []any{"x-component_gap-wall", "y-component_gap-wall", "z+depth+component_gap+lid_fit"}, "size": []any{"width+2*component_gap+2*wall", "height+2*component_gap+2*wall", "rear"}},
		{"id": "lip_outer", "type": "box", "origin": []any{"x-component_gap+lid_fit", "y-component_gap+lid_fit", "z+depth+component_gap-2"}, "size": []any{"width+2*component_gap-2*lid_fit", "height+2*component_gap-2*lid_fit", "2+lid_fit+0.1"}},
		{"id": "lip_inner", "type": "box", "origin": []any{"x-component_gap+lid_fit+wall", "y-component_gap+lid_fit+wall", "z+depth+component_gap-3"}, "size": []any{"width+2*component_gap-2*lid_fit-2*wall", "height+2*component_gap-2*lid_fit-2*wall", "4+lid_fit"}},
		{"id": "lip", "type": "cut", "inputs": []any{"lip_outer", "lip_inner"}},
		{"id": "rear_cover", "type": "fuse", "inputs": []any{"cover", "lip"}},
	}
	// Optional display opening leaves a configurable glass supporting ledge around it.
	if opening, ok := opts["display"].(map[string]any); ok {
		origin, ok1 := opening["origin"].([]any)
		size, ok2 := opening["size"].([]any)
		if !ok1 || !ok2 || len(origin) != 2 || len(size) != 2 {
			return nil, errors.New("display requires 2D origin and opening size in assembly coordinates")
		}
		ops = append(ops, map[string]any{"id": "display_opening", "type": "box", "origin": []any{origin[0], origin[1], "z-component_gap-front-1"}, "size": []any{size[0], size[1], "front+2"}}, map[string]any{"id": "shell_display", "type": "cut", "inputs": []any{"shell_hollow", "display_opening"}})
	}
	// Measured screw locations create bosses and bores through the removable cover.
	fasteners, _ := opts["fasteners"].([]any)
	bossIDs := []any{}
	coverOutput := "rear_cover"
	for index, raw := range fasteners {
		f, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("fasteners must be objects")
		}
		xy, ok := f["position"].([]any)
		if !ok || len(xy) != 2 {
			return nil, errors.New("fastener position must be [x,y]")
		}
		outer, ok := numberValue(f["boss_diameter_mm"])
		if !ok || outer < 3 || outer > 20 {
			return nil, errors.New("boss_diameter_mm must be 3..20")
		}
		hole, ok := numberValue(f["hole_diameter_mm"])
		if !ok || hole <= 0 || hole >= outer {
			return nil, errors.New("hole_diameter_mm must be positive and smaller than boss")
		}
		id := fmt.Sprintf("boss_%d", index)
		ops = append(ops, map[string]any{"id": id, "type": "cylinder", "radius": outer / 2, "height": "depth+2*component_gap", "origin": []any{xy[0], xy[1], "z-component_gap"}}, map[string]any{"id": id + "_bore", "type": "cylinder", "radius": fmt.Sprintf("(%g+hole_fit)/2", hole), "height": "depth+2*component_gap+rear+lid_fit+2", "origin": []any{xy[0], xy[1], "z-component_gap-1"}}, map[string]any{"id": id + "_ring", "type": "cut", "inputs": []any{id, id + "_bore"}})
		bossOutput := id + "_ring"
		if nutWidth, ok := numberValue(f["nut_width_mm"]); ok {
			nutDepth, depthOK := numberValue(f["nut_depth_mm"])
			if !depthOK || nutWidth <= hole || nutWidth >= outer || nutDepth <= 0 {
				return nil, errors.New("nut pocket requires width between bore and boss diameters and positive depth")
			}
			points := []any{}
			for corner := 0; corner < 6; corner++ {
				angle := float64(corner) * math.Pi / 3
				points = append(points, []any{fmt.Sprintf("(%v)+(%g+nut_fit)*%g", xy[0], nutWidth, math.Cos(angle)/math.Sqrt(3)), fmt.Sprintf("(%v)+(%g+nut_fit)*%g", xy[1], nutWidth, math.Sin(angle)/math.Sqrt(3))})
			}
			ops = append(ops, map[string]any{"id": id + "_nut", "type": "extrude_polygon", "points": points, "height": nutDepth + .01, "z": fmt.Sprintf("z+depth+component_gap-%g", nutDepth)}, map[string]any{"id": id + "_captive", "type": "cut", "inputs": []any{bossOutput, id + "_nut"}})
			bossOutput = id + "_captive"
		}
		bossIDs = append(bossIDs, bossOutput)
		next := id + "_cover"
		ops = append(ops, map[string]any{"id": next, "type": "cut", "inputs": []any{coverOutput, id + "_bore"}})
		coverOutput = next
	}
	shell := "shell_hollow"
	if _, ok := opts["display"]; ok {
		shell = "shell_display"
	}
	if display, ok := opts["display"].(map[string]any); ok {
		glassOrigin, hasOrigin := display["glass_origin"].([]any)
		glassSize, hasSize := display["glass_size"].([]any)
		if hasOrigin || hasSize {
			depth, ok := numberValue(display["recess_depth_mm"])
			if !hasOrigin || !hasSize || len(glassOrigin) != 2 || len(glassSize) != 2 || !ok || depth <= 0 || depth >= floor {
				return nil, errors.New("glass seating requires glass_origin/size pairs and recess_depth_mm between zero and front thickness")
			}
			ops = append(ops, map[string]any{"id": "glass_seat", "type": "box", "origin": []any{fmt.Sprintf("(%v)-display_fit/2", glassOrigin[0]), fmt.Sprintf("(%v)-display_fit/2", glassOrigin[1]), "z-component_gap-front-0.01"}, "size": []any{fmt.Sprintf("(%v)+display_fit", glassSize[0]), fmt.Sprintf("(%v)+display_fit", glassSize[1]), depth + .01}}, map[string]any{"id": "shell_seated", "type": "cut", "inputs": []any{shell, "glass_seat"}})
			shell = "shell_seated"
		}
	}
	if len(bossIDs) > 0 {
		inputs := append([]any{shell}, bossIDs...)
		ops = append(ops, map[string]any{"id": "shell_bosses", "type": "fuse", "inputs": inputs})
		shell = "shell_bosses"
	}
	// Explicit measured support/cradle/bracket solids and access cutouts remain editable operations.
	for _, category := range []string{"supports", "brackets", "battery_cradle", "cutouts"} {
		items, _ := opts[category].([]any)
		for index, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s items must be boxes", category)
			}
			origin, ok1 := item["origin"].([]any)
			dims, ok2 := item["size"].([]any)
			if !ok1 || !ok2 || len(origin) != 3 || len(dims) != 3 {
				return nil, fmt.Errorf("%s requires origin and size triples", category)
			}
			id := fmt.Sprintf("%s_%d", category, index)
			ops = append(ops, map[string]any{"id": id, "type": "box", "origin": origin, "size": dims})
			result := id + "_result"
			kind := "fuse"
			if category == "cutouts" {
				kind = "cut"
			}
			ops = append(ops, map[string]any{"id": result, "type": kind, "inputs": []any{shell, id}})
			shell = result
		}
	}
	parts := []DesignPart{{ID: "purchased_assembly", Name: source.Name, Source: &PartSourceReference{DesignID: source.ID, RevisionID: r.ID, SourceSHA256: r.SourceSHA256}, Manufacturing: &PartManufacturing{Classification: "reference"}}, {ID: "front_shell", Name: "Front shell", Output: shell, Manufacturing: &PartManufacturing{Classification: "printed", PrintProfileID: "prototype", MinWallMM: wall, MinFeatureMM: wall}}, {ID: "rear_cover", Name: "Removable rear cover", Output: coverOutput, Manufacturing: &PartManufacturing{Classification: "printed", PrintProfileID: "prototype", MinWallMM: rear, MinFeatureMM: wall}}}
	params["hole_fit"] = ParameterSpec{Type: "length", Default: .2, Fit: "hole_diameter"}
	params["nut_fit"] = ParameterSpec{Type: "length", Default: .2, Fit: "nut_pocket"}
	params["display_fit"] = ParameterSpec{Type: "length", Default: .3, Fit: "display_seat"}
	params["lid_fit"] = ParameterSpec{Type: "length", Default: fit, Fit: "lid"}
	params["component_gap"] = ParameterSpec{Type: "length", Default: gap, Fit: "component_pocket"}
	return &DesignDefinition{Provenance: &DesignProvenance{Kind: "assembly-envelope", SourceApp: "design", SourceDesignID: source.ID, SourceRevisionID: r.ID, SourceRevisionNumber: r.RevisionNumber, SourceSHA256: r.SourceSHA256, Generator: "assembly-enclosure", GeneratorVersion: "0.5.0", AssemblyOptions: opts}, FitProfileID: "prototype", Schema: designSchema, Units: "mm", Parameters: params, Operations: ops, Output: shell, Parts: parts, PrintProfiles: []PrintProfile{{ID: "prototype", Name: "Prototype FDM — calibrate fit", Process: "fdm", NozzleMM: .4, LayerHeightMM: .2, Fits: map[string]float64{"lid": fit, "hole_diameter": .2, "nut_pocket": .2, "display_seat": .3, "component_pocket": gap}}}, Assembly: &AssemblySpec{Name: source.Name + " enclosure", Instances: []AssemblyInstance{{ID: "electronics", PartID: "purchased_assembly"}, {ID: "front_shell", PartID: "front_shell"}, {ID: "rear_cover", PartID: "rear_cover"}}}, Checks: []map[string]any{{"id": "enclosure_clearance", "type": "assembly_collision", "min": 0}}, Release: &ReleaseEvidence{Stage: "prototype", Assumptions: []string{"Confirm component evidence, mounting points, display retention, openings and fastener lengths against delivered hardware", "Physical fit and enclosed thermal tests pending"}}}, nil
}
