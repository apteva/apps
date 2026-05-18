package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Domain attach/detach goes through the optional Domains app. When
// the dep isn't bound we fall back to a free-text `domain` column —
// same as before this app learned about Domains.
//
// Wire layer: PlatformClient.CallApp(appName, tool, args) returns the
// full MCP JSON-RPC envelope. The actual tool result is JSON-encoded
// inside result.content[0].text, mirroring the Code app's repos_export
// path in sources.go.

// callDomainsTool invokes a tool on the Domains app and unwraps the
// MCP envelope into the tool's `any` result, decoded into out.
//
// projectID is injected as `_project_id` so the call still resolves
// when the Domains app is installed global-scoped (no APTEVA_PROJECT_ID
// in its env). Pass "" only when the caller genuinely has no project
// context — a global Domains install will then reject the call.
func callDomainsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
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
	raw, err := ctx.PlatformAPI().CallApp("domains", tool, args)
	if err != nil {
		return fmt.Errorf("call domains.%s: %w", tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode domains.%s envelope: %w", tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("domains.%s: %s", tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return fmt.Errorf("domains.%s returned empty content", tool)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		return fmt.Errorf("domains.%s returned no text payload", tool)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("decode domains.%s payload: %w", tool, err)
	}
	return nil
}

// domainsAvailable reports whether the optional Domains app dep is
// bound on this install. False means callers should treat the domain
// field as free text.
func (a *App) domainsAvailable(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("domains")
	return bound != nil && bound.Kind == "app"
}

// certsAvailable reports whether the optional Certs app is bound. When
// true, attach fires cert_issue and detach fires cert_revoke. When
// false, custom domains stay HTTP-only.
func (a *App) certsAvailable(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("certs")
	return bound != nil && bound.Kind == "app"
}

// callCertsTool mirrors callDomainsTool: invoke a tool on the Certs
// app and unwrap the MCP envelope. projectID is injected as
// `_project_id` for the same global-scope reason as callDomainsTool.
func callCertsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
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
	raw, err := ctx.PlatformAPI().CallApp("certs", tool, args)
	if err != nil {
		return fmt.Errorf("call certs.%s: %w", tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode certs.%s envelope: %w", tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("certs.%s: %s", tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return fmt.Errorf("certs.%s returned empty content", tool)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		return fmt.Errorf("certs.%s returned no text payload", tool)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(text), out)
}

// resolveApex calls domains.domain_list and finds the registered apex
// that's a suffix of fqdn ("app.acme.com" → "acme.com"). Errors if
// the fqdn doesn't sit under any registered domain — that's the
// validation the picker UI is built around.
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

// attachDomainSpec captures the inputs to attach. Wrapping them keeps
// the tool/HTTP/init paths from drifting.
type attachDomainSpec struct {
	FQDN   string
	Target string // record value; empty → public_host config → auto-derived box IP
	Type   string // "CNAME" | "A"; empty → inferred from the resolved Target
	TTL    int
}

// resolveTarget picks the DNS record value for an attach. An explicit
// target arg wins, then the public_host config, then the box's own
// public IP — auto-derived so a zero-config attach still works. An
// empty return means nothing resolved.
func resolveTarget(spec attachDomainSpec) string {
	if t := strings.TrimSpace(spec.Target); t != "" {
		return t
	}
	if t := strings.TrimSpace(configOr(globalCtx, "public_host", "")); t != "" {
		return t
	}
	return deriveHostIP()
}

// deriveHostIP best-effort discovers the IP this box is reachable at.
// First choice: the host of APTEVA_PUBLIC_URL (the public URL the
// platform injects into every sidecar) — used directly if it's already
// an IP, else resolved via DNS. Fallback: the first non-loopback,
// non-private IPv4 on a local interface. Returns "" if neither yields
// anything.
//
// Caveat: if APTEVA_PUBLIC_URL's hostname sits behind a CDN/proxy, the
// DNS lookup returns the edge IP, not the origin — the operator should
// then set public_host or pass target explicitly.
func deriveHostIP() string {
	if pu := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); pu != "" {
		host := pu
		if u, err := url.Parse(pu); err == nil && u.Host != "" {
			host = u.Hostname()
		}
		if net.ParseIP(host) != nil {
			return host
		}
		if ips, err := net.LookupIP(host); err == nil {
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if v4 := ipNet.IP.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsPrivate() {
				return v4.String()
			}
		}
	}
	return ""
}

