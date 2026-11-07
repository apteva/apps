package main

import (
	"fmt"
	"math"
)

type ComponentEvidence struct {
	Manufacturer       string   `json:"manufacturer,omitempty"`
	Model              string   `json:"model"`
	HardwareRevision   string   `json:"hardware_revision,omitempty"`
	GeometryStatus     string   `json:"geometry_status"` // manufacturer_cad, drawing_derived, measured, estimated
	Source             string   `json:"source,omitempty"`
	Date               string   `json:"date,omitempty"`
	Attribution        string   `json:"attribution,omitempty"`
	License            string   `json:"license,omitempty"`
	Complete           bool     `json:"complete"`
	Omissions          []string `json:"omissions,omitempty"`
	MeasurementsNeeded []string `json:"measurements_needed,omitempty"`
	UncertaintyMM      float64  `json:"uncertainty_mm"`
}

type ReleaseEvidence struct {
	Stage          string           `json:"stage"` // prototype or verified
	Assumptions    []string         `json:"assumptions,omitempty"`
	Fastening      string           `json:"fastening,omitempty"`
	AssemblyGuide  string           `json:"assembly_guide,omitempty"`
	ServiceGuide   string           `json:"service_guide,omitempty"`
	RequiredChecks []string         `json:"required_checks,omitempty"`
	PhysicalTests  []PhysicalTest   `json:"physical_tests,omitempty"`
	Views          []InspectionView `json:"views,omitempty"`
}
type PhysicalTest struct {
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	RevisionID   int64  `json:"revision_id"`
	SourceSHA256 string `json:"source_sha256"`
	EvidenceURL  string `json:"evidence_url"`
	Notes        string `json:"notes,omitempty"`
}
type InspectionView struct {
	Name        string   `json:"name"`
	Direction   string   `json:"direction"`
	Instances   []string `json:"instances,omitempty"`
	SectionAxis string   `json:"section_axis,omitempty"`
	SectionMM   float64  `json:"section_mm,omitempty"`
}
type DatumAlignment struct {
	TargetInstance string `json:"target_instance"`
	LocalPoint     []any  `json:"local_point"`
	TargetPoint    []any  `json:"target_point"`
	Offset         []any  `json:"offset,omitempty"`
}
type ClearanceRegion struct {
	ID            string   `json:"id"`
	OwnerInstance string   `json:"owner_instance"`
	Output        string   `json:"output"`
	Purpose       string   `json:"purpose"`
	Source        string   `json:"source"`
	ClearanceMM   float64  `json:"clearance_mm"`
	Against       []string `json:"against,omitempty"`
}
type TopologyReport struct {
	InstanceID string `json:"instance_id"`
	Valid      bool   `json:"valid"`
	SolidCount int    `json:"solid_count"`
	ShellCount int    `json:"shell_count"`
	Status     string `json:"status"`
}

func validateInspectionDefinition(d *DesignDefinition, operations map[string]bool) error {
	for _, op := range d.Operations {
		if op["type"] == "import_step" {
			id, _ := op["asset_id"].(string)
			if !sha256RE.MatchString(id) {
				return fmt.Errorf("import_step requires immutable asset_id")
			}
		}
	}
	profiles := map[string]PrintProfile{}
	for _, p := range d.PrintProfiles {
		profiles[p.ID] = p
		for _, v := range p.Fits {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("fit allowances must be finite and non-negative")
			}
		}
	}
	if d.FitProfileID != "" {
		profile, ok := profiles[d.FitProfileID]
		if !ok {
			return fmt.Errorf("unknown fit_profile_id")
		}
		for name, spec := range d.Parameters {
			if spec.Fit != "" {
				v, ok := profile.Fits[spec.Fit]
				if !ok {
					return fmt.Errorf("parameter %s fit missing from profile", name)
				}
				if spec.Min != nil && v < *spec.Min || spec.Max != nil && v > *spec.Max {
					return fmt.Errorf("profile fit for %s is outside parameter limits", name)
				}
			}
		}
	}
	for _, p := range d.Parts {
		if e := p.Evidence; e != nil {
			switch e.GeometryStatus {
			case "manufacturer_cad", "drawing_derived", "measured", "estimated":
			default:
				return fmt.Errorf("part %s: invalid geometry_status", p.ID)
			}
			if e.Model == "" || e.UncertaintyMM < 0 || math.IsNaN(e.UncertaintyMM) {
				return fmt.Errorf("part %s: model and non-negative uncertainty required", p.ID)
			}
			if e.Complete && (len(e.Omissions) > 0 || len(e.MeasurementsNeeded) > 0) {
				return fmt.Errorf("part %s: complete evidence has unresolved omissions/measurements", p.ID)
			}
		}
	}
	if d.Assembly != nil {
		ids := map[string]bool{}
		for _, i := range d.Assembly.Instances {
			ids[i.ID] = true
		}
		seen := map[string]bool{}
		for _, k := range d.Assembly.Keepouts {
			if !parameterNameRE.MatchString(k.ID) || seen[k.ID] || !ids[k.OwnerInstance] || !operations[k.Output] || k.Purpose == "" || k.Source == "" || k.ClearanceMM < 0 {
				return fmt.Errorf("invalid keepout %s: require unique id, owner, operation, purpose, source and non-negative clearance", k.ID)
			}
			seen[k.ID] = true
			for _, id := range k.Against {
				if !ids[id] || id == k.OwnerInstance {
					return fmt.Errorf("keepout %s: invalid target %s", k.ID, id)
				}
			}
		}
		for _, i := range d.Assembly.Instances {
			if a := i.Alignment; a != nil {
				if !ids[a.TargetInstance] || a.TargetInstance == i.ID || len(a.LocalPoint) != 3 || len(a.TargetPoint) != 3 || (len(a.Offset) != 0 && len(a.Offset) != 3) {
					return fmt.Errorf("instance %s: invalid datum alignment", i.ID)
				}
			}
		}
	}
	if d.Release != nil && d.Release.Stage != "prototype" && d.Release.Stage != "verified" {
		return fmt.Errorf("release.stage must be prototype or verified")
	}
	checkIDs := map[string]bool{}
	for _, c := range d.Checks {
		if id, _ := c["id"].(string); id != "" {
			if checkIDs[id] {
				return fmt.Errorf("duplicate check id %s", id)
			}
			checkIDs[id] = true
		}
		for _, key := range []string{"min", "tolerance_mm", "volume_tolerance_mm3"} {
			if n, ok := numberValue(c[key]); ok && (math.IsNaN(n) || math.IsInf(n, 0) || (key != "min" && n < 0)) {
				return fmt.Errorf("check %s must be finite and non-negative", key)
			}
		}
	}
	return nil
}
