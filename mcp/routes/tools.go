package main

import (
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools exposes the stable app-level routing API. The implementation
// delegates to apteva-server's native ingress callbacks so the server
// remains the source of truth for routing/TLS.

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "routes_register",
			Description: "Register a hostname → target route through server-native ingress. " +
				"Args: hostname (req), target (req — http(s)://host:port or app://<name>[/prefix]), " +
				"owner_kind?, cert_fqdn?, tls_mode? ('auto' default or 'off'), allow_http?.",
			InputSchema: schemaObject(map[string]any{
				"hostname":   map[string]any{"type": "string"},
				"target":     map[string]any{"type": "string"},
				"owner_kind": map[string]any{"type": "string"},
				"cert_fqdn":  map[string]any{"type": "string"},
				"tls_mode":   map[string]any{"type": "string"},
				"tls":        map[string]any{"type": "string"},
				"allow_http": map[string]any{"type": "boolean"},
			}, []string{"hostname", "target"}),
			Handler: a.toolRoutesRegister,
		},
		{
			Name:        "routes_unregister",
			Description: "Remove a server-native ingress route owned by this Routes install. Args: hostname.",
			InputSchema: schemaObject(map[string]any{
				"hostname": map[string]any{"type": "string"},
			}, []string{"hostname"}),
			Handler: a.toolRoutesUnregister,
		},
		{
			Name:        "routes_list",
			Description: "List server-native ingress routes owned by this Routes install.",
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
	req, err := ingressExposeRequestFromArgs(args)
	if err != nil {
		return nil, err
	}
	route, err := ctx.PlatformAPI().ExposeIngress(req)
	if err != nil {
		return nil, err
	}
	return routeResponse(route, "exposed"), nil
}

func (a *App) toolRoutesUnregister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	host := strArg(args, "hostname")
	if host == "" {
		return nil, errors.New("hostname required")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	exists, err := ingressRouteExists(ctx, host)
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]any{"removed": false, "hostname": host}, nil
	}
	if err := ctx.PlatformAPI().UnexposeIngress(host); err != nil {
		if isMissingIngressRouteError(err) {
			return map[string]any{"removed": false, "hostname": host}, nil
		}
		return nil, err
	}
	return map[string]any{"removed": true, "hostname": host}, nil
}

func (a *App) toolRoutesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := ctx.PlatformAPI().ListIngressRoutes()
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
	rows, err := ctx.PlatformAPI().ListIngressRoutes()
	if err != nil {
		return nil, err
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for i := range rows {
		if strings.EqualFold(rows[i].Hostname, host) {
			return routeResponse(&rows[i], ""), nil
		}
	}
	return map[string]any{"route": nil, "found": false}, nil
}

// ─── Helpers ──────────────────────────────────────────────────────

func ingressExposeRequestFromArgs(args map[string]any) (sdk.IngressExposeRequest, error) {
	req := sdk.IngressExposeRequest{
		Hostname:  strArg(args, "hostname"),
		Target:    strArg(args, "target"),
		OwnerKind: firstNonEmpty(strArg(args, "owner_kind"), "routes"),
		CertFQDN:  strArg(args, "cert_fqdn"),
		AllowHTTP: boolArg(args, "allow_http"),
		TLSMode:   strArg(args, "tls_mode"),
		TLS:       strArg(args, "tls"),
	}
	if err := validateHostname(req.Hostname); err != nil {
		return req, err
	}
	if err := validateTarget(req.Target); err != nil {
		return req, err
	}
	return req, nil
}

func routeResponse(route *sdk.IngressRoute, action string) map[string]any {
	out := map[string]any{"route": route, "found": route != nil}
	if action != "" {
		out["action"] = action
	}
	if route != nil {
		out["public_url"] = publicURLForIngress(route)
	}
	return out
}

func ingressRouteExists(ctx *sdk.AppCtx, host string) (bool, error) {
	rows, err := ctx.PlatformAPI().ListIngressRoutes()
	if err != nil {
		return false, err
	}
	for i := range rows {
		if strings.EqualFold(rows[i].Hostname, host) {
			return true, nil
		}
	}
	return false, nil
}

func isMissingIngressRouteError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no rows in result set") || strings.Contains(msg, "route not found")
}

func publicURLForIngress(route *sdk.IngressRoute) string {
	if route == nil || strings.TrimSpace(route.Hostname) == "" {
		return ""
	}
	scheme := "https"
	if strings.EqualFold(route.TLSMode, "off") {
		scheme = "http"
	}
	return scheme + "://" + strings.TrimSpace(route.Hostname)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

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
	agent, err := ctx.GetAgent(id)
	if err != nil || agent == nil {
		return "app"
	}
	// PlatformAgent.Name is the install's display name; close enough
	// for v0.1. When deploy/code grow well-known kinds, the caller
	// can override owner_kind via a new arg.
	return agent.Name
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
// subscribes to for cache invalidation, and — in proxy mode — kicks
// an async re-render of the external proxy's config. Best-effort: a
// failed emit is caught by apteva-server's next poll; a failed
// re-render is logged and retried by the periodic sync loop.
func (a *App) emitRouteChanged(ctx *sdk.AppCtx, action string, route *Route) {
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
	if a.proxy != nil {
		go a.syncProxy(ctx, "routes.changed:"+action)
	}
}
