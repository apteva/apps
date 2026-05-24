// Custom-domain orchestration for content sites.
//
// One MCP tool (sites_attach_domain) coordinates three sibling apps:
//
//   1. routes        — registers hostname → http://127.0.0.1:<port>
//      (required: without routes the FQDN can't reach the sidecar)
//
//   2. domains       — optional, auto-provisions the A/CNAME record
//      against the bound DNS provider. When omitted, the response
//      includes the records the user should set manually at their
//      registrar.
//
//   3. certs         — optional, kicks off Let's Encrypt issuance
//      for the FQDN. When omitted, the domain works over HTTP only
//      (or behind a fronting proxy that terminates TLS).
//
// Detach reverses each step that ran. We intentionally don't revoke
// the cert on detach — TLS material is cheap to keep around and may
// be reused if the same FQDN is re-attached later.
//
// All three coordination steps are optional in the sense that each
// app role declared in the manifest is `required: false`. The user
// can run with just routes (HTTP-only behind their own TLS), routes
// + certs (full HTTPS with manual DNS), or all three (one-click).

package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// AttachDomainResult is the typed response shape — mirrors what the
// JSON callers (panel, agent) see.
type AttachDomainResult struct {
	Site  AttachSite      `json:"site"`
	Route AttachRoute     `json:"route"`
	DNS   AttachDNSStatus `json:"dns"`
	TLS   AttachTLSStatus `json:"tls"`
}

type AttachSite struct {
	ID       int64  `json:"id"`
	Slug     string `json:"slug"`
	Hostname string `json:"hostname"`
}

type AttachRoute struct {
	Hostname   string `json:"hostname"`
	Target     string `json:"target"`
	Registered bool   `json:"registered"`
}

// AttachDNSStatus.Records holds the records the caller needs to set
// at their registrar — populated whether or not we auto-provisioned.
// Managed=true means we wrote them via the domains app; Managed=false
// means the caller is responsible.
type AttachDNSStatus struct {
	Managed bool        `json:"managed"`
	Records []DNSRecord `json:"records"`
	Note    string      `json:"note,omitempty"`
}

type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	TTL   int    `json:"ttl,omitempty"`
}

type AttachTLSStatus struct {
	Managed bool   `json:"managed"` // true if certs is bound and we kicked off issuance
	Status  string `json:"status"`  // pending | issued | skipped | error
	CertID  string `json:"cert_id,omitempty"`
}

type DomainOption struct {
	ID              int64  `json:"id,omitempty"`
	Name            string `json:"name"`
	DNSProviderSlug string `json:"dns_provider_slug,omitempty"`
	ConnectionID    int64  `json:"connection_id,omitempty"`
}

type DomainOptionsResult struct {
	RoutingBound bool           `json:"routing_bound"`
	DNSBound     bool           `json:"dns_bound"`
	TLSBound     bool           `json:"tls_bound"`
	Target       string         `json:"target,omitempty"`
	Domains      []DomainOption `json:"domains"`
	Warnings     []string       `json:"warnings,omitempty"`
}

