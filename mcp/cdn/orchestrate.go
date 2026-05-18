package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Cross-app orchestration for cdn_zone_create / cdn_zone_delete.
//
// All three siblings (domains, certs, routes) are global-scoped on
// prod, so every call gets _project_id injected. CallAppResult is
// used over CallApp so the envelope unwrap stays in one place
// (project memory: feedback_app_to_app_calls).

// myInstallID returns this sidecar's install id, used as
// owner_install_id when calling routes_register. routes refuses
// registrations without an owner because the platform doesn't yet
// stamp caller identity into CallApp. The sidecar reads its own id
// from APTEVA_INSTALL_ID at boot.
func myInstallID() int64 {
	if v := os.Getenv("APTEVA_INSTALL_ID"); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

// writeDNS calls domains.domain_records_set. The hostname is split
// into (apex, sub) here so the caller doesn't have to know the
// registrar's data model — domains hides the per-provider details.
func writeDNS(ctx *sdk.AppCtx, projectID, hostname, recordType, recordValue string) error {
	apex, sub := splitApex(hostname)
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if recordType == "CNAME" && sub == "" {
		return errors.New("apex CNAME isn't allowed; use record_type=A with an IP, or pick a subdomain")
	}
	args := map[string]any{
		"_project_id": projectID,
		"domain":      apex,
		"name":        subArg,
		"type":        recordType,
		"value":       recordValue,
		"ttl":         600,
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", args, &out); err != nil {
		return fmt.Errorf("domains.domain_records_set: %w", err)
	}
	return nil
}

// deleteDNS is the inverse of writeDNS. Best-effort — a failed
// delete is logged but doesn't block zone tear-down (operator can
// clean up manually at the registrar).
func deleteDNS(ctx *sdk.AppCtx, projectID, hostname, recordType string) error {
	apex, sub := splitApex(hostname)
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	args := map[string]any{
		"_project_id": projectID,
		"domain":      apex,
		"name":        subArg,
		"type":        recordType,
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("domains", "domain_records_delete", args, &out)
}

// issueCert kicks the certs app to obtain a TLS cert for the
// hostname. cert_issue is async on the certs side — it returns
// immediately with status=issuing; CertCache picks up the material
// on its next refresh tick (60s). The browser may see a brief TLS
// error on the very first request after create.
func issueCert(ctx *sdk.AppCtx, projectID, hostname string) error {
	args := map[string]any{
		"_project_id": projectID,
		"fqdn":        hostname,
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("certs", "cert_issue", args, &out); err != nil {
		return fmt.Errorf("certs.cert_issue: %w", err)
	}
	return nil
}

// revokeCert is the inverse of issueCert. Best-effort.
func revokeCert(ctx *sdk.AppCtx, projectID, hostname string) error {
	args := map[string]any{
		"_project_id": projectID,
		"fqdn":        hostname,
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("certs", "cert_revoke", args, &out)
}

// registerRoute wires the host→target mapping into the routes app.
// On success, apteva-server's HostRouter picks up the change via
// the routes.changed event and starts reverse-proxying to
// originURL. owner_install_id is required by the routes app so
// it can refuse cross-owner overwrites.
//
// allowHTTP=true tells routes (and through it, apteva-server's
// HostRouter) to serve over plain HTTP without the 301 to HTTPS
// — used by local-dev zones that skip the cert leg.
func registerRoute(ctx *sdk.AppCtx, projectID, hostname, originURL string, allowHTTP bool) error {
	myID := myInstallID()
	if myID == 0 {
		return errors.New("APTEVA_INSTALL_ID not set; cdn can't register routes without an owner id")
	}
	args := map[string]any{
		"_project_id":      projectID,
		"hostname":         hostname,
		"target":           originURL,
		"owner_install_id": myID,
		"owner_kind":       "cdn",
		"cert_fqdn":        hostname,
		"allow_http":       allowHTTP,
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("routes", "routes_register", args, &out); err != nil {
		return fmt.Errorf("routes.routes_register: %w", err)
	}
	return nil
}

// looksLikeAppNotInstalled detects errors from CallApp/CallAppResult
// that indicate the target sidecar isn't running on this install
// (the optional dep is absent). Used by the dns + cert legs to
// degrade gracefully to "skipped" instead of "error" when the
// operator hasn't installed domains/certs — the explicit local-dev
// path of "install only routes + cdn".
//
// String-matching the platform's error layer is brittle but the
// alternative (a typed sentinel) would require an SDK change. Match
// is intentionally generous; a misclassification just downgrades an
// error to a skip in the panel, which the operator can re-examine.
func looksLikeAppNotInstalled(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"app not running",
		"not installed",
		"app not found",
		"unknown app",
		"no app",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// unregisterRoute is the inverse of registerRoute. Best-effort.
func unregisterRoute(ctx *sdk.AppCtx, projectID, hostname string) error {
	myID := myInstallID()
	if myID == 0 {
		return errors.New("APTEVA_INSTALL_ID not set")
	}
	args := map[string]any{
		"_project_id":      projectID,
		"hostname":         hostname,
		"owner_install_id": myID,
	}
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("routes", "routes_unregister", args, &out)
}
