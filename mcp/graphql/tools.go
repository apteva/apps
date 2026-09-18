package main

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func graphqlTools(a *App) []sdk.Tool {
	object := func(properties map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	stringType := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	project := stringType("Project context; server-side caller context is authoritative.")
	environment := stringType("Environment, for example development, staging, or production.")
	return []sdk.Tool{
		{Name: "graphql_schema_create", Description: "Create or replace a draft GraphQL schema.", InputSchema: object(map[string]any{"project_id": project, "environment": environment, "version": map[string]any{"type": "integer"}, "sdl": stringType("GraphQL SDL")}, "sdl"), HandlerCtx: a.toolSchemaCreate},
		{Name: "graphql_schema_get", Description: "Fetch the active or requested GraphQL schema.", InputSchema: object(map[string]any{"project_id": project, "environment": environment, "version": map[string]any{"type": "integer"}}), HandlerCtx: a.toolSchemaGet},
		{Name: "graphql_schema_list", Description: "List GraphQL schema versions.", InputSchema: object(map[string]any{"project_id": project, "environment": environment}), HandlerCtx: a.toolSchemaList},
		{Name: "graphql_schema_validate", Description: "Validate GraphQL SDL without storing it.", InputSchema: object(map[string]any{"sdl": stringType("GraphQL SDL")}, "sdl"), HandlerCtx: a.toolSchemaValidate},
		{Name: "graphql_schema_publish", Description: "Publish a validated GraphQL schema version.", InputSchema: object(map[string]any{"project_id": project, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolSchemaPublish},
		{Name: "graphql_source_add", Description: "Bind a database, tables, function, or HTTP source.", InputSchema: object(map[string]any{"project_id": project, "name": stringType("Stable source name"), "kind": map[string]any{"type": "string", "enum": []string{"database", "tables", "function", "http"}}, "config": map[string]any{"type": "object"}}, "name", "kind"), HandlerCtx: a.toolSourceAdd},
		{Name: "graphql_source_list", Description: "List configured GraphQL sources.", InputSchema: object(map[string]any{"project_id": project}), HandlerCtx: a.toolSourceList},
		{Name: "graphql_resolver_set", Description: "Bind a GraphQL field to a source operation, including aggregate.", InputSchema: object(map[string]any{"project_id": project, "parent_type": stringType("GraphQL parent type"), "field_name": stringType("GraphQL field"), "source_id": map[string]any{"type": "integer"}, "source": stringType("Source name"), "operation": stringType("find, get, count, aggregate, function, or request"), "config": map[string]any{"type": "object"}}, "parent_type", "field_name", "operation"), HandlerCtx: a.toolResolverSet},
		{Name: "graphql_resolver_list", Description: "List GraphQL resolver bindings.", InputSchema: object(map[string]any{"project_id": project}), HandlerCtx: a.toolResolverList},
		{Name: "graphql_deploy", Description: "Promote a schema version to active in an environment.", InputSchema: object(map[string]any{"project_id": project, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolSchemaPublish},
		{Name: "graphql_logs", Description: "List recent GraphQL request logs.", InputSchema: object(map[string]any{"project_id": project, "limit": map[string]any{"type": "integer"}}), HandlerCtx: a.toolLogs},
		{Name: "graphql_event_publish", Description: "Publish a source event to connected GraphQL subscriptions.", InputSchema: object(map[string]any{"project_id": project, "topic": stringType("Event topic"), "payload": map[string]any{"type": "object"}}, "topic"), HandlerCtx: a.toolEventPublish},
	}
}

func (a *App) toolSchemaCreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	sdl, _ := args["sdl"].(string)
	env := normalizeEnvironment(stringArg(args, "environment", "development"))
	row, validationErrors, createErr := createSchema(ctx.AppDB(), project, env, sdl, intArg(args, "version", 0))
	if createErr != nil && row == nil && len(validationErrors) == 0 {
		return nil, createErr
	}
	return map[string]any{"schema": publicSchema(row), "valid": len(validationErrors) == 0, "validation_errors": validationErrors}, nil
}

func (a *App) toolSchemaGet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := getSchema(ctx.AppReadDB(), project, stringArg(args, "environment", "development"), intArg(args, "version", 0), intArg(args, "version", 0) == 0)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("schema not found")
	}
	return map[string]any{"schema": publicSchema(row)}, nil
}

func (a *App) toolSchemaList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listSchemas(ctx.AppReadDB(), project, stringArg(args, "environment", "development"))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicSchema(&row))
	}
	return map[string]any{"schemas": out, "count": len(out)}, nil
}

func (a *App) toolSchemaValidate(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	sdl, _ := args["sdl"].(string)
	_, errors := validateSDL(sdl)
	return map[string]any{"valid": len(errors) == 0, "validation_errors": errors}, nil
}

func (a *App) toolSchemaPublish(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := publishSchema(ctx.AppDB(), project, stringArg(args, "environment", "development"), intArg(args, "version", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"schema": publicSchema(row), "deployed": true}, nil
}

func (a *App) toolSourceAdd(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	config, err := jsonObject(args["config"])
	if err != nil {
		return nil, err
	}
	row, err := createSource(ctx.AppDB(), project, stringArg(args, "name", ""), stringArg(args, "kind", ""), config)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source": publicSource(*row)}, nil
}

func (a *App) toolSourceList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listSources(ctx.AppReadDB(), project)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicSource(row))
	}
	return map[string]any{"sources": out, "count": len(out)}, nil
}

func (a *App) toolResolverSet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	config, err := jsonObject(args["config"])
	if err != nil {
		return nil, err
	}
	sourceID := int64(intArg(args, "source_id", 0))
	if sourceID == 0 {
		name := strings.TrimSpace(stringArg(args, "source", ""))
		row, findErr := getSource(ctx.AppReadDB(), project, 0, name)
		if findErr != nil {
			return nil, findErr
		}
		if row == nil {
			return nil, invalid("source not found")
		}
		sourceID = row.ID
	}
	row, err := upsertResolver(ctx.AppDB(), project, stringArg(args, "parent_type", ""), stringArg(args, "field_name", ""), stringArg(args, "operation", ""), sourceID, config)
	if err != nil {
		return nil, err
	}
	source, _ := getSource(ctx.AppReadDB(), project, row.SourceID, "")
	return map[string]any{"resolver": publicResolver(*row, source)}, nil
}

func (a *App) toolResolverList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listResolvers(ctx.AppReadDB(), project)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		source, _ := getSource(ctx.AppReadDB(), project, row.SourceID, "")
		out = append(out, publicResolver(row, source))
	}
	return map[string]any{"resolvers": out, "count": len(out)}, nil
}

func (a *App) toolLogs(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := publicLogs(ctx.AppReadDB(), project, intArg(args, "limit", 100))
	if err != nil {
		return nil, err
	}
	return map[string]any{"logs": rows, "count": len(rows)}, nil
}

func (a *App) toolEventPublish(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	payload, err := jsonObject(args["payload"])
	if err != nil {
		return nil, err
	}
	topic := strings.TrimSpace(stringArg(args, "topic", ""))
	if topic == "" {
		return nil, invalid("topic is required")
	}
	count := a.hub.publish(project, topic, payload)
	return map[string]any{"published": true, "topic": topic, "delivered": count}, nil
}

func stringArg(args map[string]any, key, fallback string) string {
	if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	}
	return fallback
}
