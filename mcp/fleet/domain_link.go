package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Per-tenant domain attach/detach via the optional Domains/Certs/Routes
// apps. Ported from apps/mcp/deploy/domain_link.go with Tenant in place
// of Deployment and the route target derived from the tenant's
// apteva-server port (stored in BaseURL).
//
// Boundary with the parent's routes app: fleet writes routes on the
// PARENT's routes table (the parent owns Caddy). Each tenant should
// keep its own routes app in hostrouter mode — see the consolidated
// proposal in CLAUDE.md for the wildcard-per-tenant follow-up.

// ─── envelope helpers ─────────────────────────────────────────────

// callDomainsTool invokes a tool on the Domains app and unwraps the
// MCP envelope. projectID is injected as `_project_id` so the call
// resolves when Domains is installed global-scoped (no
// APTEVA_PROJECT_ID in its env) — see feedback_project_id_global_calls.
func callDomainsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "domains", projectID, tool, args, out)
}

// callCertsTool — same shape; project-scoped per tenant.
func callCertsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "certs", projectID, tool, args, out)
}

// callRoutesTool — routes data is project-agnostic, so no _project_id
// injection; the route owner_install_id is what scopes ownership.
func callRoutesTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "routes", "", tool, args, out)
}

func callSiblingTool(ctx *sdk.AppCtx, appName, projectID, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	if projectID != "" {
		if args == nil {
			args = map[string]any{}
		}
		if _, ok := args["_project_id"]; !ok {
			args["_project_id"] = projectID
		}
	}
	raw, err := ctx.PlatformAPI().CallApp(appName, tool, args)
	if err != nil {
		return fmt.Errorf("call %s.%s: %w", appName, tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode %s.%s envelope: %w", appName, tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%s.%s: %s", appName, tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return nil
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" || out == nil {
		return nil
	}
	return json.Unmarshal([]byte(text), out)
}

// ─── integration availability ─────────────────────────────────────

func (a *App) domainsAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "domains")
}
func (a *App) certsAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "certs")
}
func (a *App) routesAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "routes")
}

func integrationBound(ctx *sdk.AppCtx, role string) bool {
	if ctx == nil {
		return false
	}
	b := ctx.IntegrationFor(role)
	return b != nil && b.Kind == "app"
}

// ─── apex resolution ──────────────────────────────────────────────

// resolveApex finds the registered domain that's a suffix of fqdn.
// "acme.fleet.example.com" with "fleet.example.com" registered →
// apex="fleet.example.com", sub="acme". An exact match returns
// sub="".
func resolveApex(ctx *sdk.AppCtx, projectID, fqdn string) (apex, sub string, err error) {
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := callDomainsTool(ctx, projectID, "domain_list", map[string]any{}, &resp); err != nil {
		return "", "", err
	}
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	best := ""
	for _, d := range resp.Domains {
		name := strings.ToLower(d.Name)
		if name == "" {
			continue
		}
		if fqdn == name || strings.HasSuffix(fqdn, "."+name) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("no registered domain matches %q — register it with the Domains app first", fqdn)
	}
	if fqdn == best {
		return best, "", nil
	}
	return best, strings.TrimSuffix(fqdn, "."+best), nil
}

// ─── attach / detach spec ─────────────────────────────────────────

type attachDomainSpec struct {
	FQDN   string
	Target string // record value; empty → fleet's publicHost
	Type   string // "A" | "CNAME"; empty → inferred from target shape
	TTL    int
	// ManageDNS=true (default): the Domains app writes/owns the DNS
	// record for fqdn. Used when the apex sits in our Domains catalog.
	// ManageDNS=false: client already pointed fqdn at our parent
	// machine; fleet skips the DNS write and still does cert + route.
	// Fleet forces certs to use HTTP-01 in that case, so issuance
	// never tries DNS-01 against a zone we do not control.
	ManageDNS bool
}

type domainRecordSpec struct {
	Domain string
	Name   string
	Type   string
	Value  string
	TTL    int
}

// resolveTarget picks the DNS record value: explicit > publicHost.
// publicHost is set at OnMount via detectPublicHost; on a cloud box
// that's the outbound interface IP, perfect for an A record pointing
// at the parent apteva-server.
func (a *App) resolveTarget(spec attachDomainSpec) string {
	if t := strings.TrimSpace(spec.Target); t != "" {
		return t
	}
	if a != nil && a.publicHost != "" && a.publicHost != "localhost" {
		return a.publicHost
	}
	return ""
}

