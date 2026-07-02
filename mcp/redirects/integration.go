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

// integration.go — glue calling platform ingress and the Domains app.
//
// Ingress is required: every redirect_add tries to expose the hostname
// through apteva-server's server-native ingress map so inbound traffic
// lands on this sidecar. TLS issuance is owned by platform ingress
// through CertFQDN.
//
// Domains is optional: when the hostname is registered in domains, we
// upsert an A or CNAME record pointing at the platform. If domains
// isn't installed or doesn't manage this hostname, we skip silently.

// wireHostname claims hostname for this sidecar with platform ingress
// and upserts DNS via domains when the hostname is managed there.
// Returns a human-readable warning string when something failed (or ""
// on full success); errors are not propagated because creating the
// redirect rule should not roll back on wiring failure.
func wireHostname(ctx *sdk.AppCtx, projectID, hostname string) string {
	if hostname == "" {
		return ""
	}
	var warnings []string

	// Ingress — required for public hostname routing. TLS is handled by
	// platform ingress because CertFQDN is set on the route.
	if err := registerRoute(ctx, projectID, hostname); err != nil {
		warnings = append(warnings, "ingress: "+err.Error())
	}

	// Domains — best effort. Only call domain_records_set when the
	// apex is actually present. This avoids creating noise in the
	// domains app's panel when the user manages DNS elsewhere.
	if err := maybeUpsertDNS(ctx, projectID, hostname); err != nil {
		warnings = append(warnings, "domains: "+err.Error())
	}

	if len(warnings) == 0 {
		return ""
	}
	return strings.Join(warnings, "; ")
}

// maybeUnwireHostname unexposes ingress when no rules remain for
// this hostname (within the same project_id). We never delete DNS —
// records may be shared with other services on the same hostname.
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
		ctx.Logger().Info("maybeUnwireHostname.unexpose", "host", hostname, "err", err.Error())
	}
}

// reconcileRegisteredRoutes refreshes platform ingress with this
// sidecar's current APTEVA_APP_PORT after a restart. Ingress stores
// concrete loopback targets, so a sidecar port change must be
// pushed even when no redirect rule changed.
func reconcileRegisteredRoutes(ctx *sdk.AppCtx) {
	if ctx == nil || ctx.AppDB() == nil {
		return
	}
	hosts, err := dbDistinctHostnames(ctx.AppDB(), "")
	if err != nil {
		ctx.Logger().Warn("redirects ingress reconcile list failed", "err", err.Error())
		return
	}
	if len(hosts) == 0 {
		return
	}
	var refreshed, failed int
	for _, host := range hosts {
		if err := registerRoute(ctx, "", host); err != nil {
			failed++
			ctx.Logger().Warn("redirects ingress reconcile failed", "host", host, "err", err.Error())
			continue
		}
		refreshed++
	}
	ctx.Logger().Info("redirects ingress reconciled", "refreshed", refreshed, "failed", failed, "target", sidecarTarget())
}

// ─── ingress ──────────────────────────────────────────────────────

func registerRoute(ctx *sdk.AppCtx, projectID, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	}
	_, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    sidecarTarget(),
		ProjectID: projectID,
		OwnerKind: "redirects",
		CertFQDN:  hostname,
	})
	return err
}

func unregisterRoute(ctx *sdk.AppCtx, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform api unavailable")
	}
	return ctx.PlatformAPI().UnexposeIngress(hostname)
}

// sidecarTarget builds the http://127.0.0.1:<port> URL platform
// ingress should reverse-proxy this hostname to. The platform injects
// APTEVA_APP_PORT into each sidecar — it's the free port the platform
// picked for this install, distinct from the manifest's static port.
// Falling back to the manifest port (8080) is a dev-only fallback.
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
		if ctx != nil {
			ctx.Logger().Info("domain_list unavailable", "err", err.Error())
		}
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

// maybeUpsertDNS checks whether the hostname's apex is known to
// domains. If so it upserts an A or CNAME pointing at the platform's
// public host/IP. If the domains app isn't installed or doesn't manage
// this hostname, it returns nil — DNS wiring is optional.
func maybeUpsertDNS(ctx *sdk.AppCtx, projectID, hostname string) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	apex, sub, managed, err := resolveManagedApex(ctx, projectID, hostname)
	if err != nil {
		ctx.Logger().Info("domain_list probe failed (skipping DNS)", "host", hostname, "err", err.Error())
		return nil
	}
	if !managed {
		return nil
	}
	if sub == "" {
		sub = "@"
	}

	target := platformDNSTarget(ctx)
	if target == "" {
		return errors.New("public host unavailable; set redirects public_host, APTEVA_PUBLIC_HOST, APTEVA_PUBLIC_URL, or platform public URL")
	}
	recordType := inferRecordType(target)
	if recordType == "A" {
		ip := net.ParseIP(target)
		if ip == nil || ip.To4() == nil {
			return errors.New("IPv6 DNS targets are not supported yet; use a hostname target")
		}
	}
	if recordType == "CNAME" && sub == "@" {
		return errors.New("apex CNAME isn't allowed; set public_host to an IP or use a subdomain")
	}

	var setResp struct {
		Record any `json:"record"`
	}
	if err := callDomains(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex,
		"name":   sub,
		"type":   recordType,
		"value":  target,
	}, &setResp); err != nil {
		return fmt.Errorf("domain_records_set: %w", err)
	}
	return nil
}

func resolveManagedApex(ctx *sdk.AppCtx, projectID, hostname string) (string, string, bool, error) {
	names := domainsList(ctx, projectID)
	if len(names) == 0 {
		return "", "", false, nil
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	var best string
	for _, name := range names {
		apex := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
		if apex == "" {
			continue
		}
		if host == apex || strings.HasSuffix(host, "."+apex) {
			if len(apex) > len(best) {
				best = apex
			}
		}
	}
	if best == "" {
		return "", "", false, nil
	}
	sub := ""
	if host != best {
		sub = strings.TrimSuffix(host, "."+best)
	}
	return best, sub, true, nil
}

// platformPublicHost returns the host/IP the DNS record should point
// at. Kept for the panel's _meta response.
func platformPublicHost() string {
	return platformDNSTarget(globalCtx)
}

func platformDNSTarget(ctx *sdk.AppCtx) string {
	if t := configOr(ctx, "public_host", ""); t != "" {
		return normalizeDNSTarget(t)
	}
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_HOST")); v != "" {
		return normalizeDNSTarget(v)
	}
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); v != "" {
		return normalizeDNSTarget(v)
	}
	if ctx != nil {
		info, err := ctx.PlatformInfo()
		if err == nil && info != nil {
			return normalizeDNSTarget(info.PublicURL)
		}
	}
	return ""
}

func normalizeDNSTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		raw = u.Hostname()
	} else {
		raw = strings.TrimPrefix(raw, "https://")
		raw = strings.TrimPrefix(raw, "http://")
		raw = strings.TrimSuffix(raw, "/")
		if i := strings.IndexAny(raw, ":/"); i >= 0 {
			raw = raw[:i]
		}
	}
	return strings.ToLower(strings.TrimSuffix(raw, "."))
}

func inferRecordType(target string) string {
	if net.ParseIP(target) != nil {
		return "A"
	}
	return "CNAME"
}

func configOr(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := strings.TrimSpace(ctx.Config().Get(key)); v != "" {
		return v
	}
	return def
}