// toolSitesAttachDomain implements the sites_attach_domain MCP tool.
func (a *App) toolSitesAttachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	site, err := siteFromAttachArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	fqdn := strings.ToLower(strings.TrimSpace(asString(args["fqdn"])))
	if fqdn == "" {
		return nil, errors.New("fqdn required")
	}
	if !looksLikeFQDN(fqdn) {
		return nil, fmt.Errorf("fqdn %q doesn't look like a hostname", fqdn)
	}

	autoDNS := boolArgOr(args, "auto_dns", true)
	autoTLS := boolArgOr(args, "auto_tls", true)
	// target is the DNS record value (an IP for A, a hostname for
	// CNAME). Optional — caller can let the tool figure one out from
	// platform info, or leave DNS to manual.
	target := strings.TrimSpace(asString(args["target"]))
	if target == "" {
		target = inferDNSTarget(ctx)
	}

	if ctx.IntegrationFor("routing") == nil {
		return nil, errors.New("routing integration not bound — install the routes app and bind it to this content install to register the hostname → sidecar route")
	}
	dnsBound := ctx.IntegrationFor("dns") != nil
	manageDNS := autoDNS && dnsBound
	apex, sub, err := domainPartsForAttach(ctx, pid, fqdn, manageDNS)
	if err != nil {
		return nil, err
	}
	suggested := suggestDNSRecord(sub, target)
	if manageDNS {
		if target == "" {
			return nil, errors.New("auto-DNS requires a DNS target — pass target explicitly or set APTEVA_PUBLIC_URL so the platform host can be inferred")
		}
		if suggested.Value == "" {
			return nil, errors.New("auto-DNS for a root domain requires an IP target; use a subdomain for CNAME-style targets or pass an IP target")
		}
	}
	installID, err := myInstallID()
	if err != nil {
		return nil, err
	}
	appPort := os.Getenv("APTEVA_APP_PORT")
	if appPort == "" {
		return nil, errors.New("APTEVA_APP_PORT not set — the platform should inject this; without it the routes target can't be built")
	}
	myTarget := fmt.Sprintf("http://127.0.0.1:%s", appPort)

	// 1. Update site hostname so resolveSiteIDFromRequest picks it up
	//    on the next inbound request matching this hostname.
	hostnamePtr := fqdn
	updatedSite, err := dbUpdateSite(ctx.AppDB(), pid, site.ID, nil, &hostnamePtr)
	if err != nil {
		return nil, fmt.Errorf("update site hostname: %w", err)
	}

	out := AttachDomainResult{
		Site: AttachSite{ID: updatedSite.ID, Slug: updatedSite.Slug, Hostname: updatedSite.Hostname},
	}

	// 2. Routes — required and preflighted before mutating the site row.
	// allow_http defaults to !auto_tls — if we aren't managing TLS,
	// something upstream is (ngrok tunnel, Cloudflare front, custom
	// reverse proxy). Forcing a 301 → HTTPS on the host-router side
	// would loop when that upstream is HTTPS-terminating and forwards
	// plain HTTP to apteva-server. Caller can override explicitly.
	allowHTTP := !autoTLS
	if v, ok := args["allow_http"]; ok {
		if b, ok := v.(bool); ok {
			allowHTTP = b
		}
	}
	routesArgs := map[string]any{
		"hostname":         fqdn,
		"target":           myTarget,
		"owner_install_id": installID,
		"owner_kind":       "content",
		"cert_fqdn":        fqdn,
		"allow_http":       allowHTTP,
	}
	var routeOut struct {
		Route  map[string]any `json:"route"`
		Action string         `json:"action"`
	}
	if err := ctx.PlatformAPI().CallAppResult("routes", "routes_register", routesArgs, &routeOut); err != nil {
		return nil, fmt.Errorf("routes_register failed: %w", err)
	}
	out.Route = AttachRoute{Hostname: fqdn, Target: myTarget, Registered: true}

	// 3. DNS — optional. Either we have the domains role and an apex
	//    on file → auto-provision; or we just emit the suggested
	//    records so the user can set them manually.
	if manageDNS && apex != "" {
		dnsArgs := map[string]any{
			"domain": apex,
			"name":   firstNonEmpty(sub, "@"),
			"type":   suggested.Type,
			"value":  suggested.Value,
			"ttl":    300,
		}
		var dnsOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", dnsArgs, &dnsOut); err != nil {
			// Soft fail — record the error in DNS status but keep
			// route + TLS going so the user sees partial progress.
			out.DNS = AttachDNSStatus{Managed: false, Records: []DNSRecord{suggested}, Note: "auto-DNS failed: " + err.Error()}
		} else {
			out.DNS = AttachDNSStatus{Managed: true, Records: []DNSRecord{suggested}}
		}
	} else {
		note := "set these at your registrar"
		if target == "" {
			note = "target unknown — pass `target` arg (an IP for A or a hostname for CNAME) so we can suggest the record value"
		}
		out.DNS = AttachDNSStatus{Managed: false, Records: []DNSRecord{suggested}, Note: note}
	}

	// 4. TLS — optional. cert_issue is async; we just hand off and
	//    poll-or-listen elsewhere.
	if autoTLS && ctx.IntegrationFor("tls") != nil {
		var certOut struct {
			Cert struct {
				ID     int64  `json:"id"`
				FQDN   string `json:"fqdn"`
				Status string `json:"status"`
			} `json:"cert"`
		}
		if err := ctx.PlatformAPI().CallAppResult("certs", "cert_issue", map[string]any{"fqdn": fqdn}, &certOut); err != nil {
			out.TLS = AttachTLSStatus{Managed: false, Status: "error", CertID: ""}
			out.DNS.Note = strings.TrimSpace(out.DNS.Note + " — cert_issue failed: " + err.Error())
		} else {
			out.TLS = AttachTLSStatus{
				Managed: true,
				Status:  firstNonEmpty(certOut.Cert.Status, "pending"),
				CertID:  fmt.Sprintf("%d", certOut.Cert.ID),
			}
		}
	} else {
		out.TLS = AttachTLSStatus{Managed: false, Status: "skipped"}
	}

	ctx.Emit("site.domain_attached", map[string]any{
		"site_id":  updatedSite.ID,
		"hostname": fqdn,
		"dns":      out.DNS.Managed,
		"tls":      out.TLS.Managed,
	})
	invalidatePageCache()
	return out, nil
}