// inferRecordType: IP literal → A, else CNAME. Same heuristic as
// deploy/domain_link.go.
func inferRecordType(target string) string {
	if net.ParseIP(target) != nil {
		return "A"
	}
	return "CNAME"
}

func normaliseGrantDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(s, ".")))
	d = strings.TrimPrefix(d, "*.")
	if d == "" {
		return "", errors.New("domain required")
	}
	if strings.ContainsAny(d, " \t\r\n/?:#") {
		return "", fmt.Errorf("invalid domain %q", s)
	}
	if !strings.Contains(d, ".") {
		return "", fmt.Errorf("domain %q must contain a dot", s)
	}
	return d, nil
}

func wildcardHost(domain string) string {
	if domain == "" {
		return ""
	}
	return "*." + strings.TrimPrefix(domain, "*.")
}

func recordID(apex, name, rtype string) string {
	return apex + "|" + name + "|" + strings.ToUpper(rtype)
}

func splitGrantRecordID(s string) (apex, name, rtype string, ok bool) {
	parts := strings.Split(s, "|")
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2], true
	}
	// Backward compatibility with tenant_attach_domain's older
	// "<apex>|<type>" shape. Grant records always use three parts.
	if len(parts) == 2 {
		return parts[0], "@", parts[1], true
	}
	return "", "", "", false
}

func composeRecordFQDN(domain, name string) string {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || name == "@" {
		return domain
	}
	return name + "." + domain
}

func grantCoversFQDN(grantDomain, fqdn string, wildcard bool) bool {
	grantDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(grantDomain)), ".")
	fqdn = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	return fqdn == grantDomain || (wildcard && strings.HasSuffix(fqdn, "."+grantDomain))
}

func wildcardSubArg(apex, domain string) string {
	if domain == apex {
		return "*"
	}
	return "*." + strings.TrimSuffix(domain, "."+apex)
}

func (a *App) writeDomainRecord(ctx *sdk.AppCtx, projectID, fqdn, target, rtype string, ttl int) (string, error) {
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return "", err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if rtype == "CNAME" && sub == "" {
		return "", errors.New("apex CNAME isn't allowed by DNS; use type=A with an IP, or attach a subdomain")
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex, "name": subArg, "type": rtype, "value": target, "ttl": ttl,
	}, nil); err != nil {
		return "", err
	}
	return recordID(apex, subArg, rtype), nil
}

// ─── attach orchestration ─────────────────────────────────────────

// attachDomain runs the three-step orchestration: domain_records_set →
// cert_issue (fire-and-forget) → routes_register. Persists
// (domain, record_id, attached_at) on the tenant. Idempotent on
// re-attach: domains 0.2.3 upserts, certs treats existing live cert
// as a no-op, routes_register replaces same-owner routes in place.
//
// projectID is the operator's project — we run cross-app calls with
// it so global-scoped Domains/Certs (the prod default) resolve their
// per-project data correctly.
func (a *App) attachDomain(ctx *sdk.AppCtx, projectID string, t *Tenant, spec attachDomainSpec) error {
	fqdn := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(spec.FQDN, ".")))
	if fqdn == "" {
		return errors.New("fqdn required")
	}

	var recordID string
	if spec.ManageDNS {
		if !a.domainsAvailable(ctx) {
			return errors.New("domains app not installed — install + bind it as fleet's domains integration, or pass manage_dns=false if the client already pointed DNS at this machine")
		}
		target := a.resolveTarget(spec)
		if target == "" {
			return errors.New("target required — pass target explicitly or ensure APTEVA_PUBLIC_URL / detectPublicHost yields a usable IP")
		}
		rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
		if rtype == "" {
			rtype = inferRecordType(target)
		}
		if rtype != "A" && rtype != "CNAME" {
			return fmt.Errorf("unsupported record type %q (A or CNAME)", rtype)
		}
		if rtype == "CNAME" {
			target = strings.TrimSuffix(target, ".")
		}
		ttl := spec.TTL
		if ttl <= 0 {
			ttl = 600
		}

		apex, sub, err := resolveApex(ctx, projectID, fqdn)
		if err != nil {
			return err
		}
		subArg := sub
		if subArg == "" {
			subArg = "@"
		}
		if rtype == "CNAME" && sub == "" {
			return errors.New("apex CNAME isn't allowed by DNS; use type=A with an IP, or attach a subdomain")
		}

		if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
			"domain": apex, "name": subArg, "type": rtype, "value": target, "ttl": ttl,
		}, nil); err != nil {
			return err
		}
		recordID = apex + "|" + rtype
		_ = a.store.recordEvent(t.ID, "domain.attached", "tool:attach_domain", map[string]any{
			"fqdn": fqdn, "apex": apex, "type": rtype, "target": target,
		})
	} else {
		// Client-managed DNS: we don't touch the registrar. We just
		// trust the operator that fqdn resolves to this machine and
		// continue with cert + route. detach() keys off record_id ==
		// "" to skip the corresponding domain_records_delete call.
		_ = a.store.recordEvent(t.ID, "domain.attached", "tool:attach_domain", map[string]any{
			"fqdn": fqdn, "manage_dns": false,
		})
	}

	if err := a.store.setDomain(t.ID, fqdn, recordID, nowUTC()); err != nil {
		return err
	}

	// Fire-and-forget cert issuance; async on the certs side. The
	// panel polls cert_get for status. Partial-failure mirror of
	// deploy: a failed kickoff does NOT roll back DNS — operator
	// retries cert_issue via the certs panel.
	if a.certsAvailable(ctx) {
		if cErr := callCertsTool(ctx, projectID, "cert_issue", certIssueArgs(fqdn, spec.ManageDNS), nil); cErr != nil {
			_ = a.store.recordEvent(t.ID, "domain.cert_kickoff_failed", "tool:attach_domain", map[string]any{
				"fqdn": fqdn, "error": cErr.Error(),
			})
		}
	}

	// Register the route so the parent's routes app (in proxy mode)
	// renders a Caddy block proxying public traffic for fqdn to the
	// tenant's apteva-server port. No-op when routes isn't bound.
	a.registerRouteForTenant(ctx, t.ID, fqdn)
	return nil
}

