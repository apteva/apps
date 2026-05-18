package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// MCPTools — agent-facing surface. Mirrored to the embedded
// manifest's mcp_tools list (descriptions are repeated for
// dashboard discovery).

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "cdn_zone_create",
			Description: "Stand up a public hostname for an origin URL. Writes DNS via domains (A or CNAME), issues TLS via certs, " +
				"registers the host→target route via routes. apteva-server's HostRouter then reverse-proxies inbound traffic. " +
				"For local-dev installs without domains/certs bound, pass skip_dns:true + allow_http:true and resolve the hostname " +
				"via /etc/hosts. The route leg is the only one that must land — dns and cert failures don't block 'active' status. " +
				"Args: hostname (FQDN), origin_url (http(s):// reverse-proxy target), record_type? (A | CNAME, default A), " +
				"skip_dns? (default false; true skips the DNS write — required if domains app isn't installed), " +
				"allow_http? (default false; true serves over plain HTTP, skips cert issuance, no HTTPS redirect — " +
				"required if certs app isn't installed). Returns the created zone row.",
			InputSchema: schemaObject(map[string]any{
				"hostname":    map[string]any{"type": "string"},
				"origin_url":  map[string]any{"type": "string"},
				"record_type": map[string]any{"type": "string"},
				"skip_dns":    map[string]any{"type": "boolean"},
				"allow_http":  map[string]any{"type": "boolean"},
			}, []string{"hostname", "origin_url"}),
			Handler: a.toolZoneCreate,
		},
		{
			Name:        "cdn_zone_get",
			Description: "Fetch one zone by id or hostname. Args: id OR hostname.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"hostname": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolZoneGet,
		},
		{
			Name:        "cdn_zone_list",
			Description: "List zones for this project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolZoneList,
		},
		{
			Name: "cdn_zone_delete",
			Description: "Tear down a zone — unregister the route, revoke the cert, delete the DNS record, drop the local row. " +
				"Best-effort on the registrar / certs side; the local row is always removed. Idempotent. Args: id OR hostname.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"hostname": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolZoneDelete,
		},
		{
			Name: "cdn_url_for",
			Description: "Mint a public URL on a zone for a given origin path. Pure string assembly — no I/O. " +
				"Use from consumer apps (storage, media-studio, …) to render zone-fronted URLs when an install is linked to a zone. " +
				"Args: zone_id, origin_path (must start with /). Returns { url }.",
			InputSchema: schemaObject(map[string]any{
				"zone_id":     map[string]any{"type": "integer"},
				"origin_path": map[string]any{"type": "string"},
			}, []string{"zone_id", "origin_path"}),
			Handler: a.toolURLFor,
		},
	}
}

// ─── handlers ─────────────────────────────────────────────────────

func (a *App) toolZoneCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	hostname := strings.ToLower(strings.TrimSpace(strArg(args, "hostname")))
	if err := validateHostname(hostname); err != nil {
		return nil, err
	}
	originURL := strings.TrimSpace(strArg(args, "origin_url"))
	if err := validateOriginURL(originURL); err != nil {
		return nil, err
	}

	recordType := strings.ToUpper(strings.TrimSpace(strArg(args, "record_type")))
	if recordType == "" {
		recordType = strings.ToUpper(configOr(ctx, "record_type_default", "A"))
	}
	if recordType != "A" && recordType != "CNAME" {
		return nil, fmt.Errorf("record_type must be A or CNAME; got %q", recordType)
	}

	skipDNS := boolArg(args, "skip_dns")
	allowHTTP := boolArg(args, "allow_http")

	// server_public_host is only required when we're actually going
	// to write DNS — the local-dev path (skip_dns:true) lets the
	// operator add a /etc/hosts entry by hand without configuring it.
	recordValue := configOr(ctx, "server_public_host", "")
	if !skipDNS && recordValue == "" {
		return nil, errors.New("server_public_host not configured; set it in cdn's install config, or pass skip_dns:true for local-dev zones")
	}

	// Idempotency: if a zone already exists for (project, hostname),
	// return it untouched.
	if existing, _ := dbGetZoneByHostname(ctx.AppDB(), pid, hostname); existing != nil {
		return map[string]any{"zone": existing, "created": false}, nil
	}

	z := &Zone{
		ProjectID:   pid,
		Hostname:    hostname,
		OriginURL:   originURL,
		RecordType:  recordType,
		RecordValue: recordValue,
		AllowHTTP:   allowHTTP,
		Status:      "pending",
	}
	id, err := dbInsertZone(ctx.AppDB(), z)
	if err != nil {
		return nil, fmt.Errorf("insert zone: %w", err)
	}
	z.ID = id

	// Three legs. Only the route leg gates zone liveness — dns and
	// cert are advisory in v0.1 (and intentionally skippable on the
	// local-dev path).
	var detailParts []string

	dnsStatus := runDNSLeg(ctx, pid, hostname, recordType, recordValue, skipDNS, &detailParts)
	certStatus := runCertLeg(ctx, pid, hostname, allowHTTP, &detailParts)
	routeStatus := "ok"
	if err := registerRoute(ctx, pid, hostname, originURL, allowHTTP); err != nil {
		routeStatus = "error"
		detailParts = append(detailParts, "route: "+err.Error())
	}

	// Status is driven by the route leg alone. dns/cert errors are
	// surfaced via per-leg fields + status_detail but don't gate
	// liveness — operators can fix DNS / wait for the cert without
	// the zone being marked broken.
	status := "active"
	if routeStatus != "ok" {
		status = "error"
	}
	detail := strings.Join(detailParts, "; ")
	if err := dbUpdateZoneStatus(ctx.AppDB(), id, status, detail, dnsStatus, certStatus, routeStatus); err != nil {
		return nil, fmt.Errorf("update zone status: %w", err)
	}
	z, err = dbGetZone(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"zone": z, "created": true}, nil
}

