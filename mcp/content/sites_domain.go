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
	"os"
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
	Managed bool   `json:"managed"`  // true if certs is bound and we kicked off issuance
	Status  string `json:"status"`   // pending | issued | skipped | error
	CertID  string `json:"cert_id,omitempty"`
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

	// 2. Routes — required. Without the routing role bound we still
	//    persist the hostname (above) so a future bind picks it up,
	//    but we surface the missing-role as an error so the caller
	//    knows the domain isn't actually reachable yet.
	if ctx.IntegrationFor("routing") == nil {
		return nil, errors.New("routing integration not bound — install the routes app and bind it to this content install to register the hostname → sidecar route")
	}
	installID, err := myInstallID()
	if err != nil {
		return nil, err
	}
	myTarget := fmt.Sprintf("http://127.0.0.1:%s", os.Getenv("APTEVA_APP_PORT"))
	if os.Getenv("APTEVA_APP_PORT") == "" {
		return nil, errors.New("APTEVA_APP_PORT not set — the platform should inject this; without it the routes target can't be built")
	}
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
	apex, sub := splitFQDN(fqdn)
	suggested := suggestDNSRecord(sub, target)
	if autoDNS && ctx.IntegrationFor("dns") != nil && target != "" && apex != "" {
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
		apex, sub := splitFQDN(fqdn)
		if apex != "" {
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
	// Strip scheme + path; keep host.
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	if j := strings.LastIndex(u, ":"); j >= 0 {
		u = u[:j]
	}
	return strings.TrimSpace(u)
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
