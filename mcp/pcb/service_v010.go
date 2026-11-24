package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// readinessInputs loads the state the readiness gate needs from outside the
// revision. Failures degrade to "unknown" rather than blocking the report.
func (s *Service) readinessInputs(design *Design, revision *Revision, def *Definition) ReadinessInputs {
	inputs := ReadinessInputs{}
	if summary, err := s.latestSourcing(design.ID, revision.ID); err == nil {
		inputs.Sourcing = summary
	}
	if measurements, err := s.store.ListBringUpMeasurements(s.project, design.ID, revision.ID); err == nil && len(measurements) > 0 {
		inputs.BringUp = evaluateBringUp(design.ID, revision.ID, bringUpPlan(def), measurements)
	}
	return inputs
}

// latestSourcing reads the newest sourcing artifact for a revision.
func (s *Service) latestSourcing(designID, revisionID int64) (*SourcingSummary, error) {
	artifacts, err := s.store.ListArtifacts(s.project, designID, revisionID)
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if artifact.Kind != "sourcing" || artifact.Format != "json" {
			continue
		}
		body, err := os.ReadFile(artifact.LocalPath)
		if err != nil {
			return nil, err
		}
		var summary SourcingSummary
		if err := json.Unmarshal(body, &summary); err != nil {
			return nil, err
		}
		summary.SourceRevision = revisionID
		return &summary, nil
	}
	return nil, errNotFound
}

// BOMSource matches every MPN through the component-data provider (in
// batches the provider accepts), stores a sourcing summary and a priced BOM,
// and returns both.
func (s *Service) BOMSource(app *sdk.AppCtx, designID, revisionID int64, country, currency string, buildQuantity int) (map[string]any, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	bound := app.IntegrationFor("component_data")
	if bound == nil {
		return nil, errors.New("no component-data provider is bound; bind Nexar/Octopart to the component_data role to check stock and price")
	}
	byMPN, missing := bomMPNs(def)
	if len(byMPN) == 0 {
		return nil, errors.New("revision BOM has no manufacturer part numbers")
	}
	mpns := make([]string, 0, len(byMPN))
	for mpn := range byMPN {
		mpns = append(mpns, mpn)
	}
	sort.Strings(mpns)
	tool := bound.ToolFor("components.match_bom")
	responses := []any{}
	for start := 0; start < len(mpns); start += 20 {
		end := start + 20
		if end > len(mpns) {
			end = len(mpns)
		}
		parts := []map[string]any{}
		for _, mpn := range mpns[start:end] {
			parts = append(parts, map[string]any{"mpn": mpn})
		}
		result, err := executeProvider(app, bound, tool, map[string]any{"parts": parts, "country": country, "currency": currency, "authorizedOnly": true})
		if err != nil {
			return nil, err
		}
		responses = append(responses, result)
	}
	summary := summarizeSourcing(bound.AppSlug, responses, def, buildQuantity, currency)
	summary.Country = country
	body, _ := json.MarshalIndent(summary, "", "  ")
	artifact, err := s.persistArtifact(design, revision, "sourcing", "json", "application/json", append(body, '\n'), map[string]any{"schema": sourcingSchema, "provider": bound.AppSlug, "short_parts": summary.ShortParts, "order_total": summary.OrderTotal, "currency": currency})
	if artifact == nil {
		return nil, err
	}
	priced, pricedErr := s.persistArtifact(design, revision, "bom-priced", "csv", "text/csv", renderPricedBOM(summary), map[string]any{"schema": sourcingSchema, "provider": bound.AppSlug})
	if err == nil {
		err = pricedErr
	}
	return map[string]any{"revision_id": revision.ID, "summary": summary, "artifact": artifact, "priced_bom": priced, "missing_mpn_designators": missing}, err
}