// runDNSLeg performs the DNS write step. Returns the leg's status
// and appends to detail on error/skip. Skipped reasons (explicit
// skip_dns or domains-app-not-installed) are not appended to
// detail since they're expected, not failures.
func runDNSLeg(ctx *sdk.AppCtx, pid, hostname, recordType, recordValue string, skip bool, detail *[]string) string {
	if skip {
		return "skipped"
	}
	if err := writeDNS(ctx, pid, hostname, recordType, recordValue); err != nil {
		if looksLikeAppNotInstalled(err) {
			return "skipped"
		}
		*detail = append(*detail, "dns: "+err.Error())
		return "error"
	}
	return "ok"
}

// runCertLeg performs the cert issuance step.
func runCertLeg(ctx *sdk.AppCtx, pid, hostname string, allowHTTP bool, detail *[]string) string {
	if allowHTTP {
		return "skipped"
	}
	if err := issueCert(ctx, pid, hostname); err != nil {
		if looksLikeAppNotInstalled(err) {
			return "skipped"
		}
		*detail = append(*detail, "cert: "+err.Error())
		return "error"
	}
	return "ok"
}

func (a *App) toolZoneGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	z, err := lookupZone(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"zone": z}, nil
}

func (a *App) toolZoneList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbListZones(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"zones": rows, "count": len(rows)}, nil
}

func (a *App) toolZoneDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	z, err := lookupZone(ctx, pid, args)
	if err != nil {
		return nil, err
	}

	// Best-effort tear-down. The local row is the source of truth
	// for whether the zone "exists from cdn's perspective" — if any
	// remote leg fails, the operator can clean up by hand at the
	// registrar / certs panel.
	_ = unregisterRoute(ctx, pid, z.Hostname)
	_ = revokeCert(ctx, pid, z.Hostname)
	_ = deleteDNS(ctx, pid, z.Hostname, z.RecordType)

	removed, err := dbDeleteZone(ctx.AppDB(), pid, z.ID)
	if err != nil {
		return nil, fmt.Errorf("delete zone row: %w", err)
	}
	return map[string]any{"deleted": removed, "id": z.ID, "hostname": z.Hostname}, nil
}

func (a *App) toolURLFor(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	zoneID := int64(intArg(args, "zone_id", 0))
	if zoneID == 0 {
		return nil, errors.New("zone_id required")
	}
	originPath := strArg(args, "origin_path")
	if originPath == "" {
		return nil, errors.New("origin_path required")
	}
	if !strings.HasPrefix(originPath, "/") {
		return nil, errors.New("origin_path must start with /")
	}
	z, err := dbGetZone(ctx.AppDB(), pid, zoneID)
	if err != nil {
		return nil, err
	}
	if z == nil {
		return nil, fmt.Errorf("zone %d not found", zoneID)
	}
	scheme := "https"
	if z.AllowHTTP {
		scheme = "http"
	}
	return map[string]any{
		"url":      scheme + "://" + z.Hostname + originPath,
		"hostname": z.Hostname,
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────

func lookupZone(ctx *sdk.AppCtx, pid string, args map[string]any) (*Zone, error) {
	if id := int64(intArg(args, "id", 0)); id != 0 {
		z, err := dbGetZone(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		if z == nil {
			return nil, fmt.Errorf("zone %d not found", id)
		}
		return z, nil
	}
	if h := strings.ToLower(strings.TrimSpace(strArg(args, "hostname"))); h != "" {
		z, err := dbGetZoneByHostname(ctx.AppDB(), pid, h)
		if err != nil {
			return nil, err
		}
		if z == nil {
			return nil, fmt.Errorf("zone %q not found", h)
		}
		return z, nil
	}
	return nil, errors.New("id or hostname required")
}
