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
version: 0.1.0
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
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "domain_add",
			Description: "Register a domain with this app. The domain itself must already exist at the bound DNS provider — this just records it locally for other apps to reference. Args: name (e.g. 'acme.com'), registrar?, dns_provider?, notes?.",
			InputSchema: schemaObject(map[string]any{
				"name":              map[string]any{"type": "string"},
				"registrar":         map[string]any{"type": "string"},
				"dns_provider":      map[string]any{"type": "string"},
				"notes":             map[string]any{"type": "string"},
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
	ExpiresAt       string `json:"expires_at,omitempty"`
	Notes           string `json:"notes,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
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
	if dns == "" {
		dns = reg
	}
	if dns == "" {
		// Best-effort: read the bound dns_provider's slug.
		if bound := ctx.IntegrationFor("dns_provider"); bound != nil && bound.AppSlug != "" {
			dns = bound.AppSlug
			if reg == "" {
				reg = dns
			}
		}
	}
	notes := strArg(args, "notes")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO domains (project_id, name, registrar_slug, dns_provider_slug, notes)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, name) WHERE deleted_at IS NULL
		 DO UPDATE SET
		   registrar_slug    = COALESCE(NULLIF(excluded.registrar_slug,''), domains.registrar_slug),
		   dns_provider_slug = COALESCE(NULLIF(excluded.dns_provider_slug,''), domains.dns_provider_slug),
		   notes             = COALESCE(NULLIF(excluded.notes,''), domains.notes),
		   updated_at        = CURRENT_TIMESTAMP`,
		pid, name, reg, dns, notes,
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

// ─── DNS record CRUD via dns_provider ──────────────────────────────

func providerCall(ctx *sdk.AppCtx, capability string, payload map[string]any) (json.RawMessage, error) {
	bound := ctx.IntegrationFor("dns_provider")
	if bound == nil {
		return nil, errors.New("no dns_provider bound — install/select a Porkbun or Namecheap connection")
	}
	tool := bound.ToolFor(capability)
	if tool == "" {
		return nil, fmt.Errorf("dns_provider doesn't expose %q", capability)
	}
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

func (a *App) toolDomainRecordsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	raw, err := providerCall(ctx, "dns.list_records", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	records := parsePorkbunRecords(raw)
	// Optional filtering by type/name.
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

	// Step 1: list existing records of this name+type to decide
	// upsert path. Porkbun's GET /dns/retrieveByNameType/{domain}/{type}/{subdomain}
	// returns either an existing record or empty.
	existingRaw, err := providerCall(ctx, "dns.list_records", map[string]any{"domain": domain})
	if err != nil {
		return nil, fmt.Errorf("list before upsert: %w", err)
	}
	existing := parsePorkbunRecords(existingRaw)
	wantFQ := sub
	if sub != "" {
		wantFQ = sub + "." + domain
	} else {
		wantFQ = domain
	}
	matches := filterRecords(existing, func(r DNSRecord) bool {
		if !strings.EqualFold(r.Type, rtype) {
			return false
		}
		// Match either FQDN form or short form depending on what the
		// provider returned.
		return strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub)
	})

	if len(matches) > 0 {
		// Edit by name+type — replaces all records of that name+type
		// with the single new value. v0.1 deliberately rewrites rather
		// than tracks per-id; matches the natural semantics for MX/TXT
		// where multiple values are conceptual variants of one setting.
		payload := map[string]any{
			"domain":  domain,
			"type":    rtype,
			"subdomain": sub,
			"content": value,
			"ttl":     fmt.Sprintf("%d", ttl),
		}
		if rtype == "MX" {
			parts := strings.SplitN(value, " ", 2)
			if len(parts) == 2 {
				payload["prio"] = parts[0]
				payload["content"] = parts[1]
			}
		}
		if _, err := providerCall(ctx, "dns.edit_records_by_type", payload); err != nil {
			return nil, fmt.Errorf("edit: %w", err)
		}
		return map[string]any{
			"action":  "updated",
			"domain":  domain,
			"name":    sub,
			"type":    rtype,
			"value":   value,
			"ttl":     ttl,
			"matched": len(matches),
		}, nil
	}

	// Create a new record.
	createPayload := map[string]any{
		"domain":  domain,
		"name":    sub,
		"type":    rtype,
		"content": value,
		"ttl":     fmt.Sprintf("%d", ttl),
	}
	if rtype == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			createPayload["prio"] = parts[0]
			createPayload["content"] = parts[1]
		}
	}
	if _, err := providerCall(ctx, "dns.create_record", createPayload); err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	return map[string]any{
		"action":  "created",
		"domain":  domain,
		"name":    sub,
		"type":    rtype,
		"value":   value,
		"ttl":     ttl,
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
	_, err = providerCall(ctx, "dns.delete_records_by_type", map[string]any{
		"domain":    domain,
		"type":      rtype,
		"subdomain": sub,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "domain": domain, "name": sub, "type": rtype}, nil
}

// ─── Provider response normalisation ──────────────────────────────

// parsePorkbunRecords reads the Porkbun /dns/retrieve response shape:
//   { status: "SUCCESS", records: [{id, name, type, content, ttl, prio, notes}] }
// Returns the canonical DNSRecord slice. Other providers (Namecheap)
// would have their own parser here.
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

func dbDomainList(db *sql.DB, pid string) ([]*Domain, error) {
	rows, err := db.Query(
		`SELECT id, project_id, name, COALESCE(registrar_slug,''), COALESCE(dns_provider_slug,''),
		        COALESCE(expires_at,''), COALESCE(notes,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM domains WHERE project_id = ? AND deleted_at IS NULL
		 ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Domain{}
	for rows.Next() {
		d := &Domain{}
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Name, &d.RegistrarSlug, &d.DNSProviderSlug,
			&d.ExpiresAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt); err == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

func dbDomainGet(db *sql.DB, pid string, id int64) (*Domain, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, COALESCE(registrar_slug,''), COALESCE(dns_provider_slug,''),
		        COALESCE(expires_at,''), COALESCE(notes,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM domains WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid,
	)
	d := &Domain{}
	err := row.Scan(&d.ID, &d.ProjectID, &d.Name, &d.RegistrarSlug, &d.DNSProviderSlug,
		&d.ExpiresAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

func dbDomainGetByName(db *sql.DB, pid, name string) (*Domain, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, COALESCE(registrar_slug,''), COALESCE(dns_provider_slug,''),
		        COALESCE(expires_at,''), COALESCE(notes,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM domains WHERE project_id = ? AND name = ? AND deleted_at IS NULL`,
		pid, name,
	)
	d := &Domain{}
	err := row.Scan(&d.ID, &d.ProjectID, &d.Name, &d.RegistrarSlug, &d.DNSProviderSlug,
		&d.ExpiresAt, &d.Notes, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
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
