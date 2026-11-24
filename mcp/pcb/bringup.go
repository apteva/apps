package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const bringUpSchema = "apteva-pcb-bringup/v1"

// BringUpCheck is one measurable step of the prototype bring-up plan. Numeric
// checks pass when the value lies in [ExpectedMin, ExpectedMax]; a nil bound
// is open. Kind "inspection" and "firmware" are pass/fail.
type BringUpCheck struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"` // inspection | resistance | voltage | firmware
	NetID       string   `json:"net_id,omitempty"`
	TestPoint   string   `json:"test_point,omitempty"`
	ExpectedMin *float64 `json:"expected_min,omitempty"`
	ExpectedMax *float64 `json:"expected_max,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Required    bool     `json:"required"`
	Instruction string   `json:"instruction"`
}

type BringUpMeasurement struct {
	ID          int64    `json:"id"`
	DesignID    int64    `json:"design_id"`
	RevisionID  int64    `json:"revision_id"`
	BoardSerial string   `json:"board_serial"`
	CheckID     string   `json:"check_id"`
	Value       *float64 `json:"value,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Passed      bool     `json:"passed"`
	Note        string   `json:"note,omitempty"`
	MeasuredBy  string   `json:"measured_by,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

// BringUpInput is what a caller records: Value for numeric checks, Passed
// for pass/fail checks.
type BringUpInput struct {
	CheckID string   `json:"check_id"`
	Value   *float64 `json:"value,omitempty"`
	Passed  *bool    `json:"passed,omitempty"`
	Note    string   `json:"note,omitempty"`
}

type BringUpBoard struct {
	BoardSerial string            `json:"board_serial"`
	Status      string            `json:"status"` // passed | failed | incomplete
	Passed      int               `json:"passed"`
	Failed      int               `json:"failed"`
	Missing     []string          `json:"missing_check_ids"`
	Latest      map[string]string `json:"latest"` // check_id -> passed|failed
}

type BringUpStatus struct {
	Schema     string         `json:"schema"`
	DesignID   int64          `json:"design_id"`
	RevisionID int64          `json:"revision_id"`
	Status     string         `json:"status"` // pending | passed | failed
	Checks     []BringUpCheck `json:"checks"`
	Boards     []BringUpBoard `json:"boards"`
}

var voltageNamePattern = regexp.MustCompile(`(\d+)V(\d+)?`)

// bringUpPlan derives measurable checks from the design: a visual
// inspection, a no-short resistance check and a voltage check on every power
// rail, and a firmware check when firmware is stored on the revision.
func bringUpPlan(def *Definition) []BringUpCheck {
	f := func(v float64) *float64 { return &v }
	checks := []BringUpCheck{{ID: "visual", Title: "Visual inspection", Kind: "inspection", Required: true, Instruction: "Check polarity, connector orientation, solder bridges and keepouts before applying power."}}
	ground := findGroundNet(def)
	testPoints := map[string]string{}
	for _, c := range def.Components {
		if !strings.HasPrefix(strings.ToUpper(c.Designator), "TP") {
			continue
		}
		for _, net := range def.Nets {
			for _, node := range net.Nodes {
				if node.ComponentID == c.ID {
					if _, ok := testPoints[net.ID]; !ok {
						testPoints[net.ID] = c.Designator
					}
				}
			}
		}
	}
	rails := powerRails(def, ground)
	for _, rail := range rails {
		tp := testPoints[rail.NetID]
		where := rail.Name
		if tp != "" {
			where = fmt.Sprintf("%s (%s)", rail.Name, tp)
		}
		checks = append(checks, BringUpCheck{ID: "short-" + rail.NetID, Title: "No short: " + rail.Name + " to GND", Kind: "resistance", NetID: rail.NetID, TestPoint: tp, ExpectedMin: f(100), Unit: "ohm", Required: true, Instruction: fmt.Sprintf("Unpowered, measure resistance from %s to ground. Expect at least 100 ohm.", where)})
	}
	for _, rail := range rails {
		tp := testPoints[rail.NetID]
		where := rail.Name
		if tp != "" {
			where = fmt.Sprintf("%s (%s)", rail.Name, tp)
		}
		lo, hi := roundFloat(rail.Volts*0.95, 4), roundFloat(rail.Volts*1.05, 4)
		checks = append(checks, BringUpCheck{ID: "rail-" + rail.NetID, Title: fmt.Sprintf("%s at %.2f V", rail.Name, rail.Volts), Kind: "voltage", NetID: rail.NetID, TestPoint: tp, ExpectedMin: f(lo), ExpectedMax: f(hi), Unit: "V", Required: true, Instruction: fmt.Sprintf("Powered through a current-limited supply, measure %s. Expect %.3f-%.3f V.", where, lo, hi)})
	}
	if def.Firmware != nil && strings.TrimSpace(def.Firmware.Source) != "" {
		checks = append(checks, BringUpCheck{ID: "firmware", Title: "Firmware upload and serial output", Kind: "firmware", Required: true, Instruction: "Upload the revision's firmware and confirm the serial output matches the simulated run."})
	}
	return checks
}

type powerRail struct {
	NetID, Name string
	Volts       float64
}

// powerRails finds supply nets and their nominal voltage from simulation
// sources, regulator outputs, or the net name (3V3, 5V, VBUS).
func powerRails(def *Definition, ground string) []powerRail {
	nominal := map[string]float64{}
	if def.Simulation != nil {
		for _, source := range def.Simulation.Sources {
			if source.Kind == "dc" && source.Value > 0 {
				nominal[source.NetID] = source.Value
			}
		}
	}
	_, _, regulators, _ := compileSimulationModel(def, nil)
	for _, r := range regulators {
		nominal[r.Output] = r.Voltage
	}
	rails := []powerRail{}
	for _, net := range def.Nets {
		if net.ID == ground {
			continue
		}
		volts, ok := nominal[net.ID]
		if !ok {
			volts, ok = voltageFromName(net.ID + " " + net.Name)
		}
		if !ok || volts <= 0 {
			continue
		}
		rails = append(rails, powerRail{NetID: net.ID, Name: firstNonEmpty(net.Name, net.ID), Volts: volts})
	}
	sort.Slice(rails, func(i, j int) bool { return rails[i].NetID < rails[j].NetID })
	return rails
}

func voltageFromName(name string) (float64, bool) {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "VBUS") || strings.Contains(upper, "USB5V") || strings.Contains(upper, "USB_5V") {
		return 5, true
	}
	if !strings.ContainsAny(upper, "V") || !(strings.Contains(upper, "+") || strings.HasPrefix(upper, "V") || strings.Contains(upper, "_") || strings.Contains(upper, "RAIL") || voltageNamePattern.MatchString(upper)) {
		return 0, false
	}
	match := voltageNamePattern.FindStringSubmatch(upper)
	if match == nil {
		return 0, false
	}
	whole, _ := strconv.Atoi(match[1])
	if whole <= 0 || whole > 48 {
		return 0, false
	}
	volts := float64(whole)
	if match[2] != "" {
		frac, _ := strconv.ParseFloat("0."+match[2], 64)
		volts += frac
	}
	return volts, true
}

// evaluateBringUp computes per-board results from the latest measurement of
// each check, and the overall physical-validation status: passed when at
// least one board passes every required check, failed when a board has a
// failing check and none passes, pending otherwise.
func evaluateBringUp(designID, revisionID int64, checks []BringUpCheck, measurements []BringUpMeasurement) *BringUpStatus {
	status := &BringUpStatus{Schema: bringUpSchema, DesignID: designID, RevisionID: revisionID, Status: "pending", Checks: checks, Boards: []BringUpBoard{}}
	latest := map[string]map[string]BringUpMeasurement{}
	for _, m := range measurements {
		board := latest[m.BoardSerial]
		if board == nil {
			board = map[string]BringUpMeasurement{}
			latest[m.BoardSerial] = board
		}
		if prev, ok := board[m.CheckID]; !ok || m.ID > prev.ID {
			board[m.CheckID] = m
		}
	}
	serials := make([]string, 0, len(latest))
	for serial := range latest {
		serials = append(serials, serial)
	}
	sort.Strings(serials)
	anyPassed, anyFailed := false, false
	for _, serial := range serials {
		board := BringUpBoard{BoardSerial: serial, Missing: []string{}, Latest: map[string]string{}}
		for _, check := range checks {
			m, ok := latest[serial][check.ID]
			if !ok {
				if check.Required {
					board.Missing = append(board.Missing, check.ID)
				}
				continue
			}
			if m.Passed {
				board.Passed++
				board.Latest[check.ID] = "passed"
			} else {
				board.Failed++
				board.Latest[check.ID] = "failed"
			}
		}
		switch {
		case board.Failed > 0:
			board.Status = "failed"
			anyFailed = true
		case len(board.Missing) > 0:
			board.Status = "incomplete"
		default:
			board.Status = "passed"
			anyPassed = true
		}
		status.Boards = append(status.Boards, board)
	}
	switch {
	case anyPassed:
		status.Status = "passed"
	case anyFailed:
		status.Status = "failed"
	}
	return status
}

// judgeMeasurement validates one input against its check.
func judgeMeasurement(check BringUpCheck, input BringUpInput) (bool, error) {
	switch check.Kind {
	case "inspection", "firmware":
		if input.Passed == nil {
			return false, fmt.Errorf("check %q is pass/fail; send passed", check.ID)
		}
		return *input.Passed, nil
	default:
		if input.Value == nil || math.IsNaN(*input.Value) || math.IsInf(*input.Value, 0) {
			return false, fmt.Errorf("check %q needs a numeric value in %s", check.ID, check.Unit)
		}
		v := *input.Value
		if check.ExpectedMin != nil && v < *check.ExpectedMin {
			return false, nil
		}
		if check.ExpectedMax != nil && v > *check.ExpectedMax {
			return false, nil
		}
		return true, nil
	}
}

func bringUpChecksForReadiness(status *BringUpStatus) []Check {
	if status == nil {
		return []Check{{Code: "PHYSICAL_VALIDATION_PENDING", Severity: "warning", Message: "Outputs are ready for a prototype order, but electrical bring-up requires manufactured hardware; record measurements with pcb_bringup_record"}}
	}
	switch status.Status {
	case "passed":
		boards := []string{}
		for _, b := range status.Boards {
			if b.Status == "passed" {
				boards = append(boards, b.BoardSerial)
			}
		}
		return []Check{{Code: "PHYSICAL_VALIDATION_PASSED", Severity: "info", Message: "Bring-up measurements pass on at least one manufactured board", ObjectIDs: boards}}
	case "failed":
		failed := []string{}
		for _, b := range status.Boards {
			for id, result := range b.Latest {
				if result == "failed" {
					failed = append(failed, b.BoardSerial+":"+id)
				}
			}
		}
		sort.Strings(failed)
		return []Check{{Code: "PHYSICAL_VALIDATION_FAILED", Severity: "error", Message: "A manufactured board failed a bring-up check", ObjectIDs: failed}}
	}
	return []Check{{Code: "PHYSICAL_VALIDATION_PENDING", Severity: "warning", Message: "Bring-up is incomplete: no board has passed every required check yet"}}
}

// ---- store ----

func (s *Store) AddBringUpMeasurements(project string, designID, revisionID int64, serial, author string, rows []BringUpMeasurement) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range rows {
		var value any
		if m.Value != nil {
			value = *m.Value
		}
		if _, err := tx.Exec(`INSERT INTO pcb_bringup_measurements(project_id,design_id,revision_id,board_serial,check_id,value,unit,passed,note,measured_by) VALUES(?,?,?,?,?,?,?,?,?,?)`, project, designID, revisionID, serial, m.CheckID, value, m.Unit, boolInt(m.Passed), m.Note, author); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListBringUpMeasurements(project string, designID, revisionID int64) ([]BringUpMeasurement, error) {
	rows, err := s.db.Query(`SELECT id,design_id,revision_id,board_serial,check_id,value,unit,passed,note,measured_by,created_at FROM pcb_bringup_measurements WHERE project_id=? AND design_id=? AND revision_id=? ORDER BY id`, project, designID, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BringUpMeasurement{}
	for rows.Next() {
		var m BringUpMeasurement
		var value sql.NullFloat64
		var passed int
		if err := rows.Scan(&m.ID, &m.DesignID, &m.RevisionID, &m.BoardSerial, &m.CheckID, &value, &m.Unit, &passed, &m.Note, &m.MeasuredBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		if value.Valid {
			v := value.Float64
			m.Value = &v
		}
		m.Passed = passed != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ---- service ----

func (s *Service) BringUpStatus(designID, revisionID int64) (*BringUpStatus, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	measurements, err := s.store.ListBringUpMeasurements(s.project, design.ID, revision.ID)
	if err != nil {
		return nil, err
	}
	return evaluateBringUp(design.ID, revision.ID, bringUpPlan(def), measurements), nil
}

func (s *Service) BringUpRecord(designID, revisionID int64, serial string, inputs []BringUpInput, author string) (*BringUpStatus, []BringUpMeasurement, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" || len(serial) > 120 {
		return nil, nil, errors.New("board_serial is required (up to 120 characters)")
	}
	if len(inputs) == 0 || len(inputs) > 200 {
		return nil, nil, errors.New("measurements must hold 1-200 entries")
	}
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	plan := map[string]BringUpCheck{}
	for _, check := range bringUpPlan(def) {
		plan[check.ID] = check
	}
	rows := make([]BringUpMeasurement, 0, len(inputs))
	for _, input := range inputs {
		check, ok := plan[input.CheckID]
		if !ok {
			return nil, nil, fmt.Errorf("check %q is not in this revision's bring-up plan", input.CheckID)
		}
		passed, err := judgeMeasurement(check, input)
		if err != nil {
			return nil, nil, err
		}
		if len(input.Note) > 2_000 {
			return nil, nil, fmt.Errorf("note for %q exceeds 2000 characters", input.CheckID)
		}
		rows = append(rows, BringUpMeasurement{DesignID: design.ID, RevisionID: revision.ID, BoardSerial: serial, CheckID: check.ID, Value: input.Value, Unit: check.Unit, Passed: passed, Note: strings.TrimSpace(input.Note)})
	}
	if err := s.store.AddBringUpMeasurements(s.project, design.ID, revision.ID, serial, author, rows); err != nil {
		return nil, nil, err
	}
	status, err := s.BringUpStatus(design.ID, revision.ID)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.bringup.recorded", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "board_serial": serial, "measurements": len(rows), "status": status.Status})
	}
	return status, rows, err
}