func certIssueArgs(fqdn string, manageDNS bool) map[string]any {
	args := map[string]any{"fqdn": fqdn}
	if !manageDNS {
		args["challenge_type"] = "http-01"
	}
	return args
}

// detachDomain best-effort deletes the DNS record, revokes the cert,
// unregisters the route, and clears the tenant's domain link. The
// local clear runs even on remote-side failures — a dangling registrar
// record is operator-recoverable via the Domains panel; a dangling
// tenant row pointing at a domain that doesn't resolve is worse.
func (a *App) detachDomain(ctx *sdk.AppCtx, projectID string, t *Tenant) error {
	if t.Domain == "" && t.DomainRecordID == "" {
		return nil
	}
	var deleteErr error
	if t.DomainRecordID != "" && a.domainsAvailable(ctx) {
		apex, rtype, ok := splitRecordID(t.DomainRecordID)
		if ok {
			fqdn := strings.ToLower(strings.TrimSuffix(t.Domain, "."))
			sub := ""
			if fqdn != apex {
				sub = strings.TrimSuffix(fqdn, "."+apex)
			}
			subArg := sub
			if subArg == "" {
				subArg = "@"
			}
			deleteErr = callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
				"domain": apex, "name": subArg, "type": rtype,
			}, nil)
		}
	}
	if a.certsAvailable(ctx) && t.Domain != "" {
		_ = callCertsTool(ctx, projectID, "cert_revoke", map[string]any{"fqdn": t.Domain}, nil)
	}
	a.unregisterRouteForTenant(ctx, t.Domain)
	if err := a.store.clearDomain(t.ID); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain.detached", "tool:detach_domain", map[string]any{"fqdn": t.Domain})
	return deleteErr
}

