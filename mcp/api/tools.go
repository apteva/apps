package main

import (
	"errors"
	"net"
	"net/url"
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
				"cors":        map[string]any{"type": "object"},
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
			"cors":        map[string]any{"type": "object"},
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
				"cors":         map[string]any{"type": "object"},
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
		}, nil), Handler: a.toolLogs},
	}
}

func (a *App) toolAPICreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	corsJSON, err := jsonTextArg(args, "cors", "{}")
	if err != nil {
		return nil, err
	}
	authJSON, err := jsonTextArg(args, "auth", "{}")
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
	return map[string]any{"api": api}, nil
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
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	updated, err := dbUpdateAPI(ctx.AppDB(), api.ProjectID, api.ID, args)
	if err != nil {
		return nil, err
	}
	a.configureExposure(ctx, updated)
	updated, _ = dbGetAPIByID(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"api": updated}, nil
}

func (a *App) toolAPIDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	if api.Hostname != "" {
		_ = ctx.PlatformAPI().UnexposeIngress(api.Hostname)
	}
	ok, err := dbDeleteAPI(ctx.AppDB(), api.ProjectID, api.ID)
	return map[string]any{"deleted": ok}, err
}

func (a *App) toolRouteAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	authJSON, err := jsonTextArg(args, "auth", "{}")
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
	enabled := true
	if _, ok := args["enabled"]; ok {
		enabled = boolArg(args, "enabled", true)
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
		TimeoutMS:   intArg(args, "timeout_ms", 30000),
		Enabled:     enabled,
		Priority:    intArg(args, "priority", 100),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"route": route, "action": action}, nil
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
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	ok, err := dbDeleteRoute(ctx.AppDB(), pid, int64(intArg(args, "id", 0)))
	return map[string]any{"deleted": ok}, err
}

func (a *App) toolKeyCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	ok, err := dbRevokeAPIKey(ctx.AppDB(), pid, int64(intArg(args, "id", 0)))
	return map[string]any{"revoked": ok}, err
}

func (a *App) toolLogs(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	api, err := a.resolveAPI(ctx, args)
	if err != nil || api == nil {
		return nil, err
	}
	logs, err := dbListLogs(ctx.AppDB(), api.ProjectID, api.ID, intArg(args, "limit", 100))
	return map[string]any{"logs": logs, "count": len(logs)}, err
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

func (a *App) configureExposure(ctx *sdk.AppCtx, api *API) {
	if api == nil || api.Hostname == "" {
		return
	}
	dnsStatus := api.DNSStatus
	if api.DNSMode == "domains" {
		if err := writeDomainRecord(ctx, api); err != nil {
			dnsStatus = "error: " + err.Error()
		} else {
			dnsStatus = "ok"
		}
	} else {
		dnsStatus = api.DNSMode
	}
	_, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  api.Hostname,
		Target:    "app://api/gw?project_id=" + url.QueryEscape(api.ProjectID),
		ProjectID: api.ProjectID,
		OwnerKind: "api",
		CertFQDN:  api.Hostname,
		AllowHTTP: api.AllowHTTP,
		TLSMode:   "auto",
	})
	ingressStatus := "ok"
	if err != nil {
		ingressStatus = "error: " + err.Error()
	}
	dbSetAPIExposureStatus(ctx.AppDB(), api.ProjectID, api.ID, dnsStatus, ingressStatus)
}

func writeDomainRecord(ctx *sdk.AppCtx, api *API) error {
	target := strings.TrimSpace(ctx.Config().Get("public_host"))
	if target == "" {
		return errors.New("public_host config required for dns_mode=domains")
	}
	apex, name := splitHostnameForDNS(api.Hostname)
	if apex == "" {
		return errors.New("hostname must include a domain and subdomain")
	}
	recordType := "CNAME"
	if net.ParseIP(target) != nil {
		recordType = "A"
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", map[string]any{
		"domain":      apex,
		"name":        name,
		"type":        recordType,
		"value":       target,
		"ttl":         300,
		"_project_id": api.ProjectID,
	}, &out)
}

func splitHostnameForDNS(host string) (apex, name string) {
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return "", ""
	}
	apex = strings.Join(parts[len(parts)-2:], ".")
	if len(parts) == 2 {
		return apex, "@"
	}
	return apex, strings.Join(parts[:len(parts)-2], ".")
}
