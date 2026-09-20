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
	api := stringType("GraphQL API slug, for example commerce or analytics. Defaults to default.")
	return []sdk.Tool{
		{Name: "graphql_security_get", Description: "Get API authentication and field policies.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolSecurityGet},
		{Name: "graphql_security_set", Description: "Replace API security policy; does not modify Function trust settings.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "security": map[string]any{"type": "object"}}, "security"), HandlerCtx: a.toolSecuritySet},
		{Name: "graphql_security_validate", Description: "Validate security configuration and check Function caller trust without changing it.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolSecurityValidate},
		{Name: "graphql_api_create", Description: "Create a named GraphQL API endpoint.", InputSchema: object(map[string]any{"project_id": project, "slug": stringType("URL-safe API slug"), "name": stringType("Display name"), "description": stringType("Description")}, "slug"), HandlerCtx: a.toolAPICreate},
		{Name: "graphql_api_list", Description: "List named GraphQL APIs and endpoints.", InputSchema: object(map[string]any{"project_id": project}), HandlerCtx: a.toolAPIList},
		{Name: "graphql_api_get", Description: "Fetch a named GraphQL API endpoint.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolAPIGet},
		{Name: "graphql_schema_create", Description: "Create or replace a draft GraphQL schema for an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}, "sdl": stringType("GraphQL SDL")}, "sdl"), HandlerCtx: a.toolSchemaCreate},
		{Name: "graphql_schema_get", Description: "Fetch the active or requested GraphQL schema.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}}), HandlerCtx: a.toolSchemaGet},
		{Name: "graphql_schema_list", Description: "List GraphQL schema versions for an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment}), HandlerCtx: a.toolSchemaList},
		{Name: "graphql_schema_validate", Description: "Validate GraphQL SDL without storing it.", InputSchema: object(map[string]any{"sdl": stringType("GraphQL SDL")}, "sdl"), HandlerCtx: a.toolSchemaValidate},
		{Name: "graphql_schema_publish", Description: "Publish a validated GraphQL schema version.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolSchemaPublish},
		{Name: "graphql_release_publish", Description: "Atomically publish schema, sources, resolver bindings, security, limits, and pinned modules.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "schema_version": map[string]any{"type": "integer"}, "limits": map[string]any{"type": "object"}}, "schema_version"), HandlerCtx: a.toolReleasePublish},
		{Name: "graphql_release_list", Description: "List immutable API releases.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment}), HandlerCtx: a.toolReleaseList},
		{Name: "graphql_release_get", Description: "Get an API release and its complete snapshot.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolReleaseGet},
		{Name: "graphql_release_rollback", Description: "Atomically reactivate a previous API release.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolReleaseRollback},
		{Name: "graphql_module_create", Description: "Create or replace a typed, versioned reusable resolver module draft.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Dot-separated module name"), "version": map[string]any{"type": "integer"}, "description": stringType("Description"), "inputs": map[string]any{"type": "object"}, "output_type": stringType("String, ID, Int, Float, Boolean, JSON, Decimal, Date, DateTime, or Duration"), "definition": map[string]any{"type": "object"}, "deterministic": map[string]any{"type": "boolean"}, "null_behavior": map[string]any{"type": "string", "enum": []string{"propagate", "strict"}}, "decimal_precision": map[string]any{"type": "integer"}, "decimal_scale": map[string]any{"type": "integer"}, "rounding_mode": map[string]any{"type": "string", "enum": []string{"half_even", "half_up", "down"}}, "timezone": stringType("IANA timezone"), "completeness": map[string]any{"type": "string", "enum": []string{"complete", "partial"}}}, "name", "inputs", "output_type", "definition"), HandlerCtx: a.toolModuleCreate},
		{Name: "graphql_module_get", Description: "Get a resolver module version.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name"), "version": map[string]any{"type": "integer"}}, "name"), HandlerCtx: a.toolModuleGet},
		{Name: "graphql_module_list", Description: "List resolver modules and versions for an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolModuleList},
		{Name: "graphql_module_versions", Description: "List all versions of one resolver module.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name")}, "name"), HandlerCtx: a.toolModuleVersions},
		{Name: "graphql_module_validate", Description: "Validate a typed resolver module definition without saving it.", InputSchema: object(map[string]any{"inputs": map[string]any{"type": "object"}, "definition": map[string]any{"type": "object"}}, "inputs", "definition"), HandlerCtx: a.toolModuleValidate},
		{Name: "graphql_module_test", Description: "Evaluate a saved resolver module with test inputs.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name"), "version": map[string]any{"type": "integer"}, "inputs": map[string]any{"type": "object"}}, "name", "version", "inputs"), HandlerCtx: a.toolModuleTest},
		{Name: "graphql_module_test_batch", Description: "Evaluate a saved resolver module over a batch of input objects.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name"), "version": map[string]any{"type": "integer"}, "batch": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "maxItems": 10000}}, "name", "version", "batch"), HandlerCtx: a.toolModuleTestBatch},
		{Name: "graphql_module_publish", Description: "Validate and immutably publish a resolver module version.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name"), "version": map[string]any{"type": "integer"}}, "name", "version"), HandlerCtx: a.toolModulePublish},
		{Name: "graphql_module_usages", Description: "List module sources and field bindings using a resolver module.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Module name"), "version": map[string]any{"type": "integer"}}, "name"), HandlerCtx: a.toolModuleUsages},
		{Name: "graphql_source_add", Description: "Bind a database, tables, function, HTTP, or resolver-module source to an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "name": stringType("Stable source name"), "kind": map[string]any{"type": "string", "enum": []string{"database", "tables", "function", "http", "module"}}, "config": map[string]any{"type": "object"}}, "name", "kind"), HandlerCtx: a.toolSourceAdd},
		{Name: "graphql_source_list", Description: "List configured GraphQL API sources.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolSourceList},
		{Name: "graphql_resolver_set", Description: "Bind a typed GraphQL field to a source operation. Tables aggregate_pipeline runs one immutable, parameterized multi-table query.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "parent_type": stringType("GraphQL parent type"), "field_name": stringType("GraphQL field"), "source_id": map[string]any{"type": "integer"}, "source": stringType("Source name"), "operation": stringType("find, search, get, count, aggregate, aggregate_pipeline, function, request, or resolve"), "config": map[string]any{"type": "object"}}, "parent_type", "field_name", "operation"), HandlerCtx: a.toolResolverSet},
		{Name: "graphql_resolver_list", Description: "List GraphQL resolver bindings for an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api}), HandlerCtx: a.toolResolverList},
		{Name: "graphql_deploy", Description: "Promote a schema version to active in an environment.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "environment": environment, "version": map[string]any{"type": "integer"}}, "version"), HandlerCtx: a.toolSchemaPublish},
		{Name: "graphql_logs", Description: "List recent GraphQL request logs.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "limit": map[string]any{"type": "integer"}}), HandlerCtx: a.toolLogs},
		{Name: "graphql_event_publish", Description: "Publish a source event to connected GraphQL subscriptions for an API.", InputSchema: object(map[string]any{"project_id": project, "api_slug": api, "topic": stringType("Event topic"), "payload": map[string]any{"type": "object"}}, "topic"), HandlerCtx: a.toolEventPublish},
	}
}

func (a *App) toolAPICreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := createGraphQLAPI(ctx.AppDB(), project, stringArg(args, "slug", ""), stringArg(args, "name", ""), stringArg(args, "description", ""))
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, row.Slug)
	return map[string]any{"api": publicAPI(*row)}, nil
}

func (a *App) toolAPIList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listGraphQLAPIs(ctx.AppDB(), project)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicAPI(row))
	}
	return map[string]any{"apis": out, "count": len(out)}, nil
}

