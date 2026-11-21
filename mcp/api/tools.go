package main

import (
	"encoding/json"
	"errors"
	"strings"

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
				"auth":        authPolicySchema(),
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
			"auth":        authPolicySchema(),
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
				"auth":         authPolicySchema(),
				"cors":         corsPolicySchema(),
				"timeout_ms":   map[string]any{"type": "integer", "description": "Total upstream budget in milliseconds, including queueing, preparation, execution and response transfer (default 30000, maximum 300000)."},
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
		{Name: "api_key_create", Description: "Create a generic API credential. Returns plaintext key only when newly created; an idempotent retry returns metadata with created=false and no secret.", InputSchema: schemaObject(map[string]any{
			"project_id":      map[string]any{"type": "string"},
			"api_id":          map[string]any{"type": "integer"},
			"api_slug":        map[string]any{"type": "string"},
			"name":            map[string]any{"type": "string"},
			"subject_type":    map[string]any{"type": "string"},
			"subject_id":      map[string]any{"type": "string"},
			"claims":          map[string]any{"type": "object", "description": "Generic verified claims forwarded to authenticated Functions. Reserved or secret-like claim names are rejected."},
			"scopes":          map[string]any{"type": "array", "maxItems": 64, "uniqueItems": true, "items": map[string]any{"type": "string"}},
			"expires_at":      map[string]any{"type": "string", "description": "Optional RFC3339 expiration timestamp."},
			"metadata":        map[string]any{"type": "object", "description": "Management metadata; never forwarded to Functions."},
			"external_id":     map[string]any{"type": "string", "description": "Optional unique external credential identifier within the API."},
			"idempotency_key": map[string]any{"type": "string", "description": "Optional issuance operation key. Reuse with different arguments is rejected."},
		}, []string{"name"}), Handler: a.toolKeyCreate},
		{Name: "api_key_get", Description: "Fetch API key metadata by id. Secrets and hashes are never returned.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolKeyGet},
		{Name: "api_key_list", Description: "List API keys for an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
		}, nil), Handler: a.toolKeyList},
		{Name: "api_key_list_by_subject", Description: "List generic API credentials by exact subject_type and subject_id, optionally restricted to one API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"},
		}, []string{"subject_type", "subject_id"}), Handler: a.toolKeyListBySubject},
		{Name: "api_key_revoke", Description: "Revoke an API key by id.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "integer"},
		}, []string{"id"}), Handler: a.toolKeyRevoke},
		{Name: "api_key_revoke_by_subject", Description: "Revoke every active credential for an exact generic subject, optionally restricted to one API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"},
			"subject_type": map[string]any{"type": "string"}, "subject_id": map[string]any{"type": "string"},
		}, []string{"subject_type", "subject_id"}), Handler: a.toolKeyRevokeBySubject},
		{Name: "api_usage_plan_create", Description: "Create an operational token-bucket request policy for an API. This is traffic control, not billing.", InputSchema: usagePlanInputSchema(false), Handler: a.toolUsagePlanCreate},
		{Name: "api_usage_plan_get", Description: "Fetch an API usage plan by id.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolUsagePlanGet},
		{Name: "api_usage_plan_list", Description: "List operational usage plans for an API.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}}, nil), Handler: a.toolUsagePlanList},
		{Name: "api_usage_plan_update", Description: "Update an operational usage plan.", InputSchema: usagePlanInputSchema(true), Handler: a.toolUsagePlanUpdate},
		{Name: "api_usage_plan_attach_key", Description: "Attach one usage plan to an API key, replacing its prior plan.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "key_id": map[string]any{"type": "integer"}, "usage_plan_id": map[string]any{"type": "integer"}}, []string{"key_id", "usage_plan_id"}), Handler: a.toolUsagePlanAttachKey},
		{Name: "api_usage_plan_detach_key", Description: "Remove the operational usage plan from an API key.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "key_id": map[string]any{"type": "integer"}}, []string{"key_id"}), Handler: a.toolUsagePlanDetachKey},
		{Name: "api_logs", Description: "List recent request logs for an API.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"},
			"api_id":     map[string]any{"type": "integer"},
			"api_slug":   map[string]any{"type": "string"},
			"limit":      map[string]any{"type": "integer"},
			"before_id":  map[string]any{"type": "integer", "minimum": 1},
		}, nil), Handler: a.toolLogs},
		{Name: "api_config_create", Description: "Create an immutable configuration snapshot of the current API routes and policies.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"},
		}, nil), Handler: a.toolConfigCreate},
		{Name: "api_config_clone", Description: "Clone an immutable API configuration into a new version.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}, "configuration_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"},
		}, []string{"configuration_id"}), Handler: a.toolConfigClone},
		{Name: "api_config_list", Description: "List immutable API configurations.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}}, nil), Handler: a.toolConfigList},
		{Name: "api_config_diff", Description: "Compare the routes in two immutable API configurations.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "left_configuration_id": map[string]any{"type": "integer"}, "right_configuration_id": map[string]any{"type": "integer"}}, []string{"left_configuration_id", "right_configuration_id"}), Handler: a.toolConfigDiff},
		{Name: "api_stage_create", Description: "Create an opt-in stage pointing at an immutable configuration. Existing APIs remain legacy unless a stage is created.", InputSchema: schemaObject(map[string]any{
			"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "configuration_id": map[string]any{"type": "integer"}, "hostname": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "cors": corsPolicySchema(), "auth": authPolicySchema(),
		}, []string{"name"}), Handler: a.toolStageCreate},
		{Name: "api_stage_list", Description: "List stages for an API.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "api_id": map[string]any{"type": "integer"}, "api_slug": map[string]any{"type": "string"}}, nil), Handler: a.toolStageList},
		{Name: "api_stage_get", Description: "Fetch a stage by id.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "stage_id": map[string]any{"type": "integer"}}, []string{"stage_id"}), Handler: a.toolStageGet},
		{Name: "api_stage_update", Description: "Update stage metadata, hostname, enabled state, or optional CORS/auth defaults.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "stage_id": map[string]any{"type": "integer"}, "name": map[string]any{"type": "string"}, "hostname": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "cors": corsPolicySchema(), "auth": authPolicySchema()}, []string{"stage_id"}), Handler: a.toolStageUpdate},
		{Name: "api_stage_promote", Description: "Atomically point a stage at an immutable configuration.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "stage_id": map[string]any{"type": "integer"}, "configuration_id": map[string]any{"type": "integer"}}, []string{"stage_id", "configuration_id"}), Handler: a.toolStagePromote},
		{Name: "api_stage_rollback", Description: "Roll a stage back to a prior immutable configuration.", InputSchema: schemaObject(map[string]any{"project_id": map[string]any{"type": "string"}, "stage_id": map[string]any{"type": "integer"}, "configuration_id": map[string]any{"type": "integer"}}, []string{"stage_id", "configuration_id"}), Handler: a.toolStagePromote},
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
	if cleanupErr := a.reconcileStageExposures(ctx); cleanupErr != nil {
		out["stage_exposure_error"] = safeUpstreamError(cleanupErr)
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
	in, err := normalizedCredentialInput(api.ProjectID, api.ID, args)
	if err != nil {
		return nil, err
	}
	key, plaintext, created, err := dbCreateAPIKey(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"key": key, "created": created}
	if created {
		out["secret"] = plaintext
	}
	return out, nil
}

func (a *App) toolKeyGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	key, err := dbGetAPIKey(ctx.AppDB(), pid, int64(intArg(args, "id", 0)))
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, errors.New("api key not found")
	}
	return map[string]any{"key": key}, nil
}

func (a *App) toolKeyList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	keys, err := dbListAPIKeys(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"keys": keys, "count": len(keys)}, err
}

func (a *App) optionalAPIForKeyQuery(ctx *sdk.AppCtx, args map[string]any) (int64, error) {
	if intArg(args, "api_id", 0) == 0 && strings.TrimSpace(stringArg(args, "api_slug", "")) == "" {
		return 0, nil
	}
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return 0, err
	}
	return api.ID, nil
}

func validatedSubjectArgs(args map[string]any) (string, string, error) {
	subjectType := strings.TrimSpace(stringArg(args, "subject_type", ""))
	subjectID := strings.TrimSpace(stringArg(args, "subject_id", ""))
	if !subjectTypePattern.MatchString(subjectType) || !identityString(subjectID) {
		return "", "", errors.New("valid subject_type and subject_id are required")
	}
	return subjectType, subjectID, nil
}

func (a *App) toolKeyListBySubject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	subjectType, subjectID, err := validatedSubjectArgs(args)
	if err != nil {
		return nil, err
	}
	apiID, err := a.optionalAPIForKeyQuery(ctx, args)
	if err != nil {
		return nil, err
	}
	keys, err := dbListAPIKeysBySubject(ctx.AppDB(), pid, apiID, subjectType, subjectID)
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
		a.forgetThrottle(id)
	}
	return map[string]any{"revoked": ok}, err
}

