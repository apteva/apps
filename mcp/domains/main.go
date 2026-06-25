// Domains v0.1 — DNS + domain inventory for Apteva projects.
//
// Apps that need to write DNS records (messaging for SES MX/DKIM,
// storage for CDN CNAMEs, future certs app for ACME challenges) call
// this app instead of speaking each registrar's API directly.
//
// Architecture:
//   - manifest declares one required integration: dns_provider with
//     compatible_slugs [porkbun, namecheap, ionos, spaceship].
//   - all DNS record CRUD goes through the bound provider via
//     ctx.PlatformAPI().ExecuteIntegrationTool.
//   - no local record cache — records are always live; the local
//     `domains` table just tracks which domains this project uses.
//   - upsert (domain_records_set) is composed: list-by-name+type;
//     edit if present, create if absent. Most providers don't expose
//     atomic upsert.
package main

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest ──────────────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: domains
display_name: Domains
version: 0.4.2
description: |
  DNS + domain inventory and registrar workflows. Other apps call
  this for record CRUD instead of talking to registrars directly.
  DNS providers: Porkbun, Namecheap, IONOS, Spaceship. Registration
  purchases are Porkbun-only; Spaceship availability checks are read-only.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.connections.read
  integrations:
    - role: dns_provider
      kind: integration
      compatible_slugs: [porkbun, namecheap, ionos, spaceship]
      capabilities: [dns.list_records, dns.create_record, dns.edit_records_by_type, dns.delete_records_by_type]
      tools:
        dns.list_records: list_dns_records
        dns.create_record: create_dns_record
        dns.edit_records_by_type: edit_dns_records_by_type
        dns.delete_records_by_type: delete_dns_records_by_type
      required: true
    - role: registrar_provider
      kind: integration
      compatible_slugs: [porkbun]
      capabilities: [domains.check_availability, domains.register, domains.pricing]
      tools:
        domains.check_availability: check_availability
        domains.register: register_domain
        domains.pricing: get_pricing
      required: false
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: domain_add,            description: "Register a domain with this app." }
    - { name: domain_remove,         description: "Soft-delete a domain from local inventory." }
    - { name: domain_list,           description: "List domains for the project." }
    - { name: domain_get,            description: "Fetch one domain by name." }
    - { name: domain_availability_check, description: "Check if a domain is available for registration." }
    - { name: domain_pricing_get,    description: "Fetch registrar pricing, optionally for one TLD." }
    - { name: domain_register,       description: "Register a new domain. Spends real money at the registrar." }
    - { name: domain_records_list,   description: "List DNS records on a domain." }
    - { name: domain_records_set,    description: "Upsert a DNS record." }
    - { name: domain_records_delete, description: "Delete records matching (domain, name, type)." }
  ui_panels:
    - slot: project.page
      label: Domains
      icon: globe
      entry: /ui/DomainsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/domains
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/domains.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("domains requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("domains mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/domains", Handler: a.handleDomainsList},
		{Pattern: "/domains/", Handler: a.handleDomainItem},
		{Pattern: "/connections", Handler: a.handleConnectionsList},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "domain_add",
			Description: "Register a domain with this app. By default the bound DNS provider is probed to confirm the domain exists there before recording it locally — pass skip_validation:true to bypass (just-registered domains, provider outage, externally-hosted DNS). Args: name (e.g. 'acme.com'), connection_id? (specific provider connection; defaults to the install's role binding), registrar?, dns_provider?, notes?, skip_validation?.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"connection_id":   map[string]any{"type": "integer"},
				"registrar":       map[string]any{"type": "string"},
				"dns_provider":    map[string]any{"type": "string"},
				"notes":           map[string]any{"type": "string"},
				"skip_validation": map[string]any{"type": "boolean"},
			}, []string{"name"}),
			Handler: a.toolDomainAdd,
		},
		{
			Name:        "domain_remove",
			Description: "Soft-delete a domain from this app's inventory. Doesn't touch the actual registration. Args: name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolDomainRemove,
		},
		{
			Name:        "domain_list",
			Description: "List domains for this project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolDomainList,
		},
		{
			Name:        "domain_get",
			Description: "Fetch one domain by name.",
			InputSchema: schemaObject(map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
			Handler:     a.toolDomainGet,
		},
		{
			Name:        "domain_availability_check",
			Description: "Check whether a domain is available for registration via the registrar provider. Safe/read-only. Args: domain, connection_id?. Porkbun and Spaceship are supported; if no registrar_provider is bound, the DNS provider is used when it is Porkbun or Spaceship.",
			InputSchema: schemaObject(map[string]any{
				"domain":        map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, []string{"domain"}),
			Handler: a.toolDomainAvailabilityCheck,
		},
		{
			Name:        "domain_pricing_get",
			Description: "Fetch registrar pricing via the registrar provider. Args: tld? (e.g. com), connection_id?. Porkbun is supported; Spaceship pricing is not exposed because purchase flows are intentionally disabled.",
			InputSchema: schemaObject(map[string]any{
				"tld":           map[string]any{"type": "string"},
				"connection_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolDomainPricingGet,
		},
		{
			Name: "domain_register",
			Description: "Register a new domain through the registrar provider. This SPENDS REAL MONEY against the registrar account. " +
				"Call domain_availability_check and confirm with the user first. Args: domain, years? (1-10, default 1), auto_renew? (default true), whois_privacy? (default true), coupon?, connection_id?, notes?, skip_availability_check?. Porkbun is supported for paid registration; Spaceship is availability-only.",
			InputSchema: schemaObject(map[string]any{
				"domain":                  map[string]any{"type": "string"},
				"years":                   map[string]any{"type": "integer"},
				"auto_renew":              map[string]any{"type": "boolean"},
				"whois_privacy":           map[string]any{"type": "boolean"},
				"coupon":                  map[string]any{"type": "string"},
				"connection_id":           map[string]any{"type": "integer"},
				"notes":                   map[string]any{"type": "string"},
				"skip_availability_check": map[string]any{"type": "boolean"},
			}, []string{"domain"}),
			Handler: a.toolDomainRegister,
		},
		{
			Name:        "domain_records_list",
			Description: "List DNS records on a domain via the bound dns_provider. Args: domain, type? (filter), name? (subdomain filter).",
			InputSchema: schemaObject(map[string]any{
				"domain": map[string]any{"type": "string"},
				"type":   map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
			}, []string{"domain"}),
			Handler: a.toolDomainRecordsList,
		},
		{
			Name: "domain_records_set",
			Description: "Upsert a DNS record. Composes list-by-name+type, then edit if present or create if absent. " +
				"Args: domain (e.g. 'acme.com'), name ('@' for apex, or subdomain like 'mail'), type (A|AAAA|CNAME|MX|TXT|NS|SRV|CAA), value, ttl? (default 600). " +
				"For MX records, value should be 'priority host', e.g. '10 inbound-smtp.eu-west-1.amazonaws.com'.",
			InputSchema: schemaObject(map[string]any{
				"domain": map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
				"type":   map[string]any{"type": "string"},
				"value":  map[string]any{"type": "string"},
				"ttl":    map[string]any{"type": "integer"},
			}, []string{"domain", "name", "type", "value"}),
			Handler: a.toolDomainRecordsSet,
		},
		{
			Name:        "domain_records_delete",
			Description: "Delete all records matching (domain, name, type). Args: domain, name, type.",
			InputSchema: schemaObject(map[string]any{
				"domain": map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
				"type":   map[string]any{"type": "string"},
			}, []string{"domain", "name", "type"}),
			Handler: a.toolDomainRecordsDelete,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Domain types ──────────────────────────────────────────────────

type Domain struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Name            string `json:"name"`
	RegistrarSlug   string `json:"registrar_slug,omitempty"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
	// ConnectionID pins this domain to one specific DNS provider
	// connection. Zero means fall back to the install's role
	// binding (legacy / pre-v0.3 / "Other" rows).
	ConnectionID int64  `json:"connection_id,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	Notes        string `json:"notes,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// DNSRecord is the canonical shape we hand back to callers — flat,
// provider-agnostic. The proxy layer translates each provider's actual
// shape into this struct. Raw is reserved for providers whose delete
// endpoint needs type-specific fields that are not part of the common
// display/edit surface.
type DNSRecord struct {
	ID    string         `json:"id"`    // provider-side record id when available
	Name  string         `json:"name"`  // FQDN or provider-local name (e.g. "mail.acme.com" or "mail")
	Type  string         `json:"type"`  // A | AAAA | CNAME | MX | TXT | NS | SRV | CAA | ...
	Value string         `json:"value"` // record content
	TTL   int            `json:"ttl"`
	Prio  int            `json:"prio,omitempty"` // MX priority etc.
	Notes string         `json:"notes,omitempty"`
	Raw   map[string]any `json:"raw,omitempty"`
}

type DomainAvailability struct {
	Domain       string          `json:"domain"`
	Available    bool            `json:"available"`
	Provider     string          `json:"provider"`
	ConnectionID int64           `json:"connection_id,omitempty"`
	Source       string          `json:"source,omitempty"`
	Confidence   string          `json:"confidence,omitempty"`
	Warning      string          `json:"warning,omitempty"`
	Price        string          `json:"price,omitempty"`
	Currency     string          `json:"currency,omitempty"`
	Premium      bool            `json:"premium,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

type DomainRegistrationRequest struct {
	Domain       string
	Years        int
	AutoRenew    bool
	WhoisPrivacy bool
	Coupon       string
}

// ─── Address normalisation ────────────────────────────────────────

// normaliseDomainName strips the scheme/path and any trailing dot,
// lowercases, and validates that what's left looks like a domain.
func normaliseDomainName(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "", errors.New("empty domain name")
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, ".")
	if !looksLikeDomain(s) {
		return "", fmt.Errorf("invalid domain name %q", s)
	}
	return s, nil
}

func looksLikeDomain(s string) bool {
	if len(s) < 3 || len(s) > 253 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n@/?#") {
		return false
	}
	if !strings.Contains(s, ".") {
		return false
	}
	// Reject leading/trailing dot, consecutive dots.
	if s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		if c == '.' || c == '-' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// normaliseRecordType: uppercase, validate against the record types
// most DNS providers and our messaging app care about.
func normaliseRecordType(t string) (string, error) {
	t = strings.ToUpper(strings.TrimSpace(t))
	switch t {
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "ALIAS", "PTR", "HTTPS", "SVCB", "TLSA":
		return t, nil
	}
	return "", fmt.Errorf("unsupported record type %q", t)
}

// normaliseSubaddress: '@' or ” means apex; otherwise lowercase.
func normaliseSubaddress(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "@" {
		return ""
	}
	return s
}

// ─── Local domain CRUD ────────────────────────────────────────────

func (a *App) toolDomainAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	reg := strings.ToLower(strings.TrimSpace(strArg(args, "registrar")))
	dns := strings.ToLower(strings.TrimSpace(strArg(args, "dns_provider")))

	// Resolve which connection this domain pins to. Explicit
	// connection_id wins; otherwise snapshot the install's role
	// binding so re-binding the role later doesn't quietly
	// reroute existing domains. Zero means "no pin" — record ops
	// will fall back to the role binding at call time.
	connID := int64(intArg(args, "connection_id", 0))
	if connID == 0 {
		if bound := ctx.IntegrationFor("dns_provider"); bound != nil {
			connID = bound.ConnectionID
		}
	}

	// If we have a connection, derive its slug and use that as the
	// authoritative dns_provider_slug. The free-text dns_provider arg
	// is now just a hint for the "Other / unknown" path.
	if connID > 0 {
		if conn, cerr := ctx.PlatformAPI().GetConnection(connID); cerr == nil && conn != nil && conn.AppSlug != "" {
			dns = conn.AppSlug
			if reg == "" {
				reg = dns
			}
		}
	}
	if dns == "" {
		dns = reg
	}
	notes := strArg(args, "notes")

	// Validate the domain exists at the resolved provider before
	// recording it. Catches typos and wrong-provider bindings up
	// front. Skipped when no provider can be resolved (no connection
	// pinned and no role bound), the slug is unsupported, or the
	// caller opts out.
	if !boolArg(args, "skip_validation", false) {
		if prov, _, perr := a.providerFor(ctx, connID); perr == nil {
			if _, lerr := prov.List(ctx, name); lerr != nil {
				return nil, fmt.Errorf("validate %q at provider: %w (pass skip_validation:true to add anyway)", name, lerr)
			}
		}
	}

	d, err := upsertDomainInventory(ctx, pid, name, reg, dns, notes, connID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"domain": d}, nil
}

func (a *App) toolDomainRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(
		`UPDATE domains SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "name": name}, nil
}

func (a *App) toolDomainList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbDomainList(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"domains": out, "count": len(out)}, nil
}

func (a *App) toolDomainGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name, err := normaliseDomainName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	d, err := dbDomainGetByName(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return map[string]any{"domain": nil, "found": false}, nil
	}
	return map[string]any{"domain": d, "found": true}, nil
}

// ─── DNS provider abstraction ──────────────────────────────────────
//
// dnsProviderImpl hides per-provider differences (Porkbun's per-record
// CRUD by domain name, Namecheap's "set all hosts at once" model, IONOS's
// zone-id-keyed record CRUD) behind a uniform shape. The toolDomainRecords*
// handlers go through this interface; new providers add a dnsProviderImpl
// and a slug case below.

type dnsProviderImpl interface {
	List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error)
	Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int) (action string, err error)
	Delete(ctx *sdk.AppCtx, domain, sub, rtype string) error
}

