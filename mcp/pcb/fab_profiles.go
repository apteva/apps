package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// FabProfile is a named set of fabrication rules. When a design selects one
// with active_fab_profile_id, every non-zero rule in the profile tightens the
// design-wide Rules before validation and routing. Net classes still apply on
// top, so a net class can ask for wider traces than the fab minimum but never
// narrower ones (the validator reports those).
type FabProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fabricator  string `json:"fabricator,omitempty"`
	Process     string `json:"process,omitempty"`
	MaxLayers   int    `json:"max_layers,omitempty"`
	Rules       Rules  `json:"rules"`
	Source      string `json:"source,omitempty"`
	Reference   bool   `json:"reference,omitempty"`
	Description string `json:"description,omitempty"`
	// RotationOffsets correct footprint orientation for this assembler.
	RotationOffsets []RotationOffset `json:"rotation_offsets,omitempty"`
}

const maxFabProfiles = 32

// builtinFabProfiles are reference values taken from each fab's published
// standard-process capability pages. They are marked reference: true because
// fabs change their limits; confirm against the fab before ordering.
func builtinFabProfiles() []FabProfile {
	mm := func(v float64) int64 { return int64(v * 1e6) }
	return []FabProfile{
		{ID: "jlcpcb-standard-2l", Name: "JLCPCB standard 1-2 layer", Fabricator: "jlcpcb", Process: "standard", MaxLayers: 2, Reference: true, Source: "https://jlcpcb.com/capabilities/pcb-capabilities",
			Rules: Rules{MinTraceWidthNM: mm(0.127), MinClearanceNM: mm(0.127), MinDrillNM: mm(0.3), MinAnnularRingNM: mm(0.13), MinEdgeClearanceNM: mm(0.3), MinCopperToHoleNM: mm(0.2), MinSolderMaskSliverNM: mm(0.1)}},
		{ID: "jlcpcb-standard-4l", Name: "JLCPCB standard 4-6 layer", Fabricator: "jlcpcb", Process: "standard", MaxLayers: 6, Reference: true, Source: "https://jlcpcb.com/capabilities/pcb-capabilities",
			Rules: Rules{MinTraceWidthNM: mm(0.09), MinClearanceNM: mm(0.09), MinDrillNM: mm(0.2), MinAnnularRingNM: mm(0.1), MinEdgeClearanceNM: mm(0.3), MinCopperToHoleNM: mm(0.2), MinSolderMaskSliverNM: mm(0.1)}},
		{ID: "pcbway-standard", Name: "PCBWay standard", Fabricator: "pcbway", Process: "standard", MaxLayers: 14, Reference: true, Source: "https://www.pcbway.com/capabilities.html",
			Rules: Rules{MinTraceWidthNM: mm(0.1), MinClearanceNM: mm(0.1), MinDrillNM: mm(0.2), MinAnnularRingNM: mm(0.15), MinEdgeClearanceNM: mm(0.3), MinCopperToHoleNM: mm(0.2), MinSolderMaskSliverNM: mm(0.1)}},
		{ID: "oshpark-2l", Name: "OSH Park 2 layer", Fabricator: "oshpark", Process: "2-layer", MaxLayers: 2, Reference: true, Source: "https://docs.oshpark.com/services/two-layer/",
			Rules: Rules{MinTraceWidthNM: mm(0.1524), MinClearanceNM: mm(0.1524), MinDrillNM: mm(0.254), MinAnnularRingNM: mm(0.127), MinEdgeClearanceNM: mm(0.381), MinCopperToHoleNM: mm(0.2), MinSolderMaskSliverNM: mm(0.1)}},
	}
}

func builtinFabProfile(id string) (FabProfile, bool) {
	for _, profile := range builtinFabProfiles() {
		if profile.ID == id {
			return profile, true
		}
	}
	return FabProfile{}, false
}

// activeFabProfile returns the profile a design selected, looking first in the
// design's own profiles and then in the built-in library.
func activeFabProfile(def *Definition) (*FabProfile, bool) {
	id := strings.TrimSpace(def.ActiveFabProfileID)
	if id == "" {
		return nil, false
	}
	for i := range def.FabProfiles {
		if def.FabProfiles[i].ID == id {
			return &def.FabProfiles[i], true
		}
	}
	if profile, ok := builtinFabProfile(id); ok {
		return &profile, true
	}
	return nil, false
}

// effectiveRules merges the active profile over the design rules. A profile
// value is used whenever it is stricter than the design value, so selecting
// a fab never loosens a rule the designer set deliberately.
func effectiveRules(def *Definition) Rules {
	rules := def.Rules
	profile, ok := activeFabProfile(def)
	if !ok {
		return rules
	}
	tighten := func(design *int64, fab int64) {
		if fab > *design {
			*design = fab
		}
	}
	tighten(&rules.MinClearanceNM, profile.Rules.MinClearanceNM)
	tighten(&rules.MinTraceWidthNM, profile.Rules.MinTraceWidthNM)
	tighten(&rules.MinEdgeClearanceNM, profile.Rules.MinEdgeClearanceNM)
	tighten(&rules.MinDrillNM, profile.Rules.MinDrillNM)
	tighten(&rules.MinAnnularRingNM, profile.Rules.MinAnnularRingNM)
	tighten(&rules.MinSolderMaskSliverNM, profile.Rules.MinSolderMaskSliverNM)
	tighten(&rules.MinCopperToHoleNM, profile.Rules.MinCopperToHoleNM)
	tighten(&rules.MinCourtyardClearanceNM, profile.Rules.MinCourtyardClearanceNM)
	return rules
}

