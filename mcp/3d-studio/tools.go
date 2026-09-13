package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/3d-studio/engine"
)

type Input struct {
	AssetID     int64             `json:"asset_id,omitempty"`
	RevisionID  int64             `json:"revision_id,omitempty"`
	Expected    int64             `json:"expected_revision_id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Template    string            `json:"template,omitempty"`
	Note        string            `json:"note,omitempty"`
	RequestKey  string            `json:"request_key,omitempty"`
	NodeID      string            `json:"node_id,omitempty"`
	Query       engine.Query      `json:"query,omitempty"`
	SelectionID string            `json:"selection_id,omitempty"`
	Selections  map[string]string `json:"selections,omitempty"`
	Steps       int               `json:"steps,omitempty"`
	Commands    []engine.Command  `json:"commands,omitempty"`
	Mode        string            `json:"mode,omitempty"`
	CandidateID string            `json:"candidate_id,omitempty"`
	View        string            `json:"view,omitempty"`
	Format      string            `json:"format,omitempty"`
	IncludeMesh bool              `json:"include_mesh,omitempty"`
}
type toolDef struct {
	name, description string
	fields, required  []string
	write             bool
}

var definitions = []toolDef{
	{"studio_capabilities", "Discover the Go mesh command schemas, coordinate system, limits, and car example workflow.", nil, nil, false},
	{"assets_create", "Create an editable model and immutable initial revision. Templates: empty, box, car.", []string{"name", "template"}, []string{"name"}, true},
	{"assets_list", "List up to 200 models in this project, newest first.", nil, nil, false},
	{"assets_get", "Read an asset revision with editable polygon geometry.", []string{"asset_id", "revision_id"}, []string{"asset_id"}, false},
	{"revisions_list", "List the most recent 100 immutable revisions.", []string{"asset_id"}, []string{"asset_id"}, false},
	{"revisions_restore", "Restore earlier geometry as a new revision, with optimistic concurrency and retry protection.", []string{"asset_id", "revision_id", "expected_revision_id", "request_key", "note"}, []string{"asset_id", "revision_id", "expected_revision_id", "request_key"}, true},
	{"mesh_inspect", "Inspect node counts and bounds. include_mesh returns up to 500 selected elements; use mesh_select to narrow large meshes.", []string{"asset_id", "revision_id", "candidate_id", "node_id", "selection_id", "include_mesh"}, []string{"asset_id"}, false},
	{"mesh_select", "Select faces by centroid/normal or vertices by position/IDs, and save a revision-bound handle.", []string{"asset_id", "revision_id", "name", "query"}, []string{"asset_id", "query"}, true},
	{"selection_expand", "Grow a saved selection by shared-vertex adjacency. The handle stays bound to its source revision.", []string{"asset_id", "selection_id", "steps", "name"}, []string{"asset_id", "selection_id", "steps"}, true},
	{"mesh_edit", "Atomically edit meshes. Bind selection names to saved selection IDs; result_selection names can be used later in the same batch. Preview returns a candidate; commit requires request_key.", []string{"asset_id", "expected_revision_id", "commands", "selections", "mode", "request_key", "note"}, []string{"asset_id", "expected_revision_id", "commands"}, true},
	{"mesh_edit_commit", "Commit a preview if its base is still current. request_key makes retries safe.", []string{"asset_id", "candidate_id", "request_key", "note"}, []string{"asset_id", "candidate_id", "request_key"}, true},
	{"mesh_edit_discard", "Discard an uncommitted preview candidate.", []string{"asset_id", "candidate_id"}, []string{"asset_id", "candidate_id"}, true},
	{"assets_validate", "Check polygon topology, triangulation, counts, open boundaries, and coordinate validity.", []string{"asset_id", "revision_id", "candidate_id"}, []string{"asset_id"}, false},
	{"assets_render", "Render a 640px PNG in native Go. Returns an authenticated artifact URL for visual inspection; optional selection overlay.", []string{"asset_id", "revision_id", "candidate_id", "view", "selection_id"}, []string{"asset_id"}, true},
	{"assets_export", "Export GLB with flat normals, node materials and centered pivots, or editable source JSON.", []string{"asset_id", "revision_id", "format"}, []string{"asset_id", "format"}, true},
	{"artifacts_list", "List up to 100 saved exports and preview images.", []string{"asset_id"}, []string{"asset_id"}, false},
}

func object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func str() map[string]any { return map[string]any{"type": "string", "maxLength": 1000} }
func num() map[string]any { return map[string]any{"type": "number"} }
func vec() map[string]any {
	return map[string]any{"type": "array", "items": num(), "minItems": 3, "maxItems": 3}
}
func enumeration(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func commandSchema() map[string]any {
	p := map[string]any{"op": enumeration("primitive.add", "transform", "extrude", "inset", "flatten", "vertices.patch", "vertices.weld", "faces.delete", "mesh.mirror", "node.duplicate", "node.delete", "node.rename", "node.color")}
	for _, k := range []string{"node_id", "name", "selection", "result_selection", "target_id"} {
		p[k] = str()
	}
	for _, k := range []string{"size", "color", "translation", "scale", "rotation", "pivot", "direction"} {
		p[k] = vec()
	}
	for _, k := range []string{"radius", "distance", "amount", "offset", "tolerance"} {
		p[k] = num()
	}
	p["axis"] = enumeration("x", "y", "z")
	p["shape"] = enumeration("box", "cylinder")
	p["falloff"] = enumeration("linear", "smooth")
	p["connected_only"] = map[string]any{"type": "boolean"}
	p["segments"] = map[string]any{"type": "integer", "minimum": 3, "maximum": 64}
	p["positions"] = map[string]any{"type": "array", "maxItems": engine.MaxVertices, "items": object(map[string]any{"vertex_id": str(), "position": vec()}, "vertex_id", "position")}
	return object(p, "op", "node_id")
}
func inputSchema(def toolDef) map[string]any {
	p := map[string]any{}
	for _, k := range def.fields {
		p[k] = str()
	}
	for _, k := range []string{"asset_id", "revision_id", "expected_revision_id", "steps"} {
		if _, ok := p[k]; ok {
			p[k] = map[string]any{"type": "integer", "minimum": 1}
		}
	}
	if _, ok := p["query"]; ok {
		p["query"] = object(map[string]any{"node_id": str(), "kind": enumeration("face", "vertex"), "ids": map[string]any{"type": "array", "items": str(), "maxItems": engine.MaxVertices}, "min": vec(), "max": vec(), "normal": vec(), "min_dot": map[string]any{"type": "number", "minimum": -1, "maximum": 1}}, "node_id", "kind")
	}
	if _, ok := p["commands"]; ok {
		p["commands"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": commandSchema()}
	}
	if _, ok := p["selections"]; ok {
		p["selections"] = map[string]any{"type": "object", "additionalProperties": str()}
	}
	if _, ok := p["include_mesh"]; ok {
		p["include_mesh"] = map[string]any{"type": "boolean"}
	}
	if _, ok := p["mode"]; ok {
		p["mode"] = enumeration("preview", "commit")
	}
	if _, ok := p["template"]; ok {
		p["template"] = enumeration("empty", "box", "car")
	}
	if _, ok := p["format"]; ok {
		p["format"] = enumeration("glb", "json")
	}
	if _, ok := p["view"]; ok {
		p["view"] = enumeration("perspective", "front", "side", "top")
	}
	return object(p, def.required...)
}
func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, def := range definitions {
		d := def
		out = append(out, sdk.Tool{Name: d.name, Description: d.description, InputSchema: inputSchema(d), HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			project, err := a.project(app)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, err
			}
			return a.call(ctx, project, d.name, raw)
		}})
	}
	return out
}
func (a *App) source(project string, in Input) (engine.Document, int64, error) {
	if in.CandidateID != "" {
		c, err := a.store.candidate(project, in.CandidateID)
		if err != nil {
			return engine.Document{}, 0, err
		}
		if c.AssetID != in.AssetID {
			return engine.Document{}, 0, errNotFound
		}
		return c.Result.Document, c.Base, nil
	}
	r, err := a.store.revision(project, in.AssetID, in.RevisionID)
	return r.Document, r.ID, err
}
func (a *App) call(ctx context.Context, project, name string, raw []byte) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var def *toolDef
	for i := range definitions {
		if definitions[i].name == name {
			def = &definitions[i]
			break
		}
	}
	if def == nil {
		return nil, errNotFound
	}
	var args map[string]json.RawMessage
	if err := decode(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, errors.New("input must be an object")
	}
	allowed := map[string]bool{}
	for _, k := range def.fields {
		allowed[k] = true
	}
	for k := range args {
		if !allowed[k] {
			return nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	for _, k := range def.required {
		if v, ok := args[k]; !ok || string(v) == "null" {
			return nil, fmt.Errorf("%s required", k)
		}
	}
	var in Input
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	for _, key := range []string{"asset_id", "revision_id", "expected_revision_id", "steps"} {
		if value, ok := args[key]; ok {
			var id int64
			if err := json.Unmarshal(value, &id); err != nil || id <= 0 {
				return nil, fmt.Errorf("%s must be a positive integer", key)
			}
		}
	}
	if in.AssetID == 0 && name != "studio_capabilities" && name != "assets_list" && name != "assets_create" {
		return nil, errors.New("asset_id required")
	}

	if def.write {
		a.editMu.Lock()
		defer a.editMu.Unlock()
	}
	hash := digest([]byte(name + encoded(in)))
	switch name {
	case "studio_capabilities":
		return capabilities(), nil
	case "assets_list":
		items, err := a.store.list(project)
		return map[string]any{"assets": items}, err
	case "assets_create":
		d := engine.Empty()
		var err error
		switch in.Template {
		case "", "empty":
		case "car":
			d, err = engine.ExampleCar()
		case "box":
			var r engine.EditResult
			r, err = engine.Evaluate(engine.EditRequest{Document: d, Commands: []engine.Command{{Op: "primitive.add", NodeID: "body", Shape: "box"}}})
			d = r.Document
		default:
			return nil, errors.New("unknown template")
		}
		if err != nil {
			return nil, err
		}
		r, err := a.store.create(project, in.Name, d)
		if err != nil {
			return nil, err
		}
		return a.revisionResponse(project, r, nil)
	case "assets_get":
		r, err := a.store.revision(project, in.AssetID, in.RevisionID)
		if err != nil {
			return nil, err
		}
		return a.revisionResponse(project, r, nil)
	case "revisions_list":
		rows, err := a.store.history(project, in.AssetID)
		return map[string]any{"revisions": rows}, err
	case "revisions_restore":
		if prior, err := a.store.prior(project, in.AssetID, in.RequestKey, hash); err != nil {
			return nil, err
		} else if prior != nil {
			return a.revisionResponse(project, *prior, nil)
		}
		source, err := a.store.revision(project, in.AssetID, in.RevisionID)
		if err != nil {
			return nil, err
		}
		r, err := a.store.commit(project, in.AssetID, in.Expected, source.Document, nil, in.Note, in.RequestKey, hash, nil)
		if err != nil {
			return nil, err
		}
		return a.revisionResponse(project, r, nil)
	case "mesh_select":
		r, err := a.store.revision(project, in.AssetID, in.RevisionID)
		if err != nil {
			return nil, err
		}
		sel, err := engine.Select(r.Document, in.Query)
		if err != nil {
			return nil, err
		}
		return a.store.saveSelection(project, in.AssetID, r.ID, in.Name, sel)
	case "selection_expand":
		s, err := a.store.selection(project, in.SelectionID)
		if err != nil {
			return nil, err
		}
		if s.AssetID != in.AssetID {
			return nil, errNotFound
		}
		r, err := a.store.revision(project, in.AssetID, s.RevisionID)
		if err != nil {
			return nil, err
		}
		sel, err := engine.Grow(r.Document, s.Selection, in.Steps)
		if err != nil {
			return nil, err
		}
		return a.store.saveSelection(project, in.AssetID, r.ID, in.Name, sel)
	case "mesh_edit":
		if in.Mode != "" && in.Mode != "commit" && in.Mode != "preview" {
			return nil, errors.New("mode must be commit or preview")
		}
		if in.Expected <= 0 {
			return nil, errors.New("expected_revision_id required")
		}
		if in.Mode != "preview" {
			if prior, err := a.store.prior(project, in.AssetID, in.RequestKey, hash); err != nil {
				return nil, err
			} else if prior != nil {
				return a.revisionResponse(project, *prior, nil)
			}
		}
		asset, err := a.store.asset(project, in.AssetID)
		if err != nil {
			return nil, err
		}
		if asset.Head != in.Expected {
			return nil, errConflict
		}
		r, err := a.store.revision(project, in.AssetID, in.Expected)
		if err != nil {
			return nil, err
		}
		sels := map[string]engine.Selection{}
		for alias, id := range in.Selections {
			s, err := a.store.selection(project, id)
			if err != nil {
				return nil, err
			}
			if s.AssetID != in.AssetID || s.RevisionID != in.Expected {
				return nil, errors.New("selection belongs to another asset or revision; select again")
			}
			sels[alias] = s.Selection
		}
		result, err := engine.Evaluate(engine.EditRequest{Document: r.Document, Commands: in.Commands, Selections: sels})
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if in.Mode == "preview" {
			candidate, e := a.store.saveCandidate(project, in.AssetID, in.Expected, result, in.Commands)
			if e != nil {
				return nil, e
			}
			mesh, e := engine.Triangles(result.Document)
			return map[string]any{"id": candidate.ID, "asset_id": candidate.AssetID, "base_revision_id": candidate.Base, "result": result, "render_mesh": mesh, "expires_at": candidate.Expires}, e
		}
		saved, err := a.store.commit(project, in.AssetID, in.Expected, result.Document, in.Commands, in.Note, in.RequestKey, hash, result.Selections)
		if err != nil {
			return nil, err
		}
		return a.revisionResponse(project, saved, result.Selections)
	case "mesh_edit_commit":
		if prior, err := a.store.prior(project, in.AssetID, in.RequestKey, hash); err != nil {
			return nil, err
		} else if prior != nil {
			return a.revisionResponse(project, *prior, nil)
		}
		c, err := a.store.candidate(project, in.CandidateID)
		if err != nil {
			return nil, err
		}
		if c.AssetID != in.AssetID {
			return nil, errNotFound
		}
		r, err := a.store.commit(project, in.AssetID, c.Base, c.Result.Document, c.Commands, in.Note, in.RequestKey, hash, c.Result.Selections)
		if err != nil {
			return nil, err
		}
		return a.revisionResponse(project, r, c.Result.Selections)
	case "mesh_edit_discard":
		c, err := a.store.candidate(project, in.CandidateID)
		if err != nil {
			return nil, err
		}
		if c.AssetID != in.AssetID {
			return nil, errNotFound
		}
		_, err = a.store.db.Exec(`DELETE FROM studio_candidates WHERE id=?`, c.ID)
		return map[string]bool{"discarded": err == nil}, err
	case "assets_validate", "mesh_inspect", "assets_render", "assets_export":
		d, revisionID, err := a.source(project, in)
		if err != nil {
			return nil, err
		}
		report, err := engine.Validate(d)
		if err != nil {
			return nil, err
		}
		var sel *engine.Selection
		if in.SelectionID != "" {
			s, e := a.store.selection(project, in.SelectionID)
			if e != nil {
				return nil, e
			}
			if s.AssetID != in.AssetID || s.RevisionID != revisionID || in.CandidateID != "" {
				return nil, errors.New("selection does not match this saved revision")
			}
			sel = &s.Selection
		}
		if name == "assets_validate" {
			return report, nil
		}
		if name == "mesh_inspect" {
			return inspect(d, in, sel, report)
		}
		var data []byte
		format := in.Format
		if name == "assets_render" {
			format = "png"
			data, err = engine.RenderPNG(d, in.View, sel)
		} else {
			switch format {
			case "glb":
				data, err = engine.GLB(d)
			case "json":
				data, err = json.MarshalIndent(d, "", "  ")
			default:
				return nil, errors.New("format must be glb or json")
			}
		}
		if err != nil {
			return nil, err
		}
		artifact, err := a.store.artifact(project, in.AssetID, revisionID, in.CandidateID, format, data)
		if err != nil {
			return nil, err
		}
		return map[string]any{"artifact": artifact, "report": report}, nil
	case "artifacts_list":
		if _, err := a.store.asset(project, in.AssetID); err != nil {
			return nil, err
		}
		rows, err := a.store.db.Query(`SELECT id,asset_id,revision_id,candidate_id,format,sha256 FROM studio_artifacts WHERE asset_id=? ORDER BY id DESC LIMIT 100`, in.AssetID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		artifacts := []Artifact{}
		for rows.Next() {
			var f Artifact
			if err = rows.Scan(&f.ID, &f.AssetID, &f.RevisionID, &f.CandidateID, &f.Format, &f.SHA256); err != nil {
				return nil, err
			}
			f.URL = fmt.Sprintf("/api/artifacts/%d", f.ID)
			artifacts = append(artifacts, f)
		}
		return map[string]any{"artifacts": artifacts}, rows.Err()
	}
	return nil, errNotFound
}
func (a *App) revisionResponse(project string, r Revision, _ map[string]engine.Selection) (any, error) {
	report, err := engine.Validate(r.Document)
	if err != nil {
		return nil, err
	}
	handles := map[string]SavedSelection{}
	rows, err := a.store.db.Query(`SELECT id,name,selection_json FROM studio_selections WHERE asset_id=? AND revision_id=? ORDER BY created_at,id LIMIT 200`, r.AssetID, r.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		h := SavedSelection{AssetID: r.AssetID, RevisionID: r.ID}
		var raw string
		if err = rows.Scan(&h.ID, &h.Name, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &h.Selection); err != nil {
			rows.Close()
			return nil, err
		}
		handles[h.Name] = h
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	asset, err := a.store.asset(project, r.AssetID)
	mesh, meshErr := engine.Triangles(r.Document)
	if meshErr != nil {
		return nil, meshErr
	}
	return map[string]any{"asset": asset, "revision": r, "report": report, "selections": handles, "render_mesh": mesh}, err
}
func inspect(d engine.Document, in Input, sel *engine.Selection, report engine.Report) (any, error) {
	nodes := []map[string]any{}
	if in.NodeID != "" {
		if _, err := d.Node(in.NodeID); err != nil {
			return nil, err
		}
	}
	for _, n := range d.Nodes {
		if in.NodeID != "" && n.ID != in.NodeID || sel != nil && n.ID != sel.NodeID {
			continue
		}
		item := map[string]any{"id": n.ID, "name": n.Name, "color": n.Color, "vertices": len(n.Mesh.Vertices), "faces": len(n.Mesh.Faces)}
		if in.IncludeMesh {
			ids := map[string]bool{}
			if sel != nil {
				for _, id := range sel.IDs {
					ids[id] = true
				}
			}
			vs := []engine.Vertex{}
			fs := []engine.Face{}
			needed := map[string]bool{}
			if sel == nil || sel.Kind == "face" {
				for _, f := range n.Mesh.Faces {
					if sel == nil || ids[f.ID] {
						fs = append(fs, f)
						for _, id := range f.Vertices {
							needed[id] = true
						}
					}
				}
			}
			for _, v := range n.Mesh.Vertices {
				if sel == nil || sel.Kind == "vertex" && ids[v.ID] || needed[v.ID] {
					vs = append(vs, v)
				}
			}
			if len(vs) > 500 || len(fs) > 500 {
				return nil, errors.New("inspection exceeds 500 elements; narrow the selection")
			}
			item["mesh"] = map[string]any{"vertices": vs, "faces": fs}
		}
		nodes = append(nodes, item)
	}
	return map[string]any{"report": report, "nodes": nodes}, nil
}
func capabilities() any {
	return map[string]any{"engine": "native-go", "version": engine.Version, "schema": "apteva-3d/v1", "coordinates": "meters, right-handed, Y-up; rotation is Euler degrees X then Y then Z", "command_schema": commandSchema(), "limits": map[string]int{"vertices": engine.MaxVertices, "faces": engine.MaxFaces, "commands_per_batch": 64, "nodes": 256}, "selection_semantics": "Faces match by centroid and normal. Vertex selections match positions. Handles are immutable and revision-bound. Omitting selection transforms all vertices of node. Topology commands require explicit face selection. result_selection refers to new cap faces and is usable within the same batch.", "operation_notes": map[string]string{"transform": "Positive scale only. Default pivot is selection centroid. radius enables proportional influence; falloff smooth (default) or linear. connected_only limits influence to connected components.", "inset": "Individual faces; amount is a centroid interpolation fraction (0,1), not a physical width. Concave or distorted results may be rejected.", "mesh.mirror": "Creates a baked reflected copy in target_id across world axis at offset; reverses face winding. This is not a live modifier or seam welding.", "vertices.weld": "Greedy deterministic weld of <=2000 selected vertices, tolerance <=0.1m; invalid topology rolls back the batch.", "render": "Native Go orthographic PNG; SDK returns artifact URL in JSON. Vision clients fetch the authenticated PNG to inspect it.", "export": "GLB includes flat normals and solid-color PBR materials. Each node pivot is its bounds center. No UVs, textures, animation, collision or LOD metadata yet."}, "workflow": []string{"assets_create template=car or box", "mesh_select query={node_id:body,kind:face,normal:[0,1,0]}", "mesh_edit expected_revision_id=<current> selections={roof:<handle>} mode=preview commands=[{op:extrude,node_id:body,selection:roof,direction:[0,1,0],distance:0.2,result_selection:cap}]", "assets_render candidate_id=<preview> view=side", "mesh_edit_commit candidate_id=<preview> request_key=<unique>", "assets_export format=glb"}}
}
