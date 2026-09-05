package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	pcbMechanicalEnvelopeSchema  = "apteva-mechanical-envelope/v1"
	pcbEnclosureGenerator        = "pcb-enclosure"
	pcbEnclosureGeneratorVersion = "1"
)

type PCBMechanicalResponse struct {
	Envelope   PCBMechanicalEnvelope   `json:"envelope"`
	Validation PCBMechanicalValidation `json:"validation"`
}

type PCBMechanicalValidation struct {
	Status   string `json:"status"`
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
}

type PCBMechanicalEnvelope struct {
	Schema               string                   `json:"schema"`
	SourceApp            string                   `json:"source_app"`
	SourceDesignID       int64                    `json:"source_design_id"`
	SourceRevisionID     int64                    `json:"source_revision_id"`
	SourceRevisionNumber int                      `json:"source_revision_number"`
	SourceSHA256         string                   `json:"source_sha256"`
	Datum                PCBMechanicalDatum       `json:"datum"`
	Tolerances           PCBMechanicalTolerances  `json:"tolerances"`
	Board                PCBMechanicalBoard       `json:"board"`
	MountingHoles        []PCBMechanicalHole      `json:"mounting_holes"`
	Components           []PCBComponentEnvelope   `json:"component_envelopes"`
	PanelOpenings        []PCBPanelOpening        `json:"panel_openings"`
	ClearanceZones       []PCBMechanicalClearance `json:"clearance_zones"`
}

type PCBMechanicalDatum struct {
	Origin     string `json:"origin"`
	Handedness string `json:"handedness"`
	XAxis      string `json:"x_axis"`
	YAxis      string `json:"y_axis"`
	ZAxis      string `json:"z_axis"`
}

type PCBMechanicalTolerances struct {
	XYNM              int64 `json:"xy_nm"`
	ZNM               int64 `json:"z_nm"`
	PanelOpeningFitNM int64 `json:"panel_opening_fit_nm"`
	FastenerFitNM     int64 `json:"fastener_fit_nm"`
}

type PCBPoint struct {
	XNM int64 `json:"x_nm"`
	YNM int64 `json:"y_nm"`
}

type PCBBoardCutout struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	XNM          int64      `json:"x_nm,omitempty"`
	YNM          int64      `json:"y_nm,omitempty"`
	WidthNM      int64      `json:"width_nm,omitempty"`
	HeightNM     int64      `json:"height_nm,omitempty"`
	DiameterNM   int64      `json:"diameter_nm,omitempty"`
	RotationUdeg int64      `json:"rotation_udeg,omitempty"`
	Polygon      []PCBPoint `json:"polygon,omitempty"`
}

type PCBMechanicalBoard struct {
	WidthNM        int64            `json:"width_nm"`
	HeightNM       int64            `json:"height_nm"`
	ThicknessNM    int64            `json:"thickness_nm"`
	CornerRadiusNM int64            `json:"corner_radius_nm,omitempty"`
	Outline        []PCBPoint       `json:"outline"`
	Cutouts        []PCBBoardCutout `json:"cutouts"`
}

type PCBMechanicalHole struct {
	ID              string `json:"id"`
	XNM             int64  `json:"x_nm"`
	YNM             int64  `json:"y_nm"`
	DiameterNM      int64  `json:"diameter_nm"`
	Fastener        string `json:"fastener,omitempty"`
	HeadClearanceNM int64  `json:"head_clearance_nm,omitempty"`
	PositionTolNM   int64  `json:"position_tolerance_nm,omitempty"`
	DiameterTolNM   int64  `json:"diameter_tolerance_nm,omitempty"`
}

type PCBComponentEnvelope struct {
	ID           string `json:"id"`
	ComponentID  string `json:"component_id,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Label        string `json:"label,omitempty"`
	XNM          int64  `json:"x_nm"`
	YNM          int64  `json:"y_nm"`
	ZMinNM       int64  `json:"z_min_nm"`
	ZMaxNM       int64  `json:"z_max_nm"`
	WidthNM      int64  `json:"width_nm"`
	DepthNM      int64  `json:"depth_nm"`
	RotationUdeg int64  `json:"rotation_udeg,omitempty"`
	ToleranceNM  int64  `json:"tolerance_nm,omitempty"`
}

type PCBPanelOpening struct {
	ID          string `json:"id"`
	ComponentID string `json:"component_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Face        string `json:"face"`
	XNM         int64  `json:"x_nm"`
	YNM         int64  `json:"y_nm"`
	ZNM         int64  `json:"z_nm"`
	WidthNM     int64  `json:"width_nm"`
	HeightNM    int64  `json:"height_nm"`
	RadiusNM    int64  `json:"radius_nm,omitempty"`
	DepthNM     int64  `json:"depth_nm,omitempty"`
	ToleranceNM int64  `json:"tolerance_nm,omitempty"`
}

