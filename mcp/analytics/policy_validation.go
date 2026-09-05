package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func validateIngestPolicy(p *EventIngestPolicy, required bool) error {
	if p == nil || policyEmpty(*p) {
		if required {
			return errors.New("ingest policy required")
		}
		return nil
	}
	if p.Bucket != "" && !stringIn(p.Bucket, []string{"none", "hour", "day", "week", "month"}) {
		return fmt.Errorf("invalid bucket %q", p.Bucket)
	}
	if p.Operation != "" && !stringIn(p.Operation, []string{"replace", "increment", "sum", "min", "max"}) {
		return fmt.Errorf("invalid operation %q", p.Operation)
	}
	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return err
		}
	}
	for _, key := range append(append([]string{}, p.Dimensions...), p.TimestampProperty, p.ValueKey) {
		if key != "" && !validEventPropertyKey(key) {
			return fmt.Errorf("invalid policy path %q", key)
		}
	}
	if len(p.Dimensions) > 16 {
		return errors.New("at most 16 dimensions supported")
	}
	if p.OutputProperty != "" {
		if _, ok := propsExtract("props." + strings.TrimPrefix(p.OutputProperty, "props.")); !ok {
			return errors.New("invalid output property")
		}
	}
	if p.Value != nil && p.Operation != "" && p.Operation != "replace" {
		if s, ok := p.Value.(string); !ok || !validEventPropertyKey(s) {
			if _, ok := numericValue(p.Value); !ok {
				return errors.New("policy value must be finite numeric data")
			}
		}
	}
	return nil
}