func (a *App) toolAPIGet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := resolveGraphQLAPI(ctx.AppDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	return map[string]any{"api": publicAPI(*row)}, nil
}

func (a *App) toolSchemaCreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	sdl, _ := args["sdl"].(string)
	env := normalizeEnvironment(stringArg(args, "environment", "development"))
	row, validationErrors, createErr := createSchemaForAPI(ctx.AppDB(), project, apiSlugArg(args), env, sdl, intArg(args, "version", 0))
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
	row, err := getSchemaForAPI(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"), intArg(args, "version", 0), intArg(args, "version", 0) == 0)
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
	rows, err := listSchemasForAPI(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"))
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
	args["schema_version"] = args["version"]
	return a.toolReleasePublish(callCtx, ctx, args)
}

func (a *App) toolReleasePublish(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	if err := a.verifyPublishSecurity(callCtx, project, apiSlugArg(args)); err != nil {
		return nil, err
	}
	row, err := publishAPIRelease(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"), intArg(args, "schema_version", intArg(args, "version", 0)), args["limits"])
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	return map[string]any{"release": publicAPIRelease(row, true), "deployed": true}, nil
}

func (a *App) toolReleaseList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listAPIReleases(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, publicAPIRelease(&rows[i], false))
	}
	return map[string]any{"releases": out, "count": len(out)}, nil
}