// providerFor resolves the DNS provider to use for a given connection
// id. When connID==0 it falls back to the install's role binding (the
// pre-v0.3 path and the default for new domains added without an
// explicit connection_id).
func (a *App) providerFor(ctx *sdk.AppCtx, connID int64) (dnsProviderImpl, *sdk.BoundIntegration, error) {
	if connID > 0 {
		conn, err := ctx.PlatformAPI().GetConnection(connID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up connection %d: %w", connID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %d not found", connID)
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
	bound := ctx.IntegrationFor("dns_provider")
	if bound == nil {
		return nil, nil, errors.New("no dns_provider bound — install/select a Porkbun, Namecheap, IONOS, or Spaceship connection, or pass connection_id explicitly")
	}
	switch bound.AppSlug {
	case "porkbun":
		return &porkbunProvider{bound: bound}, bound, nil
	case "namecheap":
		return &namecheapProvider{bound: bound}, bound, nil
	case "ionos":
		return &ionosProvider{bound: bound}, bound, nil
	case "spaceship":
		return &spaceshipProvider{bound: bound}, bound, nil
	}
	return nil, bound, fmt.Errorf("unsupported dns_provider slug %q (compatible: porkbun, namecheap, ionos, spaceship)", bound.AppSlug)
}

// provider is the legacy entry point — kept for callers that don't
// yet have a domain row in hand. Equivalent to providerFor(ctx, 0).
func (a *App) provider(ctx *sdk.AppCtx) (dnsProviderImpl, *sdk.BoundIntegration, error) {
	return a.providerFor(ctx, 0)
}

func providerCall(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, tool string, payload map[string]any) (json.RawMessage, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, payload)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	return res.Data, nil
}

// ─── Registrar provider abstraction ───────────────────────────────

type registrarProviderImpl interface {
	CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error)
	Pricing(ctx *sdk.AppCtx, tld string) (any, error)
	Register(ctx *sdk.AppCtx, req DomainRegistrationRequest) (json.RawMessage, error)
}