func splitRecordID(s string) (apex, rtype string, ok bool) {
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// ─── domain grants ─────────────────────────────────────────────────

// grantDomain delegates domain to a tenant. Unlike attachDomain, this
// is not just the tenant dashboard hostname; it is the base zone that
// tenant-local apps can use through the Domains facade.
func (a *App) grantDomain(ctx *sdk.AppCtx, projectID string, t *Tenant, spec attachDomainSpec) (*DomainGrant, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(spec.FQDN)
	if err != nil {
		return nil, err
	}
	if !spec.ManageDNS {
		return nil, errors.New("domain grants require managed DNS so Fleet can maintain the delegation boundary")
	}
	if !a.domainsAvailable(ctx) {
		return nil, errors.New("domains app not installed — install + bind it as fleet's domains integration")
	}
	target := a.resolveTarget(spec)
	if target == "" {
		return nil, errors.New("target required — pass target explicitly or ensure APTEVA_PUBLIC_URL / detectPublicHost yields a usable IP")
	}
	rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
	if rtype == "" {
		rtype = inferRecordType(target)
	}
	if rtype != "A" && rtype != "CNAME" {
		return nil, fmt.Errorf("unsupported record type %q (A or CNAME)", rtype)
	}
	if rtype == "CNAME" {
		target = strings.TrimSuffix(target, ".")
	}
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = 600
	}

	record, err := a.writeDomainRecord(ctx, projectID, domain, target, rtype, ttl)
	if err != nil {
		return nil, err
	}
	wildcardRecord := ""
	if spec.ManageDNS {
		apex, _, err := resolveApex(ctx, projectID, domain)
		if err != nil {
			return nil, err
		}
		wildcardName := wildcardSubArg(apex, domain)
		if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
			"domain": apex, "name": wildcardName, "type": rtype, "value": target, "ttl": ttl,
		}, nil); err != nil {
			return nil, err
		}
		wildcardRecord = recordID(apex, wildcardName, rtype)
	}

	g := &DomainGrant{
		TenantID:         t.ID,
		Domain:           domain,
		Wildcard:         true,
		Status:           "active",
		DomainRecordID:   record,
		WildcardRecordID: wildcardRecord,
	}
	if err := a.store.upsertDomainGrant(g); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.granted", "tool:tenant_domain_grant", map[string]any{
		"domain": domain, "wildcard": true, "target": target, "type": rtype,
	})

	if a.certsAvailable(ctx) {
		a.issueGrantCert(ctx, projectID, t.ID, domain)
		a.issueGrantCert(ctx, projectID, t.ID, wildcardHost(domain))
	}
	a.registerRouteForTenantHost(ctx, t, domain, domain, "tool:tenant_domain_grant")
	a.registerRouteForTenantHost(ctx, t, wildcardHost(domain), wildcardHost(domain), "tool:tenant_domain_grant")
	return g, nil
}

func (a *App) issueGrantCert(ctx *sdk.AppCtx, projectID, tenantID, fqdn string) {
	if fqdn == "" || !a.certsAvailable(ctx) {
		return
	}
	if cErr := callCertsTool(ctx, projectID, "cert_issue", map[string]any{"fqdn": fqdn}, nil); cErr != nil {
		_ = a.store.recordEvent(tenantID, "domain_grant.cert_kickoff_failed", "tool:tenant_domain_grant", map[string]any{
			"fqdn": fqdn, "error": cErr.Error(),
		})
	}
}

func (a *App) revokeDomainGrant(ctx *sdk.AppCtx, projectID string, t *Tenant, domain string) (*DomainGrant, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(domain)
	if err != nil {
		return nil, err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, fmt.Errorf("domain grant not found: %s", domain)
	}
	var deleteErr error
	if a.domainsAvailable(ctx) {
		for _, rec := range []string{g.WildcardRecordID, g.DomainRecordID} {
			if rec == "" {
				continue
			}
			apex, name, rtype, ok := splitGrantRecordID(rec)
			if !ok {
				continue
			}
			if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
				"domain": apex, "name": name, "type": rtype,
			}, nil); err != nil && deleteErr == nil {
				deleteErr = err
			}
		}
	}
	if a.certsAvailable(ctx) {
		_ = callCertsTool(ctx, projectID, "cert_revoke", map[string]any{"fqdn": domain}, nil)
		if g.Wildcard {
			_ = callCertsTool(ctx, projectID, "cert_revoke", map[string]any{"fqdn": wildcardHost(domain)}, nil)
		}
	}
	a.unregisterRouteForTenant(ctx, domain)
	if g.Wildcard {
		a.unregisterRouteForTenant(ctx, wildcardHost(domain))
	}
	if err := a.store.deleteDomainGrant(t.ID, domain); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.revoked", "tool:tenant_domain_revoke", map[string]any{"domain": domain})
	return g, deleteErr
}

