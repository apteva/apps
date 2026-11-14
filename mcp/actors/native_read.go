package main

import (
	"encoding/json"
	"errors"
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