// toolSitesDetachDomain clears a site's hostname and unregisters the
// route. DNS removal is opt-in (off by default — we don't want to
// nuke a record the user might be repointing). TLS cert is always
// left in place — Let's Encrypt rate limits make discarding it a
// false economy.
func (a *App) toolSitesDetachDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	site, err := siteFromAttachArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if site.Hostname == "" {
		return map[string]any{"detached": false, "reason": "no hostname attached"}, nil
	}
	fqdn := site.Hostname

	// Clear the site row first so further requests at this hostname
	// don't match this site even if the route unregister fails.
	empty := ""
	if _, err := dbUpdateSite(ctx.AppDB(), pid, site.ID, nil, &empty); err != nil {
		return nil, fmt.Errorf("clear site hostname: %w", err)
	}

	out := map[string]any{
		"detached": true,
		"site_id":  site.ID,
		"hostname": fqdn,
	}

	// Routes — best-effort unregister.
	if ctx.IntegrationFor("routing") != nil {
		installID, _ := myInstallID()
		var rOut map[string]any
		if err := ctx.PlatformAPI().CallAppResult("routes", "routes_unregister", map[string]any{
			"hostname":         fqdn,
			"owner_install_id": installID,
		}, &rOut); err != nil {
			out["route_unregister_error"] = err.Error()
		} else {
			out["route_unregistered"] = true
		}
	}

	// DNS — opt-in. Most callers want to keep the DNS record on
	// detach (commonly: repointing the FQDN elsewhere, or swapping
	// the underlying site).
	if boolArgOr(args, "remove_dns", false) && ctx.IntegrationFor("dns") != nil {
		apex, sub, err := domainPartsForAttach(ctx, pid, fqdn, true)
		if err != nil {
			out["dns_records_remove_error"] = err.Error()
		} else if apex != "" {
			subArg := firstNonEmpty(sub, "@")
			var dOut map[string]any
			// Try the most likely types — A and CNAME — and ignore
			// "not found" responses on either.
			for _, t := range []string{"A", "CNAME"} {
				_ = ctx.PlatformAPI().CallAppResult("domains", "domain_records_delete", map[string]any{
					"domain": apex, "name": subArg, "type": t,
				}, &dOut)
			}
			out["dns_records_removed"] = true
		}
	}

	ctx.Emit("site.domain_detached", map[string]any{"site_id": site.ID, "hostname": fqdn})
	invalidatePageCache()
	return out, nil
}

// ── helpers ──────────────────────────────────────────────────────

func buildDomainOptions(ctx *sdk.AppCtx, pid string) DomainOptionsResult {
	out := DomainOptionsResult{
		RoutingBound: ctx != nil && ctx.IntegrationFor("routing") != nil,
		DNSBound:     ctx != nil && ctx.IntegrationFor("dns") != nil,
		TLSBound:     ctx != nil && ctx.IntegrationFor("tls") != nil,
		Target:       inferDNSTarget(ctx),
		Domains:      []DomainOption{},
	}
	if !out.RoutingBound {
		out.Warnings = append(out.Warnings, "Routes app is not bound; custom host routing cannot be registered yet.")
	}
	if !out.DNSBound {
		out.Warnings = append(out.Warnings, "Domains app is not bound; DNS records must be created manually.")
		return out
	}
	domains, err := listManagedDomains(ctx, pid)
	if err != nil {
		out.Warnings = append(out.Warnings, "Could not load Domains inventory: "+err.Error())
		return out
	}
	out.Domains = domains
	if len(out.Domains) == 0 {
		out.Warnings = append(out.Warnings, "No domains are registered in the Domains app for this project.")
	}
	return out
}