func (s *Service) AssemblyGenerate(designID, revisionID int64) (*Artifact, []Check, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	files := assemblyFiles(def)
	checks := verifyPlacementCSV(def, files["assembly/pick-and-place.csv"])
	if len(checks) > 0 {
		return nil, checks, fmt.Errorf("pick-and-place verification failed with %d errors", len(checks))
	}
	body, err := deterministicZip(files)
	if err != nil {
		return nil, nil, err
	}
	artifact, err := s.persistArtifact(design, revision, "assembly", "zip", "application/zip", body, map[string]any{"engine": engineVersion, "placements": len(placementRows(def)), "files": []string{"assembly/bom.csv", "assembly/jlcpcb-bom.csv", "assembly/jlcpcb-cpl.csv", "assembly/pick-and-place.csv"}})
	return artifact, []Check{}, err
}

func (s *Service) FirmwareHeader(designID, revisionID int64) (*Artifact, map[string]int, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	pins := firmwareBoardPins(def)
	if len(pins) == 0 {
		return nil, pins, errors.New("no MCU GPIO pins connect to signal nets; name MCU pins GPIOn/IOn/Dn and add them to nets")
	}
	artifact, err := s.persistArtifact(design, revision, "firmware-header", "h", "text/x-c", renderFirmwareHeader(def), map[string]any{"engine": engineVersion, "pins": len(pins), "board": inferFirmwareBoard(def)})
	return artifact, pins, err
}

func (s *Service) Orders(designID int64) ([]Order, error) {
	if _, err := s.store.GetDesign(s.project, designID); err != nil {
		return nil, err
	}
	return s.store.ListOrders(s.project, designID)
}

// ReleaseOverview gathers what the Release workspace shows: the readiness
// gates, the release package contents, sourcing, bring-up and orders.
func (s *Service) ReleaseOverview(designID, revisionID int64) (map[string]any, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	inputs := s.readinessInputs(design, revision, def)
	readiness := productionReadinessWith(def, inputs)
	bringUp := inputs.BringUp
	if bringUp == nil {
		bringUp = evaluateBringUp(design.ID, revision.ID, bringUpPlan(def), nil)
	}
	orders, err := s.store.ListOrders(s.project, design.ID)
	if err != nil {
		return nil, err
	}
	artifacts, err := s.store.ListArtifacts(s.project, design.ID, revision.ID)
	if err != nil {
		return nil, err
	}
	var release *Artifact
	for i := range artifacts {
		if artifacts[i].Kind == "release" {
			release = &artifacts[i]
			break
		}
	}
	contents := []string{"manifest.json", "source/pcb.json", "validation/report.json", "verification/fabrication.json", "verification/kicad.json", "exchange/board.kicad_pcb", "production/readiness.json", "production/bring-up-plan.txt", "production/bring-up-plan.json", "outputs/board.svg", "outputs/bom.csv", "assembly/pick-and-place.csv", "assembly/bom.csv", "assembly/jlcpcb-cpl.csv", "assembly/jlcpcb-bom.csv"}
	for _, name := range manufacturingFileSummary(def) {
		contents = append(contents, "manufacturing/"+name)
	}
	if def.Mechanical != nil {
		contents = append(contents, "mechanical/assembly.step")
	}
	if len(firmwareBoardPins(def)) > 0 {
		contents = append(contents, "firmware/board_pins.h")
	}
	if def.Firmware != nil && strings.TrimSpace(def.Firmware.Source) != "" {
		contents = append(contents, "firmware/main.ino")
	}
	if def.Wiring != nil {
		contents = append(contents, "wiring/illustration.svg", "wiring/illustration.png", "wiring/tutorial.json")
	}
	sort.Strings(contents)
	profile := map[string]any(nil)
	if p, ok := activeFabProfile(def); ok {
		profile = map[string]any{"id": p.ID, "name": p.Name, "fabricator": p.Fabricator, "reference": p.Reference, "source": p.Source}
	}
	return map[string]any{
		"design_id": design.ID, "revision_id": revision.ID, "revision_number": revision.Number,
		"readiness": readiness, "bring_up": bringUp, "sourcing": inputs.Sourcing, "orders": orders,
		"release_artifact": release, "package_contents": contents, "fab_profile": profile,
		"placements": len(placementRows(def)), "fab_upload_pages": fabricatorUploadPages,
	}, nil
}
