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
	pcbEnclosureGeneratorVersion = "3"
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

	// Shape: "box" (bounding box, the v1 behaviour), "rounded" (rounded
	// rectangle; corners follow corner_radius_mm at the PCB outline, default
	// the board's corner radius) or "outline" (walls follow the PCB outline).
	Shape          string  `json:"shape,omitempty"`
	CornerRadiusMM float64 `json:"corner_radius_mm,omitempty"`
	// Lid: "lip" (locating lip, default) or "snap" (lip with snap bumps that
	// click into grooves in the base walls; box and rounded shapes).
	Lid    string  `json:"lid,omitempty"`
	SnapMM float64 `json:"snap_mm,omitempty"`
	// Positions are in PCB board coordinates (mm), the frame of the outline.
	Magnets     []EnclosureMagnet `json:"magnets,omitempty"`
	PogoWindows []EnclosureWindow `json:"pogo_windows,omitempty"`
}

// EnclosureMagnet is a blind pocket for a press-fit magnet. Face picks the
// surface: floor (default for the base) or lid use x_mm/y_mm in board
// coordinates; north/south/east/west put it in a side wall of the base at
// along_mm (board x for north/south, board y for east/west) and z_mm (above
// the PCB's bottom face). From says which side the pocket opens on: inside
// leaves a skin on the outer face; outside (the default for walls) sets the
// magnet flush with the outer face for the strongest pull to a neighbour.
// Every magnet also becomes a mechanical interface (kind "magnet") carrying
// its polarity, so assemblies can check that facing magnets attract.
type EnclosureMagnet struct {
	XMM        float64 `json:"x_mm"`
	YMM        float64 `json:"y_mm"`
	DiameterMM float64 `json:"diameter_mm"`
	DepthMM    float64 `json:"depth_mm"`
	Part       string  `json:"part,omitempty"` // base (default) | lid
	FitMM      float64 `json:"fit_mm,omitempty"`
	Face       string  `json:"face,omitempty"` // floor | lid | north | south | east | west
	AlongMM    float64 `json:"along_mm,omitempty"`
	ZMM        float64 `json:"z_mm,omitempty"`
	From       string  `json:"from,omitempty"`     // inside | outside
	Polarity   string  `json:"polarity,omitempty"` // N | S (the pole facing out)
	ID         string  `json:"id,omitempty"`
}

// EnclosureWindow is a through opening for pogo-pin access, test pads or
// programming headers: in the floor (bottom, default) or lid (top) at
// x_mm/y_mm, or in a side wall (north/south/east/west) at along_mm/z_mm,
// where width_mm runs along the wall and height_mm is vertical.
type EnclosureWindow struct {
	ID             string  `json:"id,omitempty"`
	XMM            float64 `json:"x_mm"`
	YMM            float64 `json:"y_mm"`
	WidthMM        float64 `json:"width_mm"`
	HeightMM       float64 `json:"height_mm"`
	CornerRadiusMM float64 `json:"corner_radius_mm,omitempty"`
	Face           string  `json:"face,omitempty"` // bottom (default) | top | north | south | east | west
	AlongMM        float64 `json:"along_mm,omitempty"`
	ZMM            float64 `json:"z_mm,omitempty"`
}