func listManagedDomains(ctx *sdk.AppCtx, pid string) ([]DomainOption, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform unavailable")
	}
	args := map[string]any{}
	if pid != "" {
		args["_project_id"] = pid
	}
	var resp struct {
		Domains []DomainOption `json:"domains"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_list", args, &resp); err != nil {
		return nil, fmt.Errorf("domains.domain_list failed: %w", err)
	}
	domains := resp.Domains
	for i := range domains {
		domains[i].Name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domains[i].Name, ".")))
	}
	sort.SliceStable(domains, func(i, j int) bool {
		return domains[i].Name < domains[j].Name
	})
	return domains, nil
}

func domainPartsForAttach(ctx *sdk.AppCtx, pid, fqdn string, requireManaged bool) (apex, sub string, err error) {
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	if requireManaged {
		domains, err := listManagedDomains(ctx, pid)
		if err != nil {
			return "", "", err
		}
		apex, sub, ok := matchManagedDomain(fqdn, domains)
		if !ok {
			return "", "", fmt.Errorf("no registered domain matches %q — add the root domain in the Domains app first, or disable auto-DNS", fqdn)
		}
		return apex, sub, nil
	}
	apex, sub = splitFQDN(fqdn)
	return apex, sub, nil
}

func matchManagedDomain(fqdn string, domains []DomainOption) (apex, sub string, ok bool) {
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	best := ""
	for _, d := range domains {
		name := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(d.Name, ".")))
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
		return "", "", false
	}
	if fqdn == best {
		return best, "", true
	}
	return best, strings.TrimSuffix(fqdn, "."+best), true
}

// siteFromAttachArgs resolves the site from id OR slug. Falls back to
// the project's default site when neither is supplied, so the panel's
// active-site context works without forcing the caller to know the id.
func siteFromAttachArgs(ctx *sdk.AppCtx, pid string, args map[string]any) (*Site, error) {
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		s, err := dbGetSite(ctx.AppDB(), pid, id)
		if err != nil || s == nil {
			return nil, errors.New("site not found")
		}
		return s, nil
	}
	if slug := asString(args["slug"]); slug != "" {
		s, err := dbGetSiteBySlug(ctx.AppDB(), pid, slug)
		if err != nil || s == nil {
			return nil, fmt.Errorf("site %q not found", slug)
		}
		return s, nil
	}
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return dbGetSite(ctx.AppDB(), pid, siteID)
}

// myInstallID reads APTEVA_INSTALL_ID — same env var the routes app
// requires as owner_install_id on register. Reading it here keeps the
// SDK surface unchanged (the platform doesn't yet forward caller
// identity through CallApp, so we have to self-report).
func myInstallID() (int64, error) {
	v := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if v == "" {
		return 0, errors.New("APTEVA_INSTALL_ID not set — required to register routes")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("APTEVA_INSTALL_ID %q not an integer: %w", v, err)
	}
	return n, nil
}

// looksLikeFQDN is a lightweight sanity check — proper validation
// happens downstream in routes/domains, but this catches the common
// "user pasted a URL" mistake before we start writing rows.
func looksLikeFQDN(s string) bool {
	if s == "" || strings.ContainsAny(s, " /\\:") {
		return false
	}
	return strings.Contains(s, ".")
}

// splitFQDN returns (apex, sub) for the common case where apex is
// the last two segments. Doesn't handle .co.uk / .com.au; for those
// the user should set the record manually or use the domains app's
// apex inventory to resolve. Documented limitation for v1.
func splitFQDN(fqdn string) (apex, sub string) {
	parts := strings.Split(fqdn, ".")
	if len(parts) < 2 {
		return "", ""
	}
	apex = parts[len(parts)-2] + "." + parts[len(parts)-1]
	if len(parts) > 2 {
		sub = strings.Join(parts[:len(parts)-2], ".")
	}
	return apex, sub
}

// suggestDNSRecord builds the record the user (or the domains app)
// should set: A for IP targets, CNAME for hostname targets. Apex
// CNAMEs aren't standard so we fall back to A there even if target
// is a hostname (the caller will need to resolve it to an IP).
func suggestDNSRecord(sub, target string) DNSRecord {
	r := DNSRecord{Name: firstNonEmpty(sub, "@"), TTL: 300, Value: target}
	if target == "" {
		r.Type = "A"
		return r
	}
	if isIP(target) {
		r.Type = "A"
	} else if sub == "" {
		// Apex — CNAME at apex is technically RFC-prohibited; many
		// providers offer ALIAS/ANAME but we surface A as the safe
		// default and leave the value blank so the user fills in
		// the IP.
		r.Type = "A"
		r.Value = ""
	} else {
		r.Type = "CNAME"
	}
	return r
}

func isIP(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// inferDNSTarget tries to extract a plausible DNS target from the
// platform's public URL (APTEVA_PUBLIC_URL env var, injected by
// apteva-server). Returns "" when the platform is on localhost or
// when we can't parse anything sensible out — the caller can then
// pass `target` explicitly or accept manual DNS.
func inferDNSTarget(_ *sdk.AppCtx) string {
	u := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL"))
	if u == "" {
		return ""
	}
	host := u
	if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	} else {
		// Strip scheme + path; keep host.
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		if i := strings.IndexAny(host, "/?"); i >= 0 {
			host = host[:i]
		}
		if j := strings.LastIndex(host, ":"); j >= 0 {
			host = host[:j]
		}
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return host
	}
	if ips, err := net.LookupIP(host); err == nil {
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsPrivate() {
				return v4.String()
			}
		}
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return host
}

// boolArgOr coerces an args map value to bool with a default. JSON
// false survives as false; missing keys return dflt.
func boolArgOr(args map[string]any, key string, dflt bool) bool {
	v, ok := args[key]
	if !ok {
		return dflt
	}
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return dflt
}