func (a *App) registrarFor(ctx *sdk.AppCtx, connID int64) (registrarProviderImpl, *sdk.BoundIntegration, error) {
	if connID > 0 {
		conn, err := ctx.PlatformAPI().GetConnection(connID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up connection %d: %w", connID, err)
		}
		if conn == nil {
			return nil, nil, fmt.Errorf("connection %d not found", connID)
		}
		bound := &sdk.BoundIntegration{
			Role:         "registrar_provider",
			Kind:         "integration",
			ConnectionID: connID,
			AppSlug:      conn.AppSlug,
		}
		switch conn.AppSlug {
		case "porkbun":
			return &porkbunRegistrar{bound: bound}, bound, nil
		case "spaceship":
			return &spaceshipRegistrar{bound: bound}, bound, nil
		}
		return nil, bound, fmt.Errorf("unsupported registrar provider %q on connection %d (compatible: porkbun, spaceship for availability)", conn.AppSlug, connID)
	}
	if bound := ctx.IntegrationFor("registrar_provider"); bound != nil {
		switch bound.AppSlug {
		case "porkbun":
			return &porkbunRegistrar{bound: bound}, bound, nil
		default:
			return nil, bound, fmt.Errorf("unsupported registrar_provider slug %q (compatible: porkbun)", bound.AppSlug)
		}
	}
	if bound := ctx.IntegrationFor("dns_provider"); bound != nil {
		switch bound.AppSlug {
		case "porkbun":
			return &porkbunRegistrar{bound: bound}, bound, nil
		case "spaceship":
			return &spaceshipRegistrar{bound: bound}, bound, nil
		default:
			return nil, bound, fmt.Errorf("dns_provider %q does not support registrar operations in domains v0.4 (compatible registrar: porkbun; spaceship availability only)", bound.AppSlug)
		}
	}
	return nil, nil, errors.New("no registrar_provider or compatible dns_provider bound - install/select a Porkbun connection, a Spaceship connection for availability, or pass connection_id explicitly")
}

func (a *App) toolDomainAvailabilityCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	reg, _, err := a.registrarFor(ctx, int64(intArg(args, "connection_id", 0)))
	if err != nil {
		avail, ferr := publicRDAPAvailability(domain, err)
		if ferr != nil {
			return nil, err
		}
		return map[string]any{"availability": avail}, nil
	}
	avail, err := reg.CheckAvailability(ctx, domain)
	if err != nil {
		avail, ferr := publicRDAPAvailability(domain, err)
		if ferr != nil {
			return nil, err
		}
		return map[string]any{"availability": avail}, nil
	}
	return map[string]any{"availability": avail}, nil
}

func (a *App) toolDomainPricingGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tld := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(strArg(args, "tld"))), ".")
	reg, bound, err := a.registrarFor(ctx, int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	pricing, err := reg.Pricing(ctx, tld)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"provider":      bound.AppSlug,
		"connection_id": bound.ConnectionID,
		"tld":           tld,
		"pricing":       pricing,
	}, nil
}

func (a *App) toolDomainRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	years := intArg(args, "years", 1)
	if years < 1 || years > 10 {
		return nil, fmt.Errorf("years must be between 1 and 10, got %d", years)
	}
	reg, bound, err := a.registrarFor(ctx, int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	var availability *DomainAvailability
	if !boolArg(args, "skip_availability_check", false) {
		availability, err = reg.CheckAvailability(ctx, domain)
		if err != nil {
			return nil, fmt.Errorf("availability check: %w", err)
		}
		if !availability.Available {
			return nil, fmt.Errorf("domain %q is not available for registration", domain)
		}
	}
	req := DomainRegistrationRequest{
		Domain:       domain,
		Years:        years,
		AutoRenew:    boolArg(args, "auto_renew", true),
		WhoisPrivacy: boolArg(args, "whois_privacy", true),
		Coupon:       strArg(args, "coupon"),
	}
	raw, err := reg.Register(ctx, req)
	if err != nil {
		return nil, err
	}
	// Porkbun registrations also give us DNS control through the same
	// connection, so pin the inventory row to the registrar connection.
	d, err := upsertDomainInventory(ctx, pid, domain, bound.AppSlug, bound.AppSlug, strArg(args, "notes"), bound.ConnectionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"registered":    true,
		"domain":        d,
		"availability":  availability,
		"provider":      bound.AppSlug,
		"connection_id": bound.ConnectionID,
		"raw":           raw,
	}, nil
}

// ─── Porkbun provider ──────────────────────────────────────────────

type porkbunProvider struct{ bound *sdk.BoundIntegration }

type porkbunRegistrar struct{ bound *sdk.BoundIntegration }

func (p *porkbunRegistrar) CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error) {
	raw, err := providerCall(ctx, p.bound, "check_availability", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	out := parsePorkbunAvailability(domain, p.bound, raw)
	return &out, nil
}

func (p *porkbunRegistrar) Pricing(ctx *sdk.AppCtx, tld string) (any, error) {
	raw, err := providerCall(ctx, p.bound, "get_pricing", map[string]any{})
	if err != nil {
		return nil, err
	}
	if tld == "" {
		var all any
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("parse pricing: %w", err)
		}
		return all, nil
	}
	entry := pricingEntryForTLD(raw, tld)
	if entry == nil {
		return nil, fmt.Errorf("no pricing found for .%s", tld)
	}
	return entry, nil
}

func (p *porkbunRegistrar) Register(ctx *sdk.AppCtx, req DomainRegistrationRequest) (json.RawMessage, error) {
	payload := map[string]any{
		"domain":       req.Domain,
		"years":        req.Years,
		"renewAuto":    yesNo(req.AutoRenew),
		"whoisPrivacy": yesNo(req.WhoisPrivacy),
	}
	if req.Coupon != "" {
		payload["coupon"] = req.Coupon
	}
	raw, err := providerCall(ctx, p.bound, "register_domain", payload)
	if err != nil {
		return nil, fmt.Errorf("register_domain: %w", err)
	}
	return raw, nil
}

func (p *porkbunProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	raw, err := providerCall(ctx, p.bound, "list_dns_records", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	return parsePorkbunRecords(raw), nil
}

func (p *porkbunProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int) (string, error) {
	existing, err := p.List(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("list before upsert: %w", err)
	}
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	matches := filterRecords(existing, func(r DNSRecord) bool {
		if !strings.EqualFold(r.Type, rtype) {
			return false
		}
		return strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub)
	})

	prio := ""
	content := value
	if rtype == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			prio = parts[0]
			content = parts[1]
		}
	}

	if len(matches) > 0 {
		// Unchanged-value short-circuit. Porkbun's edit endpoint rejects
		// an edit whose value is identical to what's already stored with
		// EDIT_ERROR_WE_WERE_UNABLE_TO_EDIT_THE_DNS_RECORD — which we'd
		// otherwise surface as a failure even though the desired state is
		// already present. When the matched record already equals what we
		// want, skip the edit and report a no-op.
		wantPrio := 0
		if prio != "" {
			wantPrio, _ = strconv.Atoi(prio)
		}
		if porkbunRecordUnchanged(matches[0], content, ttl, wantPrio) {
			return "unchanged", nil
		}
		payload := map[string]any{
			"domain":    domain,
			"type":      rtype,
			"subdomain": sub,
			"content":   content,
			"ttl":       fmt.Sprintf("%d", ttl),
		}
		if prio != "" {
			payload["prio"] = prio
		}
		if _, err := providerCall(ctx, p.bound, "edit_dns_records_by_type", payload); err != nil {
			return "", fmt.Errorf("edit: %w", err)
		}
		return "updated", nil
	}
	createPayload := map[string]any{
		"domain":  domain,
		"name":    sub,
		"type":    rtype,
		"content": content,
		"ttl":     fmt.Sprintf("%d", ttl),
	}
	if prio != "" {
		createPayload["prio"] = prio
	}
	if _, err := providerCall(ctx, p.bound, "create_dns_record", createPayload); err != nil {
		// Idempotency rescue: Porkbun returns a non-2xx (often a
		// generic 406 HTML page) when the record we tried to create
		// already exists under a name our filter didn't match — apex
		// returned as "acme.com" vs our wantFQ build, or a case-fold
		// mismatch on TXT content. Re-list and check: if the record
		// is now there with the value we wanted, the upsert succeeded
		// regardless of the original error path. Otherwise we surface
		// the create error as before.
		if after, lErr := p.List(ctx, domain); lErr == nil && hasMatchingRecord(after, wantFQ, sub, rtype, content) {
			return "updated", nil
		}
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", nil
}

