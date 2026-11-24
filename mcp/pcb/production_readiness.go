package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ProductionReadinessReport struct {
	Schema             string         `json:"schema"`
	Engine             string         `json:"engine"`
	Status             string         `json:"status"`
	Errors             int            `json:"errors"`
	Warnings           int            `json:"warnings"`
	Checks             []Check        `json:"checks"`
	Metrics            map[string]int `json:"metrics"`
	PhysicalValidation string         `json:"physical_validation"`
	NextAction         string         `json:"next_action"`
	Firmware           *ReadinessGate `json:"firmware,omitempty"`
	Sourcing           *ReadinessGate `json:"sourcing,omitempty"`
}

// ReadinessGate summarizes one gate for screens and callers.
type ReadinessGate struct {
	Status string `json:"status"` // passed | failed | pending | skipped
	Detail string `json:"detail,omitempty"`
}

// ReadinessInputs carries the state that lives outside the revision: the
// latest stock check and the bring-up measurements.
type ReadinessInputs struct {
	Sourcing *SourcingSummary
	BringUp  *BringUpStatus
}

func productionReadiness(def *Definition) *ProductionReadinessReport {
	return productionReadinessWith(def, ReadinessInputs{})
}

func productionReadinessWith(def *Definition, inputs ReadinessInputs) *ProductionReadinessReport {
	report := &ProductionReadinessReport{Schema: productionSchema, Engine: engineVersion, Status: "ready_for_prototype", Checks: []Check{}, Metrics: map[string]int{}, PhysicalValidation: "pending", NextAction: "Order a small prototype lot and execute the included power, USB, sensor, and test-point bring-up plan."}
	add := func(code, severity, message string, ids ...string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
		if severity == "error" {
			report.Errors++
		} else if severity == "warning" {
			report.Warnings++
		}
	}
	electrical := validateDefinition(def)
	if electrical.Errors > 0 {
		add("ELECTRICAL_DRC", "error", "Native electrical and geometric DRC failed")
	}
	manufacturing := verifyManufacturingFiles(def, manufacturingFiles(def))
	if manufacturing.Errors > 0 {
		add("FABRICATION_PARSE", "error", "Generated Gerber/Excellon files failed independent parsing")
	}
	kicad := verifyKiCadPCB(def, renderKiCadPCB(def))
	if kicad.Errors > 0 {
		add("KICAD_EXCHANGE", "error", "Generated KiCad exchange failed structural reconciliation")
	}
	if def.Mechanical != nil {
		mechanical := validateMechanical(def)
		if mechanical.Errors > 0 {
			add("MECHANICAL_DRC", "error", "Mechanical assembly and envelope validation failed")
		}
		if err := verifyMechanicalSTEP(def, renderMechanicalSTEP(def)); err != nil {
			add("STEP_ASSEMBLY", "error", err.Error())
		}
	}
	if p, ok := activeFabProfile(def); ok && strings.EqualFold(p.Fabricator, "jlcpcb") {
		for _, c := range jlcAssemblyChecks(def) {
			add(c.Code, c.Severity, c.Message, c.ObjectIDs...)
		}
	}
	verifiedFootprints, modelContracts, mpns, testPoints := 0, 0, 0, map[string]bool{}
	netByNode := map[string]string{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net.ID
		}
	}
	for _, component := range def.Components {
		if component.FootprintSpec != nil && component.FootprintSpec.Verified {
			verifiedFootprints++
		} else {
			add("FOOTPRINT_NOT_FROZEN", "error", "Every populated component needs a verified immutable footprint contract", component.ID)
		}
		if strings.TrimSpace(component.MPN) != "" {
			mpns++
		} else {
			add("MPN_MISSING", "warning", "Component has no manufacturer part number for sourcing", component.ID)
		}
		if component.Model3D != nil && component.Model3D.Verified {
			modelContracts++
		}
		if strings.HasPrefix(strings.ToUpper(component.Designator), "TP") {
			for _, pin := range component.Pins {
				testPoints[netByNode[component.ID+":"+pin.ID]] = true
			}
		}
	}
	requiredFixtureNets := []string{}
	for _, candidate := range []string{"usb5v", "v3v3", "gnd", "dht_data"} {
		for _, net := range def.Nets {
			if net.ID == candidate {
				requiredFixtureNets = append(requiredFixtureNets, candidate)
			}
		}
	}
	for _, netID := range requiredFixtureNets {
		if !testPoints[netID] {
			add("TEST_POINT_MISSING", "error", "Production fixture lacks an accessible test point", netID)
		}
	}
	if len(requiredFixtureNets) == 0 {
		add("TEST_PLAN_GENERIC", "warning", "No standard power/sensor fixture nets were detected; define project-specific test coverage")
	}
	firmwareResult, firmwareChecks := firmwareGate(def)
	report.Firmware = &ReadinessGate{Status: "passed"}
	for _, check := range firmwareChecks {
		add(check.Code, check.Severity, check.Message, check.ObjectIDs...)
		if check.Severity == "error" {
			report.Firmware.Status = "failed"
			report.Firmware.Detail = check.Message
		} else if report.Firmware.Status == "passed" {
			report.Firmware.Status = "pending"
			report.Firmware.Detail = check.Message
		}
	}
	if firmwareResult != nil && report.Firmware.Status == "passed" {
		report.Firmware.Detail = fmt.Sprintf("%d serial lines, %d sensor reads, %d I2C transactions", len(firmwareResult.SerialOutput), firmwareResult.SensorReads, len(firmwareResult.I2CTransactions))
	}
	sourcing := sourcingChecks(def, inputs.Sourcing)
	report.Sourcing = &ReadinessGate{Status: "passed"}
	if len(def.Components) == 0 || mpns == 0 {
		report.Sourcing = &ReadinessGate{Status: "skipped", Detail: "No manufacturer part numbers to source"}
	} else if inputs.Sourcing != nil {
		report.Sourcing.Detail = fmt.Sprintf("%d part numbers checked with %s", len(inputs.Sourcing.Parts), inputs.Sourcing.Provider)
	}
	for _, check := range sourcing {
		add(check.Code, check.Severity, check.Message, check.ObjectIDs...)
		if check.Severity == "error" {
			report.Sourcing.Status = "failed"
			report.Sourcing.Detail = check.Message
		} else if report.Sourcing.Status == "passed" {
			report.Sourcing.Status = "pending"
			report.Sourcing.Detail = check.Message
		}
	}
	for _, check := range bringUpChecksForReadiness(inputs.BringUp) {
		add(check.Code, check.Severity, check.Message, check.ObjectIDs...)
	}
	if inputs.BringUp != nil {
		report.PhysicalValidation = inputs.BringUp.Status
	}
	report.Metrics = map[string]int{"components": len(def.Components), "verified_footprints": verifiedFootprints, "mpns": mpns, "model_contracts": modelContracts, "test_points": len(testPoints), "gerber_draws": manufacturing.Summary["draws"], "drilled_holes": manufacturing.Summary["holes"], "kicad_footprints": kicad.Counts["footprints"]}
	switch {
	case report.Errors > 0:
		report.Status = "blocked"
		report.NextAction = "Fix the blocking checks, then re-run readiness."
	case report.PhysicalValidation == "passed":
		report.Status = "ready_for_production"
		report.NextAction = "Bring-up passed on a manufactured board; the revision can go to a production order."
	}
	return report
}

func productionReadinessJSON(report *ProductionReadinessReport) []byte {
	body, _ := json.MarshalIndent(report, "", "  ")
	return append(body, '\n')
}

func productionTestPlan(def *Definition) []byte {
	lines := []string{fmt.Sprintf("PCB Studio %s prototype bring-up plan for %s", strings.TrimPrefix(engineVersion, "pcb-native/"), def.Name), ""}
	for i, check := range bringUpPlan(def) {
		line := fmt.Sprintf("%d. %s [%s]: %s", i+1, check.Title, check.ID, check.Instruction)
		lines = append(lines, line)
	}
	lines = append(lines, "", "Record each result with pcb_bringup_record (check_id from the brackets) to close the physical validation gate.")
	return []byte(strings.Join(lines, "\n") + "\n")
}
