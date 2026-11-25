package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

// stlBounds reads ASCII or binary STL and returns the vertex bounds.
func stlBounds(t *testing.T, body []byte) (min, max [3]float64) {
	t.Helper()
	for k := 0; k < 3; k++ {
		min[k], max[k] = math.Inf(1), math.Inf(-1)
	}
	add := func(v [3]float64) {
		for k := 0; k < 3; k++ {
			min[k], max[k] = math.Min(min[k], v[k]), math.Max(max[k], v[k])
		}
	}
	if bytes.HasPrefix(body, []byte("solid")) && bytes.Contains(body[:min64(len(body), 512)], []byte("facet")) {
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 4 && fields[0] == "vertex" {
				var v [3]float64
				for k := 0; k < 3; k++ {
					json.Unmarshal([]byte(fields[k+1]), &v[k])
				}
				add(v)
			}
		}
		return
	}
	count := int(binary.LittleEndian.Uint32(body[80:84]))
	for i := 0; i < count; i++ {
		offset := 84 + i*50 + 12
		for j := 0; j < 3; j++ {
			var v [3]float64
			for k := 0; k < 3; k++ {
				v[k] = float64(math.Float32frombits(binary.LittleEndian.Uint32(body[offset+j*12+k*4:])))
			}
			add(v)
		}
	}
	return
}