func isWallFace(face string) bool {
	return face == "north" || face == "south" || face == "east" || face == "west"
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
	Shape                    string     `json:"shape"`
	Lid                      string     `json:"lid"`
	MagnetPockets            int        `json:"magnet_pockets,omitempty"`
	PogoWindows              int        `json:"pogo_windows,omitempty"`
	SnapFeatures             int        `json:"snap_features,omitempty"`
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
	switch input.Shape {
	case "", "box", "rounded", "outline":
	default:
		return input, errors.New("shape must be box, rounded or outline")
	}
	if input.Shape == "" {
		input.Shape = "box"
	}
	if input.Lid == "" {
		input.Lid = "lip"
	}
	switch input.Lid {
	case "lip":
	case "snap":
		if input.Shape == "outline" {
			return input, errors.New("snap lids need a box or rounded shape; use lid=lip with an outline shape")
		}
		if input.SnapMM == 0 {
			input.SnapMM = 0.5
		}
		if input.SnapMM <= input.LidFitMM || input.SnapMM > input.WallThicknessMM-0.8 {
			return input, errors.New("snap_mm must exceed lid_fit_mm and leave at least 0.8 mm of wall")
		}
		if input.LipDepthMM < 2 {
			input.LipDepthMM = 2
		}
	default:
		return input, errors.New("lid must be lip or snap")
	}
	if input.CornerRadiusMM < 0 || input.CornerRadiusMM > 50 {
		return input, errors.New("corner_radius_mm must be between 0 and 50")
	}
	if len(input.Magnets) > 32 || len(input.PogoWindows) > 32 {
		return input, errors.New("at most 32 magnets and 32 pogo windows")
	}
	for index := range input.Magnets {
		magnet := &input.Magnets[index]
		magnet.Polarity = strings.ToUpper(strings.TrimSpace(magnet.Polarity))
		if magnet.Polarity != "" && magnet.Polarity != "N" && magnet.Polarity != "S" {
			return input, fmt.Errorf("magnets[%d].polarity must be N or S", index)
		}
		if magnet.ID != "" && !parameterNameRE.MatchString(magnet.ID) {
			return input, fmt.Errorf("magnets[%d].id must be a simple identifier", index)
		}
		switch magnet.Part {
		case "", "base", "lid":
		default:
			return input, fmt.Errorf("magnets[%d].part must be base or lid", index)
		}
		if magnet.Face == "" {
			magnet.Face = map[bool]string{true: "lid", false: "floor"}[magnet.Part == "lid"]
		}
		switch {
		case magnet.Face == "lid":
			magnet.Part = "lid"
		case magnet.Face == "floor" || isWallFace(magnet.Face):
			if magnet.Part == "lid" {
				return input, fmt.Errorf("magnets[%d]: lid magnets go on face lid; the %s belongs to the base", index, magnet.Face)
			}
			magnet.Part = "base"
		default:
			return input, fmt.Errorf("magnets[%d].face must be floor, lid, north, south, east or west", index)
		}
		thickness, surface := input.FloorThicknessMM, "floor"
		if magnet.Face == "lid" {
			thickness, surface = input.LidThicknessMM, "lid"
		}
		if isWallFace(magnet.Face) {
			thickness, surface = input.WallThicknessMM, magnet.Face+" wall"
			if input.Shape == "outline" {
				return input, fmt.Errorf("magnets[%d]: side-wall magnets need a box or rounded shape", index)
			}
			if magnet.From == "" {
				magnet.From = "outside"
			}
		} else if magnet.From == "" {
			magnet.From = "inside"
		}
		if magnet.From != "inside" && magnet.From != "outside" {
			return input, fmt.Errorf("magnets[%d].from must be inside or outside", index)
		}
		if magnet.DiameterMM <= 0 || magnet.DepthMM <= 0 || magnet.FitMM < 0 || magnet.FitMM > 1 {
			return input, fmt.Errorf("magnets[%d] needs a positive diameter_mm and depth_mm (fit_mm 0-1)", index)
		}
		if magnet.DepthMM > thickness-0.4 {
			return input, fmt.Errorf("magnets[%d] depth %.2f mm leaves less than 0.4 mm of the %.2f mm %s; thicken it or use a thinner magnet", index, magnet.DepthMM, thickness, surface)
		}
	}
	for index, window := range input.PogoWindows {
		if window.WidthMM <= 0 || window.HeightMM <= 0 || window.CornerRadiusMM < 0 || window.CornerRadiusMM*2 > math.Min(window.WidthMM, window.HeightMM) {
			return input, fmt.Errorf("pogo_windows[%d] needs positive width_mm/height_mm and corner_radius_mm up to half the shorter side", index)
		}
		if window.Face != "" && window.Face != "bottom" && window.Face != "top" && !isWallFace(window.Face) {
			return input, fmt.Errorf("pogo_windows[%d].face must be bottom, top, north, south, east or west", index)
		}
		if isWallFace(window.Face) && input.Shape == "outline" {
			return input, fmt.Errorf("pogo_windows[%d]: side-wall windows need a box or rounded shape", index)
		}
		if window.ID != "" && !parameterNameRE.MatchString(window.ID) {
			return input, fmt.Errorf("pogo_windows[%d].id must be a simple identifier", index)
		}
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
	if options.Lid == "snap" {
		parameters["snap"] = parameter(options.SnapMM, .2, 2, "Snap bump protrusion beyond the lip")
	}
	baseHeightExpr := fmt.Sprintf("floor+standoff+%s+top_clearance", number(maxZ))
	outerWidthExpr := fmt.Sprintf("%s+2*(wall+xy_clearance)", number(boardW))
	outerDepthExpr := fmt.Sprintf("%s+2*(wall+xy_clearance)", number(boardD))
	innerWidthExpr := fmt.Sprintf("%s+2*xy_clearance", number(boardW))
	innerDepthExpr := fmt.Sprintf("%s+2*xy_clearance", number(boardD))
	cavityHeightExpr := fmt.Sprintf("standoff+%s+top_clearance+1", number(maxZ))
	boardX := func(x float64) string { return fmt.Sprintf("wall+xy_clearance+%s", number(x-minX)) }
	boardY := func(y float64) string { return fmt.Sprintf("wall+xy_clearance+%s", number(y-minY)) }
	cornerRadius := options.CornerRadiusMM
	if cornerRadius == 0 {
		cornerRadius = nm(envelope.Board.CornerRadiusNM)
	}
	// profile returns the enclosure footprint grown by `grow` (an expression)
	// beyond the PCB outline.
	profile := func(grow string) map[string]any {
		width := fmt.Sprintf("%s+2*(%s)", number(boardW), grow)
		depth := fmt.Sprintf("%s+2*(%s)", number(boardD), grow)
		center := []any{fmt.Sprintf("wall+xy_clearance+%s", number(boardW/2)), fmt.Sprintf("wall+xy_clearance+%s", number(boardD/2))}
		if options.Shape == "outline" {
			points := make([]any, 0, len(envelope.Board.Outline))
			for _, point := range envelope.Board.Outline {
				points = append(points, []any{boardX(nm(point.XNM)), boardY(nm(point.YNM))})
			}
			return map[string]any{"kind": "polygon", "points": points, "offset": grow}
		}
		return map[string]any{"kind": "rounded_rectangle", "width": width, "height": depth, "center": center,
			"radius": fmt.Sprintf("clamp(%s+(%s), 0, min(%s, %s)/2-0.01)", number(cornerRadius), grow, width, depth)}
	}
	operations := []map[string]any{}
	if options.Shape == "box" {
		operations = append(operations,
			map[string]any{"id": "base_outer", "type": "box", "size": []any{outerWidthExpr, outerDepthExpr, baseHeightExpr}},
			map[string]any{"id": "base_cavity", "type": "box", "size": []any{innerWidthExpr, innerDepthExpr, cavityHeightExpr}, "origin": []any{"wall", "wall", "floor"}},
		)
	} else {
		operations = append(operations,
			map[string]any{"id": "base_outer", "type": "extrude_profile", "profile": profile("wall+xy_clearance"), "height": baseHeightExpr},
			map[string]any{"id": "base_cavity", "type": "extrude_profile", "profile": profile("xy_clearance"), "origin": []any{0, 0, "floor"}, "height": cavityHeightExpr},
		)
	}
	operations = append(operations, map[string]any{"id": "base_shell", "type": "cut", "inputs": []string{"base_outer", "base_cavity"}})
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
	// Side-wall features are placed on the flat part of a wall: on a rounded
	// enclosure the corner arcs are excluded.
	flat := 0.0
	if options.Shape == "rounded" {
		flat = math.Max(0, math.Min(cornerRadius+options.WallThicknessMM+options.XYClearanceMM, math.Min(outerW, outerD)/2-0.01))
	}
	wallOffset := options.WallThicknessMM + options.XYClearanceMM
	wallSpan := func(face string, along, z, halfAlong, halfZ float64, label string) (float64, float64, error) {
		center, length := wallOffset+along-minY, outerD
		if face == "north" || face == "south" {
			center, length = wallOffset+along-minX, outerW
		}
		zWorld := options.FloorThicknessMM + options.StandoffHeightMM + z
		if center-halfAlong < flat+0.4 || center+halfAlong > length-flat-0.4 {
			return 0, 0, fmt.Errorf("%s does not fit on the flat part of the %s wall (along_mm puts it %.2f-%.2f mm along a wall whose flat run is %.2f-%.2f mm)", label, face, center-halfAlong, center+halfAlong, flat+0.4, length-flat-0.4)
		}
		if zWorld-halfZ < 0.4 || zWorld+halfZ > baseH-0.4 {
			return 0, 0, fmt.Errorf("%s does not fit in the %s wall height (z_mm puts it %.2f-%.2f mm above the base bottom; the wall is %.2f mm tall)", label, face, zWorld-halfZ, zWorld+halfZ, baseH)
		}
		return center, zWorld, nil
	}
	alongExpr := func(face string, along float64) string {
		if face == "north" || face == "south" {
			return boardX(along)
		}
		return boardY(along)
	}
	zExpr := func(z float64) string { return fmt.Sprintf("floor+standoff+%s", number(z)) }
	// outward normal and a point on the outer face for each wall
	wallPoint := func(face, along, z string, depth string) ([]any, []any) {
		switch face {
		case "west":
			return []any{depth, along, z}, []any{-1, 0, 0}
		case "east":
			return []any{fmt.Sprintf("%s-(%s)", outerWidthExpr, depth), along, z}, []any{1, 0, 0}
		case "south":
			return []any{along, depth, z}, []any{0, -1, 0}
		default:
			return []any{along, fmt.Sprintf("%s-(%s)", outerDepthExpr, depth), z}, []any{0, 1, 0}
		}
	}
	// Lid features are modelled where the lid sits on the base; the lid part
	// is then turned outside-face-down for printing (180° about X), so a lid
	// point (x, y, z) becomes (x, outer_depth - y, base_height + lid - z).
	lidPrintY := func(y string) string { return fmt.Sprintf("%s-(%s)", outerDepthExpr, y) }
	interfaces := []MechanicalInterface{
		{ID: "rim", Kind: "lid_seat", PartID: "base", Position: []any{fmt.Sprintf("(%s)/2", outerWidthExpr), fmt.Sprintf("(%s)/2", outerDepthExpr), baseHeightExpr}, Direction: []any{0, 0, 1}, Up: []any{1, 0, 0}, Notes: "Top of the base walls, where the lid panel rests"},
		{ID: "seat", Kind: "lid_seat", PartID: "lid", Position: []any{fmt.Sprintf("(%s)/2", outerWidthExpr), fmt.Sprintf("(%s)/2", outerDepthExpr), "lid"}, Direction: []any{0, 0, 1}, Up: []any{1, 0, 0}, Notes: "Underside of the lid panel (lid part frame, printed outside face down)"},
	}
	usedIDs := map[string]bool{"rim": true, "seat": true}
	interfaceID := func(given, prefix string, index int) (string, error) {
		id := given
		if id == "" {
			id = fmt.Sprintf("%s_%d", prefix, index+1)
		}
		if usedIDs[id] {
			return "", fmt.Errorf("duplicate enclosure feature id %q", id)
		}
		usedIDs[id] = true
		return id, nil
	}
	for index, window := range options.PogoWindows {
		id := fmt.Sprintf("pogo_window_%d", index+1)
		interfaceName, err := interfaceID(window.ID, "pogo_window", index)
		if err != nil {
			return nil, EnclosureGenerationReport{}, err
		}
		parameters := map[string]any{"width_mm": window.WidthMM, "height_mm": window.HeightMM}
		profile := map[string]any{"kind": "rounded_rectangle", "width": window.WidthMM, "height": window.HeightMM, "radius": window.CornerRadiusMM}
		cut := map[string]any{"id": id, "type": "extrude_profile", "profile": profile}
		switch {
		case isWallFace(window.Face):
			if _, _, err := wallSpan(window.Face, window.AlongMM, window.ZMM, window.WidthMM/2, window.HeightMM/2, fmt.Sprintf("pogo_windows[%d]", index)); err != nil {
				return nil, EnclosureGenerationReport{}, err
			}
			along, z := alongExpr(window.Face, window.AlongMM), zExpr(window.ZMM)
			through := "wall+xy_clearance/2+1"
			profile["center"] = []any{along, z}
			switch window.Face {
			case "south": // XZ sketches extrude toward -Y
				cut["plane"], cut["origin"] = "XZ", []any{0, "wall+xy_clearance/2", 0}
			case "north":
				cut["plane"], cut["origin"] = "XZ", []any{0, fmt.Sprintf("%s+1", outerDepthExpr), 0}
			case "west": // YZ sketches extrude toward +X
				cut["plane"], cut["origin"] = "YZ", []any{-1, 0, 0}
			case "east":
				cut["plane"], cut["origin"] = "YZ", []any{fmt.Sprintf("%s-wall-xy_clearance/2", outerWidthExpr), 0, 0}
			}
			cut["height"] = through
			cutters = append(cutters, id)
			position, direction := wallPoint(window.Face, along, z, "0")
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "pogo_window", PartID: "base", Position: position, Direction: direction, Up: []any{0, 0, 1}, Parameters: parameters})
		case window.Face == "top":
			profile["center"] = []any{boardX(window.XMM), boardY(window.YMM)}
			cut["origin"], cut["height"] = []any{0, 0, fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)}, "lid+lip_depth+2"
			topCutters = append(topCutters, id)
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "pogo_window", PartID: "lid", Position: []any{boardX(window.XMM), lidPrintY(boardY(window.YMM)), 0}, Direction: []any{0, 0, -1}, Up: []any{1, 0, 0}, Parameters: parameters})
		default:
			profile["center"] = []any{boardX(window.XMM), boardY(window.YMM)}
			cut["origin"], cut["height"] = []any{0, 0, -1}, "floor+2"
			bottomCutters = append(bottomCutters, id)
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "pogo_window", PartID: "base", Position: []any{boardX(window.XMM), boardY(window.YMM), 0}, Direction: []any{0, 0, -1}, Up: []any{1, 0, 0}, Parameters: parameters})
		}
		operations = append(operations, cut)
	}
	lidMagnets := []EnclosureMagnet{}
	for index, magnet := range options.Magnets {
		interfaceName, err := interfaceID(magnet.ID, "magnet", index)
		if err != nil {
			return nil, EnclosureGenerationReport{}, err
		}
		parameters := map[string]any{"diameter_mm": magnet.DiameterMM, "depth_mm": magnet.DepthMM, "from": magnet.From}
		if magnet.Polarity != "" {
			parameters["polarity"] = magnet.Polarity
		}
		if magnet.Part == "lid" {
			lidMagnets = append(lidMagnets, magnet)
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "magnet", PartID: "lid", Position: []any{boardX(magnet.XMM), lidPrintY(boardY(magnet.YMM)), 0}, Direction: []any{0, 0, -1}, Up: []any{1, 0, 0}, Parameters: parameters})
			continue
		}
		fit := magnet.FitMM
		if fit == 0 {
			fit = 0.2
		}
		id := fmt.Sprintf("magnet_%d", index+1)
		hole := map[string]any{"id": id, "type": "hole", "input": baseSolid, "diameter": magnet.DiameterMM + fit, "depth": magnet.DepthMM}
		if isWallFace(magnet.Face) {
			radius := (magnet.DiameterMM + fit) / 2
			if _, _, err := wallSpan(magnet.Face, magnet.AlongMM, magnet.ZMM, radius, radius, fmt.Sprintf("magnets[%d]", index)); err != nil {
				return nil, EnclosureGenerationReport{}, err
			}
			along, z := alongExpr(magnet.Face, magnet.AlongMM), zExpr(magnet.ZMM)
			outer, normal := wallPoint(magnet.Face, along, z, "0")
			if magnet.From == "outside" {
				hole["position"], hole["direction"] = outer, []any{-normal[0].(int), -normal[1].(int), -normal[2].(int)}
			} else {
				inner, _ := wallPoint(magnet.Face, along, z, "wall")
				hole["position"], hole["direction"] = inner, normal
			}
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "magnet", PartID: "base", Position: outer, Direction: normal, Up: []any{0, 0, 1}, Parameters: parameters})
		} else {
			position := []any{boardX(magnet.XMM), boardY(magnet.YMM), "floor"}
			direction := []any{0, 0, -1}
			if magnet.From == "outside" {
				position, direction = []any{boardX(magnet.XMM), boardY(magnet.YMM), 0}, []any{0, 0, 1}
			}
			hole["position"], hole["direction"] = position, direction
			interfaces = append(interfaces, MechanicalInterface{ID: interfaceName, Kind: "magnet", PartID: "base", Position: []any{boardX(magnet.XMM), boardY(magnet.YMM), 0}, Direction: []any{0, 0, -1}, Up: []any{1, 0, 0}, Parameters: parameters})
		}
		operations = append(operations, hole)
		baseSolid = id
	}
	snapGrooves, snapBumps := []string{}, []string{}
	if options.Lid == "snap" {
		snapGrooves, snapBumps, operations = snapFeatures(operations, innerWidthExpr, innerDepthExpr, baseHeightExpr)
		cutters = append(cutters, snapGrooves...)
	}
	allBaseCuts := append(append([]string{}, cutters...), bottomCutters...)
	if len(allBaseCuts) > 0 {
		inputs := append([]string{baseSolid}, allBaseCuts...)
		operations = append(operations, map[string]any{"id": "base_finished", "type": "cut", "inputs": inputs})
		baseSolid = "base_finished"
	}
	if options.Shape == "box" {
		operations = append(operations,
			map[string]any{"id": "lid_panel", "type": "box", "size": []any{outerWidthExpr, outerDepthExpr, "lid"}, "origin": []any{0, 0, baseHeightExpr}},
			map[string]any{"id": "lip_outer", "type": "box", "size": []any{fmt.Sprintf("%s-2*lid_fit", innerWidthExpr), fmt.Sprintf("%s-2*lid_fit", innerDepthExpr), "lip_depth"}, "origin": []any{"wall+lid_fit", "wall+lid_fit", fmt.Sprintf("%s-lip_depth", baseHeightExpr)}},
			map[string]any{"id": "lip_inner", "type": "box", "size": []any{fmt.Sprintf("%s-2*(lid_fit+lip_thickness)", innerWidthExpr), fmt.Sprintf("%s-2*(lid_fit+lip_thickness)", innerDepthExpr), "lip_depth+2"}, "origin": []any{"wall+lid_fit+lip_thickness", "wall+lid_fit+lip_thickness", fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)}},
		)
	} else {
		operations = append(operations,
			map[string]any{"id": "lid_panel", "type": "extrude_profile", "profile": profile("wall+xy_clearance"), "origin": []any{0, 0, baseHeightExpr}, "height": "lid"},
			map[string]any{"id": "lip_outer", "type": "extrude_profile", "profile": profile("xy_clearance-lid_fit"), "origin": []any{0, 0, fmt.Sprintf("%s-lip_depth", baseHeightExpr)}, "height": "lip_depth"},
			map[string]any{"id": "lip_inner", "type": "extrude_profile", "profile": profile("xy_clearance-lid_fit-lip_thickness"), "origin": []any{0, 0, fmt.Sprintf("%s-lip_depth-1", baseHeightExpr)}, "height": "lip_depth+2"},
		)
	}
	lidParts := append([]string{"lid_panel", "lid_lip"}, snapBumps...)
	operations = append(operations,
		map[string]any{"id": "lid_lip", "type": "cut", "inputs": []string{"lip_outer", "lip_inner"}},
		map[string]any{"id": "lid_joined", "type": "fuse", "inputs": lidParts},
	)
	lidSolid := "lid_joined"
	for index, magnet := range lidMagnets {
		fit := magnet.FitMM
		if fit == 0 {
			fit = 0.2
		}
		id := fmt.Sprintf("lid_magnet_%d", index+1)
		position, direction := []any{boardX(magnet.XMM), boardY(magnet.YMM), baseHeightExpr}, []any{0, 0, 1}
		if magnet.From == "outside" {
			position, direction = []any{boardX(magnet.XMM), boardY(magnet.YMM), fmt.Sprintf("%s+lid", baseHeightExpr)}, []any{0, 0, -1}
		}
		operations = append(operations, map[string]any{"id": id, "type": "hole", "input": lidSolid,
			"position": position, "direction": direction, "diameter": magnet.DiameterMM + fit, "depth": magnet.DepthMM})
		lidSolid = id
	}
	if len(topCutters) > 0 {
		inputs := append([]string{lidSolid}, topCutters...)
		operations = append(operations, map[string]any{"id": "lid_finished", "type": "cut", "inputs": inputs})
		lidSolid = "lid_finished"
	}
	boardPoints := make([]any, 0, len(envelope.Board.Outline))
	for _, point := range envelope.Board.Outline {
		boardPoints = append(boardPoints, []any{boardX(nm(point.XNM)), boardY(nm(point.YNM))})
	}
	operations = append(operations,
		map[string]any{"id": "enclosure", "type": "compound", "inputs": []string{baseSolid, lidSolid}},
		// The lid part is stored outside face down, the way it prints.
		map[string]any{"id": "lid_print", "type": "rotate", "input": lidSolid, "angle": 180,
			"center": []any{0, fmt.Sprintf("(%s)/2", outerDepthExpr), fmt.Sprintf("(%s+lid)/2", baseHeightExpr)}, "direction": []any{1, 0, 0}},
		map[string]any{"id": "pcb_board", "type": "extrude_profile", "profile": map[string]any{"kind": "polygon", "points": boardPoints},
			"origin": []any{0, 0, "floor+standoff"}, "height": number(nm(envelope.Board.ThicknessNM))},
	)
	parts := []DesignPart{
		{ID: "base", Name: "Base", Output: baseSolid, Manufacturing: &PartManufacturing{Classification: "printed"}},
		{ID: "lid", Name: "Lid", Output: "lid_print", Manufacturing: &PartManufacturing{Classification: "printed"}},
		{ID: "pcb", Name: "PCB", Output: "pcb_board", Color: "#166534", Manufacturing: &PartManufacturing{Classification: "reference"}},
	}
	assembly := &AssemblySpec{
		Name: "enclosure",
		Instances: []AssemblyInstance{
			{ID: "base_1", PartID: "base"},
			{ID: "lid_1", PartID: "lid", Mate: &InstanceMate{Interface: "seat", ToInstance: "base_1", ToInterface: "rim"}},
			{ID: "pcb_1", PartID: "pcb"},
		},
		Interfaces: interfaces,
	}

	definition := &DesignDefinition{
		Schema: designSchema, Units: "mm", Parameters: parameters, Operations: operations, Output: "enclosure",
		Parts: parts, Assembly: assembly,
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
		GeneratedServiceOpenings: serviceOpenings, Shape: options.Shape, Lid: options.Lid,
		MagnetPockets: len(options.Magnets), PogoWindows: len(options.PogoWindows), SnapFeatures: len(snapBumps), ClearanceZones: len(envelope.ClearanceZones), SourceWarnings: response.Validation.Warnings,
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

// snapFeatures adds a snap bump at the middle of each lip side (fused to the
// lid) and a matching groove in the base wall (cut from the base). The bump
// sits 0.6 mm above the lip's lower edge so the lip flexes past the rim.
func snapFeatures(operations []map[string]any, innerWidth, innerDepth, baseHeight string) ([]string, []string, []map[string]any) {
	grooves, bumps := []string{}, []string{}
	bumpZ := fmt.Sprintf("%s-lip_depth+0.6", baseHeight)
	grooveZ := fmt.Sprintf("%s-lip_depth+0.45", baseHeight)
	alongX := fmt.Sprintf("min(15, (%s)/3)", innerWidth)
	alongY := fmt.Sprintf("min(15, (%s)/3)", innerDepth)
	type side struct {
		name             string
		bumpSize, bumpAt []any
		groveSize, grAt  []any
	}
	midX := fmt.Sprintf("wall+(%s)/2-(%s)/2", innerWidth, alongX)
	midY := fmt.Sprintf("wall+(%s)/2-(%s)/2", innerDepth, alongY)
	far := func(inner string) string { return fmt.Sprintf("wall+%s", inner) }
	sides := []side{
		{"west", []any{"snap+0.3", alongY, 0.8}, []any{"wall+lid_fit-snap", midY, bumpZ},
			[]any{"snap-lid_fit+0.15+0.01", alongY, 1.1}, []any{"wall-(snap-lid_fit+0.15)", midY, grooveZ}},
		{"east", []any{"snap+0.3", alongY, 0.8}, []any{fmt.Sprintf("%s-lid_fit-0.3", far(innerWidth)), midY, bumpZ},
			[]any{"snap-lid_fit+0.15+0.01", alongY, 1.1}, []any{fmt.Sprintf("%s-0.01", far(innerWidth)), midY, grooveZ}},
		{"south", []any{alongX, "snap+0.3", 0.8}, []any{midX, "wall+lid_fit-snap", bumpZ},
			[]any{alongX, "snap-lid_fit+0.15+0.01", 1.1}, []any{midX, "wall-(snap-lid_fit+0.15)", grooveZ}},
		{"north", []any{alongX, "snap+0.3", 0.8}, []any{midX, fmt.Sprintf("%s-lid_fit-0.3", far(innerDepth)), bumpZ},
			[]any{alongX, "snap-lid_fit+0.15+0.01", 1.1}, []any{midX, fmt.Sprintf("%s-0.01", far(innerDepth)), grooveZ}},
	}
	for _, item := range sides {
		bump, groove := "snap_bump_"+item.name, "snap_groove_"+item.name
		operations = append(operations,
			map[string]any{"id": bump, "type": "box", "size": item.bumpSize, "origin": item.bumpAt},
			map[string]any{"id": groove, "type": "box", "size": item.groveSize, "origin": item.grAt},
		)
		bumps, grooves = append(bumps, bump), append(grooves, groove)
	}
	return grooves, bumps, operations
}
