package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func inspectionService(t *testing.T) *Service {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	engine, err := NewEngine(filepath.Join(root, "engine"), bun, 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{store: testStore(t), engine: engine, project: "project-a", artifactRoot: filepath.Join(root, "artifacts"), maxOperations: 256}
}
func inspectionDefinition() DesignDefinition {
	return DesignDefinition{Schema: designSchema, Units: "mm", Operations: []map[string]any{{"id": "outer", "type": "box", "size": []any{20, 20, 20}}, {"id": "inner", "type": "box", "size": []any{18, 18, 18}, "origin": []any{1, 1, 1}}, {"id": "shell", "type": "cut", "inputs": []any{"outer", "inner"}}, {"id": "board", "type": "box", "size": []any{10, 10, 1}, "origin": []any{5, 5, 5}}}, Output: "shell", Parts: []DesignPart{{ID: "shell", Name: "Shell", Output: "shell", Manufacturing: &PartManufacturing{Classification: "printed"}}, {ID: "board", Name: "Board", Output: "board", Manufacturing: &PartManufacturing{Classification: "purchased"}, Evidence: &ComponentEvidence{Model: "fixture", GeometryStatus: "measured", Complete: true}}}, Assembly: &AssemblySpec{Instances: []AssemblyInstance{{ID: "shell_instance", PartID: "shell"}, {ID: "board_instance", PartID: "board"}}}, Checks: []map[string]any{{"id": "fit", "type": "part_clearance", "a": "shell_instance", "b": "board_instance", "min": 1}, {"id": "collision", "type": "assembly_collision"}}}
}
func buildInspection(t *testing.T, s *Service, d DesignDefinition, formats ...string) (*Design, *BuildResult) {
	t.Helper()
	raw, _ := json.Marshal(d)
	canonical, _, err := normalizeDefinition(raw, 256)
	if err != nil {
		t.Fatal(err)
	}
	design, err := s.store.CreateDesign(s.project, CreateDesignInput{Name: "Fixture", Definition: canonical, Parameters: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Build(context.Background(), design.ID, 0, formats)
	if err != nil {
		t.Fatal(err)
	}
	return design, result
}
func checkID(t *testing.T, b *BuildResult, id string) CheckResult {
	t.Helper()
	for _, c := range b.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("missing check %s: %#v", id, b.Checks)
	return CheckResult{}
}
func TestExactEnclosureFitAndVisibility(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	_, base := buildInspection(t, s, d, "mesh-json")
	if c := checkID(t, base, "fit"); c.Status != "pass" {
		t.Fatalf("cavity should fit: %#v", c)
	}
	hidden := false
	d.Assembly.Instances[1].Visible = &hidden
	_, b := buildInspection(t, s, d, "mesh-json")
	if b.Report.VolumeMM3 != base.Report.VolumeMM3 || checkID(t, b, "fit").Actual != checkID(t, base, "fit").Actual {
		t.Fatal("visibility changed physics")
	}
	d.Assembly.Instances[1].Transform.Translate = []any{-5, 0, 0}
	_, b = buildInspection(t, s, d, "mesh-json")
	if checkID(t, b, "fit").Status != "fail" || checkID(t, b, "collision").Status != "fail" {
		t.Fatalf("wall interference must fail: %#v", b.Checks)
	}
	d.Assembly.Instances[1].Transform.Translate = []any{-3.5, 0, 0}
	_, b = buildInspection(t, s, d, "mesh-json")
	if c := checkID(t, b, "fit"); c.Status != "fail" || math.Abs(c.Actual.(float64)-.5) > .001 {
		t.Fatalf("expected measured .5mm clearance: %#v", c)
	}
	// A small solid completely contained in solid material must be an interference.
	d.Operations[2] = map[string]any{"id": "shell", "type": "box", "size": []any{20, 20, 20}}
	d.Assembly.Instances[1].Transform = AssemblyTransform{}
	_, b = buildInspection(t, s, d, "mesh-json")
	if checkID(t, b, "collision").Status != "fail" {
		t.Fatal("contained solid passed")
	}
}
func TestKeepoutDatumAndThinWall(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	d.Operations = append(d.Operations, map[string]any{"id": "access", "type": "swept_box", "origin": []any{0, 0, 0}, "size": []any{1, 1, 1}, "travel": []any{5, 0, 0}})
	d.Assembly.Keepouts = []ClearanceRegion{{ID: "plug", OwnerInstance: "board_instance", Output: "access", Purpose: "Connector insertion", Source: "Fixture drawing", Against: []string{"shell_instance"}}}
	_, b := buildInspection(t, s, d, "mesh-json")
	if c := checkID(t, b, "keepout_plug_shell_instance"); c.Status != "fail" {
		t.Fatalf("keepout not checked: %#v", c)
	}
	d.Assembly.Keepouts = nil
	d.Assembly.Instances[1].Alignment = &DatumAlignment{TargetInstance: "shell_instance", LocalPoint: []any{5, 5, 5}, TargetPoint: []any{5, 5, 5}, Offset: []any{0, 0, 0}}
	_, b = buildInspection(t, s, d, "mesh-json")
	if checkID(t, b, "fit").Status != "pass" {
		t.Fatal("datum placement failed")
	}
	d = DesignDefinition{Schema: designSchema, Units: "mm", Operations: []map[string]any{{"id": "wall", "type": "box", "size": []any{10, 10, .3}}}, Output: "wall", Parts: []DesignPart{{ID: "wall", Name: "Thin wall", Output: "wall", Manufacturing: &PartManufacturing{Classification: "printed", MinWallMM: 5}}}, Checks: []map[string]any{{"id": "wall_scan", "type": "print_geometry", "part": "wall", "min_wall_mm": .8}}}
	_, b = buildInspection(t, s, d, "mesh-json")
	if checkID(t, b, "wall_scan").Status != "fail" {
		t.Fatalf("thin geometry not detected: %#v", b.Checks)
	}
}
func TestPrintExportsExcludePurchasedParts(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	design, b := buildInspection(t, s, d, "stl", "3mf", "step")
	for _, a := range b.Artifacts {
		var metadata map[string]any
		_ = json.Unmarshal(a.Metadata, &metadata)
		if metadata["part_id"] == "board" {
			t.Fatal("purchased part export")
		}
	}
	a, err := s.ManufacturingPackage(context.Background(), design.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(a.LocalPath)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range zr.File {
		files[f.Name] = true
	}
	if !files["resolved-design.json"] || !files["inspection/00-front-envelopes.svg"] || !files["inspection/00-front.png"] {
		t.Fatalf("portable release evidence missing: %v", files)
	}
	for name := range files {
		if strings.HasPrefix(name, "print/") && strings.Contains(name, "board") {
			t.Fatal("purchased part entered print package")
		}
	}
}
func TestSTEPImportRoundtripAndIsolation(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	_, built := buildInspection(t, s, d, "step")
	var body []byte
	for _, a := range built.Artifacts {
		var m map[string]any
		_ = json.Unmarshal(a.Metadata, &m)
		if a.Format == "step" && m["part_id"] == "shell" {
			body, _ = os.ReadFile(a.LocalPath)
		}
	}
	asset, err := s.importSTEP(context.Background(), "fixture.step", body, ComponentEvidence{Model: "Shell test", GeometryStatus: "measured", Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if asset.Geometry == nil || math.Abs(asset.Geometry.Bounds.Size[0]-20) > .01 || len(asset.Components) == 0 {
		t.Fatalf("invalid import: %#v", asset)
	}
	result, err := s.designFromAsset(asset, "Imported fixture")
	if err != nil {
		t.Fatal(err)
	}
	design := result.(map[string]any)["design"].(*Design)
	first, err := s.Build(context.Background(), design.ID, 0, []string{"step", "stl", "3mf"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.BodyCount != 1 {
		t.Fatalf("wrong body count %d", first.Report.BodyCount)
	}
	for _, a := range first.Artifacts {
		if a.Format == "stl" || a.Format == "3mf" {
			t.Fatal("purchased-only design produced print export")
		}
	}
	linked := DesignDefinition{Schema: designSchema, Units: "mm", Operations: []map[string]any{}, Parts: []DesignPart{{ID: "import", Name: "Imported fixture", Source: &PartSourceReference{DesignID: design.ID, RevisionID: design.CurrentRevisionID, SourceSHA256: design.CurrentRevision.SourceSHA256}, Manufacturing: &PartManufacturing{Classification: "purchased"}, Evidence: &asset.Evidence}}, Assembly: &AssemblySpec{Instances: []AssemblyInstance{{ID: "mounted_component", PartID: "import", Transform: AssemblyTransform{Translate: []any{10, 0, 0}}}}}}
	linkedDesign, linkedBuild := buildInspection(t, s, linked, "mesh-json")
	var meshPathForIDs string
	for _, a := range linkedBuild.Artifacts {
		if a.Format == "mesh-json" {
			meshPathForIDs = a.LocalPath
			break
		}
	}
	rawMesh, _ := os.ReadFile(meshPathForIDs)
	var cachedPreview struct {
		Parts []struct {
			ID         string `json:"id"`
			InstanceID string `json:"instance_id"`
		} `json:"parts"`
	}
	_ = json.Unmarshal(rawMesh, &cachedPreview)
	if len(cachedPreview.Parts) == 0 || !strings.HasPrefix(cachedPreview.Parts[0].ID, "mounted_component/") || cachedPreview.Parts[0].InstanceID != "mounted_component" {
		t.Fatalf("cached preview lost instance identity: %s", rawMesh[:min(len(rawMesh), 100)])
	}
	measurement, err := s.Measure(context.Background(), linkedDesign.ID, linkedDesign.CurrentRevisionID, map[string]any{"a": "mounted_component", "b": "mounted_component", "visual_a": cachedPreview.Parts[0].ID, "visual_b": cachedPreview.Parts[0].ID, "face_a": 0, "face_b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if measurement.(map[string]any)["measurement"].(CheckResult).Status != "pass" {
		t.Fatalf("cached face selection did not resolve: %#v", measurement)
	}
	if asset.Components[0].MeshSHA256 == "" {
		t.Fatal("import did not persist validated preview cache")
	}
	meshPath := filepath.Join(s.assetDir(asset.ID), asset.Components[0].ID+".mesh.json")
	savedMesh, _ := os.ReadFile(meshPath)
	if err = os.WriteFile(meshPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Build(context.Background(), design.ID, 0, nil); err == nil {
		t.Fatal("tampered preview cache accepted")
	}
	if err = os.WriteFile(meshPath, savedMesh, 0600); err != nil {
		t.Fatal(err)
	}
	foreign := *s
	foreign.project = "project-b"
	if _, err := foreign.sourceAsset(asset.ID); err == nil {
		t.Fatal("cross-project asset visible")
	}
	p := filepath.Join(s.assetDir(asset.ID), "original.step")
	if err = os.WriteFile(p, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Build(context.Background(), design.ID, 0, nil); err == nil {
		t.Fatal("tampered source accepted")
	}
}
func TestManufacturerSTEPFixtures(t *testing.T) {
	root := os.Getenv("DESIGN_MANUFACTURER_FIXTURES")
	if root == "" {
		t.Skip("set DESIGN_MANUFACTURER_FIXTURES to the Pocket research/manufacturer directory")
	}
	s := inspectionService(t)
	s.engine.timeout = 300 * time.Second
	if cache := os.Getenv("DESIGN_MANUFACTURER_CACHE"); cache != "" {
		s.artifactRoot = cache
		metadata, _ := filepath.Glob(filepath.Join(cache, "sources", digest([]byte(s.project)), "*", "asset.json"))
		for _, path := range metadata {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var asset SourceAsset
			if err = json.Unmarshal(raw, &asset); err != nil {
				t.Fatal(err)
			}
			if asset.EngineVersion != engineVersion {
				continue
			}
			if _, err = s.store.db.Exec(`INSERT OR IGNORE INTO source_assets(project_id,id,metadata_json) VALUES(?,?,?)`, s.project, asset.ID, string(raw)); err != nil {
				t.Fatal(err)
			}
		}
	}
	cases := []struct {
		file, model string
		size        [3]float64
	}{{"pisugar3plus.STEP", "PiSugar 3 Plus", [3]float64{65, 9.22, 57}}, {"cm5-nano-a-cad/CM5-NANO-A.step", "CM5-NANO-A", [3]float64{56.235, 40.018, 11.8}}, {"cm5-official-cad/CM5R5-3dsteps.step", "CM5", [3]float64{55, 40, 4.621}}}
	var imported []*Design
	var assets []*SourceAsset
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, c.file))
			if err != nil {
				t.Fatal(err)
			}
			asset, err := s.importSTEP(context.Background(), filepath.Base(c.file), body, ComponentEvidence{Model: c.model, GeometryStatus: "manufacturer_cad", Complete: false, MeasurementsNeeded: []string{"Delivered hardware fit"}})
			if err != nil {
				t.Fatal(err)
			}
			if asset.Geometry == nil {
				t.Fatal("missing geometry bounds")
			}
			for k, expected := range c.size {
				if expected > 0 && math.Abs(asset.Geometry.Bounds.Size[k]-expected) > .15 {
					t.Fatalf("axis %d: got %.4f expected %.4f", k, asset.Geometry.Bounds.Size[k], expected)
				}
			}
			t.Logf("components=%d bounds=%v diagnostics=%v", len(asset.Components), asset.Geometry.Bounds.Size, asset.Diagnostics)
			result, err := s.designFromAsset(asset, c.model)
			if err != nil {
				t.Fatal(err)
			}
			imported = append(imported, result.(map[string]any)["design"].(*Design))
			assets = append(assets, asset)
		})
	}
	if len(imported) == 3 {
		linked := DesignDefinition{Schema: designSchema, Units: "mm", Operations: []map[string]any{}, Assembly: &AssemblySpec{Name: "Pinned purchased Pocket references"}}
		for index, design := range imported {
			id := fmt.Sprintf("component_%d", index)
			linked.Parts = append(linked.Parts, DesignPart{ID: id, Name: design.Name, Source: &PartSourceReference{DesignID: design.ID, RevisionID: design.CurrentRevisionID, SourceSHA256: design.CurrentRevision.SourceSHA256}, Manufacturing: &PartManufacturing{Classification: "purchased"}, Evidence: &assets[index].Evidence})
			b := assets[index].Geometry.Bounds
			linked.Assembly.Instances = append(linked.Assembly.Instances, AssemblyInstance{ID: id, PartID: id, Transform: AssemblyTransform{Translate: []any{-b.Min[0] + float64(index)*75, -b.Min[1], -b.Min[2]}}})
		}
		design, first := buildInspection(t, s, linked, "mesh-json")
		second, err := s.Build(context.Background(), design.ID, design.CurrentRevisionID, []string{"mesh-json"})
		if err != nil {
			t.Fatal(err)
		}
		if first.Report.Bounds != second.Report.Bounds || math.Abs(first.Report.VolumeMM3-second.Report.VolumeMM3) > .001 {
			t.Fatal("pinned manufacturer assembly changed across rebuilds")
		}
		t.Logf("reproduced pinned 3-component assembly: solids=%d bounds=%v", first.Report.BodyCount, first.Report.Bounds.Size)
	}

}

func TestExactMeasurementAndVerifiedReleaseGates(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	d.Release = &ReleaseEvidence{Stage: "verified", Fastening: "M2.5 fixture screws", AssemblyGuide: "Install board, then close cover", ServiceGuide: "Disconnect power before removal", RequiredChecks: []string{"fit", "collision"}}
	design, built := buildInspection(t, s, d, "mesh-json")
	measured, err := s.Measure(context.Background(), design.ID, design.CurrentRevisionID, map[string]any{"a": "shell_instance", "b": "board_instance"})
	if err != nil {
		t.Fatal(err)
	}
	m := measured.(map[string]any)["measurement"].(CheckResult)
	if m.Status != "pass" || math.Abs(m.Actual.(float64)-4) > .001 {
		t.Fatalf("exact measurement failed: %#v", m)
	}
	face, err := s.Measure(context.Background(), design.ID, design.CurrentRevisionID, map[string]any{"a": "board_instance", "b": "board_instance", "face_a": float64(0), "face_b": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if face.(map[string]any)["measurement"].(CheckResult).Status != "pass" {
		t.Fatalf("exact face measurement failed: %#v", face)
	}
	if _, err := s.ManufacturingPackage(context.Background(), design.ID, 0); err == nil {
		t.Fatal("verified release accepted missing physical tests")
	}
	r := design.CurrentRevision
	for _, kind := range []string{"physical_fit", "enclosed_thermal"} {
		record, _ := json.Marshal(PhysicalTest{Kind: kind, Status: "pass", RevisionID: r.ID, SourceSHA256: r.SourceSHA256, EvidenceURL: "https://example.test/fixture-evidence"})
		if _, err := s.store.db.Exec(`INSERT INTO verification_evidence(project_id,revision_id,payload_json) VALUES(?,?,?)`, s.project, r.ID, string(record)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ManufacturingPackage(context.Background(), design.ID, 0); err != nil {
		t.Fatalf("complete verified fixture refused: %v checks=%#v", err, built.Checks)
	}
	failure, _ := json.Marshal(PhysicalTest{Kind: "physical_fit", Status: "fail", RevisionID: r.ID, SourceSHA256: r.SourceSHA256, EvidenceURL: "https://example.test/fixture-failure"})
	_, _ = s.store.db.Exec(`INSERT INTO verification_evidence(project_id,revision_id,payload_json) VALUES(?,?,?)`, s.project, r.ID, string(failure))
	if _, err := s.ManufacturingPackage(context.Background(), design.ID, 0); err == nil {
		t.Fatal("older passing test overrode latest failure")
	}
}

func TestAssemblyDrivenEnclosureTemplate(t *testing.T) {
	s := inspectionService(t)
	d := inspectionDefinition()
	source, built := buildInspection(t, s, d, "mesh-json")
	options := map[string]any{"wall_mm": 2.0, "component_gap_mm": 2.0, "display": map[string]any{"origin": []any{3, 3}, "size": []any{10, 10}}, "fasteners": []any{map[string]any{"position": []any{-3, -3}, "boss_diameter_mm": 4.0, "hole_diameter_mm": 2.5}}, "supports": []any{map[string]any{"origin": []any{2, 2, -2}, "size": []any{2, 2, 3}}}, "cutouts": []any{map[string]any{"origin": []any{-5, 5, 5}, "size": []any{6, 4, 3}}}}
	generated, err := assemblyEnclosure(source, source.CurrentRevision, built.Report, options)
	if err != nil {
		t.Fatal(err)
	}
	design, result := buildInspection(t, s, *generated, "step", "stl", "3mf")
	if result.Report.BodyCount < 4 {
		t.Fatalf("template is incomplete: %#v", result.Report)
	}
	if generated.FitProfileID != "prototype" || generated.Parameters["lid_fit"].Fit != "lid" {
		t.Fatal("fit profile is not linked")
	}
	if _, err := s.ManufacturingPackage(context.Background(), design.ID, 0); err != nil && result.Run.Status != "failed" {
		t.Fatal(err)
	}
}

func TestPackageChecksumsAndReplay(t *testing.T) {
	s := inspectionService(t)
	design, _ := buildInspection(t, s, inspectionDefinition(), "mesh-json")
	artifact, err := s.ManufacturingPackage(context.Background(), design.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(artifact.LocalPath)
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, f := range archive.File {
		dest := filepath.Join(root, f.Name)
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var sums map[string]string
	b, _ := os.ReadFile(filepath.Join(root, "checksums.json"))
	if err = json.Unmarshal(b, &sums); err != nil {
		t.Fatal(err)
	}
	for name, expected := range sums {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || digest(b) != expected {
			t.Fatalf("package checksum mismatch: %s", name)
		}
	}
	cmd := exec.Command("bun", "runner/replay.mjs", root, filepath.Join(root, "replayed"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("portable replay failed: %v %s", err, out)
	}
	if _, err = os.Stat(filepath.Join(root, "replayed", "model.step")); err != nil {
		t.Fatal(err)
	}
}

func TestImportedSourcePackageReplays(t *testing.T) {
	s := inspectionService(t)
	_, built := buildInspection(t, s, inspectionDefinition(), "step")
	var original []byte
	for _, a := range built.Artifacts {
		if a.Format == "step" {
			original, _ = os.ReadFile(a.LocalPath)
			break
		}
	}
	asset, err := s.importSTEP(context.Background(), "purchased-fixture.step", original, ComponentEvidence{Model: "Purchased fixture", GeometryStatus: "measured", Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.designFromAsset(asset, "Purchased fixture")
	if err != nil {
		t.Fatal(err)
	}
	design := created.(map[string]any)["design"].(*Design)
	artifact, err := s.ManufacturingPackage(context.Background(), design.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(artifact.LocalPath)
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, file := range archive.File {
		p := filepath.Join(root, file.Name)
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bun", "runner/replay.mjs", root, filepath.Join(root, "replayed"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("imported asset replay failed: %v %s", err, out)
	}
	if _, err = os.Stat(filepath.Join(root, "replayed", "model.step")); err != nil {
		t.Fatal(err)
	}
}
