package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"strings"
	"testing"
)

func TestTextAndSVGProfiles(t *testing.T) {
	s := inspectionService(t)
	text := featureReport(t, s, "text", `{"id":"t","type":"extrude_profile","profile":{"kind":"text","text":"AB","size":10,"align":"center","valign":"middle"},"height":2}`, "t", "")
	if !text.Valid || text.VolumeMM3 < 20 {
		t.Fatalf("text should extrude to a solid: %#v", text)
	}
	near(t, "text centred x", (text.Bounds.Min[0]+text.Bounds.Max[0])/2, 0, 0.01)
	near(t, "text centred y", (text.Bounds.Min[1]+text.Bounds.Max[1])/2, 0, 0.01)
	if width := text.Bounds.Max[0] - text.Bounds.Min[0]; width < 8 || width > 20 {
		t.Fatalf("two 10 mm glyphs should be roughly 12-14 mm wide, got %v", width)
	}
	engraved := featureReport(t, s, "engraved", block+`,{"id":"t","type":"extrude_profile","origin":[10,10,9],"profile":{"kind":"text","text":"OK","size":8,"font":"sans-bold","align":"center","valign":"middle"},"height":2},{"id":"e","type":"cut","base":"block","tools":["t"]}`, "e", "")
	if engraved.VolumeMM3 >= 4000 || engraved.VolumeMM3 < 3900 {
		t.Fatalf("engraving should remove a little material: %v", engraved.VolumeMM3)
	}
	frame := featureReport(t, s, "svg", `{"id":"p","type":"extrude_profile","profile":{"kind":"svg_path","d":"M0 0H20V20H0Z M5 5H15V15H5Z"},"height":1}`, "p", "")
	near(t, "square frame from SVG (inner subpath is a hole)", frame.VolumeMM3, 300, 1e-3)
	near(t, "SVG y flipped", frame.Bounds.Min[1], -20, 1e-6)
	disc := featureReport(t, s, "svgarc", `{"id":"p","type":"extrude_profile","profile":{"kind":"svg_path","d":"M-5 0 A5 5 0 1 0 5 0 A5 5 0 1 0 -5 0 Z","scale":2},"height":1}`, "p", "")
	near(t, "SVG arcs make a 10 mm radius disc", disc.VolumeMM3, math.Pi*100, 0.05)
	curve := featureReport(t, s, "svgcurve", `{"id":"p","type":"extrude_profile","profile":{"kind":"svg_path","d":"M0 0 L20 0 Q20 20 0 20 Z","flip_y":false},"height":1}`, "p", "")
	near(t, "quadratic curve area", curve.VolumeMM3, 200+400/3.0, 0.05)
}

func binarySTLCube(size float32) []byte {
	v := [8][3]float32{{0, 0, 0}, {size, 0, 0}, {size, size, 0}, {0, size, 0}, {0, 0, size}, {size, 0, size}, {size, size, size}, {0, size, size}}
	faces := [][3]int{{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7}, {0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5}, {2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7}}
	var buffer bytes.Buffer
	buffer.Write(make([]byte, 80))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(len(faces)))
	for _, face := range faces {
		_ = binary.Write(&buffer, binary.LittleEndian, [3]float32{})
		for _, index := range face {
			_ = binary.Write(&buffer, binary.LittleEndian, v[index])
		}
		_ = binary.Write(&buffer, binary.LittleEndian, uint16(0))
	}
	return buffer.Bytes()
}

