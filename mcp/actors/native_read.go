package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (e *actorExecution) recordNativeRead(step actorStep, out map[string]any) error {
	value, ok := out["native_value"].(string)
	if out["success"] != true || !ok || len(value) > 4096 {
		return errors.New("Computer did not confirm a bounded native field value")
	}
	item, err := validateOutputItem(map[string]any{step.VerifiedField: value}, e.definition.OutputSchema)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(item)
	if len(e.items) >= e.maxItems || len(b) > maxActorItemBytes || e.datasetBytes+len(b)+1 > maxActorDatasetBytes {
		return errors.New("native read exceeds dataset limits")
	}
	if err = persistDatasetPage(e.ctx, e.run.ID, []map[string]any{item}); err != nil {
		return err
	}
	for k, v := range item {
		e.lastValues[k] = v
	}
	e.items = append(e.items, item)
	e.datasetBytes += len(b) + 1
	return nil
}

// Limits for native_read_list steps; they match Computer 0.17.0.
const (
	defaultNativeListItems = 20
	maxNativeListItems     = 50
	maxNativeListSwipes    = 10
	maxNativeReplaceBytes  = 4096
)

// nativeListItemLimit is the row limit sent to Computer.
func nativeListItemLimit(step actorStep) int {
	if step.NativeRows == "text" {
		return 1
	}
	if step.MaxItems > 0 {
		return step.MaxItems
	}
	return defaultNativeListItems
}

// recordNativeReadList stores one dataset item per row Computer read, the
// row's strings joined by newlines in verified_field. With native_rows=text
// the whole list is one item. Nothing is stored when the read fails closed:
// a screen change during paging, or fewer rows than min_items.
func (e *actorExecution) recordNativeReadList(step actorStep, out map[string]any) error {
	if out["success"] != true {
		return errors.New("Computer did not confirm the native list read")
	}
	stopped, _ := out["stopped"].(string)
	var values []string
	if step.NativeRows == "text" {
		lines, ok := nativeListStrings(out["text"])
		if !ok {
			return errors.New("Computer returned no bounded list text")
		}
		if len(lines) > 0 {
			values = append(values, strings.Join(lines, "\n"))
		}
	} else {
		raw, ok := out["rows"].([]any)
		if !ok || len(raw) > nativeListItemLimit(step) {
			return errors.New("Computer returned no bounded list rows")
		}
		for _, r := range raw {
			row, ok := r.(map[string]any)
			if !ok {
				return errors.New("Computer returned an invalid list row")
			}
			texts, ok := nativeListStrings(row["texts"])
			if !ok {
				return errors.New("Computer returned an invalid list row")
			}
			if len(texts) > 0 {
				values = append(values, strings.Join(texts, "\n"))
			}
		}
	}
	trace := map[string]any{"step": e.lastStartedStep, "rows": len(values), "stopped": stopped, "swipes": out["swipes"], "screens": out["screens"]}
	if stopped == "screen_changed" {
		detail, _ := out["detail"].(string)
		trace["detail"] = truncateString(detail, 500)
		e.addTrace("native_read_list.result", "failed", "", trace)
		return fmt.Errorf("native list read stopped because the screen changed after %d rows; nothing was saved: %s", len(values), truncateString(detail, 500))
	}
	if len(values) < step.MinItems {
		e.addTrace("native_read_list.result", "failed", "", trace)
		return fmt.Errorf("native list read found %d rows; min_items is %d", len(values), step.MinItems)
	}
	items := make([]map[string]any, 0, len(values))
	size := 0
	for _, v := range values {
		item, err := validateOutputItem(map[string]any{step.VerifiedField: v}, e.definition.OutputSchema)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(item)
		if len(b) > maxActorItemBytes {
			return errors.New("native list row exceeds the dataset item limit")
		}
		size += len(b) + 1
		items = append(items, item)
	}
	if len(e.items)+len(items) > e.maxItems || e.datasetBytes+size > maxActorDatasetBytes {
		return errors.New("native list read exceeds dataset limits")
	}
	if len(items) > 0 {
		if err := persistDatasetPage(e.ctx, e.run.ID, items); err != nil {
			return err
		}
		e.lastValues[step.VerifiedField] = items[len(items)-1][step.VerifiedField]
	}
	e.items = append(e.items, items...)
	e.datasetBytes += size
	e.addTrace("native_read_list.result", "confirmed", "", trace)
	return nil
}

// nativeListStrings accepts only a JSON array of strings.
func nativeListStrings(v any) ([]string, bool) {
	raw, ok := v.([]any)
	if !ok {
		return nil, v == nil
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		str, ok := s.(string)
		if !ok {
			return nil, false
		}
		if str = strings.TrimSpace(str); str != "" {
			out = append(out, str)
		}
	}
	return out, true
}