type PCBMechanicalClearance struct {
	ID           string `json:"id"`
	OwnerID      string `json:"owner_id,omitempty"`
	Kind         string `json:"kind"`
	XNM          int64  `json:"x_nm"`
	YNM          int64  `json:"y_nm"`
	ZMinNM       int64  `json:"z_min_nm"`
	ZMaxNM       int64  `json:"z_max_nm"`
	WidthNM      int64  `json:"width_nm"`
	DepthNM      int64  `json:"depth_nm"`
	RotationUdeg int64  `json:"rotation_udeg,omitempty"`
}

type EnclosureOptions struct {
	WallThicknessMM    float64 `json:"wall_thickness_mm"`
	XYClearanceMM      float64 `json:"xy_clearance_mm"`
	FloorThicknessMM   float64 `json:"floor_thickness_mm"`
	LidThicknessMM     float64 `json:"lid_thickness_mm"`
	StandoffHeightMM   float64 `json:"standoff_height_mm"`
	TopClearanceMM     float64 `json:"top_clearance_mm"`
	LidFitMM           float64 `json:"lid_fit_mm"`
	LipDepthMM         float64 `json:"lip_depth_mm"`
	LipThicknessMM     float64 `json:"lip_thickness_mm"`
	OpeningClearanceMM float64 `json:"opening_clearance_mm"`
}

type EnclosureGenerationReport struct {
	Status                   string     `json:"status"`
	OuterSizeMM              [3]float64 `json:"outer_size_mm"`
	BoardSizeMM              [3]float64 `json:"board_size_mm"`
	MountingHoles            int        `json:"mounting_holes"`
	PanelOpenings            int        `json:"panel_openings"`
	GeneratedServiceOpenings int        `json:"generated_service_openings"`
	ClearanceZones           int        `json:"clearance_zones"`
	SourceWarnings           int        `json:"source_warnings"`
}

func defaultEnclosureOptions() EnclosureOptions {
	return EnclosureOptions{
		WallThicknessMM: 2.4, XYClearanceMM: 1.2, FloorThicknessMM: 2,
		LidThicknessMM: 2, StandoffHeightMM: 4, TopClearanceMM: 2,
		LidFitMM: 0.25, LipDepthMM: 1.5, LipThicknessMM: 1.2,
		OpeningClearanceMM: 0.2,
	}
}

func normalizeEnclosureOptions(input EnclosureOptions, envelope PCBMechanicalEnvelope) (EnclosureOptions, error) {
	defaults := defaultEnclosureOptions()
	fill := func(value *float64, fallback float64) {
		if *value == 0 {
			*value = fallback
		}
	}
	fill(&input.WallThicknessMM, defaults.WallThicknessMM)
	fill(&input.XYClearanceMM, defaults.XYClearanceMM)
	fill(&input.FloorThicknessMM, defaults.FloorThicknessMM)
	fill(&input.LidThicknessMM, defaults.LidThicknessMM)
	fill(&input.StandoffHeightMM, defaults.StandoffHeightMM)
	fill(&input.TopClearanceMM, defaults.TopClearanceMM)
	fill(&input.LidFitMM, defaults.LidFitMM)
	fill(&input.LipDepthMM, defaults.LipDepthMM)
	fill(&input.LipThicknessMM, defaults.LipThicknessMM)
	fill(&input.OpeningClearanceMM, defaults.OpeningClearanceMM)
	values := map[string]float64{
		"wall_thickness_mm": input.WallThicknessMM, "xy_clearance_mm": input.XYClearanceMM,
		"floor_thickness_mm": input.FloorThicknessMM, "lid_thickness_mm": input.LidThicknessMM,
		"standoff_height_mm": input.StandoffHeightMM, "top_clearance_mm": input.TopClearanceMM,
		"lid_fit_mm": input.LidFitMM, "lip_depth_mm": input.LipDepthMM,
		"lip_thickness_mm": input.LipThicknessMM, "opening_clearance_mm": input.OpeningClearanceMM,
	}
	for name, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0.05 || value > 100 {
			return input, fmt.Errorf("%s must be between 0.05 and 100 mm", name)
		}
	}
	minZ := float64(0)
	for _, component := range envelope.Components {
		minZ = math.Min(minZ, nm(component.ZMinNM-component.ToleranceNM))
	}
	minimumStandoff := -minZ + nm(envelope.Tolerances.ZNM) + 0.2
	if input.StandoffHeightMM < minimumStandoff {
		input.StandoffHeightMM = minimumStandoff
	}
	if input.LipThicknessMM*2+input.LidFitMM*2 >= nm(envelope.Board.WidthNM) || input.LipThicknessMM*2+input.LidFitMM*2 >= nm(envelope.Board.HeightNM) {
		return input, errors.New("lid lip is too thick for the PCB envelope")
	}
	return input, nil
}

