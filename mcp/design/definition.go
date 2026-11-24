package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var (
	parameterNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sha256RE        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var allowedOperations = map[string]bool{
	"import_step": true, "swept_box": true, "box": true, "cylinder": true, "sphere": true,
	"extrude_rectangle": true, "extrude_circle": true, "extrude_polygon": true,
	"revolve_profile": true, "sweep_circle": true,
	"fuse": true, "cut": true, "intersect": true, "compound": true,
	"translate": true, "rotate": true, "scale": true, "mirror": true,
	"linear_pattern": true, "circular_pattern": true,
	"fillet": true, "chamfer": true,
	"component": true,
}

func normalizeDefinition(raw []byte, maxOperations int) ([]byte, *DesignDefinition, error) {
	if len(raw) == 0 {
		return nil, nil, errors.New("definition required")
	}
	if len(raw) > 1<<20 {
		return nil, nil, errors.New("definition exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var definition DesignDefinition
	if err := dec.Decode(&definition); err != nil {
		return nil, nil, fmt.Errorf("invalid definition JSON: %w", err)
	}
	if definition.Schema != designSchema {
		return nil, nil, fmt.Errorf("definition.schema must be %q", designSchema)
	}
	if definition.Units == "" {
		definition.Units = "mm"
	}
	if definition.Units != "mm" {
		return nil, nil, errors.New("v0.1 supports millimetres only")
	}
	if len(definition.Operations) == 0 && (definition.Assembly == nil || len(definition.Assembly.Instances) == 0) {
		return nil, nil, errors.New("definition requires operations or an assembly with instances")
	}
	if maxOperations <= 0 {
		maxOperations = 256
	}
	if len(definition.Operations) > maxOperations {
		return nil, nil, fmt.Errorf("definition has %d operations; maximum is %d", len(definition.Operations), maxOperations)
	}
	if strings.TrimSpace(definition.Output) == "" && definition.Assembly == nil {
		return nil, nil, errors.New("definition.output required outside an assembly")
	}
	for name, spec := range definition.Parameters {
		if !parameterNameRE.MatchString(name) {
			return nil, nil, fmt.Errorf("invalid parameter name %q", name)
		}
		if spec.Type == "" {
			spec.Type = "length"
			definition.Parameters[name] = spec
		}
		switch spec.Type {
		case "number", "length", "angle", "integer":
		case "boolean":
			if spec.Min != nil || spec.Max != nil || len(spec.Options) != 0 {
				return nil, nil, fmt.Errorf("boolean parameter %s cannot have min, max, or options", name)
			}
		default:
			return nil, nil, fmt.Errorf("parameter %s type must be number, length, angle, integer, or boolean", name)
		}
		if len(spec.Options) > 64 {
			return nil, nil, fmt.Errorf("parameter %s has more than 64 options", name)
		}
		if err := checkParameterValue(name, spec, spec.Default); err != nil {
			return nil, nil, fmt.Errorf("default: %w", err)
		}
		if spec.Min != nil && spec.Max != nil && *spec.Min > *spec.Max {
			return nil, nil, fmt.Errorf("parameter %s min exceeds max", name)
		}
		if spec.Min != nil && spec.Default < *spec.Min {
			return nil, nil, fmt.Errorf("parameter %s default is below min", name)
		}
		if spec.Max != nil && spec.Default > *spec.Max {
			return nil, nil, fmt.Errorf("parameter %s default is above max", name)
		}
	}
	ids := make(map[string]bool, len(definition.Operations))
	for index, operation := range definition.Operations {
		id, _ := operation["id"].(string)
		kind, _ := operation["type"].(string)
		if !parameterNameRE.MatchString(id) {
			return nil, nil, fmt.Errorf("operation %d has invalid id %q", index, id)
		}
		if ids[id] {
			return nil, nil, fmt.Errorf("duplicate operation id %q", id)
		}
		ids[id] = true
		if !allowedOperations[kind] {
			return nil, nil, fmt.Errorf("operation %q has unsupported type %q", id, kind)
		}
		if when, present := operation["when"]; present && when != nil {
			switch value := when.(type) {
			case bool, float64, json.Number:
			case string:
				if strings.TrimSpace(value) == "" {
					return nil, nil, fmt.Errorf("operation %q when must not be empty", id)
				}
			default:
				return nil, nil, fmt.Errorf("operation %q when must be a boolean, number, or expression string", id)
			}
		}
		if kind == "component" {
			if err := validateComponentOperation(id, operation); err != nil {
				return nil, nil, err
			}
		}
	}
	if definition.Output != "" && !ids[definition.Output] {
		return nil, nil, fmt.Errorf("definition.output references unknown operation %q", definition.Output)
	}
	if err := validateInspectionDefinition(&definition, ids); err != nil {
		return nil, nil, err
	}
	if err := validateProductDefinition(&definition, ids); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(definition)
	if err != nil {
		return nil, nil, err
	}
	return canonical, &definition, nil
}

func validateProductDefinition(definition *DesignDefinition, operations map[string]bool) error {
	materials := map[string]bool{}
	for _, material := range definition.Materials {
		if !parameterNameRE.MatchString(material.ID) {
			return fmt.Errorf("invalid material id %q", material.ID)
		}
		if materials[material.ID] {
			return fmt.Errorf("duplicate material id %q", material.ID)
		}
		if strings.TrimSpace(material.Name) == "" || strings.TrimSpace(material.Kind) == "" {
			return fmt.Errorf("material %q requires name and kind", material.ID)
		}
		if material.DensityGCM3 < 0 {
			return fmt.Errorf("material %q density must not be negative", material.ID)
		}
		materials[material.ID] = true
	}
	profiles := map[string]bool{}
	for _, profile := range definition.PrintProfiles {
		if !parameterNameRE.MatchString(profile.ID) {
			return fmt.Errorf("invalid print profile id %q", profile.ID)
		}
		if profiles[profile.ID] {
			return fmt.Errorf("duplicate print profile id %q", profile.ID)
		}
		if profile.Process == "" {
			return fmt.Errorf("print profile %q requires process", profile.ID)
		}
		if profile.MaterialID != "" && !materials[profile.MaterialID] {
			return fmt.Errorf("print profile %q references unknown material %q", profile.ID, profile.MaterialID)
		}
		if len(profile.BedSizeMM) != 0 && len(profile.BedSizeMM) != 3 {
			return fmt.Errorf("print profile %q bed_size_mm must contain three dimensions", profile.ID)
		}
		profiles[profile.ID] = true
	}
	parts := map[string]DesignPart{}
	for _, part := range definition.Parts {
		if !parameterNameRE.MatchString(part.ID) {
			return fmt.Errorf("invalid part id %q", part.ID)
		}
		if _, exists := parts[part.ID]; exists {
			return fmt.Errorf("duplicate part id %q", part.ID)
		}
		if strings.TrimSpace(part.Name) == "" {
			return fmt.Errorf("part %q requires name", part.ID)
		}
		hasOutput := strings.TrimSpace(part.Output) != ""
		hasSource := part.Source != nil
		if hasOutput == hasSource {
			return fmt.Errorf("part %q requires exactly one of output or source", part.ID)
		}
		if hasOutput && !operations[part.Output] {
			return fmt.Errorf("part %q references unknown output operation %q", part.ID, part.Output)
		}
		if hasSource {
			if part.Source.DesignID <= 0 || part.Source.RevisionID <= 0 {
				return fmt.Errorf("part %q source requires positive design_id and revision_id", part.ID)
			}
			if !sha256RE.MatchString(part.Source.SourceSHA256) {
				return fmt.Errorf("part %q source_sha256 must be a lowercase SHA-256 digest", part.ID)
			}
			if part.Source.PartID != "" && !parameterNameRE.MatchString(part.Source.PartID) {
				return fmt.Errorf("part %q source part_id is invalid", part.ID)
			}
		}
		if part.Quantity < 0 {
			return fmt.Errorf("part %q quantity must not be negative", part.ID)
		}
		if part.MaterialID != "" && !materials[part.MaterialID] {
			return fmt.Errorf("part %q references unknown material %q", part.ID, part.MaterialID)
		}
		if part.Manufacturing != nil {
			classification := part.Manufacturing.Classification
			switch classification {
			case "printed", "purchased", "reference", "fabricated":
			default:
				return fmt.Errorf("part %q has unsupported manufacturing classification %q", part.ID, classification)
			}
			if part.Manufacturing.PrintProfileID != "" && !profiles[part.Manufacturing.PrintProfileID] {
				return fmt.Errorf("part %q references unknown print profile %q", part.ID, part.Manufacturing.PrintProfileID)
			}
		}
		parts[part.ID] = part
	}
	if definition.Assembly != nil {
		if len(definition.Parts) == 0 {
			return errors.New("assembly requires named parts")
		}
		if len(definition.Assembly.Instances) > 512 {
			return errors.New("assembly exceeds 512 instances")
		}
		instances := map[string]bool{}
		for _, instance := range definition.Assembly.Instances {
			if !parameterNameRE.MatchString(instance.ID) {
				return fmt.Errorf("invalid assembly instance id %q", instance.ID)
			}
			if instances[instance.ID] {
				return fmt.Errorf("duplicate assembly instance id %q", instance.ID)
			}
			if _, exists := parts[instance.PartID]; !exists {
				return fmt.Errorf("assembly instance %q references unknown part %q", instance.ID, instance.PartID)
			}
			if instance.Quantity < 0 {
				return fmt.Errorf("assembly instance %q quantity must not be negative", instance.ID)
			}
			instances[instance.ID] = true
		}
		interfaces := map[string]bool{}
		for _, mechanical := range definition.Assembly.Interfaces {
			if !parameterNameRE.MatchString(mechanical.ID) || interfaces[mechanical.ID] {
				return fmt.Errorf("invalid or duplicate mechanical interface id %q", mechanical.ID)
			}
			if _, exists := parts[mechanical.PartID]; !exists {
				return fmt.Errorf("mechanical interface %q references unknown part %q", mechanical.ID, mechanical.PartID)
			}
			if len(mechanical.Position) != 3 {
				return fmt.Errorf("mechanical interface %q position must contain three values", mechanical.ID)
			}
			interfaces[mechanical.ID] = true
		}
		joints := map[string]bool{}
		for _, joint := range definition.Assembly.Joints {
			if !parameterNameRE.MatchString(joint.ID) || joints[joint.ID] {
				return fmt.Errorf("invalid or duplicate joint id %q", joint.ID)
			}
			switch joint.Type {
			case "fixed", "revolute", "prismatic":
			default:
				return fmt.Errorf("joint %q has unsupported type %q", joint.ID, joint.Type)
			}
			if !instances[joint.ParentInstance] || !instances[joint.ChildInstance] {
				return fmt.Errorf("joint %q references an unknown assembly instance", joint.ID)
			}
			if len(joint.Origin) != 3 {
				return fmt.Errorf("joint %q origin must contain three values", joint.ID)
			}
			joints[joint.ID] = true
		}
	}
	if definition.OpenHardware != nil {
		if strings.TrimSpace(definition.OpenHardware.ProjectName) == "" || strings.TrimSpace(definition.OpenHardware.Version) == "" {
			return errors.New("open_hardware requires project_name and version")
		}
		if strings.TrimSpace(definition.OpenHardware.License) == "" {
			return errors.New("open_hardware requires an SPDX license expression")
		}
	}
	for _, item := range definition.BOM {
		if !parameterNameRE.MatchString(item.ID) || item.Name == "" || item.Quantity <= 0 {
			return fmt.Errorf("BOM item %q requires a valid id, name, and positive quantity", item.ID)
		}
		if item.PartID != "" {
			if _, exists := parts[item.PartID]; !exists {
				return fmt.Errorf("BOM item %q references unknown part %q", item.ID, item.PartID)
			}
		}
	}
	return nil
}

func normalizeParameters(raw []byte, definition *DesignDefinition) ([]byte, error) {
	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("invalid parameters JSON: %w", err)
		}
	}
	values := make(map[string]float64, len(decoded))
	for name, rawValue := range decoded {
		spec, exists := definition.Parameters[name]
		if !exists {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
		value, err := parameterNumber(rawValue)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", name, err)
		}
		if err := checkParameterValue(name, spec, value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return json.Marshal(values)
}

// parameterNumber converts a JSON parameter value (number or boolean) to the
// float the runner uses; booleans are 1/0.
func parameterNumber(value any) (float64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return typed, nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		return typed.Float64()
	default:
		return 0, fmt.Errorf("must be a number or boolean, got %T", value)
	}
}

func checkParameterValue(name string, spec ParameterSpec, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("parameter %s must be finite", name)
	}
	switch spec.Type {
	case "boolean":
		if value != 0 && value != 1 {
			return fmt.Errorf("parameter %s must be true/false (1/0)", name)
		}
	case "integer":
		if value != math.Trunc(value) {
			return fmt.Errorf("parameter %s must be an integer", name)
		}
	}
	if spec.Min != nil && value < *spec.Min {
		return fmt.Errorf("parameter %s is below min", name)
	}
	if spec.Max != nil && value > *spec.Max {
		return fmt.Errorf("parameter %s is above max", name)
	}
	if len(spec.Options) != 0 {
		for _, option := range spec.Options {
			if math.Abs(option-value) <= 1e-9 {
				return nil
			}
		}
		return fmt.Errorf("parameter %s must be one of %v", name, spec.Options)
	}
	return nil
}

// validateComponentOperation checks the static shape of a component
// operation; the source itself is resolved by materializeDefinition.
func validateComponentOperation(id string, operation map[string]any) error {
	source, ok := operation["source"].(map[string]any)
	if !ok {
		return fmt.Errorf("component operation %q requires source {design_id, revision_id, source_sha256}", id)
	}
	designID := anyNumber(source["design_id"])
	revisionID := anyNumber(source["revision_id"])
	sha, _ := source["source_sha256"].(string)
	if designID <= 0 || revisionID <= 0 || strings.TrimSpace(sha) == "" {
		return fmt.Errorf("component operation %q source requires design_id, revision_id, and source_sha256", id)
	}
	if params, present := operation["parameters"]; present && params != nil {
		if _, ok := params.(map[string]any); !ok {
			return fmt.Errorf("component operation %q parameters must be an object", id)
		}
	}
	return nil
}

func sourceHash(definition, parameters []byte) string {
	h := sha256.New()
	h.Write(definition)
	h.Write([]byte{0})
	h.Write(parameters)
	h.Write([]byte{0})
	h.Write([]byte(engineVersion))
	return hex.EncodeToString(h.Sum(nil))
}

func diffJSON(left, right json.RawMessage) map[string]any {
	var a, b any
	_ = json.Unmarshal(left, &a)
	_ = json.Unmarshal(right, &b)
	return map[string]any{
		"changed": !bytes.Equal(left, right),
		"before":  a,
		"after":   b,
	}
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func anyNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		number, _ := typed.Float64()
		return number
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	}
	return 0
}