func TestSTLReferenceImport(t *testing.T) {
	s := inspectionService(t)
	if _, err := s.importSTLAsset("bad.stl", []byte("not an stl")); err == nil {
		t.Fatal("garbage must be rejected")
	}
	asset, err := s.importSTLAsset("cube.stl", binarySTLCube(1))
	if err != nil {
		t.Fatal(err)
	}
	definition := `{"schema":"apteva-design/v1","units":"mm",
		"operations":[{"id":"mesh","type":"import_stl","asset_id":"` + asset.ID + `","units":"cm"},{"id":"plate","type":"box","size":[30,30,2],"origin":[-10,-10,-3]}],
		"output":"mesh",
		"parts":[{"id":"scan","name":"Scan","output":"mesh","manufacturing":{"classification":"reference"}},{"id":"plate","name":"Plate","output":"plate"}],
		"assembly":{"name":"fit","instances":[{"id":"scan_1","part_id":"scan"},{"id":"plate_1","part_id":"plate"}]},
		"checks":[{"id":"gap","type":"part_clearance","a":"scan_1","b":"plate_1","min":0.5}]}`
	design := createDesign(t, s, "stl", definition, `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, []string{"stl"})
	if err != nil {
		t.Fatal(err)
	}
	scan := instanceBounds(t, result.Report, "scan_1")
	boundsNear(t, "1 cm STL cube scaled to mm", scan, [3]float64{0, 0, 0}, [3]float64{10, 10, 10})
	if c := checkID(t, result, "gap"); c.Status != "pass" || math.Abs(c.Actual.(float64)-1) > 1e-3 {
		t.Fatalf("clearance to a reference mesh should be measured exactly (1 mm): %#v", c)
	}
	for _, artifact := range result.Artifacts {
		if artifact.Format == "stl" && strings.Contains(artifact.Name, "scan") {
			t.Fatalf("reference mesh must not be exported for printing: %#v", artifact)
		}
	}
}

func TestColouredExportsAndDrawings(t *testing.T) {
	s := inspectionService(t)
	definition := `{"schema":"apteva-design/v1","units":"mm",
		"materials":[{"id":"pla_red","name":"PLA Red","kind":"pla","color":"#cc2200","density_g_cm3":1.24}],
		"operations":[{"id":"body","type":"box","size":[40,30,10]},{"id":"bore","type":"cylinder","radius":4,"height":12,"origin":[20,15,-1]},
			{"id":"drilled","type":"cut","base":"body","tools":["bore"]},{"id":"cap","type":"box","size":[40,30,2],"origin":[0,0,10]}],
		"output":"drilled",
		"parts":[{"id":"body","name":"Body","output":"drilled","material_id":"pla_red"},{"id":"cap","name":"Cap","output":"cap","color":"#1d4ed8"}],
		"assembly":{"name":"pair","instances":[{"id":"body_1","part_id":"body"},{"id":"cap_1","part_id":"cap"}]}}`
	design := createDesign(t, s, "coloured", definition, `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, []string{"glb", "3mf", "dxf", "pdf"})
	if err != nil {
		t.Fatal(err)
	}
	read := func(format, part string) []byte {
		t.Helper()
		for _, artifact := range result.Artifacts {
			var metadata struct {
				PartID string `json:"part_id"`
			}
			_ = json.Unmarshal(artifact.Metadata, &metadata)
			if artifact.Format == format && metadata.PartID == part {
				body, err := os.ReadFile(artifact.LocalPath)
				if err != nil {
					t.Fatal(err)
				}
				return body
			}
		}
		t.Fatalf("no %s artifact for part %q in %#v", format, part, result.Artifacts)
		return nil
	}
	glb := read("glb", "")
	jsonLength := binary.LittleEndian.Uint32(glb[12:16])
	var gltf struct {
		Materials []struct {
			Name string `json:"name"`
			PBR  struct {
				BaseColorFactor []float64 `json:"baseColorFactor"`
			} `json:"pbrMetallicRoughness"`
		} `json:"materials"`
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(glb[20:20+jsonLength], &gltf); err != nil {
		t.Fatal(err)
	}
	if len(gltf.Materials) != 2 || len(gltf.Nodes) != 2 {
		t.Fatalf("GLB needs one node and material per part: %#v", gltf)
	}
	red := gltf.Materials[0].PBR.BaseColorFactor
	if gltf.Materials[0].Name != "pla_red" || red[0] < 0.5 || red[2] > 0.05 {
		t.Fatalf("first material should be the red PLA: %#v", gltf.Materials)
	}
	archive, err := zip.NewReader(bytes.NewReader(read("3mf", "body")), int64(len(read("3mf", "body"))))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if file.Name != "3D/3dmodel.model" {
			continue
		}
		handle, _ := file.Open()
		model, _ := io.ReadAll(handle)
		if !bytes.Contains(model, []byte(`displaycolor="#CC2200FF"`)) || !bytes.Contains(model, []byte(`name="PLA Red"`)) {
			t.Fatalf("3MF should carry the material colour: %s", model[:400])
		}
	}
	dxf := string(read("dxf", ""))
	if !strings.Contains(dxf, "AC1009") || !strings.Contains(dxf, "\nHIDDEN\n") || strings.Count(dxf, "\nLINE\n") < 20 || !strings.HasSuffix(dxf, "EOF\n") {
		t.Fatalf("DXF should have visible and hidden lines: %d bytes", len(dxf))
	}
	if !strings.Contains(dxf, "Overall 40 x 30 x 12 mm") {
		t.Fatal("DXF title block should state the overall size")
	}
	pdf := read("pdf", "")
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.Contains(pdf, []byte("Scale 2:1")) || !bytes.Contains(pdf, []byte("[4 2] 0 d")) {
		t.Fatalf("PDF should be a 2:1 sheet with dashed hidden lines")
	}
}

func TestMouldingCheck(t *testing.T) {
	s := inspectionService(t)
	build := func(name, operations, output, check string) CheckResult {
		t.Helper()
		design := createDesign(t, s, name, `{"schema":"apteva-design/v1","units":"mm","operations":[`+operations+`],"output":"`+output+`","checks":[`+check+`]}`, `{}`)
		result, err := s.Build(context.Background(), design.ID, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		return checkID(t, result, "mould")
	}
	straight := build("straight", block, "block", `{"id":"mould","type":"moulding","pull":[0,0,1],"min_draft_deg":1}`)
	if straight.Status != "fail" || !strings.Contains(straight.Message, "under 1° draft") {
		t.Fatalf("vertical walls have no draft: %#v", straight)
	}
	tapered := build("tapered", `{"id":"f","type":"loft","ruled":true,"sections":[{"profile":{"kind":"rectangle","width":20,"height":20}},{"profile":{"kind":"rectangle","width":18,"height":18},"offset":10}]}`, "f", `{"id":"mould","type":"moulding","min_draft_deg":3}`)
	if tapered.Status != "pass" {
		t.Fatalf("5.7° tapered walls should pass a 3° draft check: %#v", tapered)
	}
	undercut := build("undercut", block+`,{"id":"bore","type":"cylinder","radius":2,"height":30,"origin":[-5,10,5],"direction":[1,0,0]},{"id":"u","type":"cut","base":"block","tools":["bore"]}`, "u", `{"id":"mould","type":"moulding","min_draft_deg":0}`)
	if undercut.Status != "fail" || !strings.Contains(undercut.Message, "undercut") {
		t.Fatalf("a side hole is an undercut for a vertical pull: %#v", undercut)
	}
	sideways := build("sideways", block+`,{"id":"bore","type":"cylinder","radius":2,"height":30,"origin":[-5,10,5],"direction":[1,0,0]},{"id":"u","type":"cut","base":"block","tools":["bore"]}`, "u", `{"id":"mould","type":"moulding","pull":[1,0,0],"min_draft_deg":0}`)
	if sideways.Status != "pass" {
		t.Fatalf("pulling along the hole axis frees it: %#v", sideways)
	}
}