func (a *App) toolKeyRevokeBySubject(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	subjectType, subjectID, err := validatedSubjectArgs(args)
	if err != nil {
		return nil, err
	}
	apiID, err := a.optionalAPIForKeyQuery(ctx, args)
	if err != nil {
		return nil, err
	}
	ids, err := dbRevokeAPIKeysBySubject(ctx.AppDB(), pid, apiID, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		a.streams.cancelMatching(pid, 0, 0, id)
		a.forgetThrottle(id)
	}
	return map[string]any{"revoked": len(ids), "key_ids": ids}, nil
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

func (a *App) toolConfigCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	c, err := dbCreateConfiguration(ctx.AppDB(), api.ProjectID, api.ID, 0, stringArg(args, "name", ""))
	return map[string]any{"configuration": c}, err
}

func (a *App) toolConfigClone(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	c, err := dbCreateConfiguration(ctx.AppDB(), api.ProjectID, api.ID, int64(intArg(args, "configuration_id", 0)), stringArg(args, "name", ""))
	return map[string]any{"configuration": c}, err
}

func (a *App) toolConfigList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	rows, err := dbListConfigurations(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"configurations": rows, "count": len(rows)}, err
}

func (a *App) toolConfigDiff(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	left, right := int64(intArg(args, "left_configuration_id", 0)), int64(intArg(args, "right_configuration_id", 0))
	lc, err := dbGetConfiguration(ctx.AppDB(), pid, left)
	if err != nil || lc == nil {
		if err == nil {
			err = errors.New("left configuration not found")
		}
		return nil, err
	}
	rc, err := dbGetConfiguration(ctx.AppDB(), pid, right)
	if err != nil || rc == nil {
		if err == nil {
			err = errors.New("right configuration not found")
		}
		return nil, err
	}
	if lc.APIID != rc.APIID {
		return nil, errors.New("configurations belong to different APIs")
	}
	diff, err := configurationDiff(ctx.AppDB(), pid, left, right)
	return map[string]any{"diff": diff}, err
}

