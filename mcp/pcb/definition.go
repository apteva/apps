package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const maxDefinitionBytes = 8 << 20

var nativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func emptyDefinition(name string) Definition {
	return Definition{
		Schema: pcbSchema,
		Name:   strings.TrimSpace(name),
		Board: Board{
			WidthNM:     50_000_000,
			HeightNM:    30_000_000,
			ThicknessNM: 1_600_000,
			Layers: []Layer{
				{ID: "F.Cu", Kind: "copper", Order: 0},
				{ID: "B.Cu", Kind: "copper", Order: 1},
			},
		},
		Rules: Rules{
			MinClearanceNM:          defaultClearance,
			MinTraceWidthNM:         defaultTraceWidth,
			MinEdgeClearanceNM:      defaultEdgeClearance,
			MinDrillNM:              defaultMinDrill,
			MinAnnularRingNM:        defaultAnnularRing,
			MinSolderMaskSliverNM:   defaultMaskSliver,
			MinCopperToHoleNM:       defaultCopperToHole,
			MinCourtyardClearanceNM: defaultCourtyardGap,
		},
		Components:        []Component{},
		Nets:              []Net{},
		Traces:            []Trace{},
		Vias:              []Via{},
		Zones:             []Zone{},
		Keepouts:          []Keepout{},
		DifferentialPairs: []DifferentialPair{},
		NetClasses:        []NetClass{},
	}
}

