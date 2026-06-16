package main

// MCP tool handlers. Five tools: track (write) + query / count / top /
// topics (read). Track is the V1 ingest path — auto-capture from the
// platform firehose is a v0.2 feature.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolTrack — record one event. The caller passes the event name and
// optionally an `app` slug, project_id, user/session ids, props, and a
// back-dated ts. We don't currently derive the caller's app from the
// MCP token — the calling install just sends it. Trust-but-verify is
// fine at this scope; analytics is for aggregates, not audit.
func (a *App) toolTrack(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	event := stringArg(args, "event")
	if event == "" {
		return nil, errors.New("event required")
	}

	app := stringArg(args, "app")
	if app == "" {
		app = "_explicit"
	}

	ts := int64Arg(args, "ts")
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}

	propsJSON := "{}"
	if raw, ok := args["props"]; ok && raw != nil {
		// Re-marshal so we store a normalized string. Reject anything
		// that doesn't round-trip — analytics_track gets fed by other
		// apps and a bad payload should fail loudly, not silently.
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("props not JSON-encodable: %w", err)
		}
		// Top-level must be an object — keeps json_extract(props, '$.X')
		// working uniformly. Arrays / scalars get wrapped under "value".
		if len(b) > 0 && b[0] != '{' {
			b, _ = json.Marshal(map[string]any{"value": raw})
		}
		propsJSON = string(b)
	}

	projectID := stringArg(args, "project_id")
	if projectID == "" {
		projectID = ctx.CurrentProject()
	}

	id, err := insertEvent(ctx.AppDB(), EventInsert{
		TS:        ts,
		App:       app,
		Topic:     event,
		ProjectID: projectID,
		InstallID: int64Arg(args, "install_id"),
		UserID:    stringArg(args, "user_id"),
		SessionID: stringArg(args, "session_id"),
		Source:    "track",
		UpsertKey: stringArg(args, "upsert_key"),
		Props:     propsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}

	// Announce on the app-event bus so live panels (and other apps) can
	// react. analytics is global, so the event's project is a property
	// of the data (its project_id), not the dispatch ctx — fall back to
	// the calling context when the caller didn't supply one. Fire-and-
	// forget; a missed fanout is recovered by the dashboard reconnecting.
	ctx.EmitWithProject("event.recorded", projectID, map[string]any{
		"id": id, "app": app, "topic": event, "ts": ts,
	})

	return map[string]any{"id": id, "ts": ts}, nil
}

// toolQuery — read events. Without group_by, returns the most recent
// rows first. With group_by, returns aggregated buckets sorted by
// count desc.
func (a *App) toolQuery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	f := filterFromArgs(args)
	limit := intArg(args, "limit")

	if gb, ok := args["group_by"]; ok && gb != nil {
		keys, err := stringSlice(gb)
		if err != nil {
			return nil, fmt.Errorf("group_by: %w", err)
		}
		if len(keys) > 0 {
			buckets, err := queryGrouped(ctx.AppDB(), f, keys, limit)
			if err != nil {
				return nil, err
			}
			return map[string]any{"buckets": buckets}, nil
		}
	}

	rows, err := queryRows(ctx.AppDB(), f, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": rows, "count": len(rows)}, nil
}

func (a *App) toolCount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	n, err := countEvents(ctx.AppDB(), filterFromArgs(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": n}, nil
}

func (a *App) toolTop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	by := stringArg(args, "by")
	if by == "" {
		return nil, errors.New("by required (e.g. \"props.platform\")")
	}
	rows, err := topByPropsKey(ctx.AppDB(), filterFromArgs(args), by, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"top": rows, "by": by}, nil
}

func (a *App) toolTopics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := listTopics(ctx.AppDB(), stringArg(args, "app"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"topics": rows}, nil
}