// inferRecordType picks the record type when the caller didn't pin
// one: a literal IP → A, a hostname → CNAME.
func inferRecordType(target string) string {
	if net.ParseIP(target) != nil {
		return "A"
	}
	return "CNAME"
}

// attachDomain validates the FQDN against the Domains app and writes
// the DNS record. Persists (domain, record_id, attached_at) on the
// deployment. record_id encodes "<apex>|<type>" so detach can target
// the same record without re-resolving.
// attachDomainResult is what attachDomain reports back. A DNS-only
// failure is no longer a hard error — pre-v0.10.0, a 406 from the
// upstream registrar (often a duplicate-record case) tanked the
// whole attach, leaving the route unwritten even though apteva-server
// would have routed traffic just fine. Now each leg reports its own
// status; the caller decides how loud to be about partial success.
type attachDomainResult struct {
	FQDN        string `json:"fqdn"`
	Apex        string `json:"apex"`
	Type        string `json:"type"`
	Target      string `json:"target"`
	DNSStatus   string `json:"dns_status"`  // ok | error | skipped
	DNSError    string `json:"dns_error,omitempty"`
	RouteStatus string `json:"route_status"` // ok | skipped | error
	RouteError  string `json:"route_error,omitempty"`
	CertStatus  string `json:"cert_status"`  // ok | skipped | error
	CertError   string `json:"cert_error,omitempty"`
}

