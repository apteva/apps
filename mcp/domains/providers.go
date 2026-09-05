package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── DNS provider abstraction ──────────────────────────────────────
//
// dnsProviderImpl hides per-provider differences (Porkbun's per-record
// CRUD by domain name, Namecheap's "set all hosts at once" model, IONOS's
// zone-id-keyed record CRUD) behind a uniform shape. The toolDomainRecords*
// handlers go through this interface; new providers add a dnsProviderImpl
// and a slug case below.

type dnsProviderImpl interface {
	List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error)
	Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (action string, err error)
	Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error
}

// providerFor resolves the DNS provider to use for a given connection
// id. When connID==0 it falls back to the install's role binding (the
// pre-v0.3 path and the default for new domains added without an
// explicit connection_id).
func (a *App) providerFor(ctx *sdk.AppCtx, connID int64, projectID string) (dnsProviderImpl, *sdk.BoundIntegration, error) {
	if connID > 0 {
		conn, err := ctx.PlatformAPI().GetConnection(connID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up connection %d: %w", connID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %d not found", connID)
		}
		if conn.Status != "" && !strings.EqualFold(conn.Status, "active") {
			return nil, nil, fmt.Errorf("connection %d is not active (status %q)", connID, conn.Status)
		}
		if projectID != "" && conn.ProjectID != "" && conn.ProjectID != projectID {
			return nil, nil, fmt.Errorf("connection %d belongs to project %q, not %q", connID, conn.ProjectID, projectID)
		}
		bound := &sdk.BoundIntegration{
			Role:         "dns_provider",
			Kind:         "integration",
			ConnectionID: connID,
			AppSlug:      conn.AppSlug,
		}
		switch conn.AppSlug {
		case "porkbun":
			return &porkbunProvider{bound: bound}, bound, nil
		case "namecheap":
			return &namecheapProvider{bound: bound}, bound, nil
		case "ionos":
			return &ionosProvider{bound: bound}, bound, nil
		case "spaceship":
			return &spaceshipProvider{bound: bound}, bound, nil
		}
		return nil, bound, fmt.Errorf("unsupported provider slug %q on connection %d (compatible: porkbun, namecheap, ionos, spaceship)", conn.AppSlug, connID)
	}
	id, err := selectedConnectionID(ctx, "dns_provider")
	if err != nil {
		return nil, nil, err
	}
	if id == 0 {
		return nil, nil, errors.New("no dns_provider bound; select a provider connection")
	}
	prov, validated, err := a.providerFor(ctx, id, projectID)
	if validated != nil {
		validated.IsDefault = true
	}
	return prov, validated, err
}

// provider is the legacy entry point — kept for callers that don't
// yet have a domain row in hand. Equivalent to providerFor(ctx, 0).
func (a *App) provider(ctx *sdk.AppCtx) (dnsProviderImpl, *sdk.BoundIntegration, error) {
	return a.providerFor(ctx, 0, "")
}

func providerCall(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, payload map[string]any) (json.RawMessage, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, payload)
	diag := providerCallDiagnostic(bound, tool, payload)
	if err != nil {
		return nil, apiError(502, fmt.Sprintf("%s: %v", diag, err))
	}
	if res == nil || !res.Success {
		body := ""
		status := 0
		if res != nil {
			body = string(res.Data)
			status = res.Status
		}
		return nil, apiError(502, fmt.Sprintf("%s non-2xx status %d: %s", diag, status, truncate(body, 400)))
	}
	if bound != nil && bound.AppSlug == "porkbun" {
		if err := porkbunResponseError(res.Data); err != nil {
			return nil, apiError(502, fmt.Sprintf("%s: %v", diag, err))
		}
	}
	return res.Data, nil
}

func porkbunResponseError(raw json.RawMessage) error {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("invalid Porkbun JSON response: %w", err)
	}
	status, _ := body["status"].(string)
	if strings.EqualFold(strings.TrimSpace(status), "SUCCESS") {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(status), "ERROR") {
		return fmt.Errorf("unexpected Porkbun status %q", status)
	}
	message := firstString([]map[string]any{body}, "message", "error", "detail")
	if message == "" {
		if errs, ok := body["errors"].([]any); ok {
			parts := make([]string, 0, len(errs))
			for _, item := range errs {
				parts = append(parts, fmt.Sprint(item))
			}
			message = strings.Join(parts, "; ")
		}
	}
	if message == "" {
		message = "upstream returned status ERROR"
	}
	return errors.New(message)
}

func providerCallDiagnostic(bound *sdk.BoundIntegration, tool string, payload map[string]any) string {
	if bound == nil {
		return fmt.Sprintf("provider <nil> connection 0 tool %s", tool)
	}
	diag := fmt.Sprintf("provider %s connection %d tool %s", bound.AppSlug, bound.ConnectionID, tool)
	if hint := providerRequestHint(bound.AppSlug, tool, payload); hint != "" {
		diag += " endpoint " + hint
	}
	return diag
}

func providerRequestHint(provider, tool string, payload map[string]any) string {
	domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strArg(payload, "domain"))), ".")
	if domain == "" {
		return ""
	}
	switch provider {
	case "spaceship":
		escapedDomain := url.PathEscape(domain)
		switch tool {
		case "list_dns_records":
			q := url.Values{}
			q.Set("take", strconv.Itoa(intArg(payload, "take", 0)))
			q.Set("skip", strconv.Itoa(intArg(payload, "skip", 0)))
			return "GET /v1/dns/records/" + escapedDomain + "?" + q.Encode()
		case "save_dns_records":
			return "PUT /v1/dns/records/" + escapedDomain
		case "delete_dns_records":
			return "DELETE /v1/dns/records/" + escapedDomain
		case "check_single_domain_availability":
			return "GET /v1/domains/" + escapedDomain + "/available"
		}
	}
	return ""
}
