package main

import (
	"encoding/json"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "api_create",
			Description: "Create an API and optionally expose a hostname. Args: slug, name?, description?, hostname?, dns_mode? (manual|domains|skipped), allow_http?, cors?, auth?.",
			InputSchema: schemaObject(map[string]any{
				"project_id":  map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"hostname":    map[string]any{"type": "string"},
				"dns_mode":    map[string]any{"type": "string"},
				"allow_http":  map[string]any{"type": "boolean"},
				"cors":        corsPolicySchema(),
				"auth":        map[string]any{"type": "object"},
			}, []string{"slug"}),
			Handler: a.toolAPICreate,
		},
		{Name: "api_get", Description: "Fetch one API by id or slug.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "integer"},
			"slug":       map[string]any{"type": "string"},
		}, nil), Handler: a.toolAPIGet},
		{Name: "api_list", Description: "List APIs in this project.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
		}, nil), Handler: a.toolAPIList},
		{Name: "api_update", Description: "Update an API. Args: id or slug plus name?, description?, status?, hostname?, dns_mode?, allow_http?, cors?, auth?.", InputSchema: schemaObject(map[string]any{
			"project_id":  map[string]any{"type": "string"},
			"id":          map[string]any{"type": "integer"},
			"slug":        map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"status":      map[string]any{"type": "string"},
			"hostname":    map[string]any{"type": "string"},
			"dns_mode":    map[string]any{"type": "string"},
			"allow_http":  map[string]any{"type": "boolean"},
			"cors":        corsPolicySchema(),
			"auth":        map[string]any{"type": "object"},
		}, nil), Handler: a.toolAPIUpdate},
		{Name: "api_delete", Description: "Delete an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "integer"},
			"slug":       map[string]any{"type": "string"},
		}, nil), Handler: a.toolAPIDelete},
		{
			Name:        "api_route_add",
			Description: "Add or update a route. target_kind is function, app, http, or app_events. For app_events, target_ref is the source app and events must contain topics, optional exact or bounded-in data.* match fields, a safe output object with optional constrained $data.* projections, and optional coalesce_ms.",
			InputSchema: schemaObject(map[string]any{
				"project_id":   map[string]any{"type": "string"},
				"api_id":       map[string]any{"type": "integer"},
				"api_slug":     map[string]any{"type": "string"},
				"method":       map[string]any{"type": "string"},
				"path_pattern": map[string]any{"type": "string"},
				"target_kind":  map[string]any{"type": "string"},
				"target_ref":   map[string]any{"type": "string"},
				"target_path":  map[string]any{"type": "string"},
				"events":       map[string]any{"type": "object"},
				"auth":         map[string]any{"type": "object"},
				"cors":         corsPolicySchema(),
				"timeout_ms":   map[string]any{"type": "integer"},
				"priority":     map[string]any{"type": "integer"},
				"enabled":      map[string]any{"type": "boolean"},
			}, []string{"method", "path_pattern", "target_kind", "target_ref"}),
			Handler: a.toolRouteAdd,
		},
		{Name: "api_route_list", Description: "List routes for an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolRouteList},
		{Name: "api_route_delete", Description: "Delete a route by id.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolRouteDelete},
		{Name: "api_key_create", Description: "Create an API key. Returns plaintext key once. Args: api_id or api_slug, name.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
			"name":       map[string]any{"type": "string"},
		}, []string{"name"}), Handler: a.toolKeyCreate},
		{Name: "api_key_list", Description: "List API keys for an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolKeyList},
		{Name: "api_key_revoke", Description: "Revoke an API key by id.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolKeyRevoke},
		{Name: "api_logs", Description: "List recent request logs for an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
			"limit":      map[string]any{"type": "integer"},
			"before_id":  map[string]any{"type": "integer", "minimum": 1},
		}, nil), Handler: a.toolLogs},
	}
}

func (a *App) toolAPICreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	corsJSON, err := jsonTextArg(args, "cors", "{}")
	if err != nil {
		return nil, err
	}
	if _, err := parseEffectiveCORSPolicy(corsJSON, "{}"); err != nil {
		return nil, err
	}
	authJSON, err := normalizedAuthArg(args, "auth", "{}")
	if err != nil {
		return nil, err
	}
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{
		ProjectID:   pid,
		Slug:        stringArg(args, "slug", ""),
		Name:        stringArg(args, "name", ""),
		Description: stringArg(args, "description", ""),
		Hostname:    stringArg(args, "hostname", ""),
		DNSMode:     stringArg(args, "dns_mode", "manual"),
		AllowHTTP:   boolArg(args, "allow_http", false),
		CORSJSON:    corsJSON,
		AuthJSON:    authJSON,
	})
	if err != nil {
		return nil, err
	}
	a.configureExposure(ctx, api)
	api, _ = dbGetAPIByID(ctx.AppDB(), pid, api.ID)
	out := map[string]any{"api": api}
	attempted, syncErr := syncAPIBrowserOriginPolicy(ctx, api)
	recordBrowserOriginSync(out, attempted, syncErr)
	return out, nil
}

