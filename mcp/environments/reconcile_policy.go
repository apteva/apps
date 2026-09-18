package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultReconcileFailureThreshold = 5
	reconcileBackoffBase             = 15 * time.Second
	reconcileBackoffMax              = 15 * time.Minute
)

func reconcileFailureThreshold() (int, error) {
	raw := strings.TrimSpace(os.Getenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD"))
	if raw == "" {
		return defaultReconcileFailureThreshold, nil
	}
	threshold, err := strconv.Atoi(raw)
	if err != nil || threshold < 1 || threshold > 1000 {
		return 0, fmt.Errorf("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD must be between 1 and 1000")
	}
	return threshold, nil
}

// The first automatic retry waits for two worker intervals. Subsequent
// failures double the delay until the cap is reached.
func reconcileBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := 2 * reconcileBackoffBase
	for i := 1; i < failures && delay < reconcileBackoffMax; i++ {
		delay *= 2
		if delay > reconcileBackoffMax {
			delay = reconcileBackoffMax
		}
	}
	return delay
}

func reconcileBlocked(d *Definition, now time.Time) bool {
	if d == nil {
		return false
	}
	if d.ReconcileStatus == "degraded" {
		return true
	}
	return d.ReconcileNextAt != nil && now.Before(*d.ReconcileNextAt)
}

func (s *service) recordReconcileFailure(id, message string, threshold int) error {
	d, err := s.db.getDefinition(id)
	if err != nil || d == nil {
		return err
	}
	failures := d.ReconcileFailures + 1
	now := time.Now().UTC()
	status := "retrying"
	next := now.Add(reconcileBackoff(failures))
	var nextAt *time.Time = &next
	var degradedAt *time.Time
	if failures >= threshold {
		status = "degraded"
		nextAt = nil
		degradedAt = &now
	}
	if err := s.db.setReconcileFailure(id, failures, message, status, nextAt, degradedAt); err != nil {
		return err
	}
	if status == "degraded" && d.ReconcileStatus != "degraded" && s.ctx != nil {
		s.ctx.Emit("environment.degraded", map[string]any{"environment_id": id, "failures": failures, "error": message})
	}
	return nil
}
