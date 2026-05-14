// Domains v0.1 — DNS + domain inventory for Apteva projects.
//
// Apps that need to write DNS records (messaging for SES MX/DKIM,
// storage for CDN CNAMEs, future certs app for ACME challenges) call
// this app instead of speaking each registrar's API directly.
//
// Architecture:
//   - manifest declares one required integration: dns_provider with
//     compatible_slugs [porkbun, namecheap].
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

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest ──────────────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: domains
display_name: Domains
version: 0.2.1
description: |
  DNS + domain inventory. Other apps call this for record CRUD
  instead of talking to registrars directly. v0.1: Porkbun.
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
      compatible_slugs: [porkbun, namecheap]
      capabilities: [dns.list_records, dns.create_record, dns.edit_records_by_type, dns.delete_records_by_type]
      tools:
        dns.list_records: list_dns_records
        dns.create_record: create_dns_record
        dns.edit_records_by_type: edit_dns_records_by_type
        dns.delete_records_by_type: delete_dns_records_by_type
      required: true
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: domain_add,            description: "Register a domain with this app." }
    - { name: domain_remove,         description: "Soft-delete a domain from local inventory." }
    - { name: domain_list,           description: "List domains for the project." }
    - { name: domain_get,            description: "Fetch one domain by name." }
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
// provider-agnostic. The proxy layer translates the provider's actual
// shape into this. Today we only support Porkbun's shape; a Namecheap
// adapter would translate XML records into this struct.
type DNSRecord struct {
	ID    string `json:"id"`     // provider-side record id (Porkbun: numeric string)
	Name  string `json:"name"`   // FQDN as the provider returns it (e.g. "mail.acme.com")
	Type  string `json:"type"`   // A | AAAA | CNAME | MX | TXT | NS | SRV | CAA
	Value string `json:"value"`  // record content
	TTL   int    `json:"ttl"`
	Prio  int    `json:"prio,omitempty"` // MX priority etc.
	Notes string `json:"notes,omitempty"`
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
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA", "ALIAS":
		return t, nil
	}
	return "", fmt.Errorf("unsupported record type %q", t)
}

// normaliseSubaddress: '@' or '' means apex; otherwise lowercase.
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

	// SQLite NULLIF(?, 0) → NULL when the caller passed no
	// connection, so the COALESCE preserves the existing pin on
	// re-add instead of clobbering it.
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
		// On conflict the insert is replaced by an UPDATE; LastInsertId
		// is 0. Fetch the existing row.
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
			pid, name).Scan(&id)
	}
	d, _ := dbDomainGet(ctx.AppDB(), pid, id)
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
// CRUD vs Namecheap's "set all hosts at once" model) behind a uniform
// shape. The toolDomainRecords* handlers go through this interface;
// new providers add a dnsProviderImpl and a slug case below.

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
		}
		return nil, bound, fmt.Errorf("unsupported provider slug %q on connection %d (compatible: porkbun, namecheap)", conn.AppSlug, connID)
	}
	bound := ctx.IntegrationFor("dns_provider")
	if bound == nil {
		return nil, nil, errors.New("no dns_provider bound — install/select a Porkbun or Namecheap connection, or pass connection_id explicitly")
	}
	switch bound.AppSlug {
	case "porkbun":
		return &porkbunProvider{bound: bound}, bound, nil
	case "namecheap":
		return &namecheapProvider{bound: bound}, bound, nil
	}
	return nil, bound, fmt.Errorf("unsupported dns_provider slug %q (compatible: porkbun, namecheap)", bound.AppSlug)
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

// ─── Porkbun provider ──────────────────────────────────────────────

type porkbunProvider struct{ bound *sdk.BoundIntegration }

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
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", nil
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

type namecheapHostsResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Errors []struct {
			Number string `xml:"Number,attr"`
			Text   string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
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
	if strings.EqualFold(parsed.Status, "ERROR") || len(parsed.Errors.Errors) > 0 {
		var msgs []string
		for _, e := range parsed.Errors.Errors {
			msgs = append(msgs, fmt.Sprintf("[%s] %s", e.Number, strings.TrimSpace(e.Text)))
		}
		return nil, fmt.Errorf("namecheap error: %s", strings.Join(msgs, "; "))
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
		if prio != "" {
			newHost.MXPref = prio
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
	body, _ := xmlDataToString(raw)
	if strings.Contains(body, "<Status>ERROR") || strings.Contains(strings.ToLower(body), "error") && strings.Contains(body, "<Number>") {
		return fmt.Errorf("namecheap setHosts error: %s", truncate(body, 400))
	}
	return nil
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
	action, err := prov.Upsert(ctx, domain, sub, rtype, value, ttl)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"action": action,
		"domain": domain,
		"name":   sub,
		"type":   rtype,
		"value":  value,
		"ttl":    ttl,
	}, nil
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
// every Porkbun + Namecheap connection in this project so the operator
// can pin one specifically when adding a domain. Not an MCP tool because
// agents shouldn't be picking connections for users; this is operator UI.
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
	for _, slug := range []string{"porkbun", "namecheap"} {
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