func (a *App) toolAPIGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	return map[string]any{"api": api}, err
}

func (a *App) toolAPIList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	rows, err := dbListAPIs(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"apis": rows, "count": len(rows)}, nil
}

func (a *App) toolAPIUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	if _, ok := args["cors"]; ok {
		corsJSON, err := jsonTextArg(args, "cors", api.CORSJSON)
		if err != nil {
			return nil, err
		}
		if _, err := parseEffectiveCORSPolicy(corsJSON, "{}"); err != nil {
			return nil, err
		}
		// dbUpdateAPI accepts structured values. Preserve a caller-supplied JSON
		// string as an object rather than storing it as a quoted JSON string.
		args["cors"] = json.RawMessage(corsJSON)
	}
	candidate := *api
	if _, ok := args["auth"]; ok {
		raw, err := normalizedAuthArg(args, "auth", api.AuthJSON)
		if err != nil {
			return nil, err
		}
		args["auth"] = json.RawMessage(raw)
		candidate.AuthJSON = raw
	}
	if _, ok := args["cors"]; ok {
		raw, err := jsonTextArg(args, "cors", api.CORSJSON)
		if err != nil {
			return nil, err
		}
		candidate.CORSJSON = raw
	}
	if err := validateEffectivePolicies(ctx.AppDB(), &candidate, nil, 0); err != nil {
		return nil, err
	}
	updated, err := dbUpdateAPI(ctx.AppDB(), api.ProjectID, api.ID, args)
	if err != nil {
		return nil, err
	}
	a.streams.cancelMatching(api.ProjectID, api.ID, 0, 0)
	if api.Hostname != "" && (updated.Hostname != api.Hostname || updated.Status != "active") {
		if err := queueExposureCleanup(ctx, api); err != nil {
			return nil, err
		}
	}
	a.configureExposure(ctx, updated)
	updated, _ = dbGetAPIByID(ctx.AppDB(), api.ProjectID, api.ID)
	out := map[string]any{"api": updated}
	attempted, syncErr := syncAPIBrowserOriginPolicy(ctx, updated)
	recordBrowserOriginSync(out, attempted, syncErr)
	return out, nil
}

func (a *App) toolAPIDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	a.streams.cancelMatching(api.ProjectID, api.ID, 0, 0)
	flushRequestLogs(ctx.AppDB())
	ok, err := dbDeleteAPI(ctx.AppDB(), api.ProjectID, api.ID)
	if err != nil {
		return nil, err
	}
	attempted := true
	syncErr := deleteAPIBrowserPolicy(ctx, api.ID)
	out := map[string]any{"deleted": ok}
	recordBrowserOriginSync(out, attempted, syncErr)
	if cleanupErr := a.reconcileExposures(ctx); cleanupErr != nil {
		out["exposure_error"] = safeUpstreamError(cleanupErr)
	}
	return out, err
}

