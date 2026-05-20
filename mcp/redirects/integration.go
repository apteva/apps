package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// emitRuleChange publishes a redirect lifecycle event on the app bus.
// Topic is one of "rule.created" / "rule.updated" / "rule.removed";
// payload is the full Redirect row so the panel (and any other
// subscriber) can update local state without an extra fetch. Best-
// effort — emit failures are swallowed because the rule operation
// itself has already succeeded by the time we get here.
func emitRuleChange(ctx *sdk.AppCtx, topic string, rule *Redirect) {
	if ctx == nil || rule == nil {
		return
	}
	ctx.Emit(topic, map[string]any{"redirect": rule})
}

// emitHit publishes a "rule.hit" event. Payload is minimal — id and
// the current second so the panel can increment its in-memory counter
// and animate the row. We don't include the full Redirect since the
// panel already has it cached from the lifecycle events.
func emitHit(ctx *sdk.AppCtx, rule *Redirect) {
	if ctx == nil || rule == nil {
		return
	}
	ctx.Emit("rule.hit", map[string]any{
		"id":       rule.ID,
		"hostname": rule.Hostname,
		"path":     rule.Path,
		"at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// shouldEmitHit returns true at most once per rule per second. Public-
// traffic redirects can run at hundreds of req/s on hot links; without
// throttling the platform's 256-event ring buffer fills up in ~3s and
// every other app's subscribers miss messages. The DB hit counter
// still increments on every request — only the bus emit is sampled.
//
// A sync.Map keyed by ruleID is enough — entry value is the last emit
// unix-second; we compare-and-swap to avoid two goroutines emitting
// for the same rule in the same second.
var hitLastEmit sync.Map // map[int64]int64 — ruleID → unix seconds

func shouldEmitHit(ruleID int64) bool {
	now := time.Now().Unix()
	prev, loaded := hitLastEmit.LoadOrStore(ruleID, now)
	if !loaded {
		return true
	}
	prevSec, _ := prev.(int64)
	if now > prevSec {
		// Replace; if another goroutine raced and replaced first, that's
		// fine — at most one of us returns true.
		return hitLastEmit.CompareAndSwap(ruleID, prevSec, now)
	}
	return false
}

// integration.go — glue calling the routes and domains apps.
//
// routes  is required: every redirect_add tries to claim the hostname
//         on apteva-server's ingress map so inbound traffic for that
//         hostname lands on this sidecar. Failure is a hard warning
//         the panel surfaces (the rule is created either way; the
//         operator can retry once routes is reachable).
//
// domains is optional: when the hostname is registered in domains,
//         we upsert a CNAME pointing at the platform's public host.
//         If domains isn't installed (or doesn't manage this hostname)
//         we skip silently — the operator manages DNS themselves.

// wireHostname claims hostname for this sidecar with routes, upserts a
// CNAME via domains when the hostname is known there, and kicks off a
// cert issuance via the certs app when it's installed. Returns a
// human-readable warning string when something failed (or "" on full
// success); errors are not propagated because creating the redirect
// rule should not roll back on wiring failure.
func wireHostname(ctx *sdk.AppCtx, projectID, hostname string) string {
	if hostname == "" {
		return ""
	}
	var warnings []string

	// Routes — required.
	if err := registerRoute(ctx, hostname); err != nil {
		warnings = append(warnings, "routes: "+err.Error())
	}

	// Domains — best effort. Probe first; only call domain_records_set
	// when the domain is actually present. This avoids creating noise
	// in the domains app's panel when the user manages DNS elsewhere.
	if err := maybeUpsertCNAME(ctx, projectID, hostname); err != nil {
		warnings = append(warnings, "domains: "+err.Error())
	}

	// Certs — best effort. Async on the certs side; cert_issue returns
	// immediately and ACME runs in the background. The route's
	// cert_fqdn is already set to this hostname (registerRoute above),
	// so apteva-server's TLS GetCertificate hook will pick up the cert
	// the moment certs finishes issuing it.
	if err := maybeIssueCert(ctx, projectID, hostname); err != nil {
		warnings = append(warnings, "certs: "+err.Error())
	}

	if len(warnings) == 0 {
		return ""
	}
	return strings.Join(warnings, "; ")
}

// maybeUnwireHostname unregisters the route when no rules remain for
// this hostname (within the same project_id). We never delete DNS —
// CNAMEs may be shared with other services on the same hostname.
func maybeUnwireHostname(ctx *sdk.AppCtx, hostname, projectID string) {
	remaining, err := dbListRedirects(ctx.AppDB(), hostname, projectID, 1, 0)
	if err != nil {
		ctx.Logger().Warn("maybeUnwireHostname.list", "host", hostname, "err", err.Error())
		return
	}
	if len(remaining) > 0 {
		return
	}
	// Also check global scope — if we're project-scoped, the same
	// hostname might still have install-level rules.
	if projectID != "" {
		globalRemaining, _ := dbListRedirects(ctx.AppDB(), hostname, "", 1, 0)
		if len(globalRemaining) > 0 {
			return
		}
	}
	if err := unregisterRoute(ctx, hostname); err != nil {
		ctx.Logger().Info("maybeUnwireHostname.unregister", "host", hostname, "err", err.Error())
	}
}

// ─── routes ───────────────────────────────────────────────────────

func registerRoute(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	installID := myInstallID()
	if installID == 0 {
		return errors.New("APTEVA_INSTALL_ID unset; cannot register route")
	}
	target := sidecarTarget()
	var resp struct {
		Route  any    `json:"route"`
		Action string `json:"action"`
	}
	err := ctx.PlatformAPI().CallAppResult("routes", "routes_register", map[string]any{
		"hostname":         hostname,
		"target":           target,
		"owner_install_id": installID,
		"owner_kind":       "redirects",
	}, &resp)
	if err != nil {
		return fmt.Errorf("routes_register: %w", err)
	}
	return nil
}

func unregisterRoute(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	installID := myInstallID()
	if installID == 0 {
		return errors.New("APTEVA_INSTALL_ID unset; cannot unregister route")
	}
	var resp struct {
		Removed bool `json:"removed"`
	}
	return ctx.PlatformAPI().CallAppResult("routes", "routes_unregister", map[string]any{
		"hostname":         hostname,
		"owner_install_id": installID,
	}, &resp)
}

// sidecarTarget builds the http://127.0.0.1:<port> URL the routes app
// should reverse-proxy this hostname to. The platform injects
// APTEVA_APP_PORT into each sidecar — it's the free port the platform
// picked for this install, distinct from the manifest's static port.
// Falling back to the manifest port (8080) is wrong on multi-app
// hosts; we'd register a bogus target and Caddy / apteva-server
// returns 502 for the configured hostname.
func sidecarTarget() string {
	port := os.Getenv("APTEVA_APP_PORT")
	if port == "" {
		// Last-resort fallback for dev where the manifest's runtime.port
		// matches the actual bind. Production always sets APTEVA_APP_PORT.
		port = "8080"
	}
	return "http://127.0.0.1:" + port
}

// ─── domains ──────────────────────────────────────────────────────

// callDomains is the one entry point to the domains app. It threads
// _project_id explicitly: the domains app is global-scope on prod and
// rejects calls that don't carry the caller's project. Project-scoped
// installs receive the env-injected APTEVA_PROJECT_ID; global-scoped
// installs depend on whichever caller path resolves it (panel passes
// it via header / query; tools pull it from args or env). Empty
// projectID is allowed — the call goes through unaugmented and the
// remote-side validation surfaces the error verbatim.
func callDomains(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	if args == nil {
		args = map[string]any{}
	}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	return ctx.PlatformAPI().CallAppResult("domains", tool, args, out)
}

// domainsList returns the apex domain names known to the Domains app
// for this project, or an empty list when domains is not installed /
// not reachable. Never errors — meant for "best-effort UI hints"
// where missing domains app is a normal state.
func domainsList(ctx *sdk.AppCtx, projectID string) []string {
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := callDomains(ctx, projectID, "domain_list", map[string]any{}, &resp); err != nil {
		ctx.Logger().Info("domain_list unavailable", "err", err.Error())
		return nil
	}
	names := make([]string, 0, len(resp.Domains))
	for _, d := range resp.Domains {
		if d.Name != "" {
			names = append(names, d.Name)
		}
	}
	return names
}

// maybeUpsertCNAME checks whether the hostname's apex is known to
// domains. If so it upserts a CNAME pointing at the platform's public
// host. If the domains app isn't installed (or doesn't manage this
// hostname), it returns nil — DNS wiring is optional.
func maybeUpsertCNAME(ctx *sdk.AppCtx, projectID, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	apex := apexOf(hostname)
	sub := strings.TrimSuffix(strings.TrimSuffix(hostname, apex), ".")
	if sub == "" {
		sub = "@"
	}

	// Is the apex in domains? domain_get returns null when missing.
	var probe struct {
		Domain map[string]any `json:"domain"`
	}
	if err := callDomains(ctx, projectID, "domain_get", map[string]any{"name": apex}, &probe); err != nil {
		// domains app not installed, or no permission — skip silently.
		ctx.Logger().Info("domain_get probe failed (skipping CNAME)", "host", hostname, "err", err.Error())
		return nil
	}
	if probe.Domain == nil {
		// Apex isn't managed here. Nothing to wire.
		return nil
	}

	target := platformPublicHost()
	if target == "" {
		return errors.New("APTEVA_PUBLIC_HOST unset; can't pick a CNAME target")
	}

	var setResp struct {
		Record any `json:"record"`
	}
	if err := callDomains(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex,
		"name":   sub,
		"type":   "CNAME",
		"value":  target,
	}, &setResp); err != nil {
		return fmt.Errorf("domain_records_set: %w", err)
	}
	return nil
}

// ─── certs ────────────────────────────────────────────────────────

// maybeIssueCert kicks off TLS cert issuance for the hostname via the
// certs app. Fire-and-forget — certs handles ACME asynchronously, and
// apteva-server's TLS GetCertificate hook picks up the result via the
// route's cert_fqdn. If certs isn't installed the call surfaces as a
// warning on the redirect response; the operator can choose to install
// certs and re-fire by updating the rule.
//
// We use the requires.apps declaration (not requires.integrations +
// IntegrationFor) so cross-app reachability matches the domains call
// path — both go through CallAppResult with _project_id threaded.
// Discriminating "certs not installed" from "certs errored" is left
// to the warning string the caller sees; we don't try to be smart
// about hiding one but not the other.
func maybeIssueCert(ctx *sdk.AppCtx, projectID, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	args := map[string]any{"fqdn": hostname}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	// CallAppResult requires non-nil out; ack is a sentinel we don't read.
	var ack map[string]any
	if err := ctx.PlatformAPI().CallAppResult("certs", "cert_issue", args, &ack); err != nil {
		return fmt.Errorf("cert_issue: %w", err)
	}
	return nil
}

// apexOf returns the registrable apex of a hostname using a naive
// "last two labels" heuristic. Real PSL handling lives in the domains
// app — this is just for picking which apex to query. Multi-label
// TLDs (.co.uk) will fall back to a wrong guess; users with those
// should add the redirect from the panel, which lets them pick the
// apex explicitly. We don't ship a PSL list in this sidecar to keep
// the binary lean.
func apexOf(hostname string) string {
	parts := strings.Split(hostname, ".")
	if len(parts) < 2 {
		return hostname
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// platformPublicHost — the hostname the CNAME should target. The
// platform injects APTEVA_PUBLIC_URL into every sidecar (sourced from
// the dashboard's "Public URL" setting); we parse it for the host
// part. If the public URL points at an IP rather than a hostname we
// return "" — CNAMEs can't target IPs, that case needs an A record
// and isn't implemented yet.
//
// APTEVA_PUBLIC_HOST stays supported as a manual override for the
// rare case where the operator wants the CNAME to target a different
// hostname than the dashboard URL (e.g. an apex behind a CDN vs the
// origin host).
func platformPublicHost() string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_HOST")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); v != "" {
		u, err := url.Parse(v)
		if err == nil && u.Hostname() != "" {
			host := u.Hostname()
			if ip := net.ParseIP(host); ip == nil {
				return host
			}
			// Public URL is an IP — can't CNAME to it.
			return ""
		}
		// Not a parseable URL — assume it's already bare host:port or
		// just a hostname. Strip scheme/port defensively.
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		v = strings.TrimSuffix(v, "/")
		if i := strings.IndexAny(v, ":/"); i >= 0 {
			v = v[:i]
		}
		if ip := net.ParseIP(v); ip != nil {
			return ""
		}
		return v
	}
	return ""
}
