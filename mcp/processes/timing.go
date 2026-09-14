package main

import (
	"errors"
	"fmt"
	"time"
)

// Timing rules are frozen in the procedure version. Step references must be
// ancestors, so time rules cannot introduce hidden dependencies or cycles.
type TimingRule struct {
	After   string `json:"after"` // run_start or step_completed
	StepKey string `json:"step_key,omitempty"`
	Offset  int    `json:"offset"`
	Unit    string `json:"unit"` // minutes, hours, days (24 hours)
}

func (r TimingRule) duration() (time.Duration, error) {
	unit := map[string]time.Duration{"minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour}[r.Unit]
	if unit == 0 || r.Offset < 0 || r.Offset > int(365*24*time.Hour/unit) {
		return 0, errors.New("timing offset must be a nonnegative whole number of minutes, hours or days, at most 365 days")
	}
	return time.Duration(r.Offset) * unit, nil
}
func validateTiming(steps []Step) error {
	byKey := map[string]Step{}
	for _, s := range steps {
		byKey[s.Key] = s
	}
	for _, s := range steps {
		ancestors := map[string]bool{}
		var visit func(string)
		visit = func(key string) {
			for _, dep := range byKey[key].DependsOn {
				if !ancestors[dep] {
					ancestors[dep] = true
					visit(dep)
				}
			}
		}
		visit(s.Key)
		for _, r := range []*TimingRule{s.StartAfter, s.DueAfter} {
			if r == nil {
				continue
			}
			if _, err := r.duration(); err != nil {
				return fmt.Errorf("step %s: %w", s.Key, err)
			}
			switch r.After {
			case "run_start":
				if r.StepKey != "" {
					return errors.New("run_start timing cannot specify step_key")
				}
			case "step_completed":
				if !ancestors[r.StepKey] {
					return fmt.Errorf("step %s timing must reference a dependency or its ancestor", s.Key)
				}
			default:
				return errors.New("timing after must be run_start or step_completed")
			}
		}
		if s.StartAfter != nil && s.DueAfter != nil && s.StartAfter.After == s.DueAfter.After && s.StartAfter.StepKey == s.DueAfter.StepKey {
			start, _ := s.StartAfter.duration()
			due, _ := s.DueAfter.duration()
			if due < start {
				return fmt.Errorf("step %s deadline cannot precede its start time", s.Key)
			}
		}
	}
	return nil
}
func timingAt(rule *TimingRule, r Run, all []StepRun) (string, error) {
	if rule == nil {
		return "", nil
	}
	anchor := r.CreatedAt
	if rule.After == "step_completed" {
		anchor = ""
		for _, s := range all {
			if s.Key == rule.StepKey && s.State == "completed" && (s.Definition.Kind != "approval" || s.Decision == "approved") {
				anchor = s.CompletedAt
			}
		}
		if anchor == "" {
			return "", nil
		}
	}
	t, err := time.Parse(time.RFC3339Nano, anchor)
	if err != nil {
		return "", fmt.Errorf("invalid timing anchor: %w", err)
	}
	duration, err := rule.duration()
	if err != nil {
		return "", err
	}
	return t.Add(duration).UTC().Format(time.RFC3339Nano), nil
}
func (a *App) resolveStepTiming(s *StepRun, r Run, all []StepRun) error {
	if terminal(s.State) {
		return nil
	}
	start, due := s.StartAt, s.DueAt
	var err error
	if start == "" {
		start, err = timingAt(s.Definition.StartAfter, r, all)
		if err != nil {
			return err
		}
	}
	if due == "" {
		due, err = timingAt(s.Definition.DueAfter, r, all)
		if err != nil {
			return err
		}
	}
	if start == s.StartAt && due == s.DueAt {
		return nil
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET start_at=?,due_at=?,revision=revision+1 WHERE id=?`, start, due, s.ID)
	if err == nil {
		s.StartAt, s.DueAt = start, due
	}
	return err
}
func stepTimeReady(s StepRun, now time.Time) bool {
	if s.Definition.StartAfter == nil {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, s.StartAt)
	return err == nil && !now.Before(t)
}
func timingSchema(description string) map[string]any {
	schema := object([]string{"after", "offset", "unit"}, map[string]any{
		"after":    map[string]any{"type": "string", "enum": []string{"run_start", "step_completed"}},
		"step_key": textField("Required for step_completed: a dependency or ancestor step key"),
		"offset":   map[string]any{"type": "integer", "minimum": 0, "maximum": 525600},
		"unit":     map[string]any{"type": "string", "enum": []string{"minutes", "hours", "days"}},
	})
	schema["description"] = description
	return schema
}