func (a *App) toolReleaseGet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := getAPIRelease(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"), intArg(args, "version", 0), false)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("release not found")
	}
	return map[string]any{"release": publicAPIRelease(row, true)}, nil
}

func (a *App) toolReleaseRollback(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	if err := a.verifyPublishSecurity(callCtx, project, apiSlugArg(args)); err != nil {
		return nil, err
	}
	row, err := rollbackAPIRelease(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "environment", "development"), intArg(args, "version", 0))
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	return map[string]any{"release": publicAPIRelease(row, true), "rolled_back": true}, nil
}

func (a *App) toolModuleCreate(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	inputs, err := jsonObject(args["inputs"])
	if err != nil {
		return nil, err
	}
	definition, err := jsonObject(args["definition"])
	if err != nil {
		return nil, err
	}
	deterministic := true
	if value, ok := args["deterministic"].(bool); ok {
		deterministic = value
	}
	row, err := createResolverModuleForAPI(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "name", ""), stringArg(args, "description", ""), stringArg(args, "output_type", ""), inputs, definition, intArg(args, "version", 0), deterministic)
	if err != nil {
		return nil, err
	}
	metadata, err := parseModuleMetadata(args)
	if err != nil {
		return nil, err
	}
	if err := setResolverModuleMetadata(ctx.AppDB(), row.ID, metadata); err != nil {
		return nil, err
	}
	row, err = getResolverModuleForAPI(ctx.AppReadDB(), project, apiSlugArg(args), row.Name, row.Version, false)
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	return map[string]any{"module": publicResolverModule(*row)}, nil
}

func (a *App) toolModuleGet(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := getResolverModuleForAPI(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "name", ""), intArg(args, "version", 0), false)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("resolver module not found")
	}
	return map[string]any{"module": publicResolverModule(*row)}, nil
}

func (a *App) toolModuleList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listResolverModulesForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, publicResolverModule(row))
	}
	return map[string]any{"modules": out, "count": len(out)}, nil
}

func (a *App) toolModuleVersions(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	result, err := a.toolModuleList(callCtx, ctx, args)
	if err != nil {
		return nil, err
	}
	name := stringArg(args, "name", "")
	all := result.(map[string]any)["modules"].([]map[string]any)
	out := []map[string]any{}
	for _, row := range all {
		if row["name"] == name {
			out = append(out, row)
		}
	}
	return map[string]any{"modules": out, "count": len(out)}, nil
}

func (a *App) toolModuleValidate(_ context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
	inputs, err := jsonObject(args["inputs"])
	if err != nil {
		return nil, err
	}
	definition, err := jsonObject(args["definition"])
	if err != nil {
		return nil, err
	}
	problems := validateResolverModuleDefinition(inputs, definition)
	return map[string]any{"valid": len(problems) == 0, "validation_errors": problems, "dependencies": moduleCallDependencies(definition)}, nil
}

func (a *App) toolModuleTest(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := getResolverModuleForAPI(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "name", ""), intArg(args, "version", 0), false)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, invalid("resolver module not found")
	}
	inputs, err := jsonObject(args["inputs"])
	if err != nil {
		return nil, err
	}
	rows, err := listResolverModulesForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	modules := map[string]resolverModule{}
	for _, module := range rows {
		modules[moduleKey(module.Name, module.Version)] = module
	}
	value, err := (moduleRuntime{modules: modules}).evaluate(*row, inputs)
	if err != nil {
		return nil, err
	}
	return map[string]any{"value": value, "module": publicResolverModule(*row)}, nil
}

