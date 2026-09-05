package main

import (
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) objectiveTools() []sdk.Tool {
	querySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"aggregation":        map[string]any{"type": "string", "enum": []string{"count", "distinct", "sum", "sum_money", "average", "min", "max", "latest", "change"}},
			"app":                map[string]any{"type": "string"},
			"topic":              map[string]any{"type": "string"},
			"source":             map[string]any{"type": "string", "enum": []string{"track", "auto", "web", "bus", "rollup"}},
			"value":              map[string]any{"type": "string", "description": "Numeric field for sum, sum_money, average, min, max, latest or change."},
			"by":                 map[string]any{"type": "string", "description": "Field for distinct, for example session_id or props.subscriber_id."},
			"where":              map[string]any{"type": "object", "description": "Equality filters keyed by props.X."},
			"currency_field":     map[string]any{"type": "string", "description": "Currency property for sum_money, for example props.currency."},
			"reporting_currency": map[string]any{"type": "string", "description": "Target currency for sum_money, for example EUR."},
			"amount_unit":        map[string]any{"type": "string", "enum": []string{"minor", "major"}, "description": "Unit stored in value for sum_money."},
			"rate_date_field":    map[string]any{"type": "string", "description": "Optional props.X date used to select the historical FX rate; event time is the default."},
		},
		"required": []string{"aggregation"},
	}
	targetSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":         map[string]any{"type": "string"},
			"metric_key":   map[string]any{"type": "string"},
			"target_value": map[string]any{"type": "number"},
			"unit":         map[string]any{"type": "string", "enum": []string{"money", "count", "percent", "number"}},
			"currency":     map[string]any{"type": "string", "description": "Required for money, for example USD."},
			"direction":    map[string]any{"type": "string", "enum": []string{"at_least", "at_most"}},
			"period_start": map[string]any{"type": "integer", "description": "Inclusive Unix milliseconds."},
			"period_end":   map[string]any{"type": "integer", "description": "Exclusive Unix milliseconds."},
			"timezone":     map[string]any{"type": "string"},
			"query":        querySchema,
		},
		"required": []string{"name", "target_value", "unit", "direction", "period_start", "period_end", "query"},
	}
	writeProps := map[string]any{
		"name":        map[string]any{"type": "string"},
		"description": map[string]any{"type": "string"},
		"owner_type":  map[string]any{"type": "string", "enum": []string{"user", "agent", "team"}},
		"owner_id":    map[string]any{"type": "string"},
		"status":      map[string]any{"type": "string", "enum": []string{"draft", "active", "paused"}},
		"targets":     map[string]any{"type": "array", "items": targetSchema},
	}
	return []sdk.Tool{
		{
			Name:        "analytics_objectives_create",
			Description: "Create an objective with one or more targets over data already ingested into the current project's Analytics event store. Queries support count, distinct, sum, sum_money, average, min, max, latest and change; project and period scope come from the platform and target.",
			InputSchema: schemaObject(writeProps, []string{"name", "targets"}),
			Handler:     a.toolObjectiveCreate,
		},
		{
			Name:        "analytics_objectives_get",
			Description: "Get one current-project objective, its targets and cached progress.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolObjectiveGet,
		},
		{
			Name:        "analytics_objectives_search",
			Description: "Search current-project objectives. Args status?, search?, include_archived?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"status":           map[string]any{"type": "string"},
				"search":           map[string]any{"type": "string"},
				"include_archived": map[string]any{"type": "boolean"},
				"limit":            map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolObjectiveSearch,
		},
		{
			Name:        "analytics_objectives_update",
			Description: "Update a current-project objective. Metadata fields are optional; passing targets replaces the target set atomically.",
			InputSchema: schemaObject(mergeSchemaProps(writeProps, map[string]any{"id": map[string]any{"type": "integer"}}), []string{"id"}),
			Handler:     a.toolObjectiveUpdate,
		},
		{
			Name:        "analytics_objectives_archive",
			Description: "Archive one current-project objective without deleting its history.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolObjectiveArchive,
		},
		{
			Name:        "analytics_objective_progress",
			Description: "Evaluate every target in one current-project objective against Analytics events for its stored period, cache the result, and return actual versus target progress.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolObjectiveProgress,
		},
		{
			Name:        "analytics_objective_metrics_list",
			Description: "List built-in objective metric query templates. These are Analytics queries, not external app integrations.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolObjectiveMetricsList,
		},
	}
}

func mergeSchemaProps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func objectiveWriteFromArgs(args map[string]any) (ObjectiveWrite, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return ObjectiveWrite{}, err
	}
	var in ObjectiveWrite
	if err := json.Unmarshal(raw, &in); err != nil {
		return ObjectiveWrite{}, fmt.Errorf("invalid objective: %w", err)
	}
	return in, nil
}

func (a *App) toolObjectiveCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	in, err := objectiveWriteFromArgs(args)
	if err != nil {
		return nil, err
	}
	o, err := createObjective(toolWriter(ctx), projectID, in)
	if err != nil {
		return nil, err
	}
	progress, err := toolObjectiveProgress(ctx, projectID, o.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objective": o, "progress": progress}, nil
}

func (a *App) toolObjectiveGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	o, err := getObjective(toolReader(ctx), projectID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objective": o}, nil
}

func (a *App) toolObjectiveSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listObjectives(toolReader(ctx), projectID, stringArg(args, "status"), stringArg(args, "search"), boolArg(args, "include_archived"), intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"objectives": rows}, nil
}

func (a *App) toolObjectiveUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	current, err := getObjective(toolWriter(ctx), projectID, id)
	if err != nil {
		return nil, err
	}
	in, err := objectiveWriteFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, ok := args["name"]; !ok {
		in.Name = current.Name
	}
	if _, ok := args["description"]; !ok {
		in.Description = current.Description
	}
	if _, ok := args["owner_type"]; !ok {
		in.OwnerType = current.OwnerType
	}
	if _, ok := args["owner_id"]; !ok {
		in.OwnerID = current.OwnerID
	}
	if _, ok := args["status"]; !ok {
		in.Status = current.Status
	}
	if _, ok := args["targets"]; !ok {
		in.Targets = nil
	}
	o, err := updateObjective(toolWriter(ctx), projectID, id, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objective": o}, nil
}

func (a *App) toolObjectiveArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	if err := archiveObjective(toolWriter(ctx), projectID, id); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolObjectiveProgress(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := scopedProject(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id <= 0 {
		return nil, errors.New("id required")
	}
	progress, err := toolObjectiveProgress(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"objective_id": id, "progress": progress}, nil
}

func (a *App) toolObjectiveMetricsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := scopedProject(ctx, args); err != nil {
		return nil, err
	}
	return map[string]any{"metrics": objectiveMetricDefinitions()}, nil
}