func validatePCBMechanicalResponse(response PCBMechanicalResponse) error {
	envelope := response.Envelope
	if response.Validation.Status == "failed" || response.Validation.Errors > 0 {
		return fmt.Errorf("PCB mechanical validation failed with %d errors", response.Validation.Errors)
	}
	if response.Validation.Status != "passed" && response.Validation.Status != "warning" {
		return errors.New("PCB mechanical validation status is missing or unsupported")
	}
	if envelope.Schema != pcbMechanicalEnvelopeSchema {
		return fmt.Errorf("unsupported PCB mechanical schema %q", envelope.Schema)
	}
	if envelope.SourceApp != "pcb" || envelope.SourceDesignID <= 0 || envelope.SourceRevisionID <= 0 || strings.TrimSpace(envelope.SourceSHA256) == "" {
		return errors.New("PCB mechanical envelope is missing immutable source provenance")
	}
	if envelope.Datum.Origin != "board-outline-lower-left-bottom-face" || envelope.Datum.Handedness != "right" || envelope.Datum.ZAxis != "front-side" {
		return errors.New("PCB mechanical datum is not supported by the enclosure generator")
	}
	if envelope.Board.ThicknessNM <= 0 || len(envelope.Board.Outline) < 3 {
		return errors.New("PCB mechanical envelope needs board thickness and a polygonal outline")
	}
	for _, hole := range envelope.MountingHoles {
		if hole.DiameterNM <= 0 {
			return fmt.Errorf("mounting hole %q has invalid geometry", hole.ID)
		}
	}
	for _, opening := range envelope.PanelOpenings {
		if opening.WidthNM <= 0 || opening.HeightNM <= 0 {
			return fmt.Errorf("panel opening %q has invalid geometry", opening.ID)
		}
		switch opening.Face {
		case "north", "south", "east", "west", "top", "bottom":
		default:
			return fmt.Errorf("panel opening %q has unsupported face %q", opening.ID, opening.Face)
		}
	}
	return nil
}