// hasMatchingRecord reports whether the provider's record set
// already contains the (name, type, value) we wanted to upsert.
// Used by Porkbun's Upsert to rescue a duplicate-record failure
// after a list-miss + create-conflict — the record was there all
// along, the filter just didn't catch it.
func hasMatchingRecord(records []DNSRecord, wantFQ, sub, rtype, content string) bool {
	for _, r := range records {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		// Match the name the same way Upsert's filter did, plus a
		// looser fallback: providers sometimes return the apex as
		// just the registered domain ("acme.com") when we asked for
		// the bare apex (sub == "").
		nameOK := strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub)
		if !nameOK && sub == "" {
			nameOK = strings.EqualFold(r.Name, wantFQ) || r.Name == "" || r.Name == "@"
		}
		if !nameOK {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(r.Value), strings.TrimSpace(content)) {
			return true
		}
	}
	return false
}

// porkbunRecordUnchanged reports whether an existing record already
// holds exactly the value/ttl/prio we'd write — i.e. the edit would be
// a true no-op (the case Porkbun rejects with EDIT_ERROR). Value is
// compared trimmed + case-insensitively, matching hasMatchingRecord.
func porkbunRecordUnchanged(existing DNSRecord, content string, ttl, prio int) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Value), strings.TrimSpace(content)) {
		return false
	}
	if existing.TTL != ttl {
		return false
	}
	return existing.Prio == prio
}

func (p *porkbunProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype string) error {
	_, err := providerCall(ctx, p.bound, "delete_dns_records_by_type", map[string]any{
		"domain":    domain,
		"type":      rtype,
		"subdomain": sub,
	})
	return err
}

// ─── Namecheap provider ────────────────────────────────────────────
//
// Namecheap's API model is read-modify-write: getHosts returns the
// full list of records as XML; setHosts replaces them all atomically.
// So upsert is "list, modify in memory, write back the full set" —
// expensive (one round-trip per write) but correct.
//
// Namecheap also requires (a) IP whitelisting on the API key and
// (b) the domain to be split into SLD ("acme") + TLD ("com").
//
// XML responses come back as JSON-encoded strings (the platform
// runner falls through non-JSON Content-Type to string).

type namecheapProvider struct{ bound *sdk.BoundIntegration }

type namecheapHost struct {
	HostID  string `xml:"HostId,attr" json:"-"`
	Name    string `xml:"Name,attr"`
	Type    string `xml:"Type,attr"`
	Address string `xml:"Address,attr"`
	TTL     string `xml:"TTL,attr"`
	MXPref  string `xml:"MXPref,attr"`
}

// namecheapStatus is the Status + Errors envelope every Namecheap API
// response carries. Embed in per-command response structs to share the
// error-detection helper.
type namecheapStatus struct {
	Status string `xml:"Status,attr"`
	Errors struct {
		Errors []struct {
			Number string `xml:"Number,attr"`
			Text   string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
}

func (s namecheapStatus) err() error {
	if !strings.EqualFold(s.Status, "ERROR") && len(s.Errors.Errors) == 0 {
		return nil
	}
	var msgs []string
	for _, e := range s.Errors.Errors {
		msgs = append(msgs, fmt.Sprintf("[%s] %s", e.Number, strings.TrimSpace(e.Text)))
	}
	if len(msgs) == 0 {
		return fmt.Errorf("namecheap error: status=%s (no details)", s.Status)
	}
	return fmt.Errorf("namecheap error: %s", strings.Join(msgs, "; "))
}

type namecheapHostsResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	namecheapStatus
	CommandResponse struct {
		Hosts struct {
			Domain string          `xml:"Domain,attr"`
			Hosts  []namecheapHost `xml:"host"`
		} `xml:"DomainDNSGetHostsResult"`
	} `xml:"CommandResponse"`
}

// xmlDataToString unwraps the runner's response shape: when the
// integration's response Content-Type isn't JSON, the runner stores
// the raw body as a JSON-encoded string. Strip the JSON quoting to
// get the original XML bytes.
func xmlDataToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty response")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
	}
	return string(raw), nil
}

func (n *namecheapProvider) callGetHosts(ctx *sdk.AppCtx, domain string) (*namecheapHostsResponse, error) {
	sld, tld := splitSLDTLD(domain)
	if sld == "" || tld == "" {
		return nil, fmt.Errorf("namecheap requires a 2-label domain (got %q)", domain)
	}
	raw, err := providerCall(ctx, n.bound, "get_dns_hosts", map[string]any{
		"SLD": sld,
		"TLD": tld,
	})
	if err != nil {
		return nil, err
	}
	body, err := xmlDataToString(raw)
	if err != nil {
		return nil, err
	}
	var parsed namecheapHostsResponse
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("parse namecheap XML: %w", err)
	}
	if err := parsed.err(); err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (n *namecheapProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	parsed, err := n.callGetHosts(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]DNSRecord, 0, len(parsed.CommandResponse.Hosts.Hosts))
	for _, h := range parsed.CommandResponse.Hosts.Hosts {
		ttl, _ := strconv.Atoi(h.TTL)
		prio, _ := strconv.Atoi(h.MXPref)
		out = append(out, DNSRecord{
			ID:    h.HostID,
			Name:  h.Name, // Namecheap returns the local part only ("@", "www", "mail")
			Type:  strings.ToUpper(h.Type),
			Value: h.Address,
			TTL:   ttl,
			Prio:  prio,
		})
	}
	return out, nil
}

func (n *namecheapProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int) (string, error) {
	parsed, err := n.callGetHosts(ctx, domain)
	if err != nil {
		return "", err
	}
	hosts := parsed.CommandResponse.Hosts.Hosts
	wantName := sub
	if wantName == "" {
		wantName = "@"
	}

	prio := ""
	content := value
	if rtype == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			prio = parts[0]
			content = parts[1]
		}
	}

	// Find matching host(s) by (Name, Type) — Namecheap allows multiple
	// records under the same name+type (round-robin A records, multi-MX),
	// but our upsert deliberately collapses to one canonical record per
	// (name, type). v0.2 can split this if needed.
	keep := make([]namecheapHost, 0, len(hosts)+1)
	matched := false
	for _, h := range hosts {
		if !matched && strings.EqualFold(h.Name, wantName) && strings.EqualFold(h.Type, rtype) {
			matched = true
			h.Address = content
			h.TTL = fmt.Sprintf("%d", ttl)
			if prio != "" {
				h.MXPref = prio
			}
			keep = append(keep, h)
			continue
		}
		// Drop additional duplicates so the canonical record wins.
		if matched && strings.EqualFold(h.Name, wantName) && strings.EqualFold(h.Type, rtype) {
			continue
		}
		keep = append(keep, h)
	}
	action := "updated"
	if !matched {
		newHost := namecheapHost{
			Name:    wantName,
			Type:    rtype,
			Address: content,
			TTL:     fmt.Sprintf("%d", ttl),
		}
		switch {
		case prio != "":
			newHost.MXPref = prio
		case rtype == "MX":
			// Namecheap rejects MX records without an MXPref. The tool
			// docs ask for "<prio> <host>" values, but be forgiving when
			// the caller forgets and pass a conventional default.
			newHost.MXPref = "10"
		}
		keep = append(keep, newHost)
		action = "created"
	}
	if err := n.writeHosts(ctx, domain, keep); err != nil {
		return "", err
	}
	return action, nil
}

func (n *namecheapProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype string) error {
	parsed, err := n.callGetHosts(ctx, domain)
	if err != nil {
		return err
	}
	hosts := parsed.CommandResponse.Hosts.Hosts
	wantName := sub
	if wantName == "" {
		wantName = "@"
	}
	keep := make([]namecheapHost, 0, len(hosts))
	for _, h := range hosts {
		if strings.EqualFold(h.Name, wantName) && strings.EqualFold(h.Type, rtype) {
			continue
		}
		keep = append(keep, h)
	}
	return n.writeHosts(ctx, domain, keep)
}

