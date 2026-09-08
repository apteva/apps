package clickguard

import (
	"errors"
	"testing"
	"time"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestWorkflowConstraintOverridesMatchingClickAcknowledgement(t *testing.T) {
	at := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	p := computer.WorkflowConstraint{ID: "schedule-task", AllowedEffect: "scheduled_external_commit", ResourceURL: "https://example.com/posts/1/edit", ScheduledAt: at.Format(time.RFC3339), Timezone: "UTC"}
	options := Options{ExpectedText: "Publish", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit", EnforceConsequence: true, WorkflowConstraints: []computer.WorkflowConstraint{p}}
	target := Target{AccessibleName: "Publish", Dangerous: true, DestructiveEffect: "immediate_publish"}
	var rejected *ConsequenceError
	if err := Validate(target, options); !errors.As(err, &rejected) || rejected.Code != "workflow_effect_mismatch" {
		t.Fatalf("fully acknowledged wrong consequence accepted: %v", err)
	}
	options.ExpectedText = "Schedule"
	options.ExpectedEffect, options.ConfirmConsequence = "scheduled_external_commit", "scheduled_external_commit"
	target.AccessibleName, target.DestructiveEffect = "Schedule", "schedule_publish"
	target.WorkflowObservations = []WorkflowObservation{{URL: p.ResourceURL, Timezone: "UTC", Date: at.Format("2006-01-02"), Time: at.Format("15:04")}}
	if err := Validate(target, options); err != nil {
		t.Fatalf("authorized schedule rejected: %v", err)
	}
	for _, field := range []string{"date", "time", "timezone", "resource", "missing", "expired", "opaque"} {
		t.Run(field, func(t *testing.T) {
			bad := target
			bad.WorkflowObservations = append([]WorkflowObservation(nil), target.WorkflowObservations...)
			opts := options
			opts.WorkflowConstraints = append([]computer.WorkflowConstraint(nil), options.WorkflowConstraints...)
			switch field {
			case "date":
				bad.WorkflowObservations[0].Date = "2000-01-01"
			case "time":
				bad.WorkflowObservations[0].Time = "25:00"
			case "timezone":
				bad.WorkflowObservations[0].Timezone = "America/New_York"
			case "resource":
				bad.WorkflowObservations[0].URL = "https://example.com/posts/2/edit"
			case "missing":
				bad.WorkflowObservations = nil
			case "expired":
				opts.WorkflowConstraints[0].ScheduledAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
			case "opaque":
				bad.OpaqueFrame = true
			}
			if err := Validate(bad, opts); err == nil {
				t.Fatal("mismatched workflow accepted")
			}
		})
	}
}
