package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "tunnel_configure_domain",
			Description: "Configure the operator-owned base domain. Args: base_domain, dns_target?, auto_dns? (default true). Existing tunnels must be deleted before changing domains.",
			InputSchema: objectSchema(map[string]any{
				"base_domain": map[string]any{"type": "string"},
				"dns_target":  map[string]any{"type": "string"},
				"auto_dns":    map[string]any{"type": "boolean"},
			}, "base_domain"),
			Handler: a.toolConfigureDomain,
		},
		{
			Name:        "tunnel_create",
			Description: "Reserve a stable hostname for the current project. Returns a connector token exactly once. Args: name.",
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string"},
			}, "name"),
			Handler: a.toolCreateTunnel,
		},
		{
			Name:        "tunnel_list",
			Description: "List active tunnels owned by the current project, including online state and traffic counters.",
			InputSchema: objectSchema(nil),
			Handler:     a.toolListTunnels,
		},
		{
			Name:        "tunnel_rotate_token",
			Description: "Revoke the current connector session and return a new one-time connector token. Args: tunnel_id.",
			InputSchema: objectSchema(map[string]any{
				"tunnel_id": map[string]any{"type": "string"},
			}, "tunnel_id"),
			Handler: a.toolRotateToken,
		},
		{
			Name:        "tunnel_delete",
			Description: "Revoke a tunnel and remove its public ingress route. Args: tunnel_id.",
			InputSchema: objectSchema(map[string]any{
				"tunnel_id": map[string]any{"type": "string"},
			}, "tunnel_id"),
			Handler: a.toolDeleteTunnel,
		},
	}
}

func (a *App) toolConfigureDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := toolProjectID(ctx)
	if err != nil {
		return nil, err
	}
	auto := true
	if value, ok := args["auto_dns"].(bool); ok {
		auto = value
	}
	cfg, _, err := a.configureDomain(projectID, configureDomainInput{
		BaseDomain: stringArg(args, "base_domain"),
		DNSTarget:  stringArg(args, "dns_target"),
		AutoDNS:    &auto,
	})
	if err != nil {
		return nil, err
	}
	return configResponse(cfg, a.ctx.IntegrationFor("domains") != nil, projectID), nil
}

func (a *App) toolCreateTunnel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := toolProjectID(ctx)
	if err != nil {
		return nil, err
	}
	return a.createTunnel(projectID, stringArg(args, "name"))
}

func (a *App) toolListTunnels(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	projectID, err := toolProjectID(ctx)
	if err != nil {
		return nil, err
	}
	_ = a.usage.flush()
	items, err := a.store.listTunnels(projectID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Connected = a.connectors.connected(items[i].ID)
	}
	return map[string]any{"tunnels": items, "count": len(items)}, nil
}

func (a *App) toolRotateToken(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := toolProjectID(ctx)
	if err != nil {
		return nil, err
	}
	token, err := randomSecret("aptun_", 32)
	if err != nil {
		return nil, err
	}
	item, err := a.store.rotateToken(stringArg(args, "tunnel_id"), projectID, tokenDigest(token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("tunnel not found")
	}
	if err != nil {
		return nil, err
	}
	a.connectors.disconnect(item.ID, "connector token rotated")
	return connectorCredentialResponse(a, item, token), nil
}

func (a *App) toolDeleteTunnel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID, err := toolProjectID(ctx)
	if err != nil {
		return nil, err
	}
	item, err := a.store.revokeTunnel(stringArg(args, "tunnel_id"), projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("tunnel not found")
	}
	if err != nil {
		return nil, err
	}
	a.connectors.disconnect(item.ID, "tunnel revoked")
	if err := a.ctx.PlatformAPI().UnexposeIngress(item.Hostname); err != nil {
		return map[string]any{
			"deleted": true,
			"warning": "tunnel revoked but its ingress route could not be removed: " + err.Error(),
		}, nil
	}
	a.ctx.EmitWithProject("tunnel.deleted", projectID, map[string]any{
		"tunnel_id": item.ID,
		"hostname":  item.Hostname,
	})
	return map[string]any{"deleted": true}, nil
}

func toolProjectID(ctx *sdk.AppCtx) (string, error) {
	projectID := strings.TrimSpace(ctx.CurrentProject())
	if projectID == "" {
		return "", fmt.Errorf("project context is required")
	}
	return projectID, nil
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