func (a *App) attachDomain(ctx *sdk.AppCtx, d *Deployment, spec attachDomainSpec) (*attachDomainResult, error) {
	fqdn := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(spec.FQDN, ".")))
	if fqdn == "" {
		return nil, errors.New("fqdn required")
	}

	res := &attachDomainResult{FQDN: fqdn}
	var apex, recordID string

	// DNS step runs only if the Domains app is bound. In the multi-
	// tenant model the tenant has only routes installed (DNS + cert
	// are written by the parent's fleet.tenant_attach_domain), so
	// !domainsAvailable is the EXPECTED state here, not an error.
	// Skip cleanly, persist the link, register the route — operator
	// still sees the domain on the deployment.
	if a.domainsAvailable(ctx) {
		target := resolveTarget(spec)
		if target == "" {
			return nil, errors.New("target required — pass target, set public_host on the deploy app, or ensure APTEVA_PUBLIC_URL is set so the box IP can be auto-derived")
		}
		rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
		if rtype == "" {
			rtype = inferRecordType(target)
		}
		if rtype != "CNAME" && rtype != "A" {
			return nil, fmt.Errorf("unsupported record type %q (CNAME or A)", rtype)
		}
		if rtype == "CNAME" {
			target = strings.TrimSuffix(target, ".")
		}
		ttl := spec.TTL
		if ttl <= 0 {
			ttl = 600
		}

		var sub string
		var err error
		apex, sub, err = resolveApex(ctx, d.ProjectID, fqdn)
		if err != nil {
			return nil, err
		}
		subArg := sub
		if subArg == "" {
			subArg = "@"
		}
		if rtype == "CNAME" && sub == "" {
			return nil, errors.New("apex CNAME isn't allowed; use type=A with an IP, or attach a subdomain")
		}

		res.Apex = apex
		res.Type = rtype
		res.Target = target

		dnsErr := callDomainsTool(ctx, d.ProjectID, "domain_records_set", map[string]any{
			"domain": apex,
			"name":   subArg,
			"type":   rtype,
			"value":  target,
			"ttl":    ttl,
		}, nil)
		if dnsErr != nil {
			res.DNSStatus = "error"
			res.DNSError = dnsErr.Error()
			emit("deploy.domain.dns_failed", map[string]any{
				"deployment_id": d.ID, "fqdn": fqdn, "error": dnsErr.Error(),
			})
		} else {
			res.DNSStatus = "ok"
			recordID = apex + "|" + rtype
		}
	} else {
		res.DNSStatus = "skipped"
		res.DNSError = "domains app not bound — multi-tenant model expects parent to own DNS"
	}

	// Persist the link. recordID stays empty when DNS was skipped or
	// failed — detach checks for that and skips the registrar delete;
	// re-attaching re-attempts the DNS write idempotently.
	if err := dbSetDeploymentDomain(globalCtx.AppDB(), d.ID, fqdn, recordID, nowUTC()); err != nil {
		return res, err
	}
	emit("deploy.domain.attached", map[string]any{
		"deployment_id": d.ID, "fqdn": fqdn, "apex": apex,
		"dns_status": res.DNSStatus,
	})

	// Fire-and-forget cert issuance when the Certs app is installed.
	// Issuance is async on the certs side too — the panel polls
	// cert status via /api/_meta and renders the badge.
	if a.certsAvailable(ctx) {
		if err := callCertsTool(ctx, d.ProjectID, "cert_issue", map[string]any{"fqdn": fqdn}, nil); err != nil {
			res.CertStatus = "error"
			res.CertError = err.Error()
			emit("deploy.domain.cert_kickoff_failed", map[string]any{
				"deployment_id": d.ID, "fqdn": fqdn, "error": err.Error(),
			})
		} else {
			res.CertStatus = "ok"
		}
	} else {
		res.CertStatus = "skipped"
	}

	// Register the route with the Routes app regardless of DNS state —
	// the Caddy block is what actually serves traffic, and apteva.ai-
	// style setups often have DNS provisioned out-of-band anyway. The
	// route only registers if a release is live (registerRouteForDeployment
	// gates on rel.Status == "live").
	fresh, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	if fresh == nil {
		fresh = d
	}
	if !a.routesAvailable(ctx) {
		res.RouteStatus = "skipped"
	} else if fresh.CurrentReleaseID == nil {
		res.RouteStatus = "skipped" // No live release; registers on next deploy_release.
	} else {
		registerRouteForDeployment(ctx, a, fresh)
		res.RouteStatus = "ok"
	}
	return res, nil
}


// detachDomain best-effort deletes the DNS record (if we know what we
// wrote) and clears the deployment's domain link. The DB clear runs
// even if the remote delete fails — leaving the row pointed at a
// dead record is worse than a leaked record at the registrar, which
// the user can clean up via the Domains panel.
func (a *App) detachDomain(ctx *sdk.AppCtx, d *Deployment) error {
	if d.Domain == "" && d.DomainRecordID == "" {
		return nil
	}
	var deleteErr error
	if d.DomainRecordID != "" && a.domainsAvailable(ctx) {
		apex, rtype, ok := splitRecordID(d.DomainRecordID)
		if ok {
			fqdn := strings.ToLower(strings.TrimSuffix(d.Domain, "."))
			sub := ""
			if fqdn != apex {
				sub = strings.TrimSuffix(fqdn, "."+apex)
			}
			subArg := sub
			if subArg == "" {
				subArg = "@"
			}
			deleteErr = callDomainsTool(ctx, d.ProjectID, "domain_records_delete", map[string]any{
				"domain": apex, "name": subArg, "type": rtype,
			}, nil)
		}
	}
	if err := dbSetDeploymentDomain(globalCtx.AppDB(), d.ID, "", "", ""); err != nil {
		return err
	}
	if a.certsAvailable(ctx) && d.Domain != "" {
		_ = callCertsTool(ctx, d.ProjectID, "cert_revoke", map[string]any{"fqdn": d.Domain}, nil)
	}
	// Drop the route entry so apteva-server stops proxying to a
	// deployment the user just severed from its domain. No-op when
	// Routes isn't installed; the platform host router falls through
	// to its existing path-based routing.
	unregisterRouteForDeployment(ctx, a, d.Domain)
	emit("deploy.domain.detached", map[string]any{
		"deployment_id": d.ID, "fqdn": d.Domain,
	})
	return deleteErr
}

