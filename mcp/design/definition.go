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

var parameterNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var allowedOperations = map[string]bool{
	"box": true, "cylinder": true, "sphere": true,
	"extrude_rectangle": true, "extrude_circle": true, "extrude_polygon": true,
	"fuse": true, "cut": true, "intersect": true, "compound": true,
	"translate": true, "rotate": true, "scale": true,
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
	if len(definition.Operations) == 0 {
		return nil, nil, errors.New("definition.operations must not be empty")
	}
	if maxOperations <= 0 {
		maxOperations = 256
	}
	if len(definition.Operations) > maxOperations {
		return nil, nil, fmt.Errorf("definition has %d operations; maximum is %d", len(definition.Operations), maxOperations)
	}
	if strings.TrimSpace(definition.Output) == "" {
		return nil, nil, errors.New("definition.output required")
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
	if !ids[definition.Output] {
		return nil, nil, fmt.Errorf("definition.output references unknown operation %q", definition.Output)
	}
	canonical, err := json.Marshal(definition)
	if err != nil {
		return nil, nil, err
	}
	return canonical, &definition, nil
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
