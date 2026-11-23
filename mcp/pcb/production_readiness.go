package main

import (
	"encoding/json"
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
}

func productionReadiness(def *Definition) *ProductionReadinessReport {
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
	add("PHYSICAL_VALIDATION_PENDING", "warning", "Outputs are ready for a prototype order, but electrical bring-up and environmental testing require manufactured hardware")
	report.Metrics = map[string]int{"components": len(def.Components), "verified_footprints": verifiedFootprints, "mpns": mpns, "model_contracts": modelContracts, "test_points": len(testPoints), "gerber_draws": manufacturing.Summary["draws"], "drilled_holes": manufacturing.Summary["holes"], "kicad_footprints": kicad.Counts["footprints"]}
	if report.Errors > 0 {
		report.Status = "blocked"
	}
	return report
}

func productionReadinessJSON(report *ProductionReadinessReport) []byte {
	body, _ := json.MarshalIndent(report, "", "  ")
	return append(body, '\n')
}

func productionTestPlan(def *Definition) []byte {
	lines := []string{"PCB Studio 0.9 prototype bring-up plan", "", "1. Visual inspection: polarity, connector orientation, solder bridges, antenna keepout.", "2. Resistance check: verify USB_5V and +3V3 are not shorted to GND before power.", "3. Current-limited power: apply 5 V over USB-C at a 100 mA limit, then raise to 500 mA after checking +3V3.", "4. Test points: TP1 USB_5V, TP2 +3V3, TP3 GND, TP4 DHT_DATA.", "5. USB enumeration and firmware upload: confirm ESP32-C3 bootloader and serial output.", "6. Sensor test: compare DHT22 temperature/humidity against a calibrated reference at ambient conditions.", "7. Environmental sweep: log at low/high humidity and temperature; record offsets and failures.", "8. Mechanical fit: verify mounting-hole position, USB-C/JST insertion, DHT airflow, and enclosure clearances.", "", "Physical validation status: pending prototype manufacture."}
	return []byte(strings.Join(lines, "\n") + "\n")
}