func (a *App) toolSum(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	value := stringArg(args, "value")
	if value == "" {
		return nil, errors.New("value required (e.g. \"props.views\")")
	}
	var groupBy []string
	if raw, ok := args["group_by"]; ok && raw != nil {
		keys, err := stringSlice(raw)
		if err != nil {
			return nil, fmt.Errorf("group_by: %w", err)
		}
		groupBy = keys
	}
	rows, err := sumByValue(ctx.AppDB(), filterFromArgs(args), value, groupBy, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"buckets": rows, "value": value}, nil
}

func (a *App) toolEventSpecsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	specs, err := listEventSpecs(ctx.AppDB(), specFilter{
		ProjectID: stringArg(args, "project_id"),
		App:       stringArg(args, "app"),
		Status:    stringArg(args, "status"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"specs": specs}, nil
}

func (a *App) toolEventSpecGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if id := int64Arg(args, "id"); id > 0 {
		spec, err := getEventSpecByID(ctx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"spec": spec}, nil
	}
	spec, err := getEventSpec(ctx.AppDB(), stringArg(args, "project_id"), stringArg(args, "app"), stringArg(args, "topic"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"spec": spec}, nil
}

func (a *App) toolEventSpecUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spec, err := specFromArgs(args)
	if err != nil {
		return nil, err
	}
	if spec.ProjectID == "" {
		spec.ProjectID = ctx.CurrentProject()
	}
	saved, err := upsertEventSpec(ctx.AppDB(), spec, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"spec": saved}, nil
}

func (a *App) toolEventSpecDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM event_specs WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolEventPropertyUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	prop, err := propertyFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := upsertEventPropertySpec(ctx.AppDB(), prop); err != nil {
		return nil, err
	}
	spec, err := getEventSpecByID(ctx.AppDB(), prop.EventSpecID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"spec": spec}, nil
}

