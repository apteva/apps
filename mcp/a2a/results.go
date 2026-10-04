package main

import (
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// asyncAskResult is the normal MCP result for an agent_ask handoff. The
// result is deliberately platform-neutral: the caller can keep working and
// correlate later task events with task_id, regardless of how its runtime
// presents those events.
func asyncAskResult(app *sdk.AppCtx, task *Task, to map[string]any, reply string) map[string]any {
	pending := task != nil && openStatuses[task.Status]
	updatesMode := "event"
	if asyncTaskNotificationsAvailable(app) {
		updatesMode = "stream"
	}
	result := map[string]any{
		"accepted":        true,
		"delivered":       true, // retained for clients of older A2A versions
		"task_id":         task.ID,
		"status":          task.Status,
		"pending":         pending,
		"reply_expected":  pending,
		"delivery_status": "delivered",
		"updates": map[string]any{
			"mode":            updatesMode,
			"id_field":        "task_id",
			"events":          []string{"task.updated", "task.completed", "task.failed", "task.canceled"},
			"terminal_events": []string{"task.completed", "task.failed", "task.canceled"},
		},
	}
	if to != nil {
		result["to"] = to
	}
	if reply != "" {
		result["reply"] = reply
	}
	if pending {
		result["note"] = fmt.Sprintf("request accepted as task %d; later progress and terminal events are correlated by task_id", task.ID)
	} else if reply != "" {
		result["note"] = "request completed; the reply is included in this result"
	} else {
		result["note"] = fmt.Sprintf("request finished with status %q", task.Status)
	}
	return result
}