func (a *App) toolModuleTestBatch(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := getResolverModuleForAPI(ctx.AppReadDB(), project, apiSlugArg(args), stringArg(args, "name", ""), intArg(args, "version", 0), false)
	if err != nil || row == nil {
		return nil, invalid("resolver module not found")
	}
	all, err := listResolverModulesForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	modules := map[string]resolverModule{}
	for _, module := range all {
		modules[moduleKey(module.Name, module.Version)] = module
	}
	raw, ok := args["batch"].([]any)
	if !ok {
		return nil, invalid("batch must be an array")
	}
	if len(raw) > 10000 {
		return nil, invalid("batch exceeds 10000 inputs")
	}
	values := make([]any, len(raw))
	runtime := moduleRuntime{modules: modules}
	for i, item := range raw {
		input, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("batch item %d must be an object", i)
		}
		values[i], err = runtime.evaluate(*row, input)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"values": values, "count": len(values), "module": publicResolverModule(*row)}, nil
}

func (a *App) toolModulePublish(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	row, err := publishResolverModuleForAPI(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "name", ""), intArg(args, "version", 0))
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	return map[string]any{"module": publicResolverModule(*row), "published": true}, nil
}

func (a *App) toolModuleUsages(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	name := stringArg(args, "name", "")
	version := intArg(args, "version", 0)
	sources, err := listSourcesForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	resolvers, err := listResolversForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	sourceIDs := map[int64]bool{}
	sourceRows := []map[string]any{}
	for _, source := range sources {
		if source.Kind == "module" && source.Config["module"] == name && (version == 0 || moduleInt(source.Config["version"]) == version) {
			sourceIDs[source.ID] = true
			sourceRows = append(sourceRows, publicSource(source))
		}
	}
	resolverRows := []map[string]any{}
	for _, resolver := range resolvers {
		if sourceIDs[resolver.SourceID] {
			var source *sourceRecord
			for i := range sources {
				if sources[i].ID == resolver.SourceID {
					source = &sources[i]
					break
				}
			}
			resolverRows = append(resolverRows, publicResolver(resolver, source))
		}
	}
	return map[string]any{"sources": sourceRows, "resolvers": resolverRows, "count": len(resolverRows)}, nil
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
	row, err := createSourceForAPI(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "name", ""), stringArg(args, "kind", ""), config)
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	return map[string]any{"source": publicSource(*row)}, nil
}

func (a *App) toolSourceList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listSourcesForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
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
		row, findErr := getSourceForAPI(ctx.AppReadDB(), project, apiSlugArg(args), 0, name)
		if findErr != nil {
			return nil, findErr
		}
		if row == nil {
			return nil, invalid("source not found")
		}
		sourceID = row.ID
	}
	row, err := upsertResolverForAPI(ctx.AppDB(), project, apiSlugArg(args), stringArg(args, "parent_type", ""), stringArg(args, "field_name", ""), stringArg(args, "operation", ""), sourceID, config)
	if err != nil {
		return nil, err
	}
	a.invalidateRuntime(project, apiSlugArg(args))
	source, _ := getSourceForAPI(ctx.AppReadDB(), project, apiSlugArg(args), row.SourceID, "")
	return map[string]any{"resolver": publicResolver(*row, source)}, nil
}

func (a *App) toolResolverList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := listResolversForAPI(ctx.AppReadDB(), project, apiSlugArg(args))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		source, _ := getSourceForAPI(ctx.AppReadDB(), project, apiSlugArg(args), row.SourceID, "")
		out = append(out, publicResolver(row, source))
	}
	return map[string]any{"resolvers": out, "count": len(out)}, nil
}

func (a *App) toolLogs(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	project, err := appProject(ctx, callCtx, args)
	if err != nil {
		return nil, err
	}
	rows, err := publicLogs(ctx.AppReadDB(), storageProject(project, apiSlugArg(args)), intArg(args, "limit", 100))
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
	count := a.hub.publishForAPI(project, apiSlugArg(args), topic, payload)
	return map[string]any{"published": true, "topic": topic, "delivered": count}, nil
}

func stringArg(args map[string]any, key, fallback string) string {
	if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func apiSlugArg(args map[string]any) string {
	if value := stringArg(args, "api_slug", ""); value != "" {
		return normalizeAPISlug(value)
	}
	if value := stringArg(args, "api", ""); value != "" {
		return normalizeAPISlug(value)
	}
	return "default"
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
