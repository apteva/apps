package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

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

// wireHostname claims hostname for this sidecar with routes, and
// upserts a CNAME via domains when the hostname is known there.
// Returns a human-readable warning string when something failed (or
// "" on full success); errors are not propagated because creating
// the redirect rule should not roll back on wiring failure.
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
// should reverse-proxy this hostname to. The platform exposes the
// sidecar's listening port via APTEVA_PORT.
func sidecarTarget() string {
	port := os.Getenv("APTEVA_PORT")
	if port == "" {
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

// platformPublicHost — the DNS target the CNAME should point at. Set
// by the platform when this sidecar starts; falls back to the local
// hostname only as a last-resort, which won't work for public DNS but
// at least tells the panel something concrete in dev.
func platformPublicHost() string {
	if v := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_HOST")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("PUBLIC_URL")); v != "" {
		// PUBLIC_URL is sometimes a full URL like https://x.example.com.
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		v = strings.TrimSuffix(v, "/")
		return v
	}
	return ""
}