func generatePCBEnclosure(response PCBMechanicalResponse, input EnclosureOptions) (*DesignDefinition, EnclosureGenerationReport, error) {
	if err := validatePCBMechanicalResponse(response); err != nil {
		return nil, EnclosureGenerationReport{}, err
	}
	envelope := response.Envelope
	options, err := normalizeEnclosureOptions(input, envelope)
	if err != nil {
		return nil, EnclosureGenerationReport{}, err
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, point := range envelope.Board.Outline {
		x, y := nm(point.XNM), nm(point.YNM)
		minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
	}
	boardW, boardD := maxX-minX, maxY-minY
	maxZ := nm(envelope.Board.ThicknessNM)
	for _, component := range envelope.Components {
		maxZ = math.Max(maxZ, nm(component.ZMaxNM+component.ToleranceNM))
	}
	panelOwners := map[string]bool{}
	for _, opening := range envelope.PanelOpenings {
		if opening.ComponentID != "" {
			panelOwners[opening.ComponentID] = true
		}
	}
	for _, zone := range envelope.ClearanceZones {
		switch zone.Kind {
		case "airflow", "thermal", "antenna":
			maxZ = math.Max(maxZ, nm(zone.ZMaxNM)+nm(envelope.Tolerances.ZNM))
		case "cable_insertion":
			if zone.OwnerID != "" && !panelOwners[zone.OwnerID] {
				return nil, EnclosureGenerationReport{}, fmt.Errorf("cable clearance %q has no panel opening for owner %q", zone.ID, zone.OwnerID)
			}
		}
	}
	outerW := boardW + 2*(options.WallThicknessMM+options.XYClearanceMM)
	outerD := boardD + 2*(options.WallThicknessMM+options.XYClearanceMM)
	baseH := options.FloorThicknessMM + options.StandoffHeightMM + maxZ + options.TopClearanceMM
	totalH := baseH + options.LidThicknessMM

	parameters := map[string]ParameterSpec{
		"wall":          parameter(options.WallThicknessMM, 1, 8, "Enclosure wall thickness"),
		"xy_clearance":  parameter(options.XYClearanceMM, .2, 10, "Clearance around the PCB outline"),
		"floor":         parameter(options.FloorThicknessMM, 1, 8, "Base floor thickness"),
		"lid":           parameter(options.LidThicknessMM, 1, 8, "Lid panel thickness"),
		"standoff":      parameter(options.StandoffHeightMM, options.StandoffHeightMM, 30, "PCB height above the enclosure floor"),
		"top_clearance": parameter(options.TopClearanceMM, .5, 30, "Clearance above the tallest component"),
		"lid_fit":       parameter(options.LidFitMM, .1, 1.5, "Per-side lid lip clearance"),
		"lip_depth":     parameter(options.LipDepthMM, .5, 6, "Depth of the locating lip"),
		"lip_thickness": parameter(options.LipThicknessMM, .6, 4, "Thickness of the locating lip"),
	}
	baseHeightExpr := fmt.Sprintf("floor+standoff+%s+top_clearance", number(maxZ))
	outerWidthExpr := fmt.Sprintf("%s+2*(wall+xy_clearance)", number(boardW))
	outerDepthExpr := fmt.Sprintf("%s+2*(wall+xy_clearance)", number(boardD))
	innerWidthExpr := fmt.Sprintf("%s+2*xy_clearance", number(boardW))
	innerDepthExpr := fmt.Sprintf("%s+2*xy_clearance", number(boardD))
	operations := []map[string]any{
		{"id": "base_outer", "type": "box", "size": []any{outerWidthExpr, outerDepthExpr, baseHeightExpr}},
		{"id": "base_cavity", "type": "box", "size": []any{innerWidthExpr, innerDepthExpr, fmt.Sprintf("standoff+%s+top_clearance+1", number(maxZ))}, "origin": []any{"wall", "wall", "floor"}},
		{"id": "base_shell", "type": "cut", "inputs": []string{"base_outer", "base_cavity"}},
	}
	baseInputs := []string{"base_shell"}
	for index, hole := range envelope.MountingHoles {
		bossID := fmt.Sprintf("boss_%d", index+1)
		holeID := fmt.Sprintf("fastener_%d", index+1)
		x := fmt.Sprintf("wall+xy_clearance+%s", number(nm(hole.XNM)-minX))
		y := fmt.Sprintf("wall+xy_clearance+%s", number(nm(hole.YNM)-minY))
		outerRadius := math.Max(nm(hole.HeadClearanceNM)/2, nm(hole.DiameterNM)/2+options.WallThicknessMM)
		if outerRadius <= 0 {
			outerRadius = nm(hole.DiameterNM)/2 + options.WallThicknessMM
		}
		fastenerRadius := nm(hole.DiameterNM)/2 + nm(envelope.Tolerances.FastenerFitNM+hole.DiameterTolNM)/2
		operations = append(operations,
			map[string]any{"id": bossID, "type": "cylinder", "radius": outerRadius, "height": "standoff", "origin": []any{x, y, "floor"}},
			map[string]any{"id": holeID, "type": "cylinder", "radius": fastenerRadius, "height": "floor+standoff+2", "origin": []any{x, y, -1}},
		)
		baseInputs = append(baseInputs, bossID)
	}
	baseSolid := "base_shell"
	if len(baseInputs) > 1 {
		operations = append(operations, map[string]any{"id": "base_with_bosses", "type": "fuse", "inputs": baseInputs})
		baseSolid = "base_with_bosses"
	}
	cutters := []string{}
	topCutters := []string{}
	bottomCutters := []string{}
	for index, opening := range envelope.PanelOpenings {
		fit := nm(envelope.Tolerances.PanelOpeningFitNM+opening.ToleranceNM) + options.OpeningClearanceMM
		width, height := nm(opening.WidthNM)+2*fit, nm(opening.HeightNM)+2*fit
		cutterID := fmt.Sprintf("opening_%d", index+1)
		xCenter := fmt.Sprintf("wall+xy_clearance+%s", number(nm(opening.XNM)-minX))
		yCenter := fmt.Sprintf("wall+xy_clearance+%s", number(nm(opening.YNM)-minY))
		zOrigin := fmt.Sprintf("floor+standoff+%s", number(nm(opening.ZNM)-height/2))
		depth := math.Max(nm(opening.DepthNM)+2, options.WallThicknessMM+options.XYClearanceMM+2)
		var size []any
		var origin []any
		switch opening.Face {
		case "south":
			size, origin = []any{width, depth, height}, []any{fmt.Sprintf("%s-%s", xCenter, number(width/2)), -1, zOrigin}
			cutters = append(cutters, cutterID)
		case "north":
			size, origin = []any{width, depth, height}, []any{fmt.Sprintf("%s-%s", xCenter, number(width/2)), fmt.Sprintf("%s-%s+1", outerDepthExpr, number(depth)), zOrigin}
			cutters = append(cutters, cutterID)
		case "west":
			size, origin = []any{depth, width, height}, []any{-1, fmt.Sprintf("%s-%s", yCenter, number(width/2)), zOrigin}
			cutters = append(cutters, cutterID)
		case "east":
			size, origin = []any{depth, width, height}, []any{fmt.Sprintf("%s-%s+1", outerWidthExpr, number(depth)), fmt.Sprintf("%s-%s", yCenter, number(width/2)), zOrigin}
			cutters = append(cutters, cutterID)
		case "top":
			size, origin = []any{width, height, "lid+lip_depth+2"}, []any{fmt.Sprintf("%s-%s", xCenter, number(width/2)), fmt.Sprintf("%s-%s", yCenter, number(height/2)), fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)}
			topCutters = append(topCutters, cutterID)
		case "bottom":
			size, origin = []any{width, height, "floor+2"}, []any{fmt.Sprintf("%s-%s", xCenter, number(width/2)), fmt.Sprintf("%s-%s", yCenter, number(height/2)), -1}
			bottomCutters = append(bottomCutters, cutterID)
		}
		operations = append(operations, map[string]any{"id": cutterID, "type": "box", "size": size, "origin": origin})
	}
	serviceOpenings := 0
	for _, zone := range envelope.ClearanceZones {
		if zone.Kind != "service" || (zone.OwnerID != "" && panelOwners[zone.OwnerID]) {
			continue
		}
		serviceOpenings++
		cutterID := fmt.Sprintf("service_opening_%d", serviceOpenings)
		width := nm(zone.WidthNM) + 2*(options.OpeningClearanceMM+nm(envelope.Tolerances.XYNM))
		depth := nm(zone.DepthNM) + 2*(options.OpeningClearanceMM+nm(envelope.Tolerances.XYNM))
		xCenter := fmt.Sprintf("wall+xy_clearance+%s", number(nm(zone.XNM)-minX))
		yCenter := fmt.Sprintf("wall+xy_clearance+%s", number(nm(zone.YNM)-minY))
		boxID := cutterID
		operations = append(operations, map[string]any{
			"id": boxID, "type": "box", "size": []any{width, depth, "lid+lip_depth+2"},
			"origin": []any{fmt.Sprintf("%s-%s", xCenter, number(width/2)), fmt.Sprintf("%s-%s", yCenter, number(depth/2)), fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)},
		})
		if zone.RotationUdeg != 0 {
			rotatedID := cutterID + "_rotated"
			operations = append(operations, map[string]any{
				"id": rotatedID, "type": "rotate", "input": boxID, "angle": float64(zone.RotationUdeg) / 1e6,
				"center": []any{xCenter, yCenter, baseHeightExpr}, "direction": []any{0, 0, 1},
			})
			cutterID = rotatedID
		}
		topCutters = append(topCutters, cutterID)
	}
	allBaseCuts := append(append([]string{}, cutters...), bottomCutters...)
	if len(allBaseCuts) > 0 {
		inputs := append([]string{baseSolid}, allBaseCuts...)
		operations = append(operations, map[string]any{"id": "base_finished", "type": "cut", "inputs": inputs})
		baseSolid = "base_finished"
	}
	operations = append(operations,
		map[string]any{"id": "lid_panel", "type": "box", "size": []any{outerWidthExpr, outerDepthExpr, "lid"}, "origin": []any{0, 0, baseHeightExpr}},
		map[string]any{"id": "lip_outer", "type": "box", "size": []any{fmt.Sprintf("%s-2*lid_fit", innerWidthExpr), fmt.Sprintf("%s-2*lid_fit", innerDepthExpr), "lip_depth"}, "origin": []any{"wall+lid_fit", "wall+lid_fit", fmt.Sprintf("%s-lip_depth", baseHeightExpr)}},
		map[string]any{"id": "lip_inner", "type": "box", "size": []any{fmt.Sprintf("%s-2*(lid_fit+lip_thickness)", innerWidthExpr), fmt.Sprintf("%s-2*(lid_fit+lip_thickness)", innerDepthExpr), "lip_depth+2"}, "origin": []any{"wall+lid_fit+lip_thickness", "wall+lid_fit+lip_thickness", fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)}},
		map[string]any{"id": "lid_lip", "type": "cut", "inputs": []string{"lip_outer", "lip_inner"}},
		map[string]any{"id": "lid_joined", "type": "fuse", "inputs": []string{"lid_panel", "lid_lip"}},
	)
	lidSolid := "lid_joined"
	if len(topCutters) > 0 {
		inputs := append([]string{lidSolid}, topCutters...)
		operations = append(operations, map[string]any{"id": "lid_finished", "type": "cut", "inputs": inputs})
		lidSolid = "lid_finished"
	}
	operations = append(operations, map[string]any{"id": "enclosure", "type": "compound", "inputs": []string{baseSolid, lidSolid}})

	definition := &DesignDefinition{
		Schema: designSchema, Units: "mm", Parameters: parameters, Operations: operations, Output: "enclosure",
		Checks: []map[string]any{{"type": "bounding_box", "max": []any{outerW + .01, outerD + .01, totalH + .01}}},
		Provenance: &DesignProvenance{
			Kind: "pcb-mechanical-envelope", SourceSchema: envelope.Schema, SourceApp: envelope.SourceApp,
			SourceDesignID: envelope.SourceDesignID, SourceRevisionID: envelope.SourceRevisionID,
			SourceRevisionNumber: envelope.SourceRevisionNumber, SourceSHA256: envelope.SourceSHA256,
			Generator: pcbEnclosureGenerator, GeneratorVersion: pcbEnclosureGeneratorVersion, Options: options,
		},
	}
	report := EnclosureGenerationReport{
		Status: "ready", OuterSizeMM: [3]float64{outerW, outerD, totalH},
		BoardSizeMM:   [3]float64{boardW, boardD, nm(envelope.Board.ThicknessNM)},
		MountingHoles: len(envelope.MountingHoles), PanelOpenings: len(envelope.PanelOpenings),
		GeneratedServiceOpenings: serviceOpenings, ClearanceZones: len(envelope.ClearanceZones), SourceWarnings: response.Validation.Warnings,
	}
	return definition, report, nil
}