// writeHosts replaces the entire DNS host list for a domain via
// Namecheap's setHosts. Builds the numbered-form-param payload
// (HostName1, RecordType1, Address1, TTL1, MXPref1, …).
func (n *namecheapProvider) writeHosts(ctx *sdk.AppCtx, domain string, hosts []namecheapHost) error {
	sld, tld := splitSLDTLD(domain)
	if sld == "" || tld == "" {
		return fmt.Errorf("namecheap requires a 2-label domain (got %q)", domain)
	}
	payload := map[string]any{
		"SLD": sld,
		"TLD": tld,
	}
	for i, h := range hosts {
		idx := i + 1
		payload[fmt.Sprintf("HostName%d", idx)] = h.Name
		payload[fmt.Sprintf("RecordType%d", idx)] = h.Type
		payload[fmt.Sprintf("Address%d", idx)] = h.Address
		if h.TTL != "" {
			payload[fmt.Sprintf("TTL%d", idx)] = h.TTL
		}
		if h.MXPref != "" {
			payload[fmt.Sprintf("MXPref%d", idx)] = h.MXPref
		}
	}
	raw, err := providerCall(ctx, n.bound, "set_dns_hosts", payload)
	if err != nil {
		return err
	}
	body, err := xmlDataToString(raw)
	if err != nil {
		return err
	}
	var parsed struct {
		XMLName xml.Name `xml:"ApiResponse"`
		namecheapStatus
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return fmt.Errorf("parse namecheap setHosts XML: %w", err)
	}
	return parsed.err()
}

// splitSLDTLD splits "acme.com" into ("acme", "com"). Subdomains are
// rejected — Namecheap's API operates at the registered-domain level
// and treats subdomains as host records (Name="mail" within domain
// "acme.com"). For domains with multi-label TLDs ("acme.co.uk") this
// splits at the first dot which is wrong — v0.1 returns an error
// telling the operator to use the registered domain explicitly.
func splitSLDTLD(domain string) (sld, tld string) {
	idx := strings.IndexByte(domain, '.')
	if idx <= 0 {
		return "", ""
	}
	return domain[:idx], domain[idx+1:]
}

// ─── IONOS provider ────────────────────────────────────────────────
//
// IONOS's Hosting DNS API is zone-id oriented: records live under a
// zone identified by an opaque id, not by the domain name. So every op
// first resolves the domain to its zone id via list_zones, then reads
// the whole zone (get_zone) and does per-record CRUD by record id
// (create_records / update_record / delete_record).
//
// Record `name` is the full FQDN, the same convention Porkbun uses, so
// the canonical DNSRecord mapping and the wantFQ/sub matching below are
// identical in spirit to porkbunProvider.
//
// create_records takes a top-level JSON array body; the catalog tool
// declares body_root_param: "records" so the integration runner sends
// the `records` array verbatim as the request body.

type ionosProvider struct{ bound *sdk.BoundIntegration }

type ionosZoneRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type ionosRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // full FQDN, e.g. "mail.acme.com"
	RootName string `json:"rootName"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Prio     int    `json:"prio"`
	Disabled bool   `json:"disabled"`
}

type ionosZone struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Records []ionosRecord `json:"records"`
}

// zoneIDFor resolves a domain name to its IONOS zone id. list_zones
// returns a top-level array of {id,name,type}; match on name.
func (p *ionosProvider) zoneIDFor(ctx *sdk.AppCtx, domain string) (string, error) {
	raw, err := providerCall(ctx, p.bound, "list_zones", map[string]any{})
	if err != nil {
		return "", err
	}
	var zones []ionosZoneRef
	if err := json.Unmarshal(raw, &zones); err != nil {
		return "", fmt.Errorf("parse ionos zones: %w", err)
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, domain) {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("no IONOS zone found for %q", domain)
}

// fetchZone resolves the zone id and pulls the full zone (id + records)
// in one place so Upsert/Delete have both without a second lookup.
func (p *ionosProvider) fetchZone(ctx *sdk.AppCtx, domain string) (*ionosZone, error) {
	zid, err := p.zoneIDFor(ctx, domain)
	if err != nil {
		return nil, err
	}
	raw, err := providerCall(ctx, p.bound, "get_zone", map[string]any{"zoneId": zid})
	if err != nil {
		return nil, err
	}
	var z ionosZone
	if err := json.Unmarshal(raw, &z); err != nil {
		return nil, fmt.Errorf("parse ionos zone: %w", err)
	}
	if z.ID == "" {
		z.ID = zid
	}
	return &z, nil
}

func (p *ionosProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	z, err := p.fetchZone(ctx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]DNSRecord, 0, len(z.Records))
	for _, r := range z.Records {
		out = append(out, DNSRecord{
			ID:    r.ID,
			Name:  r.Name,
			Type:  strings.ToUpper(r.Type),
			Value: r.Content,
			TTL:   r.TTL,
			Prio:  r.Prio,
		})
	}
	return out, nil
}

// ionosSplitMX pulls the priority out of an MX value of the form
// "<prio> <host>". When no priority is present it defaults to 10 — IONOS
// rejects MX/SRV records without one, mirroring namecheapProvider.
func ionosSplitMX(rtype, value string) (content string, prio int) {
	content = value
	if rtype == "MX" || rtype == "SRV" {
		if parts := strings.SplitN(value, " ", 2); len(parts) == 2 {
			if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				prio = n
				content = strings.TrimSpace(parts[1])
			}
		}
		if prio == 0 {
			prio = 10
		}
	}
	return content, prio
}

func (p *ionosProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int) (string, error) {
	z, err := p.fetchZone(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("list before upsert: %w", err)
	}
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	content, prio := ionosSplitMX(rtype, value)

	var match *ionosRecord
	for i := range z.Records {
		r := &z.Records[i]
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub) {
			match = r
			break
		}
	}

	if match != nil {
		if strings.EqualFold(strings.TrimSpace(match.Content), strings.TrimSpace(content)) &&
			match.TTL == ttl && match.Prio == prio {
			return "unchanged", nil
		}
		payload := map[string]any{
			"zoneId":   z.ID,
			"recordId": match.ID,
			"content":  content,
			"ttl":      ttl,
		}
		if rtype == "MX" || rtype == "SRV" {
			payload["prio"] = prio
		}
		if _, err := providerCall(ctx, p.bound, "update_record", payload); err != nil {
			return "", fmt.Errorf("update: %w", err)
		}
		return "updated", nil
	}

	rec := map[string]any{
		"name":    wantFQ,
		"type":    rtype,
		"content": content,
		"ttl":     ttl,
	}
	if rtype == "MX" || rtype == "SRV" {
		rec["prio"] = prio
	}
	createPayload := map[string]any{
		"zoneId":  z.ID,
		"records": []any{rec},
	}
	if _, err := providerCall(ctx, p.bound, "create_records", createPayload); err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", nil
}

func (p *ionosProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype string) error {
	z, err := p.fetchZone(ctx, domain)
	if err != nil {
		return err
	}
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	var ids []string
	for _, r := range z.Records {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub) {
			ids = append(ids, r.ID)
		}
	}
	for _, id := range ids {
		if _, err := providerCall(ctx, p.bound, "delete_record", map[string]any{
			"zoneId":   z.ID,
			"recordId": id,
		}); err != nil {
			return fmt.Errorf("delete record %s: %w", id, err)
		}
	}
	return nil
}

// ─── Spaceship provider ───────────────────────────────────────────
//
// Spaceship DNS works with batch "save" and "delete" operations. The
// integration catalog exposes those as list_dns_records, save_dns_records,
// and delete_dns_records, so this adapter stays inside the integration
// boundary and never talks to Spaceship directly.

type spaceshipProvider struct{ bound *sdk.BoundIntegration }

type spaceshipRegistrar struct{ bound *sdk.BoundIntegration }

func (r *spaceshipRegistrar) CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error) {
	raw, err := providerCall(ctx, r.bound, "check_single_domain_availability", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	out := parseSpaceshipAvailability(domain, r.bound, raw)
	return &out, nil
}

func (r *spaceshipRegistrar) Pricing(*sdk.AppCtx, string) (any, error) {
	return nil, errors.New("Spaceship pricing is not exposed in domains; paid registration flows are disabled for this provider")
}

func (r *spaceshipRegistrar) Register(*sdk.AppCtx, DomainRegistrationRequest) (json.RawMessage, error) {
	return nil, errors.New("Spaceship registration is intentionally not supported by domains because it would spend money; use Porkbun for domain_register")
}

func (p *spaceshipProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	raw, err := providerCall(ctx, p.bound, "list_dns_records", map[string]any{
		"domain": domain,
		"take":   500,
		"skip":   0,
	})
	if err != nil {
		return nil, err
	}
	return parseSpaceshipRecords(domain, raw), nil
}

func (p *spaceshipProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int) (string, error) {
	existing, err := p.List(ctx, domain)
	if err != nil {
		return "", fmt.Errorf("list before upsert: %w", err)
	}
	content, prio := spaceshipCanonicalValuePrio(rtype, value)
	var match *DNSRecord
	for i := range existing {
		r := &existing[i]
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if spaceshipRecordNameMatches(r.Name, domain, sub) {
			match = r
			break
		}
	}
	if match != nil &&
		strings.EqualFold(strings.TrimSpace(match.Value), strings.TrimSpace(content)) &&
		match.TTL == ttl && match.Prio == prio {
		return "unchanged", nil
	}
	item, err := spaceshipRecordItem(domain, sub, rtype, value, ttl)
	if err != nil {
		return "", err
	}
	if _, err := providerCall(ctx, p.bound, "save_dns_records", map[string]any{
		"domain": domain,
		"items":  []any{item},
	}); err != nil {
		return "", fmt.Errorf("save: %w", err)
	}
	if match != nil {
		return "updated", nil
	}
	return "created", nil
}

func (p *spaceshipProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype string) error {
	existing, err := p.List(ctx, domain)
	if err != nil {
		return err
	}
	records := make([]any, 0, 1)
	for _, r := range existing {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if !spaceshipRecordNameMatches(r.Name, domain, sub) {
			continue
		}
		records = append(records, spaceshipDeleteItem(r))
	}
	if len(records) == 0 {
		return nil
	}
	_, err = providerCall(ctx, p.bound, "delete_dns_records", map[string]any{
		"domain":  domain,
		"records": records,
	})
	return err
}

func parseSpaceshipRecords(domain string, raw json.RawMessage) []DNSRecord {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	items := spaceshipArrayFrom(root, "items", "records")
	out := make([]DNSRecord, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rtype := strings.ToUpper(spaceshipStringField(m, "type"))
		name := spaceshipStringField(m, "name")
		value, prio := spaceshipRecordValue(rtype, m)
		out = append(out, DNSRecord{
			ID:    spaceshipRecordID(m, rtype, name, value),
			Name:  spaceshipCanonicalName(domain, name),
			Type:  rtype,
			Value: value,
			TTL:   spaceshipIntField(m, "ttl"),
			Prio:  prio,
			Raw:   copyStringAnyMap(m),
		})
	}
	return out
}

func parseSpaceshipAvailability(domain string, bound *sdk.BoundIntegration, raw json.RawMessage) DomainAvailability {
	out := DomainAvailability{
		Domain:       domain,
		Provider:     bound.AppSlug,
		ConnectionID: bound.ConnectionID,
		Source:       "spaceship",
		Confidence:   "provider",
		Raw:          raw,
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return out
	}
	scopes := []map[string]any{root}
	for _, key := range []string{"response", "data", "result"} {
		if nested, ok := root[key].(map[string]any); ok {
			scopes = append(scopes, nested)
		}
	}
	if v, ok := firstBool(scopes, "available", "isAvailable"); ok {
		out.Available = v
	} else if s := firstString(scopes, "availability", "status"); s != "" {
		out.Available = availabilityStringIsAvailable(s)
	}
	if s := firstString(scopes, "price", "registrationPrice"); s != "" {
		out.Price = s
	}
	if s := firstString(scopes, "currency"); s != "" {
		out.Currency = s
	}
	if v, ok := firstBool(scopes, "premium", "isPremium"); ok {
		out.Premium = v
	}
	return out
}

func spaceshipArrayFrom(root any, keys ...string) []any {
	switch v := root.(type) {
	case []any:
		return v
	case map[string]any:
		for _, key := range keys {
			if arr, ok := v[key].([]any); ok {
				return arr
			}
		}
		for _, key := range []string{"data", "result", "response"} {
			if nested, ok := v[key]; ok {
				if arr := spaceshipArrayFrom(nested, keys...); len(arr) > 0 {
					return arr
				}
			}
		}
	}
	return nil
}

func spaceshipCanonicalName(domain, name string) string {
	name = strings.TrimSpace(strings.TrimSuffix(name, "."))
	if name == "" || name == "@" {
		return domain
	}
	return strings.ToLower(name)
}

func spaceshipRecordNameMatches(name, domain, sub string) bool {
	name = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(name), "."))
	sub = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(sub), "."))
	domain = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(domain), "."))
	if sub == "" {
		return name == "" || name == "@" || name == domain
	}
	wantFQ := sub + "." + domain
	return name == sub || name == wantFQ
}

func spaceshipStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			case int:
				return strconv.Itoa(t)
			}
		}
	}
	return ""
}

func spaceshipIntField(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case int:
				return t
			case float64:
				return int(t)
			case string:
				n, _ := strconv.Atoi(strings.TrimSpace(t))
				return n
			}
		}
	}
	return 0
}

func spaceshipRecordValue(rtype string, m map[string]any) (string, int) {
	switch rtype {
	case "MX":
		return spaceshipStringField(m, "exchange", "value", "target"), spaceshipIntField(m, "preference", "priority")
	case "SRV":
		prio := spaceshipIntField(m, "priority")
		weight := spaceshipIntField(m, "weight")
		port := spaceshipIntField(m, "port")
		target := spaceshipStringField(m, "target", "value")
		if target != "" {
			return fmt.Sprintf("%d %d %s", weight, port, target), prio
		}
		return "", prio
	case "CAA":
		flag := spaceshipIntField(m, "flag")
		tag := spaceshipStringField(m, "tag")
		value := spaceshipStringField(m, "value")
		if tag != "" && value != "" {
			return fmt.Sprintf("%d %s %s", flag, tag, value), 0
		}
		return value, 0
	default:
		return spaceshipStringField(m, "address", "value", "cname", "exchange", "nameserver", "aliasName", "pointer", "target", "targetName"), spaceshipIntField(m, "priority", "preference")
	}
}

func spaceshipCanonicalValuePrio(rtype, value string) (string, int) {
	value = strings.TrimSpace(value)
	switch rtype {
	case "MX":
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			if prio, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				return strings.TrimSpace(parts[1]), prio
			}
		}
		return value, 10
	case "SRV":
		parts := strings.Fields(value)
		if len(parts) >= 4 {
			if prio, err := strconv.Atoi(parts[0]); err == nil {
				return strings.Join(parts[1:], " "), prio
			}
		}
	}
	return value, 0
}

func spaceshipRecordItem(domain, sub, rtype, value string, ttl int) (map[string]any, error) {
	name := "@"
	if sub != "" {
		name = sub
	}
	item := map[string]any{
		"type": rtype,
		"name": name,
	}
	if ttl > 0 {
		item["ttl"] = ttl
	}
	content, prio := spaceshipCanonicalValuePrio(rtype, value)
	switch rtype {
	case "A", "AAAA":
		item["address"] = content
	case "TXT":
		item["value"] = content
	case "CNAME":
		item["cname"] = content
	case "MX":
		item["exchange"] = content
		item["preference"] = prio
	case "NS":
		item["nameserver"] = content
	case "ALIAS":
		item["aliasName"] = content
	case "PTR":
		item["pointer"] = content
	case "CAA":
		parts := strings.Fields(content)
		if len(parts) < 3 {
			return nil, errors.New("Spaceship CAA value must be '<flag> <tag> <value>', for example '0 issue letsencrypt.org'")
		}
		flag, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("Spaceship CAA flag must be numeric: %w", err)
		}
		item["flag"] = flag
		item["tag"] = parts[1]
		item["value"] = strings.Join(parts[2:], " ")
	case "SRV":
		parts := strings.Fields(value)
		if len(parts) < 4 {
			return nil, errors.New("Spaceship SRV value must be '<priority> <weight> <port> <target>'")
		}
		priority, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV priority must be numeric: %w", err)
		}
		weight, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV weight must be numeric: %w", err)
		}
		port, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV port must be numeric: %w", err)
		}
		item["priority"] = priority
		item["weight"] = weight
		item["port"] = port
		item["target"] = strings.Join(parts[3:], " ")
	default:
		return nil, fmt.Errorf("Spaceship DNS write support is not implemented for %s records", rtype)
	}
	_ = domain
	return item, nil
}

func spaceshipDeleteItem(r DNSRecord) map[string]any {
	if len(r.Raw) > 0 {
		item := copyStringAnyMap(r.Raw)
		delete(item, "ttl")
		return item
	}
	item := map[string]any{
		"type": r.Type,
		"name": r.Name,
	}
	if r.Prio != 0 {
		item["priority"] = r.Prio
	}
	switch r.Type {
	case "A", "AAAA":
		item["address"] = r.Value
	case "TXT", "CAA":
		item["value"] = r.Value
	case "CNAME":
		item["cname"] = r.Value
	case "MX":
		item["exchange"] = r.Value
		item["preference"] = r.Prio
	case "NS":
		item["nameserver"] = r.Value
	case "ALIAS":
		item["aliasName"] = r.Value
	case "PTR":
		item["pointer"] = r.Value
	default:
		item["value"] = r.Value
	}
	return item
}

func spaceshipRecordID(m map[string]any, rtype, name, value string) string {
	if id := spaceshipStringField(m, "id", "recordId"); id != "" {
		return id
	}
	return strings.ToLower(strings.Join([]string{rtype, name, value}, ":"))
}

func copyStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ─── Tool handlers (use dnsProviderImpl) ───────────────────────────

// resolveProviderForDomain looks up the connection pinned on the
// domain row (when one exists) and returns the matching provider.
// Falls back to the role binding for domains not in the inventory or
// rows added before per-domain pinning landed.
func (a *App) resolveProviderForDomain(ctx *sdk.AppCtx, args map[string]any, name string) (dnsProviderImpl, error) {
	var connID int64
	if pid, perr := resolveProjectFromArgs(args); perr == nil {
		if d, _ := dbDomainGetByName(ctx.AppDB(), pid, name); d != nil {
			connID = d.ConnectionID
		}
	}
	prov, _, err := a.providerFor(ctx, connID)
	return prov, err
}

func (a *App) toolDomainRecordsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	prov, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	records, err := prov.List(ctx, domain)
	if err != nil {
		return nil, err
	}
	if t := strings.ToUpper(strArg(args, "type")); t != "" {
		records = filterRecords(records, func(r DNSRecord) bool { return r.Type == t })
	}
	if n := strings.ToLower(strArg(args, "name")); n != "" && n != "@" {
		fq := n + "." + domain
		records = filterRecords(records, func(r DNSRecord) bool {
			return strings.EqualFold(r.Name, fq) || strings.EqualFold(r.Name, n)
		})
	}
	return map[string]any{"records": records, "count": len(records), "domain": domain}, nil
}

func (a *App) toolDomainRecordsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	sub := normaliseSubaddress(strArg(args, "name"))
	rtype, err := normaliseRecordType(strArg(args, "type"))
	if err != nil {
		return nil, err
	}
	value := strArg(args, "value")
	if value == "" {
		return nil, errors.New("value required")
	}
	ttl := intArg(args, "ttl", 600)

	prov, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	deletedConflicts, err := deleteApexAddressConflicts(ctx, prov, domain, sub, rtype)
	if err != nil {
		return nil, err
	}
	action, err := prov.Upsert(ctx, domain, sub, rtype, value, ttl)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"action": action,
		"domain": domain,
		"name":   sub,
		"type":   rtype,
		"value":  value,
		"ttl":    ttl,
	}
	if len(deletedConflicts) > 0 {
		out["deleted_conflicts"] = deletedConflicts
	}
	return out, nil
}

func deleteApexAddressConflicts(ctx *sdk.AppCtx, prov dnsProviderImpl, domain, sub, rtype string) ([]string, error) {
	if sub != "" {
		return nil, nil
	}
	conflicts := apexAddressConflictTypes(rtype)
	if len(conflicts) == 0 {
		return nil, nil
	}
	records, err := prov.List(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("list before conflict cleanup: %w", err)
	}
	var deleted []string
	for _, conflictType := range conflicts {
		if !hasRecordAtName(records, domain, "", conflictType) {
			continue
		}
		if err := prov.Delete(ctx, domain, "", conflictType); err != nil {
			return deleted, fmt.Errorf("delete conflicting %s record: %w", conflictType, err)
		}
		deleted = append(deleted, conflictType)
	}
	return deleted, nil
}

func apexAddressConflictTypes(rtype string) []string {
	switch rtype {
	case "A", "AAAA":
		return []string{"ALIAS", "CNAME"}
	case "ALIAS":
		return []string{"A", "AAAA", "CNAME"}
	case "CNAME":
		return []string{"A", "AAAA", "ALIAS"}
	default:
		return nil
	}
}

func hasRecordAtName(records []DNSRecord, domain, sub, rtype string) bool {
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	for _, r := range records {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub) {
			return true
		}
		if sub == "" && (r.Name == "" || r.Name == "@") {
			return true
		}
	}
	return false
}

func (a *App) toolDomainRecordsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, err
	}
	sub := normaliseSubaddress(strArg(args, "name"))
	rtype, err := normaliseRecordType(strArg(args, "type"))
	if err != nil {
		return nil, err
	}
	prov, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	if err := prov.Delete(ctx, domain, sub, rtype); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "domain": domain, "name": sub, "type": rtype}, nil
}

// ─── Provider response normalisation ──────────────────────────────

func parsePorkbunRecords(raw json.RawMessage) []DNSRecord {
	var probe struct {
		Status  string `json:"status"`
		Records []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
			TTL     string `json:"ttl"`
			Prio    string `json:"prio"`
			Notes   string `json:"notes"`
		} `json:"records"`
	}
	_ = json.Unmarshal(raw, &probe)
	out := make([]DNSRecord, 0, len(probe.Records))
	for _, r := range probe.Records {
		ttl, _ := strconv.Atoi(r.TTL)
		prio, _ := strconv.Atoi(r.Prio)
		out = append(out, DNSRecord{
			ID:    r.ID,
			Name:  r.Name,
			Type:  strings.ToUpper(r.Type),
			Value: r.Content,
			TTL:   ttl,
			Prio:  prio,
			Notes: r.Notes,
		})
	}
	return out
}

func parsePorkbunAvailability(domain string, bound *sdk.BoundIntegration, raw json.RawMessage) DomainAvailability {
	out := DomainAvailability{
		Domain:       domain,
		Provider:     bound.AppSlug,
		ConnectionID: bound.ConnectionID,
		Raw:          raw,
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return out
	}
	scopes := []map[string]any{root}
	for _, key := range []string{"response", "data", "result"} {
		if nested, ok := root[key].(map[string]any); ok {
			scopes = append(scopes, nested)
		}
	}
	if v, ok := firstBool(scopes, "available"); ok {
		out.Available = v
	} else if s := firstString(scopes, "avail", "available", "availability"); s != "" {
		out.Available = availabilityStringIsAvailable(s)
	}
	if s := firstString(scopes, "price", "registration", "registrationPrice", "premiumRegistrationPrice"); s != "" {
		out.Price = s
		out.Currency = "USD"
	}
	if v, ok := firstBool(scopes, "premium", "isPremium", "IsPremiumName"); ok {
		out.Premium = v
	} else if s := firstString(scopes, "type"); strings.Contains(strings.ToLower(s), "premium") {
		out.Premium = true
	}
	return out
}

func firstString(scopes []map[string]any, keys ...string) string {
	for _, m := range scopes {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				switch t := v.(type) {
				case string:
					if strings.TrimSpace(t) != "" {
						return strings.TrimSpace(t)
					}
				case float64:
					return strconv.FormatFloat(t, 'f', -1, 64)
				case int:
					return strconv.Itoa(t)
				case bool:
					if t {
						return "true"
					}
					return "false"
				}
			}
		}
	}
	return ""
}

func firstBool(scopes []map[string]any, keys ...string) (bool, bool) {
	for _, m := range scopes {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				switch t := v.(type) {
				case bool:
					return t, true
				case string:
					s := strings.ToLower(strings.TrimSpace(t))
					switch s {
					case "true", "yes", "y", "1", "available":
						return true, true
					case "false", "no", "n", "0", "unavailable", "taken":
						return false, true
					}
				}
			}
		}
	}
	return false, false
}

func availabilityStringIsAvailable(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y", "1", "available":
		return true
	default:
		return false
	}
}

func pricingEntryForTLD(raw json.RawMessage, tld string) any {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	tld = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tld)), ".")
	if m, ok := root.(map[string]any); ok {
		if entry := mapLookupTLD(m, tld); entry != nil {
			return entry
		}
		for _, key := range []string{"pricing", "prices", "response", "data"} {
			if nested, ok := m[key].(map[string]any); ok {
				if entry := mapLookupTLD(nested, tld); entry != nil {
					return entry
				}
			}
		}
	}
	return nil
}

func mapLookupTLD(m map[string]any, tld string) any {
	for _, key := range []string{tld, "." + tld} {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

var rdapLookupBaseURL = "https://rdap.org/domain/"

func publicRDAPAvailability(domain string, primaryErr error) (*DomainAvailability, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rdapLookupBaseURL+domain, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("availability check failed: %w; RDAP fallback failed: %w", primaryErr, err)
	}
	defer res.Body.Close()
	warning := fmt.Sprintf("Registrar availability check failed (%s). Used public RDAP fallback; availability is best-effort and final registration is still performed by Porkbun.", primaryErr)
	switch res.StatusCode {
	case http.StatusOK:
		var raw json.RawMessage
		_ = json.NewDecoder(res.Body).Decode(&raw)
		return &DomainAvailability{
			Domain:     domain,
			Available:  false,
			Provider:   "rdap",
			Source:     "rdap",
			Confidence: "high",
			Warning:    warning,
			Raw:        raw,
		}, nil
	case http.StatusNotFound:
		return &DomainAvailability{
			Domain:     domain,
			Available:  true,
			Provider:   "rdap",
			Source:     "rdap",
			Confidence: "best_effort",
			Warning:    warning,
		}, nil
	default:
		return nil, fmt.Errorf("availability check failed: %w; RDAP fallback returned HTTP %d", primaryErr, res.StatusCode)
	}
}

func filterRecords(in []DNSRecord, keep func(DNSRecord) bool) []DNSRecord {
	out := make([]DNSRecord, 0, len(in))
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// ─── HTTP routes (panel data + tool dispatch) ──────────────────────

func (a *App) handleDomainsList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbDomainList(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"domains": out})
}

func (a *App) handleDomainItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/domains/")
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "name required")
		return
	}
	name, err := normaliseDomainName(parts[0])
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := dbDomainGetByName(globalCtx.AppDB(), pid, name)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if d == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if len(parts) == 2 && parts[1] == "records" {
		out, err := a.toolDomainRecordsList(globalCtx, map[string]any{"domain": name})
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	httpJSON(w, map[string]any{"domain": d})
}

// handleConnectionsList — feeds the panel's connection picker. Returns
// every Porkbun + Namecheap + IONOS + Spaceship connection in this project so the
// operator can pin one specifically when adding a domain. Not an MCP tool
// because agents shouldn't be picking connections for users; operator UI.
func (a *App) handleConnectionsList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	type conn struct {
		ID      int64  `json:"id"`
		AppSlug string `json:"app_slug"`
		Name    string `json:"name"`
		Status  string `json:"status"`
	}
	out := []conn{}
	for _, slug := range []string{"porkbun", "namecheap", "ionos", "spaceship"} {
		rows, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: pid, AppSlug: slug})
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, c := range rows {
			out = append(out, conn{ID: c.ID, AppSlug: c.AppSlug, Name: c.Name, Status: c.Status})
		}
	}
	httpJSON(w, map[string]any{"connections": out})
}

// handleToolsCall — same generic dispatcher messaging uses, so the
// panel can call any tool via a single HTTP path.
func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Tool == "" {
		httpErr(w, http.StatusBadRequest, "tool required")
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	var handler sdk.ToolHandler
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		httpErr(w, http.StatusNotFound, "unknown tool: "+body.Tool)
		return
	}
	out, err := handler(globalCtx, body.Args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── DB helpers ────────────────────────────────────────────────────

const domainSelectCols = `id, project_id, name,
	COALESCE(registrar_slug,''), COALESCE(dns_provider_slug,''),
	COALESCE(connection_id, 0),
	COALESCE(expires_at,''), COALESCE(notes,''),
	COALESCE(created_at,''), COALESCE(updated_at,'')`

func scanDomain(s interface{ Scan(...any) error }) (*Domain, error) {
	d := &Domain{}
	err := s.Scan(&d.ID, &d.ProjectID, &d.Name,
		&d.RegistrarSlug, &d.DNSProviderSlug,
		&d.ConnectionID,
		&d.ExpiresAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func upsertDomainInventory(ctx *sdk.AppCtx, pid, name, reg, dns, notes string, connID int64) (*Domain, error) {
	// SQLite NULLIF(?, 0) makes "no connection" NULL, so COALESCE
	// preserves an existing pin on re-add instead of clobbering it.
	res, err := ctx.AppDB().Exec(
		`INSERT INTO domains (project_id, name, registrar_slug, dns_provider_slug, notes, connection_id)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, 0))
		 ON CONFLICT(project_id, name) WHERE deleted_at IS NULL
		 DO UPDATE SET
		   registrar_slug    = COALESCE(NULLIF(excluded.registrar_slug,''), domains.registrar_slug),
		   dns_provider_slug = COALESCE(NULLIF(excluded.dns_provider_slug,''), domains.dns_provider_slug),
		   connection_id     = COALESCE(excluded.connection_id, domains.connection_id),
		   notes             = COALESCE(NULLIF(excluded.notes,''), domains.notes),
		   updated_at        = CURRENT_TIMESTAMP`,
		pid, name, reg, dns, notes, connID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert domain: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
			pid, name).Scan(&id)
	}
	return dbDomainGet(ctx.AppDB(), pid, id)
}

func dbDomainList(db *sql.DB, pid string) ([]*Domain, error) {
	rows, err := db.Query(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE project_id = ? AND deleted_at IS NULL
		 ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Domain{}
	for rows.Next() {
		if d, err := scanDomain(rows); err == nil && d != nil {
			out = append(out, d)
		}
	}
	return out, nil
}

func dbDomainGet(db *sql.DB, pid string, id int64) (*Domain, error) {
	return scanDomain(db.QueryRow(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid,
	))
}

func dbDomainGetByName(db *sql.DB, pid, name string) (*Domain, error) {
	return scanDomain(db.QueryRow(
		`SELECT `+domainSelectCols+`
		 FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name,
	))
}

// ─── Tiny utilities ────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

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

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