// proxyTenantDomainRecordSet is the Fleet-side endpoint the tenant
// Domains facade will call. It validates that the requested FQDN sits
// under a grant for the tenant, then writes the real DNS record via
// the parent Domains app.
func (a *App) proxyTenantDomainRecordSet(ctx *sdk.AppCtx, projectID string, t *Tenant, rec domainRecordSpec) error {
	if t == nil {
		return errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(rec.Domain)
	if err != nil {
		return err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return err
	}
	if g == nil || g.Status != "active" {
		return fmt.Errorf("domain %q is not granted to tenant %s", domain, t.ID)
	}
	fqdn := composeRecordFQDN(domain, rec.Name)
	if !grantCoversFQDN(domain, fqdn, g.Wildcard) {
		return fmt.Errorf("record %q is outside granted domain %q", fqdn, domain)
	}
	rtype := strings.ToUpper(strings.TrimSpace(rec.Type))
	if rtype == "" {
		return errors.New("record type required")
	}
	if strings.TrimSpace(rec.Value) == "" {
		return errors.New("record value required")
	}
	ttl := rec.TTL
	if ttl <= 0 {
		ttl = 600
	}
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex, "name": subArg, "type": rtype, "value": rec.Value, "ttl": ttl,
	}, nil); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.record_set", "tool:tenant_domain_record_set", map[string]any{
		"domain": domain, "fqdn": fqdn, "type": rtype,
	})
	return nil
}

func (a *App) proxyTenantDomainRecordDelete(ctx *sdk.AppCtx, projectID string, t *Tenant, rec domainRecordSpec) error {
	if t == nil {
		return errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(rec.Domain)
	if err != nil {
		return err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return err
	}
	if g == nil || g.Status != "active" {
		return fmt.Errorf("domain %q is not granted to tenant %s", domain, t.ID)
	}
	fqdn := composeRecordFQDN(domain, rec.Name)
	if !grantCoversFQDN(domain, fqdn, g.Wildcard) {
		return fmt.Errorf("record %q is outside granted domain %q", fqdn, domain)
	}
	rtype := strings.ToUpper(strings.TrimSpace(rec.Type))
	if rtype == "" {
		return errors.New("record type required")
	}
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
		"domain": apex, "name": subArg, "type": rtype,
	}, nil); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.record_deleted", "tool:tenant_domain_record_delete", map[string]any{
		"domain": domain, "fqdn": fqdn, "type": rtype,
	})
	return nil
}

// ─── route integration ────────────────────────────────────────────

// registerRouteForTenant publishes (fqdn → tenant apteva-server port)
// in the parent's routes table. Idempotent — replaces an existing
// route from the same owner. No-op when routes isn't bound.
func (a *App) registerRouteForTenant(ctx *sdk.AppCtx, tenantID, fqdn string) {
	if fqdn == "" || !a.routesAvailable(ctx) {
		return
	}
	t, _, err := a.store.get(tenantID)
	if err != nil || t == nil {
		return
	}
	a.registerRouteForTenantHost(ctx, t, fqdn, fqdn, "tool:attach_domain")
}

func (a *App) registerRouteForTenantHost(ctx *sdk.AppCtx, t *Tenant, hostname, certFQDN, actor string) {
	if t == nil || hostname == "" || !a.routesAvailable(ctx) {
		return
	}
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 {
		_ = a.store.recordEvent(t.ID, "route.register_skipped", actor,
			map[string]any{"reason": "no_port_in_base_url"})
		return
	}
	// Local-on-parent: route to loopback. Hosted-on-VPS: route to
	// the VPS's public IP (the parent's reverse proxy reaches it
	// directly). For hosted the BaseURL already carries the VPS IP
	// in t.BaseURL, so we can re-use it as-is.
	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	if t.IsHosted() {
		target = t.BaseURL // already http://<vps-ip>:<port>
	}
	if err := callRoutesTool(ctx, "routes_register", map[string]any{
		"hostname":         hostname,
		"target":           target,
		"owner_install_id": myInstallID(),
		"owner_kind":       "fleet",
		"cert_fqdn":        certFQDN,
	}, nil); err != nil {
		_ = a.store.recordEvent(t.ID, "route.register_failed", actor,
			map[string]any{"fqdn": hostname, "error": err.Error()})
		return
	}
	_ = a.store.recordEvent(t.ID, "route.registered", actor,
		map[string]any{"fqdn": hostname, "port": port})
}

// unregisterRouteForTenant — cleanup half. Safe when routes isn't
// bound or the route was never registered.
func (a *App) unregisterRouteForTenant(ctx *sdk.AppCtx, fqdn string) {
	if fqdn == "" || !a.routesAvailable(ctx) {
		return
	}
	_ = callRoutesTool(ctx, "routes_unregister", map[string]any{
		"hostname":         fqdn,
		"owner_install_id": myInstallID(),
	}, nil)
}

// myInstallID reads APTEVA_INSTALL_ID — the platform injects it at
// spawn so the routes app can tag ownership. 0 when unset (the routes
// app rejects with a clear error).
func myInstallID() int64 {
	v := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func nowUTC() time.Time { return time.Now().UTC() }
