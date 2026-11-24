package main

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const fabricationVerificationSchema = "apteva-pcb-fabrication-verification/v1"

type FabricationFileMetrics struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Bytes        int    `json:"bytes"`
	FileFunction string `json:"file_function,omitempty"`
	Apertures    int    `json:"apertures,omitempty"`
	Draws        int    `json:"draws,omitempty"`
	Arcs         int    `json:"arcs,omitempty"`
	Flashes      int    `json:"flashes,omitempty"`
	Moves        int    `json:"moves,omitempty"`
	Regions      int    `json:"regions,omitempty"`
	DrillTools   int    `json:"drill_tools,omitempty"`
	Holes        int    `json:"holes,omitempty"`
	Slots        int    `json:"slots,omitempty"`
}

type FabricationVerification struct {
	Schema  string                   `json:"schema"`
	Engine  string                   `json:"engine"`
	Status  string                   `json:"status"`
	Errors  int                      `json:"errors"`
	Checks  []Check                  `json:"checks"`
	Files   []FabricationFileMetrics `json:"files"`
	Summary map[string]int           `json:"summary"`
}

var (
	gerberAperturePattern = regexp.MustCompile(`^%ADD([0-9]+)(C|R|O),([0-9.]+)(?:X([0-9.]+))?\*%$`)
	gerberSelectPattern   = regexp.MustCompile(`^D([0-9]+)\*$`)
	gerberCoordPattern    = regexp.MustCompile(`^X(-?[0-9]+)Y(-?[0-9]+)(?:I(-?[0-9]+)J(-?[0-9]+))?D0([123])\*$`)
	gerberFunctionPattern = regexp.MustCompile(`^%TF\.FileFunction,(.+)\*%$`)
	drillToolPattern      = regexp.MustCompile(`^T([0-9]+)C([0-9.]+)$`)
	drillSelectPattern    = regexp.MustCompile(`^T([0-9]+)$`)
	drillCoordPattern     = regexp.MustCompile(`^X(-?[0-9.]+)Y(-?[0-9.]+)$`)
	drillSlotPattern      = regexp.MustCompile(`^X(-?[0-9.]+)Y(-?[0-9.]+)G85X(-?[0-9.]+)Y(-?[0-9.]+)$`)
	drillFunctionPattern  = regexp.MustCompile(`^; #@! TF\.FileFunction,(.+)$`)
)

// fabVerifyExpect overrides expectations that a derived output (a panel)
// draws differently from the native board. EdgeDraws < 0 derives it.
type fabVerifyExpect struct {
	EdgeDraws int
}

func verifyManufacturingFiles(def *Definition, files map[string][]byte) *FabricationVerification {
	return verifyManufacturingFilesWith(def, files, fabVerifyExpect{EdgeDraws: -1})
}

