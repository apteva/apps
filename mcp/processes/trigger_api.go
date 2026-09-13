package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func triggerSchema() map[string]any {
	return object([]string{"name", "source_install_id", "topic"}, map[string]any{
		"name": textField("Trigger name"), "source_install_id": map[string]any{"type": "integer", "minimum": 1}, "topic": textField("Published event name or prefix.*"),
		"filters":  map[string]any{"type": "array", "maxItems": 20, "items": object([]string{"path", "op"}, map[string]any{"path": textField("Event path, e.g. data.plan"), "op": map[string]any{"type": "string", "enum": []string{"eq", "neq", "exists", "contains", "gt", "gte", "lt", "lte"}}, "value": map[string]any{}})},
		"mappings": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Declared parameter key to event path, e.g. customer_id: data.id"},
	})
}
func (a *App) triggerTools() []sdk.Tool {
	names := []string{"trigger_sources", "triggers", "trigger_get", "trigger_create", "trigger_update", "trigger_activate", "trigger_pause", "trigger_preview", "trigger_test_run", "trigger_events", "trigger_event_retry"}
	out := []sdk.Tool{}
	for _, name := range names {
		name := name
		props := map[string]any{}
		required := []string{}
		if name != "trigger_sources" {
			props["process_id"] = textField("Process ID")
			required = append(required, "process_id")
		}
		if name == "triggers" || name == "trigger_create" || name == "trigger_update" {
			props["assignment_id"] = textField("Assignment ID")
			required = append(required, "assignment_id")
		}
		if name != "trigger_sources" && name != "triggers" && name != "trigger_create" {
			props["trigger_id"] = textField("Trigger ID")
			required = append(required, "trigger_id")
		}
		if name == "trigger_create" || name == "trigger_update" {
			props["trigger"] = triggerSchema()
			required = append(required, "trigger")
		}
		if name == "trigger_update" || name == "trigger_activate" || name == "trigger_pause" {
			props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
			required = append(required, "expected_revision")
		}
		if name == "trigger_preview" || name == "trigger_test_run" {
			props["event"] = map[string]any{"type": "object", "description": "Sample event envelope with topic and data"}
			required = append(required, "event")
		}
		if name == "trigger_test_run" || name == "trigger_event_retry" {
			props["idempotency_key"] = textField("Stable key for this explicit test execution")
			required = append(required, "idempotency_key")
		}
		if name == "trigger_event_retry" {
			props["event_record_id"] = textField("Failed event history ID to retry using current trigger rules")
			required = append(required, "event_record_id")
		}
		description := "Manage event triggers on process assignments. Create paused, preview sample events, then activate. Check sync_pending before claiming the listener is live."
		if name == "trigger_preview" {
			description = "Preview filters and mapped parameters without creating a run."
		}
		if name == "trigger_test_run" {
			description = "Explicitly start real work from a sample event. Requires an active assignment; use preview first. Reuse the idempotency key on retries."
		}
		out = append(out, sdk.Tool{Name: name, Description: description, InputSchema: object(required, props), HandlerCtx: func(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			caller := sdk.CallerFrom(ctx)
			if caller == nil || caller.AgentID <= 0 || caller.ProjectID == "" {
				return nil, errors.New("trusted agent and project required")
			}
			if fixed := a.ctx.CurrentProject(); fixed != "" && fixed != caller.ProjectID {
				return nil, errors.New("project scope mismatch")
			}
			return a.execute(caller.ProjectID, fmt.Sprintf("agent:%d:%s", caller.AgentID, caller.ThreadID), name, args)
		}})
	}
	return out
}
func (a *App) executeTrigger(project, action string, args map[string]any) (any, error) {
	process, id := str(args, "process_id"), str(args, "trigger_id")
	if action == "trigger_sources" {
		api := a.ctx.EventBusAPI()
		if api == nil {
			return nil, errors.New("event subscription API unavailable")
		}
		sources, e := api.ListAppEventSources(project)
		return map[string]any{"sources": sources}, e
	}
	if action == "triggers" {
		ts, e := a.triggers(project, process, str(args, "assignment_id"))
		return map[string]any{"triggers": ts}, e
	}
	if action == "trigger_create" || action == "trigger_update" {
		var c TriggerConfig
		raw, e := json.Marshal(args["trigger"])
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, e
		}
		if action == "trigger_create" {
			id = ""
		}
		return a.saveTrigger(project, process, str(args, "assignment_id"), id, number(args, "expected_revision"), c)
	}
	t, e := a.trigger(project, process, id)
	if e != nil {
		return nil, e
	}
	switch action {
	case "trigger_get":
		return t, nil
	case "trigger_activate", "trigger_pause":
		status := "paused"
		if action == "trigger_activate" {
			status = "active"
		}
		return a.setTriggerStatus(project, process, id, status, number(args, "expected_revision"))
	case "trigger_events":
		events, e := a.triggerHistory(t)
		return map[string]any{"events": events}, e
	case "trigger_event_retry":
		if t.Status != "active" {
			return nil, errors.New("activate the trigger before retrying an event")
		}
		var raw, state string
		if e := a.db.QueryRow(`SELECT event_json,status FROM process_trigger_events WHERE id=? AND trigger_id=? AND project_id=?`, str(args, "event_record_id"), t.ID, project).Scan(&raw, &state); e != nil {
			return nil, errNotFound
		}
		if state != "failed" {
			return nil, errors.New("only failed events can be retried")
		}
		key := str(args, "idempotency_key")
		if key == "" || len(key) > 160 {
			return nil, errors.New("retry idempotency_key required (max 160)")
		}
		var event sdk.Event
		if e = json.Unmarshal([]byte(raw), &event); e != nil {
			return nil, e
		}
		event.Data["retry_of"] = str(args, "event_record_id")
		event.Data["event_id"] = "retry:" + str(args, "event_record_id") + ":" + key
		event.Data["revision"] = float64(t.SubscriptionRevision)
		return a.consumeTriggerEvent(t, event, false)
	case "trigger_preview", "trigger_test_run":
		sample, ok := args["event"].(map[string]any)
		if !ok {
			return nil, errors.New("sample event object required")
		}
		sample = cloneMap(sample)
		sample["source_install_id"] = float64(t.Config.SourceInstallID)
		sample["project_id"] = project
		if !topicMatch(t.Config.Topic, str(sample, "topic")) {
			return map[string]any{"matched": false, "reason": "event topic does not match"}, nil
		}
		if action == "trigger_preview" {
			return a.triggerPreview(t, sample)
		}
		key := str(args, "idempotency_key")
		if key == "" || len(key) > 160 {
			return nil, errors.New("test idempotency_key required (max 160)")
		}
		sample["event_id"] = "test:" + key
		sample["subscription_key"] = t.ID
		return a.consumeTriggerEvent(t, sdk.Event{Event: sdk.AppBusDeliveryEvent, ProjectID: project, SourceInstallID: t.Config.SourceInstallID, DeliveryID: "test:" + key, Data: sample}, true)
	}
	return nil, errors.New("unknown trigger action")
}
func cloneMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Runtime-provided required inputs are allowed only for unscheduled assignments
// with a saved trigger mapping covering every missing required parameter.
func (a *App) assignmentParametersReady(d Definition, x Assignment) error {
	if _, err := validateParameters(d.Parameters, x.Parameters, false); err != nil {
		return err
	}
	_, err := validateParameters(d.Parameters, x.Parameters, true)
	if err == nil || x.Schedule != nil {
		return err
	}
	rows, e := a.db.Query(`SELECT config_json FROM process_triggers WHERE assignment_id=?`, x.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var c TriggerConfig
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &c) != nil {
			continue
		}
		covered := true
		for _, p := range d.Parameters {
			_, provided := x.Parameters[p.Key]
			if p.Required && !provided && p.Default == nil && strings.TrimSpace(c.Mappings[p.Key]) == "" {
				covered = false
			}
		}
		if covered {
			return nil
		}
	}
	return err
}
