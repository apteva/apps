// platform_cf.go — typed wrappers around
// ctx.PlatformAPI().ExecuteIntegrationTool for the cloudflare
// integration. All Cloudflare API calls go through the platform proxy:
// the connection's API token never leaves the platform's process and
// account_id is auto-substituted from the connection's stored creds.
//
// Each helper calls one integration tool and unwraps Cloudflare's
// canonical {"result": …} envelope. Tools we use:
//
//   - list_zones                   GET  /zones
//   - list_tunnels                 GET  /accounts/:id/cfd_tunnel
//   - create_tunnel                POST /accounts/:id/cfd_tunnel
//   - get_tunnel_token             GET  /accounts/:id/cfd_tunnel/:tid/token
//   - update_tunnel_configuration  PUT  /accounts/:id/cfd_tunnel/:tid/configurations
//   - delete_tunnel                DELETE /accounts/:id/cfd_tunnel/:tid
//   - list_dns_records             GET  /zones/:zid/dns_records
//   - create_dns_record            POST /zones/:zid/dns_records
//   - update_dns_record            PATCH /zones/:zid/dns_records/:rid
//   - delete_dns_record            DELETE /zones/:zid/dns_records/:rid

package main

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// cfZone is a single CF zone (a domain on the operator's account).
type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cfTunnelInfo combines the metadata + connector token. Token is only
// returned by create_tunnel and get_tunnel_token; list_tunnels omits it.
type cfTunnelInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

type cfDNSRecordRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// cfExec runs one integration tool and decodes the {"result": …}
// envelope into dst. Pass dst=nil to ignore the result body.
func cfExec(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any, dst any) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return fmt.Errorf("cf %s: %w", tool, err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return fmt.Errorf("cf %s: %d %s", tool, statusOf(res), body)
	}
	if dst == nil {
		return nil
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(res.Data, &env); err != nil {
		return fmt.Errorf("cf %s: parse envelope: %w", tool, err)
	}
	// Some endpoints return a bare string (get_tunnel_token) — unmarshal
	// behaves correctly for that since dst is a *string in those cases.
	if err := json.Unmarshal(env.Result, dst); err != nil {
		return fmt.Errorf("cf %s: parse result: %w", tool, err)
	}
	return nil
}

func statusOf(res *sdk.ExecuteResult) int {
	if res == nil {
		return 0
	}
	return res.Status
}

func cfListZones(ctx *sdk.AppCtx, connID int64) ([]cfZone, error) {
	var zones []cfZone
	if err := cfExec(ctx, connID, "list_zones", nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

// cfFindTunnel returns the tunnel with this exact name, or nil if
// none exists. Used for idempotent setup — if the local DB row got
// lost but the tunnel survives in CF, we adopt it.
func cfFindTunnel(ctx *sdk.AppCtx, connID int64, name string) (*cfTunnelInfo, error) {
	var tuns []cfTunnelInfo
	if err := cfExec(ctx, connID, "list_tunnels", map[string]any{
		"name":       name,
		"is_deleted": false,
	}, &tuns); err != nil {
		return nil, err
	}
	for i := range tuns {
		if tuns[i].Name == name {
			return &tuns[i], nil
		}
	}
	return nil, nil
}

func cfCreateTunnel(ctx *sdk.AppCtx, connID int64, name string) (*cfTunnelInfo, error) {
	var t cfTunnelInfo
	if err := cfExec(ctx, connID, "create_tunnel", map[string]any{
		"name":       name,
		"config_src": "cloudflare",
	}, &t); err != nil {
		return nil, err
	}
	if t.ID == "" {
		return nil, fmt.Errorf("cf create_tunnel returned empty id")
	}
	return &t, nil
}

// cfGetTunnelToken fetches the connector token for an existing tunnel.
// CF returns the token as a bare string in the result envelope.
func cfGetTunnelToken(ctx *sdk.AppCtx, connID int64, tunnelID string) (string, error) {
	var token string
	if err := cfExec(ctx, connID, "get_tunnel_token", map[string]any{
		"tunnel_id": tunnelID,
	}, &token); err != nil {
		return "", err
	}
	return strings.TrimSpace(token), nil
}

func cfPutTunnelConfig(ctx *sdk.AppCtx, connID int64, tunnelID, hostname, service string) error {
	return cfExec(ctx, connID, "update_tunnel_configuration", map[string]any{
		"tunnel_id": tunnelID,
		"config": map[string]any{
			"ingress": []map[string]any{
				{"hostname": hostname, "service": service},
				{"service": "http_status:404"},
			},
		},
	}, nil)
}

func cfDeleteTunnel(ctx *sdk.AppCtx, connID int64, tunnelID string) error {
	return cfExec(ctx, connID, "delete_tunnel", map[string]any{
		"tunnel_id": tunnelID,
		"cascade":   true,
	}, nil)
}

// cfUpsertCNAME ensures a proxied CNAME points hostname at target.
// Returns the record id so we can delete it during destroy. Uses
// list_dns_records → create / update to avoid duplicate-record errors.
func cfUpsertCNAME(ctx *sdk.AppCtx, connID int64, zoneID, hostname, target string) (string, error) {
	var existing []cfDNSRecordRow
	if err := cfExec(ctx, connID, "list_dns_records", map[string]any{
		"zone_id": zoneID,
		"type":    "CNAME",
		"name":    hostname,
	}, &existing); err != nil {
		return "", err
	}

	body := map[string]any{
		"zone_id": zoneID,
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"proxied": true,
		"ttl":     1,
	}

	if len(existing) > 0 {
		body["record_id"] = existing[0].ID
		var rec cfDNSRecordRow
		if err := cfExec(ctx, connID, "update_dns_record", body, &rec); err != nil {
			return "", err
		}
		return existing[0].ID, nil
	}
	var rec cfDNSRecordRow
	if err := cfExec(ctx, connID, "create_dns_record", body, &rec); err != nil {
		return "", err
	}
	return rec.ID, nil
}

func cfDeleteDNSRecord(ctx *sdk.AppCtx, connID int64, zoneID, recordID string) error {
	return cfExec(ctx, connID, "delete_dns_record", map[string]any{
		"zone_id":   zoneID,
		"record_id": recordID,
	}, nil)
}
