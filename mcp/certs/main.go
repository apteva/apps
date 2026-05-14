// Apteva Certs v0.1 — TLS certificate issuance via ACME DNS-01.
//
// All DNS writes go through the Domains app (cross-app MCP call), so
// this app never holds registrar credentials. Issuance is async: the
// caller fires cert_issue and polls cert_get; renewal happens on a
// daily timer. cert_material is privileged — only the server (TLS
// cache) and this app's own renewal worker may call it.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: certs
display_name: Certs
version: 0.3.4
description: TLS certificate issuance via ACME DNS-01 (through Domains app) or HTTP-01 (webroot).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
  integrations:
    - role: domains
      kind: app
      required: false
      compatible_app_names: [domains]
      label: Domains app
      hint: Required for DNS-01. Optional — HTTP-01 (webroot) works without it.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: cert_issue,    description: "Issue a TLS cert for an FQDN (async)." }
    - { name: cert_get,      description: "Fetch one cert by id or fqdn." }
    - { name: cert_list,     description: "List certs in this project." }
    - { name: cert_material, description: "PEM cert + key (PRIVILEGED — server only)." }
    - { name: cert_revoke,   description: "Revoke + delete a cert." }
    - { name: cert_renew,    description: "Force-renew a cert." }
  ui_panels:
    - { slot: project.page, label: "Certs", icon: lock, entry: /ui/CertsPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/certs
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/certs.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	directoryURL  string
	contactEmail  string
	renewWindow   time.Duration
	dnsTimeout    time.Duration
	challengeType string // "dns-01" | "http-01" | "auto"
	webrootPath   string // filesystem dir for http-01 challenge files

	// Per-FQDN issuance lock so concurrent cert_issue / renew calls
	// for the same name don't collide on the challenge slot.
	mu       sync.Mutex
	inFlight map[string]bool

	// Renewal worker tick — owned by the app, not the SDK scheduler.
	// time.Ticker keeps the dependency surface small and lets tests
	// trigger renewalPass directly.
	stopCh chan struct{}
}

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
		return errors.New("certs requires a db block")
	}
	globalCtx = ctx

	a.directoryURL = strings.TrimSpace(configOr(ctx, "acme_directory_url",
		"https://acme-staging-v02.api.letsencrypt.org/directory"))
	a.contactEmail = strings.TrimSpace(configOr(ctx, "acme_email", ""))
	a.renewWindow = time.Duration(atoiOr(configOr(ctx, "renewal_window_days", "30"), 30)) * 24 * time.Hour
	a.dnsTimeout = time.Duration(atoiOr(configOr(ctx, "dns_propagation_timeout_seconds", "180"), 180)) * time.Second
	a.challengeType = strings.TrimSpace(configOr(ctx, "challenge_type", "auto"))
	a.webrootPath = strings.TrimSpace(configOr(ctx, "webroot_path", "/var/www/acme"))
	a.inFlight = map[string]bool{}
	a.stopCh = make(chan struct{})

	ctx.Logger().Info("certs mounted",
		"directory", a.directoryURL,
		"email_configured", a.contactEmail != "",
		"renew_window", a.renewWindow.String(),
		"challenge_type", a.challengeType,
		"webroot_path", a.webrootPath,
	)

	// Daily renewal pass. Runs once on boot too — cheap when there's
	// nothing to renew, and catches certs that crossed the window
	// while the app was down.
	go a.renewalLoop(24 * time.Hour)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	close(a.stopCh)
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── Routes ────────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/certs", Handler: a.handleCertsCollection},
		{Pattern: "/api/certs/", Handler: a.handleCertItem},
		{Pattern: "/api/_meta", Handler: a.handleMeta},
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

// ─── Renewal loop ──────────────────────────────────────────────────

func (a *App) renewalLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	a.renewalPass()
	for {
		select {
		case <-a.stopCh:
			return
		case <-t.C:
			a.renewalPass()
		}
	}
}

// ─── Helpers ───────────────────────────────────────────────────────

func configOr(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v := ctx.Config().Get(key); v != "" {
		return v
	}
	return def
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// emit publishes an event over the platform bus, if available.
func emit(topic string, data any) {
	if globalCtx == nil {
		return
	}
	globalCtx.Emit(topic, data)
}

// withIssuanceLock serialises issuance for a single FQDN. Different
// FQDNs run concurrently. Returns a release func; caller must defer
// it. Returns false if another goroutine already holds the lock.
func (a *App) withIssuanceLock(fqdn string) (release func(), held bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight[fqdn] {
		return func() {}, false
	}
	a.inFlight[fqdn] = true
	return func() {
		a.mu.Lock()
		delete(a.inFlight, fqdn)
		a.mu.Unlock()
	}, true
}

// shortCtx returns a context with a sane bound for the whole ACME
// dance. Caller can layer tighter timeouts inside.
func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// fmtErr makes a stable, single-line error string for the DB.
func fmtErr(stage string, err error) string {
	return fmt.Sprintf("%s: %s", stage, strings.ReplaceAll(err.Error(), "\n", " "))
}

// selectChallengeType resolves the configured choice. Explicit values
// win and are returned as-is (so misconfigurations fail loudly at
// issuance time with a clear error). "auto" picks dns-01 when the
// Domains app is installed AND has at least one registered domain —
// the existing, proven path; otherwise falls back to http-01.
func (a *App) selectChallengeType(ctx *sdk.AppCtx) string {
	switch a.challengeType {
	case "dns-01", "http-01":
		return a.challengeType
	case "", "auto":
		if domainsAvailable(ctx) {
			return "dns-01"
		}
		return "http-01"
	default:
		// Unrecognised value — bias toward the proven path so a typo
		// doesn't silently route to the newer http-01 code.
		return "dns-01"
	}
}

// domainsAvailable returns true when the Domains app is installed AND
// has at least one registered domain. An empty domains list is
// treated as "not available" because dns-01 would fail at resolveApex
// anyway — better to fall through to http-01 in auto mode.
func domainsAvailable(ctx *sdk.AppCtx) bool {
	var resp struct {
		Domains []any `json:"domains"`
	}
	if err := callDomainsTool(ctx, "domain_list", map[string]any{}, &resp); err != nil {
		return false
	}
	return len(resp.Domains) > 0
}
