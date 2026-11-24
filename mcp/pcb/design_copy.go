package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DerivedFrom records which design a copy or template application came from,
// so later template changes can be pushed to every derived design.
type DerivedFrom struct {
	DesignID   int64    `json:"design_id"`
	RevisionID int64    `json:"revision_id"`
	Include    []string `json:"include"`
}

// Template sections a copy or template application can carry.
//
//	board            outline, size, layers, cutouts, rules, net classes, fab profiles
//	mechanical       mechanical spec (holes, envelopes, panel openings)
//	fixed_components components marked fixed, their owned keepouts, and the
//	                 nets between fixed components
//	components       every component and every net
//	keepouts         all keepouts
//	zones            copper zones
//	routing          traces, vias, differential pairs
//	schematic, firmware, simulation, wiring
var templateSections = []string{"board", "mechanical", "fixed_components", "components", "keepouts", "zones", "routing", "schematic", "firmware", "simulation", "wiring"}

var defaultTemplateSections = []string{"board", "mechanical", "fixed_components"}

func normalizeSections(include []string, fallback []string) ([]string, error) {
	if len(include) == 0 {
		include = fallback
	}
	valid := map[string]bool{}
	for _, s := range templateSections {
		valid[s] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range include {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "all" {
			return append([]string(nil), templateSections...), nil
		}
		if !valid[s] {
			return nil, fmt.Errorf("unknown section %q; use %s or all", raw, strings.Join(templateSections, ", "))
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

func hasSection(sections []string, name string) bool {
	for _, s := range sections {
		if s == name {
			return true
		}
	}
	return false
}

// applyTemplateSections copies the chosen sections of src onto dst. Sections
// replace what dst had; fixed components are upserted by component ID so the
// target's own parts and nets stay in place.
func applyTemplateSections(dst, src *Definition, sections []string) {
	src = cloneDefinition(src)
	if hasSection(sections, "board") {
		dst.Board, dst.Rules, dst.NetClasses = src.Board, src.Rules, src.NetClasses
		dst.FabProfiles, dst.ActiveFabProfileID = src.FabProfiles, src.ActiveFabProfileID
	}
	if hasSection(sections, "mechanical") {
		dst.Mechanical = src.Mechanical
	}
	if hasSection(sections, "components") {
		dst.Components, dst.Nets = src.Components, src.Nets
	} else if hasSection(sections, "fixed_components") {
		fixed := map[string]bool{}
		for _, c := range src.Components {
			if !c.Fixed {
				continue
			}
			fixed[c.ID] = true
			if i := findComponent(dst.Components, c.ID); i >= 0 {
				dst.Components[i] = c
			} else {
				dst.Components = append(dst.Components, c)
			}
		}
		// Owned keepouts move with their fixed component.
		kept := dst.Keepouts[:0:0]
		for _, k := range dst.Keepouts {
			if !fixed[k.OwnerID] {
				kept = append(kept, k)
			}
		}
		for _, k := range src.Keepouts {
			if fixed[k.OwnerID] {
				kept = append(kept, k)
			}
		}
		dst.Keepouts = kept
		// Nets that only join fixed components (e.g. a shared connector's
		// ground pins) come with them; nodes are merged into same-ID nets.
		for _, n := range src.Nets {
			nodes := []Node{}
			for _, node := range n.Nodes {
				if fixed[node.ComponentID] {
					nodes = append(nodes, node)
				}
			}
			if len(nodes) == 0 {
				continue
			}
			i := findNet(dst.Nets, n.ID)
			if i < 0 {
				n.Nodes = nodes
				dst.Nets = append(dst.Nets, n)
				continue
			}
			have := map[string]bool{}
			for _, node := range dst.Nets[i].Nodes {
				have[node.ComponentID+":"+node.PinID] = true
			}
			for _, node := range nodes {
				if !have[node.ComponentID+":"+node.PinID] {
					dst.Nets[i].Nodes = append(dst.Nets[i].Nodes, node)
				}
			}
		}
	}
	if hasSection(sections, "keepouts") {
		dst.Keepouts = src.Keepouts
	}
	if hasSection(sections, "zones") {
		dst.Zones = src.Zones
	}
	if hasSection(sections, "routing") {
		dst.Traces, dst.Vias, dst.DifferentialPairs = src.Traces, src.Vias, src.DifferentialPairs
	}
	if hasSection(sections, "schematic") {
		dst.Schematic = src.Schematic
	}
	if hasSection(sections, "firmware") {
		dst.Firmware = src.Firmware
	}
	if hasSection(sections, "simulation") {
		dst.Simulation = src.Simulation
	}
	if hasSection(sections, "wiring") {
		dst.Wiring = src.Wiring
	}
	if dst.Components == nil {
		dst.Components = []Component{}
	}
	if dst.Nets == nil {
		dst.Nets = []Net{}
	}
	if dst.Traces == nil {
		dst.Traces = []Trace{}
	}
	if dst.Vias == nil {
		dst.Vias = []Via{}
	}
}

// pruneDangling drops nodes, traces, vias and keepouts that reference parts or
// nets the copy did not carry, so a partial copy still validates.
func pruneDangling(def *Definition) {
	components := map[string]bool{}
	for _, c := range def.Components {
		components[c.ID] = true
	}
	nets := def.Nets[:0:0]
	for _, n := range def.Nets {
		nodes := n.Nodes[:0:0]
		for _, node := range n.Nodes {
			if components[node.ComponentID] {
				nodes = append(nodes, node)
			}
		}
		n.Nodes = nodes
		nets = append(nets, n)
	}
	def.Nets = nets
	netIDs := map[string]bool{}
	for _, n := range def.Nets {
		netIDs[n.ID] = true
	}
	traces := def.Traces[:0:0]
	for _, t := range def.Traces {
		if netIDs[t.NetID] {
			traces = append(traces, t)
		}
	}
	def.Traces = traces
	vias := def.Vias[:0:0]
	for _, v := range def.Vias {
		if netIDs[v.NetID] {
			vias = append(vias, v)
		}
	}
	def.Vias = vias
	zones := def.Zones[:0:0]
	for _, z := range def.Zones {
		if netIDs[z.NetID] {
			zones = append(zones, z)
		}
	}
	def.Zones = zones
	keepouts := def.Keepouts[:0:0]
	for _, k := range def.Keepouts {
		if k.OwnerID == "" || components[k.OwnerID] {
			keepouts = append(keepouts, k)
		}
	}
	def.Keepouts = keepouts
	pairs := def.DifferentialPairs[:0:0]
	for _, p := range def.DifferentialPairs {
		if netIDs[p.PositiveNetID] && netIDs[p.NegativeNetID] {
			pairs = append(pairs, p)
		}
	}
	def.DifferentialPairs = pairs
	classes := def.NetClasses[:0:0]
	for _, c := range def.NetClasses {
		ids := c.NetIDs[:0:0]
		for _, id := range c.NetIDs {
			if netIDs[id] {
				ids = append(ids, id)
			}
		}
		c.NetIDs = ids
		classes = append(classes, c)
	}
	def.NetClasses = classes
	if def.Schematic != nil && !hasAllSchematicParts(def) {
		def.Schematic = nil
	}
}

func hasAllSchematicParts(def *Definition) bool {
	body, _ := json.Marshal(def.Schematic)
	components := map[string]bool{}
	for _, c := range def.Components {
		components[c.ID] = true
	}
	var probe struct {
		Symbols []struct {
			ComponentID string `json:"component_id"`
		} `json:"symbols"`
	}
	_ = json.Unmarshal(body, &probe)
	for _, s := range probe.Symbols {
		if s.ComponentID != "" && !components[s.ComponentID] {
			return false
		}
	}
	return true
}

// CopyDesign creates a new design from another design's revision. With the
// default sections (board, mechanical, fixed_components) the copy is a
// template start: shared outline, holes and connector positions, no circuit.
func (s *Service) CopyDesign(sourceID, revisionID int64, name string, include []string, markTemplate bool, note, author string) (*Design, []string, error) {
	_, revision, src, err := s.revision(sourceID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	sections, err := normalizeSections(include, templateSections)
	if err != nil {
		return nil, nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = src.Name + " copy"
	}
	dst := &Definition{Schema: src.Schema, Name: name, Board: src.Board, Rules: src.Rules}
	applyTemplateSections(dst, src, sections)
	pruneDangling(dst)
	dst.Template = markTemplate
	dst.DerivedFrom = &DerivedFrom{DesignID: sourceID, RevisionID: revision.ID, Include: sections}
	canonical, _, hash, err := normalizeDefinition(mustJSON(dst), name)
	if err != nil {
		return nil, nil, fmt.Errorf("copied definition is not valid: %w", err)
	}
	if strings.TrimSpace(note) == "" {
		note = fmt.Sprintf("Copied from design %d revision %d (%s)", sourceID, revision.Number, strings.Join(sections, ", "))
	}
	design, err := s.store.CreateDesign(s.project, name, canonical, mustJSON([]map[string]any{{"type": "design.copy", "source_design_id": sourceID, "source_revision_id": revision.ID, "include": sections}}), hash, note, author)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.design.created", map[string]any{"design_id": design.ID, "revision_id": design.CurrentRevisionID, "name": design.Name, "source_design_id": sourceID})
	}
	return design, sections, err
}

// TemplateApply replaces the chosen sections of a target design with the
// template's, as a new revision on the target.
func (s *Service) TemplateApply(targetID, expectedParentID, templateID, templateRevisionID int64, include []string, note, author string) (*Revision, *ValidationReport, error) {
	if targetID == templateID {
		return nil, nil, errors.New("a design cannot be its own template")
	}
	parent, dst, err := s.parentForWrite(targetID, expectedParentID)
	if err != nil {
		return nil, nil, err
	}
	_, templateRevision, src, err := s.revision(templateID, templateRevisionID)
	if err != nil {
		return nil, nil, err
	}
	sections, err := normalizeSections(include, defaultTemplateSections)
	if err != nil {
		return nil, nil, err
	}
	applyTemplateSections(dst, src, sections)
	dst.DerivedFrom = &DerivedFrom{DesignID: templateID, RevisionID: templateRevision.ID, Include: sections}
	canonical, next, hash, err := normalizeDefinition(mustJSON(dst), dst.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("template result is not valid: %w", err)
	}
	if strings.TrimSpace(note) == "" {
		note = fmt.Sprintf("Apply template design %d revision %d (%s)", templateID, templateRevision.Number, strings.Join(sections, ", "))
	}
	ops := mustJSON([]map[string]any{{"type": "template.apply", "template_design_id": templateID, "template_revision_id": templateRevision.ID, "include": sections}})
	revision, err := s.store.CreateRevision(s.project, targetID, parent.ID, canonical, ops, hash, note, author)
	if err != nil {
		return nil, nil, err
	}
	if s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
	}
	report := validateDefinition(next)
	return revision, &report, nil
}

// TemplateSyncResult is one derived design's outcome in a template sync.
type TemplateSyncResult struct {
	DesignID   int64  `json:"design_id"`
	Name       string `json:"name"`
	Status     string `json:"status"` // updated | current | would_update | failed
	RevisionID int64  `json:"revision_id,omitempty"`
	Errors     int    `json:"validation_errors"`
	Error      string `json:"error,omitempty"`
}

// TemplateSync pushes the template's current revision to every design that
// was copied from it or had it applied, using each design's recorded sections.
func (s *Service) TemplateSync(templateID int64, dryRun bool, author string) ([]TemplateSyncResult, error) {
	_, templateRevision, _, err := s.revision(templateID, 0)
	if err != nil {
		return nil, err
	}
	designs, err := s.store.ListDesigns(s.project, "", "active", 200)
	if err != nil {
		return nil, err
	}
	out := []TemplateSyncResult{}
	for _, d := range designs {
		if d.ID == templateID {
			continue
		}
		_, revision, def, err := s.revision(d.ID, 0)
		if err != nil || def.DerivedFrom == nil || def.DerivedFrom.DesignID != templateID {
			continue
		}
		result := TemplateSyncResult{DesignID: d.ID, Name: d.Name}
		sections := def.DerivedFrom.Include
		if !hasSection(sections, "board") && !hasSection(sections, "mechanical") && !hasSection(sections, "fixed_components") {
			sections = defaultTemplateSections
		}
		// A full copy diverges on purpose; only the template sections sync.
		synced := []string{}
		for _, section := range sections {
			if section == "board" || section == "mechanical" || section == "fixed_components" || section == "keepouts" {
				synced = append(synced, section)
			}
		}
		switch {
		case def.DerivedFrom.RevisionID == templateRevision.ID:
			result.Status = "current"
		case dryRun:
			result.Status = "would_update"
		default:
			next, report, err := s.TemplateApply(d.ID, revision.ID, templateID, templateRevision.ID, synced, "", author)
			if err != nil {
				result.Status, result.Error = "failed", err.Error()
			} else {
				result.Status, result.RevisionID, result.Errors = "updated", next.ID, report.Errors
			}
		}
		out = append(out, result)
	}
	return out, nil
}

// Templates lists designs whose current revision is marked as a template.
func (s *Service) Templates() ([]map[string]any, error) {
	designs, err := s.store.ListDesigns(s.project, "", "active", 200)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, d := range designs {
		_, revision, def, err := s.revision(d.ID, 0)
		if err != nil || !def.Template {
			continue
		}
		fixed := 0
		for _, c := range def.Components {
			if c.Fixed {
				fixed++
			}
		}
		out = append(out, map[string]any{"design_id": d.ID, "name": d.Name, "revision_id": revision.ID, "revision_number": revision.Number, "board_mm": []float64{float64(def.Board.WidthNM) / 1e6, float64(def.Board.HeightNM) / 1e6}, "fixed_components": fixed})
	}
	return out, nil
}
