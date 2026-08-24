package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const fabricationVerificationSchema = "apteva-pcb-fabrication-verification/v1"

type FabricationFileMetrics struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Bytes      int    `json:"bytes"`
	Apertures  int    `json:"apertures,omitempty"`
	Draws      int    `json:"draws,omitempty"`
	Moves      int    `json:"moves,omitempty"`
	Regions    int    `json:"regions,omitempty"`
	DrillTools int    `json:"drill_tools,omitempty"`
	Holes      int    `json:"holes,omitempty"`
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
	gerberAperturePattern = regexp.MustCompile(`^%ADD([0-9]+)C,([0-9.]+)\*%$`)
	gerberCoordPattern    = regexp.MustCompile(`^X(-?[0-9]+)Y(-?[0-9]+)D0([12])\*$`)
	drillToolPattern      = regexp.MustCompile(`^T([0-9]+)C([0-9.]+)$`)
	drillCoordPattern     = regexp.MustCompile(`^X(-?[0-9.]+)Y(-?[0-9.]+)$`)
)

func verifyManufacturingFiles(def *Definition, files map[string][]byte) *FabricationVerification {
	report := &FabricationVerification{Schema: fabricationVerificationSchema, Engine: engineVersion, Status: "passed", Checks: []Check{}, Files: []FabricationFileMetrics{}, Summary: map[string]int{}}
	add := func(code, message string, ids ...string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: "error", Message: message, ObjectIDs: ids})
		report.Errors++
		report.Status = "failed"
	}
	required := []string{"board.gbrjob", "drill/board.drl", "gerbers/Edge_Cuts.gbr"}
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" || layer.Kind == "silkscreen" {
			required = append(required, "gerbers/"+gerberFilename(layer.ID)+".gbr")
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
	for _, name := range names {
		body := files[name]
		switch {
		case strings.HasSuffix(name, ".gbr"):
			metrics, issues := parseGerberForVerification(name, body, def.Board)
			report.Files = append(report.Files, metrics)
			for _, issue := range issues {
				add(issue.Code, issue.Message, name)
			}
			report.Summary["draws"] += metrics.Draws
			report.Summary["regions"] += metrics.Regions
		case strings.HasSuffix(name, ".drl"):
			metrics, issues := parseExcellonForVerification(name, body, def.Board)
			report.Files = append(report.Files, metrics)
			for _, issue := range issues {
				add(issue.Code, issue.Message, name)
			}
			report.Summary["holes"] += metrics.Holes
		case strings.HasSuffix(name, ".gbrjob"):
			metrics := FabricationFileMetrics{Path: name, Kind: "gerber-job", Bytes: len(body)}
			report.Files = append(report.Files, metrics)
			if issue := verifyGerberJob(body, files); issue != nil {
				add(issue.Code, issue.Message, name)
			}
		}
	}
	expectedHoles := expectedDrillCount(def)
	if report.Summary["holes"] != expectedHoles {
		add("FAB_DRILL_RECONCILIATION", fmt.Sprintf("Parsed %d holes but native geometry contains %d drilled pads/vias", report.Summary["holes"], expectedHoles), "drill/board.drl")
	}
	if edge := fileMetrics(report.Files, "gerbers/Edge_Cuts.gbr"); edge == nil || edge.Draws != 4 {
		add("FAB_OUTLINE_RECONCILIATION", "Parsed board outline must contain exactly four closed rectangular edges", "gerbers/Edge_Cuts.gbr")
	}
	if report.Summary["draws"] == 0 {
		add("FAB_NO_GEOMETRY", "Fabrication package contains no parsed drawing geometry")
	}
	return report
}

type fabricationParseIssue struct{ Code, Message string }

func parseGerberForVerification(name string, body []byte, board Board) (FabricationFileMetrics, []fabricationParseIssue) {
	metrics := FabricationFileMetrics{Path: name, Kind: "gerber", Bytes: len(body)}
	issues := []fabricationParseIssue{}
	text := strings.ReplaceAll(string(body), "\r", "")
	if !strings.Contains(text, "%FSLAX46Y46*%") || !strings.Contains(text, "%MOMM*%") {
		issues = append(issues, fabricationParseIssue{"FAB_GERBER_HEADER", "Gerber must declare 4.6 coordinate format and millimetre units"})
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "M02*") {
		issues = append(issues, fabricationParseIssue{"FAB_GERBER_EOF", "Gerber is missing M02 termination"})
	}
	regionDepth := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch line {
		case "G36*":
			regionDepth++
			metrics.Regions++
		case "G37*":
			regionDepth--
			if regionDepth < 0 {
				issues = append(issues, fabricationParseIssue{"FAB_GERBER_REGION", "Gerber closes a region that was never opened"})
				regionDepth = 0
			}
		}
		if gerberAperturePattern.MatchString(line) {
			metrics.Apertures++
			continue
		}
		match := gerberCoordPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		x, _ := strconv.ParseInt(match[1], 10, 64)
		y, _ := strconv.ParseInt(match[2], 10, 64)
		if match[3] == "1" {
			metrics.Draws++
		} else {
			metrics.Moves++
		}
		if x < 0 || y < 0 || x > board.WidthNM || y > board.HeightNM {
			issues = append(issues, fabricationParseIssue{"FAB_GERBER_BOUNDS", fmt.Sprintf("Coordinate %.6f,%.6f mm is outside the native board", float64(x)/1e6, float64(y)/1e6)})
		}
	}
	if regionDepth != 0 {
		issues = append(issues, fabricationParseIssue{"FAB_GERBER_REGION", "Gerber contains an unterminated region"})
	}
	if metrics.Apertures == 0 {
		issues = append(issues, fabricationParseIssue{"FAB_GERBER_APERTURE", "Gerber declares no circular aperture"})
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
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if match := drillToolPattern.FindStringSubmatch(line); match != nil {
			key := strings.TrimLeft(match[1], "0")
			if key == "" {
				key = "0"
			}
			tools[key] = true
			metrics.DrillTools++
			continue
		}
		if strings.HasPrefix(line, "T") && len(line) > 1 && !strings.Contains(line, "C") {
			selected = strings.TrimLeft(strings.TrimPrefix(line, "T"), "0")
			if selected == "" {
				selected = "0"
			}
			if !tools[selected] {
				issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_TOOL", "Excellon selects an undefined drill tool"})
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
		if x < 0 || y < 0 || x > float64(board.WidthNM)/1e6 || y > float64(board.HeightNM)/1e6 {
			issues = append(issues, fabricationParseIssue{"FAB_EXCELLON_BOUNDS", fmt.Sprintf("Drill coordinate %.6f,%.6f mm is outside the native board", x, y)})
		}
	}
	return metrics, issues
}

func verifyGerberJob(body []byte, files map[string][]byte) *fabricationParseIssue {
	var job struct {
		Files []struct {
			Path string `json:"Path"`
		} `json:"FilesAttributes"`
	}
	if err := json.Unmarshal(body, &job); err != nil {
		return &fabricationParseIssue{"FAB_JOB_JSON", "Gerber job manifest is not valid JSON: " + err.Error()}
	}
	listed := map[string]bool{}
	for _, file := range job.Files {
		listed[file.Path] = true
	}
	for name := range files {
		if name != "board.gbrjob" && !listed[name] {
			return &fabricationParseIssue{"FAB_JOB_RECONCILIATION", "Gerber job manifest omits " + name}
		}
	}
	return nil
}

func expectedDrillCount(def *Definition) int {
	count := len(def.Vias)
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if pad.DrillNM > 0 {
				count++
			}
		}
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