// withFabProfile returns a shallow copy of def whose Rules are the effective
// rules, for validators and the router to consume unchanged.
func withFabProfile(def *Definition) *Definition {
	if def == nil || strings.TrimSpace(def.ActiveFabProfileID) == "" {
		return def
	}
	copy := *def
	copy.Rules = effectiveRules(def)
	return &copy
}

// fabProfileChecks reports profile selection problems and process limits the
// rule values alone cannot express (layer count).
func fabProfileChecks(def *Definition) []Check {
	id := strings.TrimSpace(def.ActiveFabProfileID)
	if id == "" {
		return nil
	}
	profile, ok := activeFabProfile(def)
	if !ok {
		return []Check{{Code: "FAB_PROFILE_UNKNOWN", Severity: "error", Message: fmt.Sprintf("Active fab profile %q is neither in the design nor in the built-in library", id)}}
	}
	checks := []Check{}
	if profile.MaxLayers > 0 && copperLayerCount(def) > profile.MaxLayers {
		checks = append(checks, Check{Code: "FAB_PROFILE_LAYERS", Severity: "error", Message: fmt.Sprintf("Board has %d copper layers; %s supports %d", copperLayerCount(def), profile.Name, profile.MaxLayers), ObjectIDs: []string{profile.ID}})
	}
	for _, class := range def.NetClasses {
		if class.TraceWidthNM > 0 && class.TraceWidthNM < profile.Rules.MinTraceWidthNM {
			checks = append(checks, Check{Code: "FAB_PROFILE_NET_CLASS", Severity: "error", Message: fmt.Sprintf("Net class trace width is below the %s minimum", profile.Name), ObjectIDs: []string{class.ID}})
		}
		if class.ClearanceNM > 0 && class.ClearanceNM < profile.Rules.MinClearanceNM {
			checks = append(checks, Check{Code: "FAB_PROFILE_NET_CLASS", Severity: "error", Message: fmt.Sprintf("Net class clearance is below the %s minimum", profile.Name), ObjectIDs: []string{class.ID}})
		}
		if class.ViaDrillNM > 0 && class.ViaDrillNM < profile.Rules.MinDrillNM {
			checks = append(checks, Check{Code: "FAB_PROFILE_NET_CLASS", Severity: "error", Message: fmt.Sprintf("Net class via drill is below the %s minimum", profile.Name), ObjectIDs: []string{class.ID}})
		}
	}
	if profile.Reference {
		checks = append(checks, Check{Code: "FAB_PROFILE_REFERENCE", Severity: "info", Message: fmt.Sprintf("%s uses reference values from %s; confirm the fab's current limits before ordering", profile.Name, profile.Source), ObjectIDs: []string{profile.ID}})
	}
	return checks
}

func fabProfileLimits(def *Definition) error {
	if len(def.FabProfiles) > maxFabProfiles {
		return fmt.Errorf("definition exceeds %d fab profiles", maxFabProfiles)
	}
	seen := map[string]bool{}
	for _, profile := range def.FabProfiles {
		if !idValid(profile.ID) {
			return fmt.Errorf("fab profile id %q is invalid", profile.ID)
		}
		if seen[profile.ID] {
			return fmt.Errorf("duplicate fab profile %q", profile.ID)
		}
		seen[profile.ID] = true
		if len(profile.Name) > 200 || len(profile.Description) > 2_000 || len(profile.Source) > 2_000 {
			return fmt.Errorf("fab profile %q text exceeds limits", profile.ID)
		}
	}
	return nil
}

func applyFabProfileOperation(def *Definition, op Operation) error {
	switch op.Type {
	case "fab_profile.add":
		if op.FabProfile == nil {
			return errors.New("fab_profile.add requires fab_profile")
		}
		profile := *op.FabProfile
		if builtin, ok := builtinFabProfile(profile.ID); ok && profile.Name == "" {
			profile = builtin
		}
		if !idValid(profile.ID) {
			return fmt.Errorf("fab profile id %q is invalid", profile.ID)
		}
		for _, existing := range def.FabProfiles {
			if existing.ID == profile.ID {
				return fmt.Errorf("fab profile %q already exists", profile.ID)
			}
		}
		def.FabProfiles = append(def.FabProfiles, profile)
		sort.Slice(def.FabProfiles, func(i, j int) bool { return def.FabProfiles[i].ID < def.FabProfiles[j].ID })
	case "fab_profile.remove":
		index := -1
		for i, existing := range def.FabProfiles {
			if existing.ID == op.FabProfileID {
				index = i
			}
		}
		if index < 0 {
			return fmt.Errorf("fab profile %q not found", op.FabProfileID)
		}
		def.FabProfiles = append(def.FabProfiles[:index], def.FabProfiles[index+1:]...)
		if def.ActiveFabProfileID == op.FabProfileID {
			def.ActiveFabProfileID = ""
		}
	case "fab_profile.select":
		id := strings.TrimSpace(op.FabProfileID)
		if id != "" {
			found := false
			for _, existing := range def.FabProfiles {
				found = found || existing.ID == id
			}
			if !found {
				builtin, ok := builtinFabProfile(id)
				if !ok {
					return fmt.Errorf("fab profile %q not found in the design or the built-in library", id)
				}
				// Copy the built-in into the design so the revision stays
				// reproducible if the library values change later.
				def.FabProfiles = append(def.FabProfiles, builtin)
				sort.Slice(def.FabProfiles, func(i, j int) bool { return def.FabProfiles[i].ID < def.FabProfiles[j].ID })
			}
		}
		def.ActiveFabProfileID = id
	default:
		return fmt.Errorf("unsupported fab profile operation %q", op.Type)
	}
	return nil
}
