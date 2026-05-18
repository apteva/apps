// Auth v0.1 — identity layer for Apteva-deployed SaaS.
//
// One Apteva project owns one auth install (user pool). Inside that
// pool, multiple `clients` (OAuth-spec term — Auth0 calls them
// "Applications", Cognito calls them "App Clients") consume the auth
// instance. Agents administer the pool via MCP tools; the deployed
// SaaS frontend hits the HTTP routes; the dashboard renders Users /
// Clients / Settings panels.
//
// Files in this package:
//   main.go    — App, manifest, OnMount, route + tool wiring, helpers
//   types.go   — domain types and JSON shapes
//   db.go      — SQL access (no business logic)
//   crypto.go  — argon2id, sha256 token hashing, EdDSA JWT sign/verify
//   handlers.go— HTTP handlers (signup/login/refresh/logout/me/jwks)
//   tools.go   — MCP tool handlers
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ────────────────────────────────────────────────
//
// Mirrors apteva.yaml. Embedded so `auth --help` is self-describing
// and so the running binary can validate its own manifest at boot.
// Keep in sync with apteva.yaml — manifest_test.go enforces this.

const manifestYAML = `schema: apteva-app/v1
name: auth
display_name: Auth
version: 0.2.1
description: |
  Identity layer for Apteva-deployed SaaS. Email + password signup/login,
  asymmetric JWT issuance, refresh-token sessions, OAuth client registry,
  email verification, password reset, magic links, TOTP MFA.
author: Apteva
scopes: [project]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
    - platform.apps.call
    - net.egress
  apps:
    - name: messaging
      optional: true
      reason: Sends transactional email. Without it, links go to the audit log.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: auth_users_search
      description: Filtered user search.
    - name: auth_users_get
      description: Snapshot of one user.
    - name: auth_users_get_context
      description: Snapshot + sessions + MFA + audit events.
    - name: auth_users_disable
      description: Disable + revoke sessions.
    - name: auth_users_enable
      description: Re-enable a disabled user.
    - name: auth_users_revoke_sessions
      description: Force-logout one user.
    - name: auth_audit_search
      description: Filter the audit log.
    - name: auth_stats
      description: Active / signup / login counts.
    - name: auth_clients_list
      description: List OAuth clients.
    - name: auth_clients_create
      description: Register a new OAuth client.
    - name: auth_clients_rotate_secret
      description: Rotate a client secret.
    - name: auth_clients_disable
      description: Disable a client.
  ui_panels:
    - slot: project.page
      label: Auth
      icon: key
      entry: /ui/AuthPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/auth
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/auth.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App boilerplate ──────────────────────────────────────────────────

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("auth requires a db block")
	}
	globalCtx = ctx
	// Seed the first signing key on first boot. JWTs can't be issued
	// without an active key; doing this lazily on first /login would
	// race with concurrent boots. Eager seed on mount is simple and
	// idempotent.
	if err := ensureSigningKey(ctx.AppDB(), envProject()); err != nil {
		return fmt.Errorf("seed signing key: %w", err)
	}
	ctx.Logger().Info("auth mounted",
		"scope_project_id", envProject())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error          { return nil }
func (a *App) Channels() []sdk.ChannelFactory       { return nil }
func (a *App) Workers() []sdk.Worker                { return nil }
func (a *App) EventHandlers() []sdk.EventHandler    { return nil }

// ─── HTTP routes ──────────────────────────────────────────────────────
//
// Reverse-proxied at /apps/auth/* by apteva-server. The deployed SaaS
// frontend hits these directly; the dashboard panels also use them.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// /health is registered by the SDK framework — don't double-register.
		{Pattern: "/.well-known/jwks.json", Handler: a.handleJWKS},
		{Pattern: "/.well-known/openid-configuration", Handler: a.handleOIDCConfig},

		{Pattern: "/signup", Handler: a.handleSignup},
		{Pattern: "/login", Handler: a.handleLogin},
		{Pattern: "/logout", Handler: a.handleLogout},
		{Pattern: "/refresh", Handler: a.handleRefresh},
		{Pattern: "/me", Handler: a.handleMe},

		// Admin surface — consumed by the dashboard AuthPanel. Auth is
		// the SDK's bearer-token gate (platform proxy attaches it).
		{Method: "GET", Pattern: "/admin/stats", Handler: a.handleAdminStats},
		{Method: "GET", Pattern: "/admin/users", Handler: a.handleAdminUsersList},
		{Method: "GET", Pattern: "/admin/users/{id}/context", Handler: a.handleAdminUsersGetContext},
		{Method: "POST", Pattern: "/admin/users/{id}/disable", Handler: a.handleAdminUsersDisable},
		{Method: "POST", Pattern: "/admin/users/{id}/enable", Handler: a.handleAdminUsersEnable},
		{Method: "POST", Pattern: "/admin/users/{id}/revoke_sessions", Handler: a.handleAdminUsersRevokeSessions},
		{Method: "GET", Pattern: "/admin/clients", Handler: a.handleAdminClientsList},
		{Method: "POST", Pattern: "/admin/clients", Handler: a.handleAdminClientsCreate},
		{Method: "POST", Pattern: "/admin/clients/{client_id}/rotate", Handler: a.handleAdminClientsRotate},
		{Method: "POST", Pattern: "/admin/clients/{client_id}/disable", Handler: a.handleAdminClientsDisable},
		{Method: "GET", Pattern: "/admin/audit", Handler: a.handleAdminAudit},
		{Method: "GET", Pattern: "/admin/oidc", Handler: a.handleAdminOIDC},
	}
}

// ─── MCP tools ────────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "auth_users_search",
			Description: "Filtered user search. Args: q (substring on email/display_name), status (active|disabled|deleted), mfa (bool), created_after (RFC3339), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"q":             map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string"},
				"mfa":           map[string]any{"type": "boolean"},
				"created_after": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolUsersSearch,
		},
		{
			Name:        "auth_users_get",
			Description: "Fetch one user. Args: id OR email.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolUsersGet,
		},
		{
			Name:        "auth_users_get_context",
			Description: "Snapshot + active sessions + MFA factors + last 20 audit events.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolUsersGetContext,
		},
		{
			Name:        "auth_users_revoke_sessions",
			Description: "Force-logout one user across all sessions. Args: user_id.",
			InputSchema: schemaObject(map[string]any{
				"user_id": map[string]any{"type": "integer"},
			}, []string{"user_id"}),
			Handler: a.toolUsersRevokeSessions,
		},
		{
			Name:        "auth_users_disable",
			Description: "Disable a user and revoke all sessions. Args: user_id, reason.",
			InputSchema: schemaObject(map[string]any{
				"user_id": map[string]any{"type": "integer"},
				"reason":  map[string]any{"type": "string"},
			}, []string{"user_id"}),
			Handler: a.toolUsersDisable,
		},
		{
			Name:        "auth_users_enable",
			Description: "Re-enable a disabled user. Args: user_id.",
			InputSchema: schemaObject(map[string]any{
				"user_id": map[string]any{"type": "integer"},
			}, []string{"user_id"}),
			Handler: a.toolUsersEnable,
		},
		{
			Name:        "auth_audit_search",
			Description: "Filter the audit log. Args: user_id, event, since (RFC3339), until (RFC3339), limit (default 100, max 500).",
			InputSchema: schemaObject(map[string]any{
				"user_id": map[string]any{"type": "integer"},
				"event":   map[string]any{"type": "string"},
				"since":   map[string]any{"type": "string"},
				"until":   map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolAuditSearch,
		},
		{
			Name:        "auth_stats",
			Description: "Active / disabled / locked user counts; signups_7d, logins_24h.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolStats,
		},
		{
			Name:        "auth_clients_list",
			Description: "List OAuth clients (frontends/services that consume auth).",
			InputSchema: schemaObject(map[string]any{
				"include_disabled": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolClientsList,
		},
		{
			Name:        "auth_clients_create",
			Description: "Register a new OAuth client. Returns client_id + (for confidential clients) one-time client_secret. Args: name, type (spa|web|native|m2m), redirect_uris [string], allowed_grant_types [string], require_mfa (bool).",
			InputSchema: schemaObject(map[string]any{
				"name":                map[string]any{"type": "string"},
				"type":                map[string]any{"type": "string"},
				"redirect_uris":       map[string]any{"type": "array"},
				"allowed_origins":     map[string]any{"type": "array"},
				"allowed_grant_types": map[string]any{"type": "array"},
				"require_mfa":         map[string]any{"type": "boolean"},
				"jwt_audience":        map[string]any{"type": "string"},
			}, []string{"name", "type"}),
			Handler: a.toolClientsCreate,
		},
		{
			Name:        "auth_clients_rotate_secret",
			Description: "Rotate a client's secret. Returns the new value once. Args: client_id.",
			InputSchema: schemaObject(map[string]any{
				"client_id": map[string]any{"type": "string"},
			}, []string{"client_id"}),
			Handler: a.toolClientsRotateSecret,
		},
		{
			Name:        "auth_clients_disable",
			Description: "Disable a client. Args: client_id.",
			InputSchema: schemaObject(map[string]any{
				"client_id": map[string]any{"type": "string"},
			}, []string{"client_id"}),
			Handler: a.toolClientsDisable,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution (mirrors CRM) ─────────────────────────────────

func envProject() string {
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
}

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if pid := envProject(); pid != "" {
		return pid, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := envProject(); pid != "" {
		return pid, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required when install scope=global")
}

// ─── Config helpers ──────────────────────────────────────────────────
//
// Read install configuration with sane defaults. Stored as text in
// the framework's Config map; we coerce here so business logic stays
// simple.

func cfgInt(ctx *sdk.AppCtx, name string, dflt int) int {
	if ctx == nil || ctx.Config() == nil {
		return dflt
	}
	v := ctx.Config().Get(name)
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return dflt
	}
	return n
}

func cfgBool(ctx *sdk.AppCtx, name string, dflt bool) bool {
	if ctx == nil || ctx.Config() == nil {
		return dflt
	}
	v := strings.ToLower(strings.TrimSpace(ctx.Config().Get(name)))
	if v == "" {
		return dflt
	}
	return v == "true" || v == "1" || v == "yes"
}

func cfgStr(ctx *sdk.AppCtx, name, dflt string) string {
	if ctx == nil || ctx.Config() == nil {
		return dflt
	}
	if v := ctx.Config().Get(name); v != "" {
		return v
	}
	return dflt
}

// ─── Signing-key bootstrap ───────────────────────────────────────────
//
// Generate the first EdDSA keypair on mount when none exists. Subsequent
// rotations are explicit (admin endpoint, deferred to v0.2).

func ensureSigningKey(db *sql.DB, projectID string) error {
	if projectID == "" {
		// Global scope — keys are project-scoped, so seeding here would
		// be premature. The first /login for each project will lazily
		// create a key (handled in jwtSign).
		return nil
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM signing_keys WHERE project_id = ? AND retired_at IS NULL`,
		projectID,
	).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})
	kid, err := randSlug(16)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO signing_keys(project_id, kid, alg, private_pem, public_pem) VALUES(?,?,?,?,?)`,
		projectID, kid, "EdDSA", string(privPEM), string(pubPEM),
	)
	return err
}

// ─── Schema helper (mirrors CRM) ─────────────────────────────────────

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

// ─── Stashed AppCtx for HTTP handlers (mirrors CRM) ──────────────────
//
// The SDK doesn't expose a per-request AppCtx; HTTP handlers reach for
// this global. If the SDK grows a request-scoped accessor we'll switch.

var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// ─── HTTP utilities ──────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	httpStatus(w, code, map[string]string{"error": msg})
}

// ─── Time helpers ────────────────────────────────────────────────────

func nowUTC() time.Time { return time.Now().UTC() }
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