func parameter(value, min, max float64, description string) ParameterSpec {
	return ParameterSpec{Type: "length", Default: value, Min: &min, Max: &max, Description: description}
}

func nm(value int64) float64 { return float64(value) / 1e6 }

func number(value float64) string {
	formatted := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", value), "0"), ".")
	if formatted == "" || formatted == "-" {
		return "0"
	}
	return formatted
}

func enclosureOptionsFromArgs(args map[string]any) (EnclosureOptions, error) {
	value, ok := args["options"]
	if !ok || value == nil {
		return EnclosureOptions{}, nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return EnclosureOptions{}, err
	}
	var options EnclosureOptions
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return EnclosureOptions{}, fmt.Errorf("invalid enclosure options: %w", err)
	}
	return options, nil
}

func fetchPCBMechanicalEnvelope(app *sdk.AppCtx, designID, revisionID int64) (PCBMechanicalResponse, error) {
	if app == nil || app.PlatformAPI() == nil {
		return PCBMechanicalResponse{}, errors.New("PCB source platform client unavailable")
	}
	bound := app.IntegrationFor("pcb_source")
	if bound == nil || bound.Kind != "app" || bound.InstallID <= 0 {
		return PCBMechanicalResponse{}, errors.New("bind PCB Studio to the optional pcb_source role first")
	}
	tool := "pcb_mechanical_get"
	if bound.ToolFor != nil {
		tool = bound.ToolFor("pcb.mechanical.read")
	}
	var response PCBMechanicalResponse
	err := app.PlatformAPI().CallAppResult("pcb", tool, map[string]any{
		"design_id": designID, "revision_id": revisionID, "_project_id": app.CurrentProject(),
	}, &response)
	if err != nil {
		return response, fmt.Errorf("PCB mechanical handoff: %w", err)
	}
	if err := validatePCBMechanicalResponse(response); err != nil {
		return response, err
	}
	return response, nil
}
