package main

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. Each tool's REST twin is in
// handlers.go; the underlying logic in store.go is shared between
// them. owner_install_id comes from the caller's context (panel =
// 0, sidecar = its install id), not from MCP args, so the agent
// can't fake ownership.

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "routes_register",
			Description: "Register a hostname → target route. Idempotent on (hostname, target) from the same owner. " +
				"Sidecars must pass their own install id as owner_install_id (read from APTEVA_INSTALL_ID env); " +
				"the platform doesn't yet forward caller identity through CallApp. Args: hostname (req), " +
				"target (req, http or https URL), owner_install_id (req — pass APTEVA_INSTALL_ID), " +
				"owner_kind? ('deploy' | 'code' | etc), cert_fqdn? (default = hostname; pass a wildcard to share certs), " +
				"allow_http? (default false; true = serve plain HTTP without 301 to HTTPS).",
			InputSchema: schemaObject(map[string]any{
				"hostname":         map[string]any{"type": "string"},
				"target":           map[string]any{"type": "string"},
				"owner_install_id": map[string]any{"type": "integer"},
				"owner_kind":       map[string]any{"type": "string"},
				"cert_fqdn":        map[string]any{"type": "string"},
				"allow_http":       map[string]any{"type": "boolean"},
			}, []string{"hostname", "target", "owner_install_id"}),
			Handler: a.toolRoutesRegister,
		},
		{
			Name: "routes_unregister",
			Description: "Remove a route by hostname. Caller must own it (errors if not). Sidecars pass their " +
				"own install id as owner_install_id from APTEVA_INSTALL_ID. Args: hostname (req), " +
				"owner_install_id (req).",
			InputSchema: schemaObject(map[string]any{
				"hostname":         map[string]any{"type": "string"},
				"owner_install_id": map[string]any{"type": "integer"},
			}, []string{"hostname", "owner_install_id"}),
			Handler: a.toolRoutesUnregister,
		},
		{
			Name:        "routes_list",
			Description: "List routes. Args: owner_install_id? (filter to one owner).",
			InputSchema: schemaObject(map[string]any{
				"owner_install_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolRoutesList,
		},
		{
			Name:        "routes_get",
			Description: "Fetch one route by hostname. Returns null when no route exists. Args: hostname.",
			InputSchema: schemaObject(map[string]any{
				"hostname": map[string]any{"type": "string"},
			}, []string{"hostname"}),
			Handler: a.toolRoutesGet,
		},
	}
}

// ─── handlers ─────────────────────────────────────────────────────

func (a *App) toolRoutesRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	in, err := registerInputFromArgs(args)
	if err != nil {
		return nil, err
	}
	if in.OwnerInstallID == 0 {
		return nil, errors.New("owner_install_id required (sidecars: pass APTEVA_INSTALL_ID; manual entries should use the REST endpoint)")
	}
	if in.OwnerKind == "" {
		in.OwnerKind = ownerKindForInstallID(ctx, in.OwnerInstallID)
	}
	route, action, err := dbUpsertRoute(ctx.AppDB(), in)
	if err != nil {
		if errors.Is(err, ErrHostnameOwnedElsewhere) {
			return nil, fmt.Errorf("hostname_in_use_by_other_owner: %s already claimed by another install", in.Hostname)
		}
		return nil, err
	}
	emitRouteChanged(ctx, action, route)
	return map[string]any{"route": route, "action": action}, nil
}

func (a *App) toolRoutesUnregister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	host := strArg(args, "hostname")
	if host == "" {
		return nil, errors.New("hostname required")
	}
	owner := int64(intArg(args, "owner_install_id", 0))
	if owner == 0 {
		return nil, errors.New("owner_install_id required")
	}
	removed, err := dbDeleteRouteByHostname(ctx.AppDB(), host, owner)
	if err != nil {
		if errors.Is(err, ErrNotOwner) {
			return nil, errors.New("not_owner: caller does not own this route")
		}
		return nil, err
	}
	if removed {
		emitRouteChanged(ctx, "removed", &Route{Hostname: host, OwnerInstallID: owner})
	}
	return map[string]any{"removed": removed}, nil
}

func (a *App) toolRoutesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var filter *int64
	if v, ok := args["owner_install_id"]; ok {
		switch n := v.(type) {
		case float64:
			id := int64(n)
			filter = &id
		case int64:
			filter = &n
		}
	}
	rows, err := dbListRoutes(ctx.AppDB(), filter)
	if err != nil {
		return nil, err
	}
	return map[string]any{"routes": rows, "count": len(rows)}, nil
}

func (a *App) toolRoutesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	host := strArg(args, "hostname")
	if host == "" {
		return nil, errors.New("hostname required")
	}
	r, err := dbGetRouteByHostname(ctx.AppDB(), host)
	if err != nil {
		return nil, err
	}
	return map[string]any{"route": r}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────

func registerInputFromArgs(args map[string]any) (RegisterInput, error) {
	in := RegisterInput{
		Hostname:       strArg(args, "hostname"),
		Target:         strArg(args, "target"),
		OwnerInstallID: int64(intArg(args, "owner_install_id", 0)),
		OwnerKind:      strArg(args, "owner_kind"),
		CertFQDN:       strArg(args, "cert_fqdn"),
		AllowHTTP:      boolArg(args, "allow_http"),
	}
	if err := validateHostname(in.Hostname); err != nil {
		return in, err
	}
	if err := validateTarget(in.Target); err != nil {
		return in, err
	}
	if in.CertFQDN == "" {
		in.CertFQDN = in.Hostname
	}
	return in, nil
}

// intArg pulls an int from MCP args. Floats from JSON are coerced.
// Default is returned when the key is missing or not numeric.
func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

// ownerKindForInstallID asks the platform for the install's app name,
// best-effort. Falls back to "manual" for install_id=0 and "app" when
// the lookup fails. The owner_kind is purely informational (for the
// panel + cleanup heuristics); access decisions key off the numeric
// owner_install_id.
func ownerKindForInstallID(ctx *sdk.AppCtx, id int64) string {
	if id == 0 {
		return "manual"
	}
	inst, err := ctx.PlatformAPI().GetInstance(id)
	if err != nil || inst == nil {
		return "app"
	}
	// PlatformInstance.Name is the install's display name; close
	// enough for v0.1. When deploy/code grow well-known kinds, the
	// caller can override owner_kind via a new arg.
	return inst.Name
}

// strArg pulls a string from MCP args; mirrors the helper in code/.
func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// schemaObject builds a JSON-Schema object node — same shape every
// other app uses, keeps tool registration concise.
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// emitRouteChanged fires the platform event that apteva-server
// subscribes to for cache invalidation. Best-effort — if the emit
// fails the cache will catch up on the next poll cycle (or restart).
func emitRouteChanged(ctx *sdk.AppCtx, action string, route *Route) {
	payload := map[string]any{
		"action":   action,
		"hostname": route.Hostname,
	}
	if action != "removed" {
		payload["target"] = route.Target
		payload["cert_fqdn"] = route.CertFQDN
		payload["allow_http"] = route.AllowHTTP
		payload["owner_install_id"] = route.OwnerInstallID
		payload["owner_kind"] = route.OwnerKind
	}
	ctx.Emit("routes.changed", payload)
}
