package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
)

func releaseStage(d *DesignDefinition) string {
	if d.Release != nil {
		return d.Release.Stage
	}
	return "prototype"
}
func validateReleaseEvidence(d *DesignDefinition, r *Revision, b *BuildResult) error {
	if releaseStage(d) != "verified" {
		return nil
	}
	e := d.Release
	if b.Run.Status != "passed" || len(e.Assumptions) > 0 {
		return errors.New("verified release requires passing validation and resolved assumptions")
	}
	if e.Fastening == "" || e.AssemblyGuide == "" || e.ServiceGuide == "" {
		return errors.New("verified release requires fastening, assembly and service instructions")
	}
	if len(e.RequiredChecks) == 0 {
		return errors.New("verified release requires explicitly named required checks")
	}
	for _, id := range e.RequiredChecks {
		found := false
		for _, c := range b.Checks {
			if c.ID == id && c.Status == "pass" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("required check %s is missing or not passed", id)
		}
	}
	for _, p := range d.Parts {
		if p.Manufacturing != nil && p.Manufacturing.Classification == "purchased" {
			if p.Evidence == nil || !p.Evidence.Complete || len(p.Evidence.Omissions) > 0 || len(p.Evidence.MeasurementsNeeded) > 0 {
				return fmt.Errorf("component %s evidence is incomplete", p.ID)
			}
		}
	}
	for _, kind := range []string{"physical_fit", "enclosed_thermal"} {
		found := false
		for _, t := range e.PhysicalTests {
			if t.Kind == kind && t.RevisionID == r.ID && t.SourceSHA256 == r.SourceSHA256 {
				found = t.Status == "pass" && t.EvidenceURL != ""
			}
		}
		if !found {
			return fmt.Errorf("verified release requires revision-bound %s evidence", kind)
		}
	}
	return nil
}
func (s *Service) portableDefinition(id int64, body []byte) ([]byte, map[string][]byte, error) {
	runtime, _, err := s.materializeComponentSources(id, body)
	if err != nil {
		return nil, nil, err
	}
	var root map[string]any
	if err = json.Unmarshal(runtime, &root); err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	var walk func(map[string]any) error
	walk = func(d map[string]any) error {
		if assets, ok := d["_assets"].(map[string]any); ok {
			for id, raw := range assets {
				a, err := s.sourceAsset(id)
				if err != nil {
					return err
				}
				prefix := "sources/" + id + "/"
				meta, _ := json.MarshalIndent(a, "", "  ")
				files[prefix+"asset.json"] = meta
				source, err := os.ReadFile(s.assetDir(id) + "/original.step")
				if err != nil {
					return err
				}
				files[prefix+"original.step"] = source
				payload := raw.(map[string]any)
				paths := map[string]any{}
				meshPaths := map[string]any{}
				for _, c := range a.Components {
					b, err := os.ReadFile(s.assetDir(id) + "/" + c.File)
					if err != nil {
						return err
					}
					files[prefix+c.File] = b
					paths[c.ID] = prefix + c.File
					if c.MeshSHA256 != "" {
						name := c.ID + ".mesh.json"
						b, err := os.ReadFile(s.assetDir(id) + "/" + name)
						if err != nil {
							return err
						}
						files[prefix+name] = b
						meshPaths[c.ID] = prefix + name
					}

				}
				payload["files"] = paths
				payload["meshes"] = meshPaths
			}
		}
		if components, ok := d["_resolved_components"].([]any); ok {
			for _, raw := range components {
				c := raw.(map[string]any)
				if child, ok := c["definition"].(map[string]any); ok {
					if err := walk(child); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err = walk(root); err != nil {
		return nil, nil, err
	}
	out, err := json.MarshalIndent(root, "", "  ")
	return out, files, err
}
func printableMesh(assembly []byte, parts map[string]EngineArtifact, d *DesignDefinition) ([]byte, bool, error) {
	if len(d.Parts) == 0 {
		return assembly, true, nil
	}
	// Use manufacturing-oriented unique parts; quantity and instance transforms are in assembly.json/BOM.
	merged := meshDocument{}
	for _, p := range d.Parts {
		if p.Manufacturing != nil && (p.Manufacturing.Classification == "purchased" || p.Manufacturing.Classification == "reference") {
			continue
		}
		a, ok := parts[p.ID]
		if !ok {
			continue
		}
		body, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, false, err
		}
		m, err := parseMesh(body)
		if err != nil {
			return nil, false, err
		}
		offset := uint32(len(merged.Vertices) / 3)
		merged.Vertices = append(merged.Vertices, m.Vertices...)
		merged.Normals = append(merged.Normals, m.Normals...)
		for _, i := range m.Triangles {
			merged.Triangles = append(merged.Triangles, i+offset)
		}
	}
	if len(merged.Triangles) == 0 {
		return nil, false, nil
	}
	b, err := json.Marshal(merged)
	return b, true, err
}

// Lightweight orthographic evidence views are portable SVGs, not a claim of a sectioned CAD drawing.
func inspectionSVGs(report GeometryReport, e *ReleaseEvidence) map[string][]byte {
	views := []InspectionView{{Name: "front", Direction: "front"}, {Name: "top", Direction: "top"}, {Name: "side", Direction: "side"}}
	if e != nil && len(e.Views) > 0 {
		views = e.Views
	}
	out := map[string][]byte{}
	for index, v := range views {
		x, y := 0, 2
		if v.Direction == "top" {
			x, y = 0, 1
		}
		if v.Direction == "side" {
			x, y = 1, 2
		}
		xmin, ymin := report.Bounds.Min[x], report.Bounds.Min[y]
		width, height := report.Bounds.Size[x], report.Bounds.Size[y]
		if width <= 0 || height <= 0 {
			continue
		}
		svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%g %g %g %g"><title>%s — instance envelopes, mm</title><rect x="%g" y="%g" width="%g" height="%g" fill="#fff"/>`, xmin-5, ymin-5, width+10, height+10, html.EscapeString(v.Name), xmin-5, ymin-5, width+10, height+10)
		for _, p := range report.Parts {
			if len(v.Instances) > 0 {
				found := false
				for _, id := range v.Instances {
					if id == p.InstanceID {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			svg += fmt.Sprintf(`<g><rect x="%g" y="%g" width="%g" height="%g" fill="none" stroke="#334155" stroke-width="0.3"/><title>%s</title></g>`, p.Bounds.Min[x], p.Bounds.Min[y], p.Bounds.Size[x], p.Bounds.Size[y], html.EscapeString(p.InstanceID))
		}
		svg += "</svg>"
		out[fmt.Sprintf("%02d-%s-envelopes.svg", index, v.Direction)] = []byte(svg)
	}
	return out
}