func verifyManufacturingFilesWith(def *Definition, files map[string][]byte, expect fabVerifyExpect) *FabricationVerification {
	report := &FabricationVerification{Schema: fabricationVerificationSchema, Engine: engineVersion, Status: "passed", Checks: []Check{}, Files: []FabricationFileMetrics{}, Summary: map[string]int{}}
	add := func(code, message string, ids ...string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: "error", Message: message, ObjectIDs: ids})
		report.Errors++
		report.Status = "failed"
	}
	required := []string{"board.gbrjob", pthDrillPath, npthDrillPath, "gerbers/Edge_Cuts.gbr"}
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" || layer.Kind == "silkscreen" {
			required = append(required, "gerbers/"+gerberFilename(layer.ID)+".gbr")
		}
	}
	for _, side := range outerSides(def) {
		required = append(required, "gerbers/"+side.Prefix+"_Mask.gbr")
		if len(pastePads(def, side)) > 0 {
			required = append(required, "gerbers/"+side.Prefix+"_Paste.gbr")
		}
	}
	for _, name := range required {
		if len(files[name]) == 0 {
			add("FAB_FILE_MISSING", "Required fabrication file is missing", name)
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	functions := map[string]string{}
	for _, name := range names {
		body := files[name]
		switch {
		case strings.HasSuffix(name, ".gbr"):
			metrics, issues := parseGerberForVerification(name, body, def.Board)
			report.Files = append(report.Files, metrics)
			functions[name] = metrics.FileFunction
			for _, issue := range issues {
				add(issue.Code, issue.Message, name)
			}
			report.Summary["draws"] += metrics.Draws
			report.Summary["arcs"] += metrics.Arcs
			report.Summary["flashes"] += metrics.Flashes
			report.Summary["regions"] += metrics.Regions
		case strings.HasSuffix(name, ".drl"):
			metrics, issues := parseExcellonForVerification(name, body, def.Board)
			report.Files = append(report.Files, metrics)
			functions[name] = metrics.FileFunction
			for _, issue := range issues {
				add(issue.Code, issue.Message, name)
			}
			report.Summary["holes"] += metrics.Holes
			report.Summary["slots"] += metrics.Slots
		}
	}
	if body, ok := files["board.gbrjob"]; ok {
		report.Files = append(report.Files, FabricationFileMetrics{Path: "board.gbrjob", Kind: "gerber-job", Bytes: len(body)})
		for _, issue := range verifyGerberJob(body, files, functions) {
			add(issue.Code, issue.Message, "board.gbrjob")
		}
	}

	// Drill reconciliation: each program must contain exactly the plated or
	// non-plated holes and slots the native geometry declares.
	want := expectedDrills(def)
	for _, program := range []struct {
		path         string
		holes, slots int
		function     string
	}{{pthDrillPath, want.pthHoles, want.pthSlots, "Plated,1,"}, {npthDrillPath, want.npthHoles, want.npthSlots, "NonPlated,1,"}} {
		metrics := fileMetrics(report.Files, program.path)
		if metrics == nil {
			continue
		}
		if metrics.Holes != program.holes || metrics.Slots != program.slots {
			add("FAB_DRILL_RECONCILIATION", fmt.Sprintf("Parsed %d holes and %d slots but native geometry contains %d holes and %d slots", metrics.Holes, metrics.Slots, program.holes, program.slots), program.path)
		}
		if !strings.HasPrefix(metrics.FileFunction, program.function) {
			add("FAB_DRILL_FUNCTION", fmt.Sprintf("Drill program declares file function %q, want %s...", metrics.FileFunction, program.function), program.path)
		}
	}
	expectedEdges := expect.EdgeDraws
	if expectedEdges < 0 {
		expectedEdges = expectedEdgeDrawCount(def)
	}
	if edge := fileMetrics(report.Files, "gerbers/Edge_Cuts.gbr"); edge == nil || edge.Draws != expectedEdges {
		actual := 0
		if edge != nil {
			actual = edge.Draws
		}
		add("FAB_OUTLINE_RECONCILIATION", fmt.Sprintf("Parsed %d outline/cutout segments but native geometry contains %d", actual, expectedEdges), "gerbers/Edge_Cuts.gbr")
	}
	for _, side := range outerSides(def) {
		if metrics := fileMetrics(report.Files, "gerbers/"+side.Prefix+"_Mask.gbr"); metrics != nil {
			if got, wantOpenings := metrics.Flashes+metrics.Regions, maskOpeningCount(def, side); got != wantOpenings {
				add("FAB_MASK_RECONCILIATION", fmt.Sprintf("Parsed %d solder-mask openings but native geometry needs %d", got, wantOpenings), metrics.Path)
			}
		}
		if metrics := fileMetrics(report.Files, "gerbers/"+side.Prefix+"_Paste.gbr"); metrics != nil {
			if got, wantApertures := metrics.Flashes+metrics.Regions, len(pastePads(def, side)); got != wantApertures {
				add("FAB_PASTE_RECONCILIATION", fmt.Sprintf("Parsed %d paste apertures but native geometry has %d SMD pads", got, wantApertures), metrics.Path)
			}
		}
	}
	verifyCopperNumbering(def, functions, add)
	if report.Summary["draws"]+report.Summary["flashes"] == 0 {
		add("FAB_NO_GEOMETRY", "Fabrication package contains no parsed drawing geometry")
	}
	return report
}

// verifyCopperNumbering checks the copper stack is labelled L1 Top .. LN Bot
// with Inr in between, in native layer order.
func verifyCopperNumbering(def *Definition, functions map[string]string, add func(code, message string, ids ...string)) {
	copper := copperLayers(def)
	for index, layer := range copper {
		path := "gerbers/" + gerberFilename(layer.ID) + ".gbr"
		got, ok := functions[path]
		if !ok {
			continue
		}
		position := "Inr"
		if index == 0 {
			position = "Top"
		} else if index == len(copper)-1 {
			position = "Bot"
		}
		if want := fmt.Sprintf("Copper,L%d,%s", index+1, position); got != want {
			add("FAB_COPPER_NUMBERING", fmt.Sprintf("Copper layer %s is labelled %q, want %q", layer.ID, got, want), path)
		}
	}
}

type fabricationParseIssue struct{ Code, Message string }

func parseGerberForVerification(name string, body []byte, board Board) (FabricationFileMetrics, []fabricationParseIssue) {
	metrics := FabricationFileMetrics{Path: name, Kind: "gerber", Bytes: len(body)}
	issues := []fabricationParseIssue{}
	issue := func(code, message string) {
		issues = append(issues, fabricationParseIssue{code, message})
	}
	text := strings.ReplaceAll(string(body), "\r", "")
	if !strings.Contains(text, "%FSLAX46Y46*%") || !strings.Contains(text, "%MOMM*%") {
		issue("FAB_GERBER_HEADER", "Gerber must declare 4.6 coordinate format and millimetre units")
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "M02*") {
		issue("FAB_GERBER_EOF", "Gerber is missing M02 termination")
	}
	apertures := map[string]bool{}
	selected := ""
	mode := ""
	multiQuadrant := false
	inRegion := false
	var current, regionStart Point
	haveCurrent := false
	outside := 0
	checkBounds := func(x, y int64) {
		if x < 0 || y < 0 || x > board.WidthNM || y > board.HeightNM {
			if outside == 0 {
				issue("FAB_GERBER_BOUNDS", fmt.Sprintf("Coordinate %.6f,%.6f mm is outside the native board", float64(x)/1e6, float64(y)/1e6))
			}
			outside++
		}
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch line {
		case "G36*":
			if inRegion {
				issue("FAB_GERBER_REGION", "Gerber opens a region inside another region")
			}
			inRegion = true
			metrics.Regions++
			haveCurrent = false
			continue
		case "G37*":
			if !inRegion {
				issue("FAB_GERBER_REGION", "Gerber closes a region that was never opened")
			} else if haveCurrent && current != regionStart {
				issue("FAB_GERBER_REGION_OPEN", fmt.Sprintf("Region contour does not close at %.6f,%.6f mm", float64(regionStart.XNM)/1e6, float64(regionStart.YNM)/1e6))
			}
			inRegion = false
			continue
		case "G01*", "G02*", "G03*":
			mode = strings.TrimSuffix(line, "*")
			continue
		case "G75*":
			multiQuadrant = true
			continue
		case "G74*":
			multiQuadrant = false
			continue
		}
		if match := gerberFunctionPattern.FindStringSubmatch(line); match != nil {
			metrics.FileFunction = match[1]
			continue
		}
		if match := gerberAperturePattern.FindStringSubmatch(line); match != nil {
			metrics.Apertures++
			apertures[match[1]] = true
			size, _ := strconv.ParseFloat(match[3], 64)
			if size <= 0 {
				issue("FAB_GERBER_APERTURE", "Gerber declares a zero-size aperture D"+match[1])
			}
			continue
		}
		if match := gerberSelectPattern.FindStringSubmatch(line); match != nil {
			selected = match[1]
			if !apertures[selected] {
				issue("FAB_GERBER_APERTURE", "Gerber selects undefined aperture D"+selected)
			}
			continue
		}
		match := gerberCoordPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		x, _ := strconv.ParseInt(match[1], 10, 64)
		y, _ := strconv.ParseInt(match[2], 10, 64)
		end := Point{XNM: x, YNM: y}
		checkBounds(x, y)
		switch match[5] {
		case "2":
			metrics.Moves++
			if inRegion {
				regionStart = end
			}
		case "3":
			metrics.Flashes++
			if inRegion {
				issue("FAB_GERBER_REGION", "Gerber flashes inside a region")
			}
			if selected == "" {
				issue("FAB_GERBER_APERTURE", "Gerber flashes before selecting an aperture")
			}
		case "1":
			metrics.Draws++
			if !haveCurrent {
				issue("FAB_GERBER_MODE", "Gerber draws without a current point")
			}
			if mode == "" {
				issue("FAB_GERBER_MODE", "Gerber draws before setting an interpolation mode")
			}
			if !inRegion && selected == "" {
				issue("FAB_GERBER_APERTURE", "Gerber draws before selecting an aperture")
			}
			if mode == "G02" || mode == "G03" {
				metrics.Arcs++
				if !multiQuadrant {
					issue("FAB_GERBER_ARC_MODE", "Gerber arc drawn without G75 multi-quadrant mode")
				}
				if match[3] == "" {
					issue("FAB_GERBER_ARC", "Gerber arc has no I/J centre offset")
				} else if haveCurrent {
					i, _ := strconv.ParseInt(match[3], 10, 64)
					j, _ := strconv.ParseInt(match[4], 10, 64)
					cx, cy := float64(current.XNM+i), float64(current.YNM+j)
					r0 := math.Hypot(float64(current.XNM)-cx, float64(current.YNM)-cy)
					r1 := math.Hypot(float64(x)-cx, float64(y)-cy)
					if math.Abs(r0-r1) > 2_000 || r0 == 0 {
						issue("FAB_GERBER_ARC", fmt.Sprintf("Arc to %.6f,%.6f mm has inconsistent radius (%.4f vs %.4f mm)", float64(x)/1e6, float64(y)/1e6, r0/1e6, r1/1e6))
					}
				}
			} else if match[3] != "" {
				issue("FAB_GERBER_ARC", "Linear draw carries an arc centre offset")
			}
		}
		current, haveCurrent = end, true
	}
	if inRegion {
		issue("FAB_GERBER_REGION", "Gerber contains an unterminated region")
	}
	if metrics.Apertures == 0 {
		issue("FAB_GERBER_APERTURE", "Gerber declares no aperture")
	}
	if metrics.FileFunction == "" {
		issue("FAB_GERBER_FUNCTION", "Gerber is missing a %TF.FileFunction attribute")
	}
	return metrics, issues
}

func parseExcellonForVerification(name string, body []byte, board Board) (FabricationFileMetrics, []fabricationParseIssue) {
	metrics := FabricationFileMetrics{Path: name, Kind: "excellon", Bytes: len(body)}
	issues := []fabricationParseIssue{}
	text := strings.ReplaceAll(string(body), "\r", "")
	if !strings.HasPrefix(text, "M48\n") || !strings.Contains(text, "METRIC,TZ") {
		issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_HEADER", "Excellon must declare M48 and metric trailing-zero format"})
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "M30") {
		issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_EOF", "Excellon is missing M30 termination"})
	}
	tools := map[string]bool{}
	selected := ""
	inBounds := func(x, y float64) bool {
		return x >= 0 && y >= 0 && x <= float64(board.WidthNM)/1e6 && y <= float64(board.HeightNM)/1e6
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if match := drillFunctionPattern.FindStringSubmatch(line); match != nil {
			metrics.FileFunction = match[1]
			continue
		}
		if match := drillToolPattern.FindStringSubmatch(line); match != nil {
			key := strings.TrimLeft(match[1], "0")
			tools[key] = true
			metrics.DrillTools++
			continue
		}
		if match := drillSelectPattern.FindStringSubmatch(line); match != nil {
			selected = strings.TrimLeft(match[1], "0")
			if selected != "" && !tools[selected] {
				issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_TOOL", "Excellon selects an undefined drill tool"})
			}
			continue
		}
		if match := drillSlotPattern.FindStringSubmatch(line); match != nil {
			metrics.Slots++
			if selected == "" {
				issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_TOOL", "Excellon routes a slot before selecting a tool"})
			}
			x0, _ := strconv.ParseFloat(match[1], 64)
			y0, _ := strconv.ParseFloat(match[2], 64)
			x1, _ := strconv.ParseFloat(match[3], 64)
			y1, _ := strconv.ParseFloat(match[4], 64)
			if !inBounds(x0, y0) || !inBounds(x1, y1) {
				issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_BOUNDS", fmt.Sprintf("Slot %.6f,%.6f-%.6f,%.6f mm is outside the native board", x0, y0, x1, y1)})
			}
			continue
		}
		match := drillCoordPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		metrics.Holes++
		if selected == "" {
			issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_TOOL", "Excellon drills before selecting a tool"})
		}
		x, _ := strconv.ParseFloat(match[1], 64)
		y, _ := strconv.ParseFloat(match[2], 64)
		if !inBounds(x, y) {
			issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_BOUNDS", fmt.Sprintf("Drill coordinate %.6f,%.6f mm is outside the native board", x, y)})
		}
	}
	if metrics.FileFunction == "" {
		issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_FUNCTION", "Excellon is missing a TF.FileFunction header"})
	}
	return metrics, issues
}

func verifyGerberJob(body []byte, files map[string][]byte, functions map[string]string) []fabricationParseIssue {
	var job struct {
		Files []struct {
			Path         string `json:"Path"`
			FileFunction string `json:"FileFunction"`
		} `json:"FilesAttributes"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return []fabricationParseIssue{{"FAB_JOB_JSON", "Gerber job manifest is not valid JSON: " + err.Error()}}
	}
	issues := []fabricationParseIssue{}
	listed := map[string]string{}
	for _, file := range job.Files {
		listed[file.Path] = file.FileFunction
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "board.gbrjob" {
			continue
		}
		function, ok := listed[name]
		if !ok {
			issues = append(issues, fabricationParseIssue{"FAB_JOB_RECONCILIATION", "Gerber job manifest omits " + name})
			continue
		}
		if header, parsed := functions[name]; parsed && header != function {
			issues = append(issues, fabricationParseIssue{"FAB_JOB_FUNCTION", fmt.Sprintf("Gerber job lists %s as %q but the file declares %q", name, function, header)})
		}
	}
	return issues
}

type drillExpectation struct {
	pthHoles, pthSlots, npthHoles, npthSlots int
}

// expectedDrills counts holes and slots straight from the native definition.
func expectedDrills(def *Definition) drillExpectation {
	var out drillExpectation
	count := func(plated, slot bool) {
		switch {
		case plated && slot:
			out.pthSlots++
		case plated:
			out.pthHoles++
		case slot:
			out.npthSlots++
		default:
			out.npthHoles++
		}
	}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			switch {
			case pad.DrillWidthNM > 0 && pad.DrillHeightNM > 0:
				count(!pad.NonPlated, pad.DrillWidthNM != pad.DrillHeightNM)
			case pad.DrillNM > 0:
				count(!pad.NonPlated, false)
			}
		}
	}
	for _, via := range def.Vias {
		if via.DrillNM > 0 {
			count(true, false)
		}
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			if hole.DiameterNM > 0 {
				count(hole.Plated, false)
			}
		}
	}
	for _, cutout := range def.Board.Cutouts {
		if !cutout.Plated {
			continue
		}
		if cutout.Kind == "circle" && cutout.DiameterNM > 0 {
			count(true, false)
		}
		if cutout.Kind == "slot" && min64(cutout.WidthNM, cutout.HeightNM) > 0 {
			count(true, cutout.WidthNM != cutout.HeightNM)
		}
	}
	return out
}

// expectedDrillCount is the total of round holes and slots in both programs.
func expectedDrillCount(def *Definition) int {
	want := expectedDrills(def)
	return want.pthHoles + want.pthSlots + want.npthHoles + want.npthSlots
}

func expectedEdgeDrawCount(def *Definition) int {
	return contourSegmentCount(boardEdgeContours(def.Board))
}

func contourSegmentCount(contours []contour) int {
	count := 0
	for _, c := range contours {
		count += len(c.Segs)
	}
	return count
}

func fileMetrics(files []FabricationFileMetrics, path string) *FabricationFileMetrics {
	for i := range files {
		if files[i].Path == path {
			return &files[i]
		}
	}
	return nil
}