func (a *App) toolStageCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	configID := int64(intArg(args, "configuration_id", 0))
	var c *APIConfiguration
	if configID == 0 {
		c, err = dbCreateConfiguration(ctx.AppDB(), api.ProjectID, api.ID, 0, "")
		if err != nil {
			return nil, err
		}
		configID = c.ID
	}
	stage := APIStage{ProjectID: api.ProjectID, APIID: api.ID, Name: stringArg(args, "name", ""), ConfigurationID: configID, Hostname: stringArg(args, "hostname", ""), Status: stringArg(args, "status", "active")}
	if _, ok := args["cors"]; ok {
		stage.CORSJSON, err = jsonTextArg(args, "cors", "")
		if err != nil {
			return nil, err
		}
	}
	if _, ok := args["auth"]; ok {
		stage.AuthJSON, err = normalizedAuthArg(args, "auth", "")
		if err != nil {
			return nil, err
		}
	}
	s, err := dbCreateStage(ctx.AppDB(), stage)
	if err == nil {
		a.configureStageExposure(ctx, s)
	}
	return map[string]any{"stage": s}, err
}

func (a *App) toolStageList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	rows, err := dbListStages(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"stages": rows, "count": len(rows)}, err
}

func (a *App) toolStageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	s, err := dbGetStage(ctx.AppDB(), pid, int64(intArg(args, "stage_id", 0)))
	if err == nil && s == nil {
		err = errors.New("stage not found")
	}
	return map[string]any{"stage": s}, err
}

func (a *App) toolStageUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	s, err := dbGetStage(ctx.AppDB(), pid, int64(intArg(args, "stage_id", 0)))
	if err != nil || s == nil {
		if err == nil {
			err = errors.New("stage not found")
		}
		return nil, err
	}
	updated, err := dbUpdateStage(ctx.AppDB(), s, args)
	if err == nil {
		a.configureStageExposure(ctx, updated)
	}
	return map[string]any{"stage": updated}, err
}

func (a *App) toolStagePromote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	s, err := dbPromoteStage(ctx.AppDB(), pid, int64(intArg(args, "stage_id", 0)), int64(intArg(args, "configuration_id", 0)))
	return map[string]any{"stage": s}, err
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
