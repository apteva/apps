package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// The public definition schema is also the allowlist for partial edits. Legacy
// ownership, kinds and graphical coordinates cannot enter a semantic patch.
func patchSchema() map[string]any {
	props := definitionSchema()["properties"].(map[string]any)
	for _, name := range []string{"steps", "parameters"} {
		full := props[name].(map[string]any)
		item := full["items"].(map[string]any)
		updateProps := map[string]any{}
		for key, value := range item["properties"].(map[string]any) {
			updateProps[key] = value
		}
		for _, key := range []string{"start_after", "due_after"} {
			if value, ok := updateProps[key]; ok {
				updateProps[key] = map[string]any{"anyOf": []any{value, map[string]any{"type": "null"}}}
			}
		}
		update := object([]string{"key"}, updateProps)
		update["minProperties"] = 2
		props[name] = object([]string{}, map[string]any{
			"add":    map[string]any{"type": "array", "maxItems": full["maxItems"], "items": item},
			"update": map[string]any{"type": "array", "maxItems": full["maxItems"], "items": update},
			"remove": map[string]any{"type": "array", "maxItems": full["maxItems"], "items": textField("Existing stable key")},
		})
	}
	out := object([]string{}, props)
	out["minProperties"] = 1
	return out
}

type CollectionPatchReceipt struct {
	Added   []string `json:"added,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Decode directly into typed records: unchanged int64 fields never pass through
// a map[string]any/float64 round trip. Arrays and nested fields replace values.
func mergePatch[T any](base T, fields map[string]json.RawMessage, schema map[string]any) (T, error) {
	allowed := schema["properties"].(map[string]any)
	for field, value := range fields {
		if _, ok := allowed[field]; !ok {
			return base, fmt.Errorf("unsupported patch field %s", field)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && field != "default" && field != "start_after" && field != "due_after" {
			return base, fmt.Errorf("%s cannot be null; use an empty value to clear it", field)
		}
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	var merged map[string]json.RawMessage
	if err = json.Unmarshal(raw, &merged); err != nil {
		return base, err
	}
	for field, value := range fields {
		merged[field] = value
	}
	raw, err = json.Marshal(merged)
	if err != nil {
		return base, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result T
	err = decoder.Decode(&result)
	return result, err
}

func patchCollection[T any](items []T, raw json.RawMessage, schema map[string]any, keyOf func(T) string) ([]T, CollectionPatchReceipt, error) {
	receipt := CollectionPatchReceipt{}
	var operations map[string]json.RawMessage
	if err := json.Unmarshal(raw, &operations); err != nil || operations == nil {
		return nil, receipt, errors.New("collection patch must be an object with add, update or remove")
	}
	var additions, updates []map[string]json.RawMessage
	var removals []string
	for name, value := range operations {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, receipt, errors.New("collection operations cannot be null")
		}
		var err error
		switch name {
		case "add":
			err = json.Unmarshal(value, &additions)
		case "update":
			err = json.Unmarshal(value, &updates)
		case "remove":
			err = json.Unmarshal(value, &removals)
		default:
			err = fmt.Errorf("unknown collection operation %s", name)
		}
		if err != nil {
			return nil, receipt, err
		}
	}
	if len(additions)+len(updates)+len(removals) == 0 {
		return nil, receipt, errors.New("collection patch needs at least one change")
	}
	index := map[string]int{}
	for i, item := range items {
		index[keyOf(item)] = i
	}
	seen := map[string]bool{}
	reserve := func(key string, mustExist bool) error {
		_, exists := index[key]
		if key == "" || seen[key] {
			return errors.New("each patch key must be nonempty and used only once")
		}
		if exists != mustExist {
			return fmt.Errorf("key %s: add needs a new key; update/remove need an existing key", key)
		}
		seen[key] = true
		return nil
	}
	readKey := func(fields map[string]json.RawMessage) string {
		var key string
		json.Unmarshal(fields["key"], &key)
		return key
	}
	// Copy the slice; failed validation must not alter the original definition.
	result := append([]T(nil), items...)
	for _, fields := range updates {
		key := readKey(fields)
		if err := reserve(key, true); err != nil {
			return nil, receipt, err
		}
		if len(fields) < 2 {
			return nil, receipt, errors.New("update needs a field in addition to key")
		}
		item, err := mergePatch(result[index[key]], fields, schema)
		if err != nil {
			return nil, receipt, err
		}
		result[index[key]] = item
		receipt.Updated = append(receipt.Updated, key)
	}
	for _, fields := range additions {
		key := readKey(fields)
		if err := reserve(key, false); err != nil {
			return nil, receipt, err
		}
		var zero T
		item, err := mergePatch(zero, fields, schema)
		if err != nil {
			return nil, receipt, err
		}
		result = append(result, item)
		receipt.Added = append(receipt.Added, key)
	}
	removed := map[string]bool{}
	for _, key := range removals {
		if err := reserve(key, true); err != nil {
			return nil, receipt, err
		}
		removed[key] = true
		receipt.Removed = append(receipt.Removed, key)
	}
	filtered := make([]T, 0, len(result))
	for _, item := range result {
		if !removed[keyOf(item)] {
			filtered = append(filtered, item)
		}
	}
	return filtered, receipt, nil
}

func (a *App) patchDefinition(project, id, actor string, args map[string]any) (any, error) {
	expected, ok := args["expected_version"].(float64)
	if !ok || expected < 1 || math.IsInf(expected, 0) || math.IsNaN(expected) || expected != math.Trunc(expected) || expected > float64(1<<31-1) {
		return nil, errors.New("expected_version must be a positive integer")
	}
	if id == "" {
		return nil, errors.New("process_id is required")
	}
	p, err := a.get(project, id)
	if err != nil {
		return nil, err
	}
	if p.Version != int(expected) {
		return nil, errConflict
	}
	raw, err := json.Marshal(args["changes"])
	if err != nil || len(raw) > 128*1024 {
		return nil, errors.New("changes must be valid JSON of at most 128 KB")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil || len(fields) == 0 {
		return nil, errors.New("changes must be a nonempty object")
	}
	d := p.Definition
	changed := make([]string, 0, len(fields))
	for name := range fields {
		changed = append(changed, name)
	}
	sort.Strings(changed)
	collections := map[string]CollectionPatchReceipt{}
	fullSchema := definitionSchema()
	props := fullSchema["properties"].(map[string]any)
	for _, name := range []string{"steps", "parameters"} {
		value, exists := fields[name]
		if !exists {
			continue
		}
		itemSchema := props[name].(map[string]any)["items"].(map[string]any)
		var receipt CollectionPatchReceipt
		if name == "steps" {
			d.Steps, receipt, err = patchCollection(d.Steps, value, itemSchema, func(s Step) string { return s.Key })
		} else {
			d.Parameters, receipt, err = patchCollection(d.Parameters, value, itemSchema, func(p Parameter) string { return p.Key })
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		collections[name] = receipt
		delete(fields, name)
	}
	d, err = mergePatch(d, fields, fullSchema)
	if err != nil {
		return nil, err
	}
	// The shared save path validates the entire resulting graph/schema and uses
	// a transaction/CAS for the immutable version and follow-latest assignments.
	saved, err := a.save(project, id, actor, int(expected), d)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"process_id": saved.ID, "previous_version": int(expected), "version": saved.Version,
		"status": saved.Status, "changed_fields": changed, "collections": collections,
		"reread": map[string]any{"tool": "processes_get", "args": map[string]any{"process_id": saved.ID, "version": saved.Version}},
	}, nil
}