// certStatusFor returns a small struct describing the cert state of
// the deployment's FQDN, or nil when no cert exists / certs app not
// installed. Used by /api/_meta so the panel can render a badge
// without making the UI talk to certs directly.
type certStatusEntry struct {
	FQDN      string `json:"fqdn"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (a *App) certStatusFor(ctx *sdk.AppCtx, projectID, fqdn string) *certStatusEntry {
	if !a.certsAvailable(ctx) || fqdn == "" {
		return nil
	}
	var resp struct {
		Cert *struct {
			FQDN      string `json:"fqdn"`
			Status    string `json:"status"`
			ExpiresAt string `json:"expires_at,omitempty"`
			Error     string `json:"error,omitempty"`
		} `json:"cert"`
	}
	if err := callCertsTool(ctx, projectID, "cert_get", map[string]any{"fqdn": fqdn}, &resp); err != nil {
		return nil
	}
	if resp.Cert == nil {
		return nil
	}
	return &certStatusEntry{
		FQDN: resp.Cert.FQDN, Status: resp.Cert.Status,
		ExpiresAt: resp.Cert.ExpiresAt, Error: resp.Cert.Error,
	}
}

func splitRecordID(s string) (apex, rtype string, ok bool) {
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// ─── Routes app integration ───────────────────────────────────────
//
// Routes is the platform's hostname-routing layer (apps/mcp/routes).
// When deploy.attach_domain runs and a current release is live, we
// register the (fqdn → 127.0.0.1:port) pair so apteva-server's host
// router can proxy public requests to the supervised process.
//
// Optional dep — if Routes isn't installed, public reachability falls
// back to whatever the operator has wired externally (Caddy, nginx,
// or just-not-public). The DNS record is still written via Domains;
// the user can reach the deployment by IP/port directly until Routes
// shows up.

func (a *App) routesAvailable(ctx *sdk.AppCtx) bool {
	if ctx == nil {
		return false
	}
	bound := ctx.IntegrationFor("routes")
	return bound != nil && bound.Kind == "app"
}

// routesTargetedPorts returns the set of TCP ports the routes app
// currently has Caddy/proxy entries pointing at, parsed from each
// route's target (e.g. "http://127.0.0.1:7101" → 7101). The
// allocator skips these so a fresh release can never grab a port
// some hostname is still resolving to — the cross-bleed class of
// bug (apteva.ai → :7101 served the wrong content because :7101
// was OS-free but Caddy still routed to it).
//
// Best-effort: routes app missing or unreachable → empty map +
// the existing allocator gates (DB lease, procfs scan, bind-probe)
// still apply. Targets that don't parse as host:port (unix sockets,
// non-loopback hosts) are ignored — they can't collide with our
// loopback bind anyway.
func routesTargetedPorts(ctx *sdk.AppCtx, app *App) map[int]bool {
	out := map[int]bool{}
	if app == nil || !app.routesAvailable(ctx) {
		return out
	}
	var resp struct {
		Routes []struct {
			Target string `json:"target"`
		} `json:"routes"`
	}
	if err := callRoutesTool(ctx, "routes_list", map[string]any{}, &resp); err != nil {
		// Don't log noisily — routes_list is called once per allocation
		// and a transient error shouldn't poison the build log.
		return out
	}
	for _, r := range resp.Routes {
		if p, ok := parseLoopbackPort(r.Target); ok {
			out[p] = true
		}
	}
	return out
}

// parseLoopbackPort extracts the port from a routes-app target URL
// targeting 127.0.0.1 / localhost / [::1]. Returns (0, false) for
// anything else — non-loopback targets aren't ports we'd allocate
// to anyway.
func parseLoopbackPort(target string) (int, bool) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return 0, false
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0, false
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p <= 0 {
		return 0, false
	}
	return p, true
}

// callRoutesTool mirrors callDomainsTool / callCertsTool. Pass
// owner_install_id (deploy's own install id) explicitly — the
// platform's CallApp proxy doesn't yet forward caller identity to
// MCP targets, so the routes app trusts the caller-supplied value.
func callRoutesTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	raw, err := ctx.PlatformAPI().CallApp("routes", tool, args)
	if err != nil {
		return fmt.Errorf("call routes.%s: %w", tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode routes.%s envelope: %w", tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("routes.%s: %s", tool, env.Error.Message)
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

// myInstallID reads APTEVA_INSTALL_ID from the env. The routes app's
// register tool requires it as owner_install_id; the platform sets
// it when spawning the sidecar. Returns 0 if unset (which the routes
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

// registerRouteForDeployment publishes a deployment's current release
// port at its attached domain. Idempotent — calling again with the
// same args updates the route in place. Skipped (no error) when
// Routes isn't installed or when there's no live release to point
// at; callers can rely on this being safe to fan out from anywhere.
//
// Defense-in-depth gate: even though promoteToLive only calls us
// after pidOwnsPort succeeds, we re-verify here. Any caller (e.g.
// the panel triggering a manual re-register, future code paths) gets
// the same guarantee — the route never points at a port whose owner
// we can't prove.
func registerRouteForDeployment(ctx *sdk.AppCtx, app *App, d *Deployment) {
	if d == nil || d.Domain == "" {
		return
	}
	if app == nil || !app.routesAvailable(ctx) {
		return
	}
	if d.CurrentReleaseID == nil {
		return
	}
	rel, err := dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	if err != nil || rel == nil {
		return
	}
	if rel.Status != "live" {
		emit("deploy.route.register_skipped", map[string]any{
			"deployment_id": d.ID, "fqdn": d.Domain,
			"reason": "release_not_live", "status": rel.Status,
		})
		return
	}
	if !pidOwnsPort(rel.PID, rel.Port) {
		emit("deploy.route.register_skipped", map[string]any{
			"deployment_id": d.ID, "fqdn": d.Domain,
			"reason": "pid_does_not_own_port", "pid": rel.PID, "port": rel.Port,
		})
		return
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", rel.Port)
	if err := callRoutesTool(ctx, "routes_register", map[string]any{
		"hostname":         d.Domain,
		"target":           target,
		"owner_install_id": myInstallID(),
		"owner_kind":       "deploy",
		"cert_fqdn":        d.Domain,
	}, nil); err != nil {
		emit("deploy.route.register_failed", map[string]any{
			"deployment_id": d.ID, "fqdn": d.Domain, "error": err.Error(),
		})
		return
	}
	emit("deploy.route.registered", map[string]any{
		"deployment_id": d.ID, "fqdn": d.Domain, "port": rel.Port,
	})
}

// unregisterRouteForDeployment is the cleanup half — called from
// detachDomain and from deploy_destroy. Safe when Routes isn't
// installed; safe when no route was ever registered (the routes app
// returns removed: false).
func unregisterRouteForDeployment(ctx *sdk.AppCtx, app *App, fqdn string) {
	if fqdn == "" {
		return
	}
	if app == nil || !app.routesAvailable(ctx) {
		return
	}
	_ = callRoutesTool(ctx, "routes_unregister", map[string]any{
		"hostname":         fqdn,
		"owner_install_id": myInstallID(),
	}, nil)
}

// currentReleasePort returns the live port for a deployment, or 0
// when no release is live. Best-effort — DB-only, no IPC.
func currentReleasePort(ctx *sdk.AppCtx, d *Deployment) int {
	if d.CurrentReleaseID == nil {
		return 0
	}
	rel, err := dbGetRelease(ctx.AppDB(), *d.CurrentReleaseID)
	if err != nil || rel == nil {
		return 0
	}
	if rel.Status != "live" {
		return 0
	}
	return rel.Port
}