func min64(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildEnclosure(t *testing.T, s *Service, name string, options EnclosureOptions, formats []string) (*BuildResult, *DesignDefinition) {
	t.Helper()
	definition, _, err := generatePCBEnclosure(pcbEnvelopeFixture(), options)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	body, _ := json.Marshal(definition)
	design := createDesign(t, s, name, string(body), `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, formats)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result, definition
}

func partVolume(t *testing.T, report GeometryReport, instance string) float64 {
	t.Helper()
	for _, part := range report.Parts {
		if part.InstanceID == instance {
			return part.VolumeMM3
		}
	}
	t.Fatalf("instance %s missing", instance)
	return 0
}

func TestEnclosureExportsBaseAndLidSeparately(t *testing.T) {
	s := inspectionService(t)
	options := EnclosureOptions{FloorThicknessMM: 3, LidThicknessMM: 3}
	result, definition := buildEnclosure(t, s, "parts", options, []string{"stl"})
	normalized, _ := normalizeEnclosureOptions(options, pcbEnvelopeFixture().Envelope)
	base := instanceBounds(t, result.Report, "base_1")
	lid := instanceBounds(t, result.Report, "lid_1")
	baseHeight := base.Max[2]
	near(t, "lid sits on the rim", lid.Max[2], baseHeight+normalized.LidThicknessMM, 1e-3)
	near(t, "lid lip reaches into the base", lid.Min[2], baseHeight-normalized.LipDepthMM, 1e-3)
	near(t, "lid spans the base in x", lid.Max[0]-lid.Min[0], base.Max[0]-base.Min[0], 1e-3)
	pcb := instanceBounds(t, result.Report, "pcb_1")
	near(t, "PCB rests on the standoffs", pcb.Min[2], normalized.FloorThicknessMM+normalized.StandoffHeightMM, 1e-3)
	files := map[string][]byte{}
	for _, artifact := range result.Artifacts {
		if artifact.Format != "stl" {
			continue
		}
		var metadata struct {
			PartID string `json:"part_id"`
		}
		_ = json.Unmarshal(artifact.Metadata, &metadata)
		body, err := os.ReadFile(artifact.LocalPath)
		if err != nil {
			t.Fatal(err)
		}
		files[metadata.PartID] = body
	}
	if files["base"] == nil || files["lid"] == nil {
		t.Fatalf("base and lid need their own STL files, got %d", len(files))
	}
	if files["pcb"] != nil {
		t.Fatal("the PCB is a reference part and must not be exported for printing")
	}
	min, max := stlBounds(t, files["lid"])
	near(t, "lid STL starts at the bed", min[2], 0, 0.01)
	near(t, "lid STL is panel + lip tall", max[2], normalized.LidThicknessMM+normalized.LipDepthMM, 0.01)
	// The panel (widest section) is at the bottom: print orientation.
	if !strings.Contains(string(mustJSON(definition.Assembly.Interfaces)), `"seat"`) {
		t.Fatal("lid seat interface missing")
	}
}

func mustJSON(value any) []byte {
	body, _ := json.Marshal(value)
	return body
}

func TestEnclosureWallMagnetsAndWindows(t *testing.T) {
	s := inspectionService(t)
	plain, _ := buildEnclosure(t, s, "plain", EnclosureOptions{Shape: "rounded"}, nil)
	plainBase := partVolume(t, plain.Report, "base_1")
	magnets := []EnclosureMagnet{
		{Face: "west", AlongMM: 45, ZMM: 2, DiameterMM: 6, DepthMM: 1.5, Polarity: "n", ID: "west_mag"},
		{Face: "east", AlongMM: 45, ZMM: 2, DiameterMM: 6, DepthMM: 1.5, Polarity: "S", From: "inside"},
		{Face: "south", AlongMM: 40, ZMM: 2, DiameterMM: 6, DepthMM: 1.5},
		{Face: "north", AlongMM: 40, ZMM: 2, DiameterMM: 6, DepthMM: 1.5},
	}
	withMagnets, definition := buildEnclosure(t, s, "magnets", EnclosureOptions{Shape: "rounded", Magnets: magnets}, nil)
	removed := plainBase - partVolume(t, withMagnets.Report, "base_1")
	perPocket := math.Pi * 3.1 * 3.1 * 1.5
	near(t, "four wall pockets removed", removed, 4*perPocket, 0.5)
	var west, east *MechanicalInterface
	for i := range definition.Assembly.Interfaces {
		switch definition.Assembly.Interfaces[i].ID {
		case "west_mag":
			west = &definition.Assembly.Interfaces[i]
		case "magnet_2":
			east = &definition.Assembly.Interfaces[i]
		}
	}
	if west == nil || east == nil || west.Kind != "magnet" || west.Parameters["polarity"] != "N" || east.Parameters["from"] != "inside" {
		t.Fatalf("magnets should become interfaces with polarity: %#v %#v", west, east)
	}
	if west.Direction[0] != -1 || east.Direction[0] != 1 {
		t.Fatalf("wall interfaces face outward: west %v east %v", west.Direction, east.Direction)
	}
	windows := []EnclosureWindow{
		{Face: "west", AlongMM: 10, ZMM: 3, WidthMM: 8, HeightMM: 3, CornerRadiusMM: 1},
		{Face: "north", AlongMM: 20, ZMM: 3, WidthMM: 8, HeightMM: 3},
	}
	withWindows, _ := buildEnclosure(t, s, "windows", EnclosureOptions{Shape: "rounded", PogoWindows: windows}, nil)
	cut := plainBase - partVolume(t, withWindows.Report, "base_1")
	wall := defaultEnclosureOptions().WallThicknessMM
	expected := ((8*3 - (4-math.Pi)*1) + 8*3) * wall
	near(t, "wall windows go through the wall only", cut, expected, 0.5)

	fixture := pcbEnvelopeFixture()
	for options, want := range map[*EnclosureOptions]string{
		{Shape: "outline", Magnets: []EnclosureMagnet{{Face: "west", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 1}}}:                                                                 "box or rounded",
		{Shape: "outline", PogoWindows: []EnclosureWindow{{Face: "east", AlongMM: 20, ZMM: 2, WidthMM: 4, HeightMM: 2}}}:                                                               "box or rounded",
		{Magnets: []EnclosureMagnet{{Face: "west", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 2.2}}}:                                                                                 "leaves less than 0.4 mm",
		{Shape: "rounded", Magnets: []EnclosureMagnet{{Face: "west", AlongMM: -2, ZMM: 2, DiameterMM: 4, DepthMM: 1}}}:                                                                 "flat part of the west wall",
		{Magnets: []EnclosureMagnet{{Face: "south", AlongMM: 20, ZMM: 40, DiameterMM: 4, DepthMM: 1}}}:                                                                                 "wall height",
		{Magnets: []EnclosureMagnet{{Face: "west", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 1, Polarity: "X"}}}:                                                                    "polarity must be N or S",
		{Magnets: []EnclosureMagnet{{Face: "west", Part: "lid", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 1}}}:                                                                      "belongs to the base",
		{Magnets: []EnclosureMagnet{{ID: "m", Face: "west", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 1}, {ID: "m", Face: "east", AlongMM: 20, ZMM: 2, DiameterMM: 4, DepthMM: 1}}}: "duplicate",
	} {
		if _, _, err := generatePCBEnclosure(fixture, *options); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%+v: want %q, got %v", *options, want, err)
		}
	}
}
