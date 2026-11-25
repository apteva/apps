package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// I2CBusReport checks addresses across blocks that will share one I2C bus
// (for example boards chained over Qwiic), plus each board's own buses.
type I2CBusReport struct {
	Status         string        `json:"status"` // passed | failed
	Devices        []I2CDevice   `json:"devices"`
	SharedBus      []I2CConflict `json:"shared_bus_conflicts"`
	BoardConflicts []I2CConflict `json:"board_conflicts"`
	FreeAddresses  int           `json:"free_addresses"`
}

func (s *Service) I2CBusCheck(designIDs []int64) (*I2CBusReport, error) {
	if len(designIDs) == 0 || len(designIDs) > 64 {
		return nil, errors.New("design_ids must list 1 to 64 designs")
	}
	report := &I2CBusReport{Devices: []I2CDevice{}, SharedBus: []I2CConflict{}, BoardConflicts: []I2CConflict{}}
	seen := map[int64]bool{}
	used := map[int]bool{}
	for _, id := range designIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		design, _, def, err := s.revision(id, 0)
		if err != nil {
			return nil, fmt.Errorf("design %d: %w", id, err)
		}
		devices := i2cDevices(def)
		for i := range devices {
			devices[i].DesignID, devices[i].DesignName = design.ID, design.Name
			used[devices[i].Address] = true
		}
		report.BoardConflicts = append(report.BoardConflicts, i2cConflicts(devices, func(d I2CDevice) string { return fmt.Sprintf("design %d %s", d.DesignID, boardBusKey(d)) })...)
		report.Devices = append(report.Devices, devices...)
	}
	// On the shared bus every block's devices answer on the same wires, but
	// two parts inside one block on different local buses (behind a mux or a
	// separate controller) do not collide with each other: only count a
	// shared-bus conflict when the devices come from different designs.
	for _, c := range i2cConflicts(report.Devices, func(I2CDevice) string { return "shared" }) {
		designs := map[int64]bool{}
		for _, d := range c.Devices {
			designs[d.DesignID] = true
		}
		if len(designs) > 1 {
			report.SharedBus = append(report.SharedBus, c)
		}
	}
	for a := 0x08; a <= 0x77; a++ {
		if !used[a] {
			report.FreeAddresses++
		}
	}
	report.Status = "passed"
	if len(report.SharedBus)+len(report.BoardConflicts) > 0 {
		report.Status = "failed"
	}
	return report, nil
}

func (s *Service) lcscCatalog(app *sdk.AppCtx) (*lcscCatalog, error) {
	base := ""
	if app != nil {
		base = app.Config().Get("lcsc_catalog_url")
	}
	return newLCSCCatalog(base)
}

// LCSCMatch looks up every placed BOM line in the LCSC catalog. With apply,
// exact MPN matches are written to the components' lcsc field as a new
// revision; value-only matches are never applied automatically.
func (s *Service) LCSCMatch(app *sdk.AppCtx, designID, revisionID, expectedParentID int64, buildQuantity int, apply, overwrite, acceptParametric bool, author string) (map[string]any, error) {
	catalog, err := s.lcscCatalog(app)
	if err != nil {
		return nil, err
	}
	if buildQuantity <= 0 {
		buildQuantity = 5
	}
	if apply {
		if expectedParentID <= 0 {
			return nil, errors.New("apply requires expected_parent_id (the current revision)")
		}
		revisionID = expectedParentID
	}
	_, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	matches := lcscMatchBOM(catalog, def, buildQuantity, overwrite)
	total, unpriced := 0.0, 0
	for _, m := range matches {
		if m.Match != nil && m.UnitUSD > 0 {
			total += m.UnitUSD * float64(len(m.ComponentIDs)*buildQuantity)
		} else if m.Status != "already_set" {
			unpriced++
		}
	}
	out := map[string]any{"revision_id": revision.ID, "build_quantity": buildQuantity, "matches": matches, "parts_total_usd": total, "unpriced_lines": unpriced, "catalog": catalog.base}
	if !apply {
		return out, nil
	}
	parent, base, err := s.parentForWrite(designID, expectedParentID)
	if err != nil {
		return nil, err
	}
	ops := []Operation{}
	for _, m := range matches {
		if m.Status != "matched" || m.Match == nil || !(m.Exact || m.Basis == "compatible_mpn" || acceptParametric && m.Basis == "parametric") {
			continue
		}
		for _, id := range m.ComponentIDs {
			i := findComponent(base.Components, id)
			if i < 0 {
				continue
			}
			c := base.Components[i]
			c.LCSC = m.Match.LCSC
			if m.Basis == "compatible_mpn" {
				c.MPN = m.Match.MPN
			}
			ops = append(ops, Operation{Type: "component.replace", Component: &c})
		}
	}
	if len(ops) == 0 {
		out["applied"] = 0
		return out, nil
	}
	next, _, err := s.commitOperations(designID, parent, base, ops, fmt.Sprintf("Assign LCSC part numbers to %d components", len(ops)), author)
	if err != nil {
		return nil, err
	}
	out["applied"], out["revision"] = len(ops), next
	return out, nil
}

func (s *Service) LCSCSearch(app *sdk.AppCtx, query, pkg string, limit int) ([]LCSCPart, error) {
	catalog, err := s.lcscCatalog(app)
	if err != nil {
		return nil, err
	}
	return catalog.Search(strings.TrimSpace(query), pkg, limit)
}