func (a *App) toolEventPropertyDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	specID := int64Arg(args, "event_spec_id")
	key := stringArg(args, "key")
	if specID <= 0 || key == "" {
		return nil, errors.New("event_spec_id and key required")
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM event_property_specs WHERE event_spec_id=? AND key=?`, specID, key)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolEventValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ev, err := eventInsertFromArgs(args)
	if err != nil {
		return nil, err
	}
	if ev.ProjectID == "" {
		ev.ProjectID = ctx.CurrentProject()
	}
	out, err := validateEventAgainstSpecs(ctx.AppDB(), ev)
	if err != nil {
		return nil, err
	}
	return map[string]any{"valid": len(out.Violations) == 0, "reject": out.Reject, "violations": out.Violations, "ingest": previewEventIngest(ctx.AppDB(), ev)}, nil
}

func (a *App) toolEventViolations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := listEventSpecViolations(ctx.AppDB(), filterFromArgs(args), intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"violations": rows}, nil
}

// ─── arg helpers ──────────────────────────────────────────────────

func filterFromArgs(args map[string]any) Filter {
	f := Filter{
		App:       stringArg(args, "app"),
		Topic:     stringArg(args, "topic"),
		ProjectID: stringArg(args, "project_id"),
		Source:    stringArg(args, "source"),
		Since:     int64Arg(args, "since"),
		Until:     int64Arg(args, "until"),
	}
	if w, ok := args["where"].(map[string]any); ok {
		f.Where = w
	}
	return f
}

func stringArg(args map[string]any, name string) string {
	v, ok := args[name]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intArg(args map[string]any, name string) int {
	return int(int64Arg(args, name))
}

func int64Arg(args map[string]any, name string) int64 {
	v, ok := args[name]
	if !ok || v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func stringSlice(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	out := make([]string, 0, len(arr))
	for i, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("[%d] expected string, got %T", i, x)
		}
		out = append(out, s)
	}
	return out, nil
}

func specFromArgs(args map[string]any) (EventSpec, error) {
	spec := EventSpec{
		ID:             int64Arg(args, "id"),
		ProjectID:      stringArg(args, "project_id"),
		App:            stringArg(args, "app"),
		Topic:          stringArg(args, "topic"),
		Kind:           stringArg(args, "kind"),
		DisplayName:    stringArg(args, "display_name"),
		Description:    stringArg(args, "description"),
		Category:       stringArg(args, "category"),
		Status:         stringArg(args, "status"),
		ValidationMode: stringArg(args, "validation_mode"),
		IngestMode:     stringArg(args, "ingest_mode"),
		CreatedBy:      stringArg(args, "created_by"),
	}
	if spec.Topic == "" {
		spec.Topic = stringArg(args, "event")
	}
	if raw, ok := args["properties"]; ok && raw != nil {
		props, err := propertySlice(raw)
		if err != nil {
			return spec, err
		}
		spec.Properties = props
	}
	if raw, ok := args["upsert_policy"]; ok && raw != nil {
		policy, err := ingestPolicyFromAny(raw)
		if err != nil {
			return spec, fmt.Errorf("upsert_policy: %w", err)
		}
		spec.UpsertPolicy = policy
	}
	if raw, ok := args["rollup_policy"]; ok && raw != nil {
		policy, err := ingestPolicyFromAny(raw)
		if err != nil {
			return spec, fmt.Errorf("rollup_policy: %w", err)
		}
		spec.RollupPolicy = policy
	}
	return spec, nil
}

func ingestPolicyFromAny(raw any) (*EventIngestPolicy, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var policy EventIngestPolicy
	if err := json.Unmarshal(b, &policy); err != nil {
		return nil, err
	}
	normalizeIngestPolicy(&policy)
	return &policy, nil
}

func propertySlice(v any) ([]EventPropertySpec, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("properties expected array, got %T", v)
	}
	out := make([]EventPropertySpec, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("properties[%d] expected object, got %T", i, item)
		}
		prop, err := propertyFromArgs(m)
		if err != nil {
			return nil, fmt.Errorf("properties[%d]: %w", i, err)
		}
		out = append(out, prop)
	}
	return out, nil
}

func propertyFromArgs(args map[string]any) (EventPropertySpec, error) {
	prop := EventPropertySpec{
		ID:                int64Arg(args, "id"),
		EventSpecID:       int64Arg(args, "event_spec_id"),
		Key:               stringArg(args, "key"),
		Type:              stringArg(args, "type"),
		Required:          boolArg(args, "required"),
		Description:       stringArg(args, "description"),
		PIIClassification: stringArg(args, "pii_classification"),
		ExampleValue:      stringArg(args, "example_value"),
	}
	if raw, ok := args["enum_values"]; ok && raw != nil {
		vals, err := stringSlice(raw)
		if err != nil {
			return prop, fmt.Errorf("enum_values: %w", err)
		}
		prop.EnumValues = vals
	}
	return prop, nil
}

func eventInsertFromArgs(args map[string]any) (EventInsert, error) {
	propsJSON := "{}"
	if raw, ok := args["props"]; ok && raw != nil {
		b, err := json.Marshal(raw)
		if err != nil {
			return EventInsert{}, err
		}
		if len(b) > 0 && b[0] != '{' {
			b, _ = json.Marshal(map[string]any{"value": raw})
		}
		propsJSON = string(b)
	}
	topic := stringArg(args, "topic")
	if topic == "" {
		topic = stringArg(args, "event")
	}
	app := stringArg(args, "app")
	if app == "" {
		app = "_explicit"
	}
	return EventInsert{
		TS:        int64Arg(args, "ts"),
		App:       app,
		Topic:     topic,
		ProjectID: stringArg(args, "project_id"),
		UserID:    stringArg(args, "user_id"),
		SessionID: stringArg(args, "session_id"),
		UpsertKey: stringArg(args, "upsert_key"),
		Source:    stringArg(args, "source"),
		Props:     propsJSON,
	}, nil
}

func boolArg(args map[string]any, name string) bool {
	v, ok := args[name]
	if !ok || v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}
