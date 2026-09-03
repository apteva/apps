package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	parameterNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sha256RE        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var allowedOperations = map[string]bool{
	"box": true, "cylinder": true, "sphere": true,
	"extrude_rectangle": true, "extrude_circle": true, "extrude_polygon": true,
	"revolve_profile": true, "sweep_circle": true,
	"fuse": true, "cut": true, "intersect": true, "compound": true,
	"translate": true, "rotate": true, "scale": true, "mirror": true,
	"linear_pattern": true, "circular_pattern": true,
	"fillet": true, "chamfer": true,
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
		case "number", "length", "angle":
		default:
			return nil, nil, fmt.Errorf("parameter %s type must be number, length, or angle", name)
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
	}
	if definition.Output != "" && !ids[definition.Output] {
		return nil, nil, fmt.Errorf("definition.output references unknown operation %q", definition.Output)
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
	values := map[string]float64{}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("invalid parameters JSON: %w", err)
		}
	}
	for name, value := range values {
		spec, exists := definition.Parameters[name]
		if !exists {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
		if spec.Min != nil && value < *spec.Min {
			return nil, fmt.Errorf("parameter %s is below min", name)
		}
		if spec.Max != nil && value > *spec.Max {
			return nil, fmt.Errorf("parameter %s is above max", name)
		}
	}
	return json.Marshal(values)
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
