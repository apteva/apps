package main

// MCP tool handlers. Five tools: track (write) + query / count / top /
// topics (read). Track is the V1 ingest path — auto-capture from the
// platform firehose is a v0.2 feature.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// toolTrack — record one event. The caller passes the event name and
// optionally an `app` slug, user/session ids, props, and a back-dated
// ts. The project is always owned by the platform dispatch context, not
// by model-supplied args; otherwise an agent can accidentally scope data
// to arbitrary page slugs.
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

	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}

	ev := EventInsert{
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
	}
	validation, err := validateEventAgainstSpecs(ctx.AppDB(), ev)
	if err != nil {
		return nil, err
	}
	if validation.Reject {
		return validationFailure(ctx.AppDB(), ev, validation), nil
	}

	id, err := insertEvent(ctx.AppDB(), ev)
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
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
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
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
	n, err := countEvents(ctx.AppDB(), f)
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
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := topByPropsKey(ctx.AppDB(), f, by, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"top": rows, "by": by}, nil
}

func (a *App) toolTopics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listTopics(ctx.AppDB(), projectID, stringArg(args, "app"))
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
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := sumByValue(ctx.AppDB(), f, value, groupBy, intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"buckets": rows, "value": value}, nil
}

func (a *App) toolEventSpecsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	specs, err := listEventSpecs(ctx.AppDB(), specFilter{
		ProjectID: projectID,
		App:       stringArg(args, "app"),
		Status:    stringArg(args, "status"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"specs": specs}, nil
}

func (a *App) toolEventSpecsForApp(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	app := stringArg(args, "app")
	if app == "" {
		return nil, errors.New("app required")
	}
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	specs, err := listEventSpecs(ctx.AppDB(), specFilter{
		ProjectID: projectID,
		App:       app,
		Status:    "active",
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project_id": projectID,
		"app":        app,
		"specs":      specs,
		"guidance":   "Use analytics_track with app and event/topic from one of these specs. Do not pass project_id; the platform assigns it automatically. For policy-managed upsert topics, omit upsert_key because the declared bucket and dimensions compute it.",
	}, nil
}

func (a *App) toolEventSpecGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if id := int64Arg(args, "id"); id > 0 {
		spec, err := getEventSpecByID(ctx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		if err := ensureSpecInCurrentProject(ctx, args, spec.ProjectID); err != nil {
			return nil, err
		}
		return map[string]any{"spec": spec}, nil
	}
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	spec, err := getEventSpec(ctx.AppDB(), projectID, stringArg(args, "app"), stringArg(args, "topic"))
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
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	spec.ProjectID = projectID
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
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(`DELETE FROM event_specs WHERE id=? AND project_id=?`, id, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("event spec not found in current project")
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolEventPropertyUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	prop, err := propertyFromArgs(args)
	if err != nil {
		return nil, err
	}
	spec, err := getEventSpecByID(ctx.AppDB(), prop.EventSpecID)
	if err != nil {
		return nil, err
	}
	if err := ensureSpecInCurrentProject(ctx, args, spec.ProjectID); err != nil {
		return nil, err
	}
	if err := upsertEventPropertySpec(ctx.AppDB(), prop); err != nil {
		return nil, err
	}
	spec, err = getEventSpecByID(ctx.AppDB(), prop.EventSpecID)
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
	spec, err := getEventSpecByID(ctx.AppDB(), specID)
	if err != nil {
		return nil, err
	}
	if err := ensureSpecInCurrentProject(ctx, args, spec.ProjectID); err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`DELETE FROM event_property_specs WHERE event_spec_id=? AND key=?`, specID, key)
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
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	ev.ProjectID = projectID
	out, err := validateEventAgainstSpecs(ctx.AppDB(), ev)
	if err != nil {
		return nil, err
	}
	return validationResult(ctx.AppDB(), ev, out), nil
}

func (a *App) toolEventViolations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	f, err := scopedFilter(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listEventSpecViolations(ctx.AppDB(), f, intArg(args, "limit"))
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

func scopedFilter(ctx *sdk.AppCtx, args map[string]any) (Filter, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return Filter{}, err
	}
	f := filterFromArgs(args)
	f.ProjectID = projectID
	return f, nil
}

func scopedProject(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	current := ""
	if ctx != nil {
		current = strings.TrimSpace(ctx.CurrentProject())
	}
	if current == "" {
		return "", errors.New("analytics tools require a platform project context")
	}
	supplied := strings.TrimSpace(stringArg(args, "project_id"))
	if supplied != "" && supplied != current {
		return "", fmt.Errorf("project_id is assigned by the platform; got %q but current project is %q", supplied, current)
	}
	return current, nil
}

func ensureSpecInCurrentProject(ctx *sdk.AppCtx, args map[string]any, specProjectID string) error {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return err
	}
	if specProjectID != projectID {
		return fmt.Errorf("event spec belongs to project %q, current project is %q", specProjectID, projectID)
	}
	return nil
}

func validationResult(db *sql.DB, ev EventInsert, out validationOutcome) map[string]any {
	resp := map[string]any{
		"valid":      len(out.Violations) == 0,
		"reject":     out.Reject,
		"violations": out.Violations,
		"summary":    validationSummary(out.Violations),
		"ingest":     previewEventIngest(db, ev),
	}
	if spec, err := getEventSpec(db, ev.ProjectID, ev.App, ev.Topic); err == nil {
		resp["spec"] = spec.App + "." + spec.Topic
		resp["example"] = exampleEventForSpec(spec)
	}
	return resp
}

func validationFailure(db *sql.DB, ev EventInsert, out validationOutcome) map[string]any {
	resp := validationResult(db, ev, out)
	resp["error"] = firstViolationType(out.Violations)
	return resp
}

func validationSummary(violations []EventSpecViolation) map[string]any {
	summary := map[string]any{
		"error": firstViolationType(violations),
	}
	var missing []string
	for _, v := range violations {
		if v.ViolationType == "missing_required" && v.PropertyKey != "" {
			missing = append(missing, v.PropertyKey)
		}
	}
	if len(missing) > 0 {
		summary["missing"] = missing
	}
	return summary
}

func firstViolationType(violations []EventSpecViolation) string {
	if len(violations) == 0 {
		return ""
	}
	return violations[0].ViolationType
}

func exampleEventForSpec(spec *EventSpec) map[string]any {
	example := map[string]any{
		"app":   spec.App,
		"event": spec.Topic,
		"props": map[string]any{},
	}
	props := example["props"].(map[string]any)
	for _, prop := range spec.Properties {
		if !prop.Required {
			continue
		}
		value := exampleValue(prop)
		if strings.HasPrefix(prop.Key, "props.") {
			setNestedExample(props, strings.TrimPrefix(prop.Key, "props."), value)
			continue
		}
		example[prop.Key] = value
	}
	return example
}

func exampleValue(prop EventPropertySpec) any {
	if prop.ExampleValue != "" {
		return prop.ExampleValue
	}
	if len(prop.EnumValues) > 0 {
		return prop.EnumValues[0]
	}
	switch prop.Type {
	case "number", "timestamp":
		return 0
	case "boolean":
		return true
	case "object":
		return map[string]any{}
	case "array":
		return []any{}
	default:
		return "string"
	}
}

func setNestedExample(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	cur := root
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == len(parts)-1 {
			cur[part] = value
			return
		}
		next, ok := cur[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[part] = next
		}
		cur = next
	}
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