func normalizeDefinition(raw []byte, fallbackName string) ([]byte, *Definition, string, error) {
	if len(raw) == 0 {
		def := emptyDefinition(fallbackName)
		raw, _ = json.Marshal(def)
	}
	if len(raw) > maxDefinitionBytes {
		return nil, nil, "", fmt.Errorf("definition exceeds %d bytes", maxDefinitionBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var def Definition
	if err := dec.Decode(&def); err != nil {
		return nil, nil, "", fmt.Errorf("decode definition: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, nil, "", err
	}
	if def.Schema == "" {
		def.Schema = pcbSchema
	}
	if def.Schema != pcbSchema {
		return nil, nil, "", fmt.Errorf("unsupported PCB schema %q", def.Schema)
	}
	if strings.TrimSpace(def.Name) == "" {
		def.Name = strings.TrimSpace(fallbackName)
	}
	if len(def.Board.Layers) == 0 {
		def.Board.Layers = emptyDefinition("").Board.Layers
	}
	if def.Board.ThicknessNM == 0 {
		def.Board.ThicknessNM = 1_600_000
	}
	if def.Board.Cutouts == nil {
		def.Board.Cutouts = []BoardCutout{}
	}
	if def.Rules.MinClearanceNM == 0 {
		def.Rules.MinClearanceNM = defaultClearance
	}
	if def.Rules.MinTraceWidthNM == 0 {
		def.Rules.MinTraceWidthNM = defaultTraceWidth
	}
	if def.Rules.MinEdgeClearanceNM == 0 {
		def.Rules.MinEdgeClearanceNM = defaultEdgeClearance
	}
	if def.Rules.MinDrillNM == 0 {
		def.Rules.MinDrillNM = defaultMinDrill
	}
	if def.Rules.MinAnnularRingNM == 0 {
		def.Rules.MinAnnularRingNM = defaultAnnularRing
	}
	if def.Rules.MinSolderMaskSliverNM == 0 {
		def.Rules.MinSolderMaskSliverNM = defaultMaskSliver
	}
	if def.Rules.MinCopperToHoleNM == 0 {
		def.Rules.MinCopperToHoleNM = defaultCopperToHole
	}
	if def.Rules.MinCourtyardClearanceNM == 0 {
		def.Rules.MinCourtyardClearanceNM = defaultCourtyardGap
	}
	if def.Components == nil {
		def.Components = []Component{}
	}
	if def.Nets == nil {
		def.Nets = []Net{}
	}
	if def.Traces == nil {
		def.Traces = []Trace{}
	}
	if def.Vias == nil {
		def.Vias = []Via{}
	}
	if def.Zones == nil {
		def.Zones = []Zone{}
	}
	if def.Keepouts == nil {
		def.Keepouts = []Keepout{}
	}
	if def.DifferentialPairs == nil {
		def.DifferentialPairs = []DifferentialPair{}
	}
	if def.NetClasses == nil {
		def.NetClasses = []NetClass{}
	}
	if def.Wiring != nil {
		if def.Wiring.Schema == "" {
			def.Wiring.Schema = wiringSchema
		}
		if def.Wiring.Canvas.Width == 0 {
			def.Wiring.Canvas.Width = 1200
		}
		if def.Wiring.Canvas.Height == 0 {
			def.Wiring.Canvas.Height = 720
		}
		if def.Wiring.Canvas.Grid == 0 {
			def.Wiring.Canvas.Grid = 10
		}
		if def.Wiring.Parts == nil {
			def.Wiring.Parts = []WiringPart{}
		}
		if def.Wiring.Wires == nil {
			def.Wiring.Wires = []WiringWire{}
		}
		if def.Wiring.Annotations == nil {
			def.Wiring.Annotations = []WiringAnnotation{}
		}
		if def.Wiring.Steps == nil {
			def.Wiring.Steps = []WiringStep{}
		}
	}
	if def.Mechanical != nil {
		if def.Mechanical.Schema == "" {
			def.Mechanical.Schema = mechanicalSchema
		}
		if def.Mechanical.Datum.Origin == "" {
			def.Mechanical.Datum = defaultMechanicalDatum()
		}
		if def.Mechanical.Tolerances.XYNM == 0 {
			def.Mechanical.Tolerances.XYNM = 200_000
		}
		if def.Mechanical.Tolerances.ZNM == 0 {
			def.Mechanical.Tolerances.ZNM = 200_000
		}
		if def.Mechanical.Tolerances.PanelOpeningFitNM == 0 {
			def.Mechanical.Tolerances.PanelOpeningFitNM = 300_000
		}
		if def.Mechanical.Tolerances.FastenerFitNM == 0 {
			def.Mechanical.Tolerances.FastenerFitNM = 150_000
		}
		if def.Mechanical.MountingHoles == nil {
			def.Mechanical.MountingHoles = []MechanicalHole{}
		}
		if def.Mechanical.ComponentEnvelopes == nil {
			def.Mechanical.ComponentEnvelopes = []ComponentEnvelope{}
		}
		if def.Mechanical.PanelOpenings == nil {
			def.Mechanical.PanelOpenings = []PanelOpening{}
		}
		if def.Mechanical.ClearanceZones == nil {
			def.Mechanical.ClearanceZones = []MechanicalClearance{}
		}
	}
	if err := structuralLimits(&def); err != nil {
		return nil, nil, "", err
	}
	canonical, err := json.Marshal(def)
	if err != nil {
		return nil, nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, &def, hex.EncodeToString(sum[:]), nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("definition contains multiple JSON values")
}

func structuralLimits(def *Definition) error {
	if def.Board.WidthNM <= 0 || def.Board.HeightNM <= 0 {
		return errors.New("board width_nm and height_nm must be positive")
	}
	if def.Board.WidthNM > 10_000_000_000 || def.Board.HeightNM > 10_000_000_000 {
		return errors.New("board dimensions exceed 10 metres")
	}
	// Native v1's clearance checker is deliberately simple and deterministic.
	// Keep its accepted graph bounded until the spatial-indexed validator lands.
	if len(def.Board.Layers) > 64 || len(def.Components) > 10_000 || len(def.Nets) > 20_000 || len(def.Traces) > 2_000 || len(def.Vias) > 10_000 || len(def.Zones) > 2_000 || len(def.Keepouts) > 2_000 || len(def.DifferentialPairs) > 1_000 || len(def.NetClasses) > 1_000 {
		return errors.New("definition exceeds native v1 object limits")
	}
	if def.Wiring != nil && (len(def.Wiring.Parts) > 1_000 || len(def.Wiring.Wires) > 5_000 || len(def.Wiring.Steps) > 1_000) {
		return errors.New("wiring definition exceeds native v1 object limits")
	}
	if def.Mechanical != nil && (len(def.Mechanical.MountingHoles) > 1_000 || len(def.Mechanical.ComponentEnvelopes) > 10_000 || len(def.Mechanical.PanelOpenings) > 2_000 || len(def.Mechanical.ClearanceZones) > 2_000) {
		return errors.New("mechanical definition exceeds native v1 object limits")
	}
	if len(def.Board.Outline) > 16_384 {
		return errors.New("board outline exceeds point limit")
	}
	if len(def.Board.Cutouts) > 2_000 {
		return errors.New("board cutouts exceed limit")
	}
	for _, cutout := range def.Board.Cutouts {
		if len(cutout.Polygon) > 16_384 {
			return fmt.Errorf("board cutout %q exceeds point limit", cutout.ID)
		}
	}
	for _, t := range def.Traces {
		if len(t.Points) > 4_096 {
			return fmt.Errorf("trace %q exceeds point limit", t.ID)
		}
	}
	for _, z := range def.Zones {
		if len(z.Polygon) > 16_384 {
			return fmt.Errorf("zone %q exceeds point limit", z.ID)
		}
	}
	for _, k := range def.Keepouts {
		if len(k.Polygon) > 16_384 {
			return fmt.Errorf("keepout %q exceeds point limit", k.ID)
		}
	}
	return nil
}

func decodeDefinition(raw json.RawMessage) (*Definition, error) {
	_, def, _, err := normalizeDefinition(raw, "")
	return def, err
}

func idValid(id string) bool { return nativeIDPattern.MatchString(strings.TrimSpace(id)) }

func semanticDiff(fromID, toID int64, a, b *Definition, fromHash, toHash string) RevisionDiff {
	return RevisionDiff{
		FromRevisionID:    fromID,
		ToRevisionID:      toID,
		SourceChanged:     fromHash != toHash,
		BoardChanged:      !equalJSON(a.Board, b.Board),
		RulesChanged:      !equalJSON(a.Rules, b.Rules),
		Components:        diffByID(a.Components, b.Components, func(v Component) string { return v.ID }),
		Nets:              diffByID(a.Nets, b.Nets, func(v Net) string { return v.ID }),
		Traces:            diffByID(a.Traces, b.Traces, func(v Trace) string { return v.ID }),
		Vias:              diffByID(a.Vias, b.Vias, func(v Via) string { return v.ID }),
		Zones:             diffByID(a.Zones, b.Zones, func(v Zone) string { return v.ID }),
		Keepouts:          diffByID(a.Keepouts, b.Keepouts, func(v Keepout) string { return v.ID }),
		DifferentialPairs: diffByID(a.DifferentialPairs, b.DifferentialPairs, func(v DifferentialPair) string { return v.ID }),
		NetClasses:        diffByID(a.NetClasses, b.NetClasses, func(v NetClass) string { return v.ID }),
		SimulationChanged: !equalJSON(a.Simulation, b.Simulation),
		WiringChanged:     !equalJSON(a.Wiring, b.Wiring),
		MechanicalChanged: !equalJSON(a.Mechanical, b.Mechanical),
	}
}

func equalJSON(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}

func diffByID[T any](a, b []T, id func(T) string) map[string][]string {
	ma, mb := map[string]T{}, map[string]T{}
	for _, v := range a {
		ma[id(v)] = v
	}
	for _, v := range b {
		mb[id(v)] = v
	}
	out := map[string][]string{"added": {}, "removed": {}, "changed": {}}
	for key, v := range ma {
		other, ok := mb[key]
		if !ok {
			out["removed"] = append(out["removed"], key)
		} else if !equalJSON(v, other) {
			out["changed"] = append(out["changed"], key)
		}
	}
	for key := range mb {
		if _, ok := ma[key]; !ok {
			out["added"] = append(out["added"], key)
		}
	}
	for _, vals := range out {
		sort.Strings(vals)
	}
	return out
}