func (a *App) toolRouteAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	authJSON, err := normalizedAuthArg(args, "auth", "{}")
	if err != nil {
		return nil, err
	}
	corsJSON, err := jsonTextArg(args, "cors", "{}")
	if err != nil {
		return nil, err
	}
	eventsJSON, err := jsonTextArg(args, "events", "{}")
	if err != nil {
		return nil, err
	}
	if _, err := parseEffectiveCORSPolicy(api.CORSJSON, corsJSON); err != nil {
		return nil, err
	}
	enabled := true
	if _, ok := args["enabled"]; ok {
		enabled = boolArg(args, "enabled", true)
	}
	method, err := normalizeMethod(stringArg(args, "method", ""))
	if err != nil {
		return nil, err
	}
	pattern, err := normalizePathPattern(stringArg(args, "path_pattern", ""))
	if err != nil {
		return nil, err
	}
	candidate := &APIRoute{Method: method, PathPattern: pattern, AuthJSON: authJSON, CORSJSON: corsJSON, Enabled: enabled, TargetKind: stringArg(args, "target_kind", "")}
	if err := validateEffectivePolicies(ctx.AppDB(), api, candidate, 0); err != nil {
		return nil, err
	}
	if err := validateRouteTargetPath(stringArg(args, "target_path", "")); err != nil {
		return nil, err
	}
	if candidate.TargetKind == "app" && enabled {
		if err := a.validateAppTarget(api.ProjectID, stringArg(args, "target_ref", "")); err != nil {
			return nil, err
		}
	}
	timeout, err := boundedIntArg(args, "timeout_ms", 30000, 1, 300000)
	if err != nil {
		return nil, err
	}
	priority, err := boundedIntArg(args, "priority", 100, -2147483648, 2147483647)
	if err != nil {
		return nil, err
	}
	route, action, err := dbUpsertRoute(ctx.AppDB(), routeInput{
		ProjectID:   api.ProjectID,
		APIID:       api.ID,
		Method:      stringArg(args, "method", ""),
		PathPattern: stringArg(args, "path_pattern", ""),
		TargetKind:  stringArg(args, "target_kind", ""),
		TargetRef:   stringArg(args, "target_ref", ""),
		TargetPath:  stringArg(args, "target_path", ""),
		EventsJSON:  eventsJSON,
		AuthJSON:    authJSON,
		CORSJSON:    corsJSON,
		TimeoutMS:   timeout,
		Enabled:     enabled,
		Priority:    priority,
		PrioritySet: true,
	})
	if err != nil {
		return nil, err
	}
	a.streams.cancelMatching(api.ProjectID, api.ID, route.ID, 0)
	out := map[string]any{"route": route, "action": action}
	attempted, syncErr := syncAPIBrowserOriginPolicy(ctx, api)
	recordBrowserOriginSync(out, attempted, syncErr)
	return out, nil
}

func (a *App) toolRouteList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	rows, err := dbListRoutes(ctx.AppDB(), api.ProjectID, api.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"routes": rows, "count": len(rows)}, nil
}

func (a *App) toolRouteDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	route, err := dbGetRouteByID(ctx.AppDB(), pid, int64(intArg(args, "id", 0)))
	if err != nil {
		return nil, err
	}
	if route == nil {
		return map[string]any{"deleted": false}, nil
	}
	api, err := dbGetAPIByID(ctx.AppDB(), pid, route.APIID)
	if err != nil {
		return nil, err
	}
	if api != nil {
		if err := validateEffectivePolicies(ctx.AppDB(), api, nil, route.ID); err != nil {
			return nil, err
		}
	}
	ok, err := dbDeleteRoute(ctx.AppDB(), pid, route.ID)
	if err == nil {
		a.streams.cancelMatching(pid, route.APIID, route.ID, 0)
	}
	out := map[string]any{"deleted": ok}
	if err == nil && api != nil {
		attempted, syncErr := syncAPIBrowserOriginPolicy(ctx, api)
		recordBrowserOriginSync(out, attempted, syncErr)
	}
	return out, err
}

func (a *App) toolKeyCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	key, plaintext, err := dbCreateAPIKey(ctx.AppDB(), api.ProjectID, api.ID, stringArg(args, "name", "default"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"key": key, "secret": plaintext}, nil
}

func (a *App) toolKeyList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	keys, err := dbListAPIKeys(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"keys": keys, "count": len(keys)}, err
}

func (a *App) toolKeyRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64(intArg(args, "id", 0))
	ok, err := dbRevokeAPIKey(ctx.AppDB(), pid, id)
	if err == nil {
		a.streams.cancelMatching(pid, 0, 0, id)
	}
	return map[string]any{"revoked": ok}, err
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	logs, err := dbListLogsBefore(ctx.AppDB(), api.ProjectID, api.ID, intArg(args, "limit", 100), int64(intArg(args, "before_id", 0)))
	out := map[string]any{"logs": logs, "count": len(logs)}
	if len(logs) > 0 {
		out["next_before_id"] = logs[len(logs)-1].ID
	}
	return out, err
}

func (a *App) resolveAPI(ctx *sdk.AppCtx, args map[string]any) (*API, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	if id := intArg(args, "id", 0); id != 0 {
		api, err := dbGetAPIByID(ctx.AppDB(), pid, int64(id))
		if err != nil || api != nil {
			return api, err
		}
		return nil, errors.New("api not found")
	}
	if id := intArg(args, "api_id", 0); id != 0 {
		api, err := dbGetAPIByID(ctx.AppDB(), pid, int64(id))
		if err != nil || api != nil {
			return api, err
		}
		return nil, errors.New("api not found")
	}
	slug := stringArg(args, "slug", "")
	if slug == "" {
		slug = stringArg(args, "api_slug", "")
	}
	if slug == "" {
		return nil, errors.New("id, api_id, slug, or api_slug required")
	}
	api, err := dbGetAPIBySlug(ctx.AppDB(), pid, slug)
	if err != nil || api != nil {
		return api, err
	}
	return nil, errors.New("api not found")
}
