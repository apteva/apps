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
version: 0.4.1
description: |
  Identity layer for Apteva-deployed SaaS, partitioned by Organization
  (row-level multi-tenancy a la Auth0/Clerk/Stytch B2B). One install
  owns N orgs; each org has its own users, clients, signing keys, JWKS,
  and audit log. EdDSA JWTs, refresh-token rotation, email verification,
  password reset, magic links, TOTP MFA.
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
    - name: auth_orgs_list
      description: List organizations in the project.
    - name: auth_orgs_create
      description: Create a new organization.
    - name: auth_orgs_update
      description: Rename / recolor / set policy overrides.
    - name: auth_orgs_archive
      description: Archive (soft-disable) an organization.
    - name: auth_users_search
      description: Filtered user search; org-scoped or project-wide.
    - name: auth_users_get
      description: Snapshot of one user (requires org).
    - name: auth_users_get_context
      description: Snapshot + sessions + MFA + audit events (requires org).
    - name: auth_users_disable
      description: Disable + revoke sessions (requires org).
    - name: auth_users_enable
      description: Re-enable a disabled user (requires org).
    - name: auth_users_revoke_sessions
      description: Force-logout one user (requires org).
    - name: auth_audit_search
      description: Filter the audit log; org-scoped or project-wide.
    - name: auth_stats
      description: User counts; org-scoped or project-wide.
    - name: auth_clients_list
      description: List OAuth clients; org-scoped or project-wide.
    - name: auth_clients_create
      description: Register a new OAuth client (requires org).
    - name: auth_clients_rotate_secret
      description: Rotate a client secret (org derived from client).
    - name: auth_clients_disable
      description: Disable a client (org derived from client).
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
	pid := envProject()
	// Migrations have already run by the time OnMount fires (see
	// app-sdk run.go). Backfill in 002_organizations.sql guarantees
	// a Default org exists for every project_id seen at upgrade time;
	// for a fresh install there's no project_id in the DB yet, so we
	// create one here once we know our pid.
	if pid != "" {
		orgID := dbDefaultOrgID(ctx.AppDB(), pid)
		if orgID == 0 {
			id, err := dbCreateOrg(ctx.AppDB(), pid, "default", "Default", "#94a3b8")
			if err != nil {
				return fmt.Errorf("seed default org: %w", err)
			}
			orgID = id
		}
		if err := ensureSigningKey(ctx.AppDB(), pid, orgID); err != nil {
			return fmt.Errorf("seed signing key: %w", err)
		}
	}
	ctx.Logger().Info("auth mounted",
		"scope_project_id", pid)
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
		// Per-org discovery (v0.4.0).
		{Pattern: "/orgs/{slug}/.well-known/jwks.json", Handler: a.handleJWKS},
		{Pattern: "/orgs/{slug}/.well-known/openid-configuration", Handler: a.handleOIDCConfig},

		// Legacy discovery — resolves to the default org. Scheduled for
		// removal in v0.5.0; old SaaS code keeps working for one release
		// window so callers can update their JWT verifier configuration.
		{Pattern: "/.well-known/jwks.json", Handler: a.handleJWKS},
		{Pattern: "/.well-known/openid-configuration", Handler: a.handleOIDCConfig},

		// Public auth endpoints — tenant resolved from client_id at runtime.
		{Pattern: "/signup", Handler: a.handleSignup},
		{Pattern: "/login", Handler: a.handleLogin},
		{Pattern: "/logout", Handler: a.handleLogout},
		{Pattern: "/refresh", Handler: a.handleRefresh},
		{Pattern: "/me", Handler: a.handleMe},

		// Admin surface — consumed by the dashboard AuthPanel. Auth is
		// the SDK's bearer-token gate (platform proxy attaches it).
		{Method: "GET", Pattern: "/admin/organizations", Handler: a.handleAdminOrgsList},
		{Method: "POST", Pattern: "/admin/organizations", Handler: a.handleAdminOrgsCreate},
		{Method: "PATCH", Pattern: "/admin/organizations/{id}", Handler: a.handleAdminOrgsPatch},
		{Method: "POST", Pattern: "/admin/organizations/{id}/archive", Handler: a.handleAdminOrgsArchive},

		{Method: "GET", Pattern: "/admin/stats", Handler: a.handleAdminStats},
		{Method: "GET", Pattern: "/admin/users", Handler: a.handleAdminUsersList},
		{Method: "POST", Pattern: "/admin/users", Handler: a.handleAdminUsersCreate},
		{Method: "GET", Pattern: "/admin/users/{id}/context", Handler: a.handleAdminUsersGetContext},
		{Method: "PATCH", Pattern: "/admin/users/{id}", Handler: a.handleAdminUsersPatch},
		{Method: "POST", Pattern: "/admin/users/{id}/disable", Handler: a.handleAdminUsersDisable},
		{Method: "POST", Pattern: "/admin/users/{id}/enable", Handler: a.handleAdminUsersEnable},
		{Method: "POST", Pattern: "/admin/users/{id}/revoke_sessions", Handler: a.handleAdminUsersRevokeSessions},
		{Method: "POST", Pattern: "/admin/users/{id}/send_password_reset", Handler: a.handleAdminUsersSendPasswordReset},
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
	// orgSelector — every tool that operates on user/client data
	// accepts either organization_id or organization_slug. Reads roll
	// up project-wide when omitted; mutations error.
	orgSelector := map[string]any{
		"organization_id":   map[string]any{"type": "integer"},
		"organization_slug": map[string]any{"type": "string"},
	}
	merge := func(base map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range orgSelector {
			out[k] = v
		}
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	return []sdk.Tool{
		// ─── Organizations ─────────────────────────────────────────
		{
			Name:        "auth_orgs_list",
			Description: "List organizations in this project. Args: include_archived (bool).",
			InputSchema: schemaObject(map[string]any{
				"include_archived": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolOrgsList,
		},
		{
			Name:        "auth_orgs_create",
			Description: "Create an organization. Args: slug (lowercase a-z 0-9 -), name, color (hex, optional). Auto-seeds an EdDSA keypair for the new org's JWKS.",
			InputSchema: schemaObject(map[string]any{
				"slug":  map[string]any{"type": "string"},
				"name":  map[string]any{"type": "string"},
				"color": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: a.toolOrgsCreate,
		},
		{
			Name:        "auth_orgs_update",
			Description: "Update an organization. Args: organization_id OR organization_slug, name, color, policy_overrides (JSON string).",
			InputSchema: schemaObject(merge(map[string]any{
				"name":             map[string]any{"type": "string"},
				"color":            map[string]any{"type": "string"},
				"policy_overrides": map[string]any{"type": "string"},
			}), nil),
			Handler: a.toolOrgsUpdate,
		},
		{
			Name:        "auth_orgs_archive",
			Description: "Archive an organization (soft-disable; cannot archive 'default'). Args: organization_id OR organization_slug.",
			InputSchema: schemaObject(orgSelector, nil),
			Handler:     a.toolOrgsArchive,
		},

		// ─── Users ─────────────────────────────────────────────────
		{
			Name:        "auth_users_search",
			Description: "Filtered user search. Org-scoped when organization_id/slug is given; project-wide otherwise. Args: q (substring on email/display_name), status (active|disabled|deleted), mfa (bool), created_after (RFC3339), limit (default 50, max 200).",
			InputSchema: schemaObject(merge(map[string]any{
				"q":             map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string"},
				"mfa":           map[string]any{"type": "boolean"},
				"created_after": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}), nil),
			Handler: a.toolUsersSearch,
		},
		{
			Name:        "auth_users_get",
			Description: "Fetch one user. Requires organization_id/slug. Args: id OR email.",
			InputSchema: schemaObject(merge(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}), nil),
			Handler: a.toolUsersGet,
		},
		{
			Name:        "auth_users_get_context",
			Description: "Snapshot + active sessions + MFA factors + last 20 audit events. Requires organization_id/slug.",
			InputSchema: schemaObject(merge(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
			}), nil),
			Handler: a.toolUsersGetContext,
		},
		{
			Name:        "auth_users_revoke_sessions",
			Description: "Force-logout one user across all sessions. Requires organization_id/slug.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id": map[string]any{"type": "integer"},
			}), []string{"user_id"}),
			Handler: a.toolUsersRevokeSessions,
		},
		{
			Name:        "auth_users_disable",
			Description: "Disable a user and revoke all sessions. Requires organization_id/slug.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id": map[string]any{"type": "integer"},
				"reason":  map[string]any{"type": "string"},
			}), []string{"user_id"}),
			Handler: a.toolUsersDisable,
		},
		{
			Name:        "auth_users_enable",
			Description: "Re-enable a disabled user. Requires organization_id/slug.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id": map[string]any{"type": "integer"},
			}), []string{"user_id"}),
			Handler: a.toolUsersEnable,
		},

		// ─── Audit + stats ─────────────────────────────────────────
		{
			Name:        "auth_audit_search",
			Description: "Filter the audit log. Org-scoped when organization_id/slug given; project-wide otherwise.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id": map[string]any{"type": "integer"},
				"event":   map[string]any{"type": "string"},
				"since":   map[string]any{"type": "string"},
				"until":   map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}), nil),
			Handler: a.toolAuditSearch,
		},
		{
			Name:        "auth_stats",
			Description: "User counts (active / disabled / locked) + signups_7d + logins_24h. Org-scoped when given; project-wide rollup otherwise.",
			InputSchema: schemaObject(orgSelector, nil),
			Handler:     a.toolStats,
		},

		// ─── OAuth clients ─────────────────────────────────────────
		{
			Name:        "auth_clients_list",
			Description: "List OAuth clients. Org-scoped when given; project-wide otherwise.",
			InputSchema: schemaObject(merge(map[string]any{
				"include_disabled": map[string]any{"type": "boolean"},
			}), nil),
			Handler: a.toolClientsList,
		},
		{
			Name:        "auth_clients_create",
			Description: "Register a new OAuth client. Requires organization_id/slug. Returns client_id + (for confidential clients) one-time client_secret.",
			InputSchema: schemaObject(merge(map[string]any{
				"name":                map[string]any{"type": "string"},
				"type":                map[string]any{"type": "string"},
				"redirect_uris":       map[string]any{"type": "array"},
				"allowed_origins":     map[string]any{"type": "array"},
				"allowed_grant_types": map[string]any{"type": "array"},
				"require_mfa":         map[string]any{"type": "boolean"},
				"jwt_audience":        map[string]any{"type": "string"},
			}), []string{"name", "type"}),
			Handler: a.toolClientsCreate,
		},
		{
			Name:        "auth_clients_rotate_secret",
			Description: "Rotate a client's secret. Returns the new value once. Org is derived from the client row.",
			InputSchema: schemaObject(map[string]any{
				"client_id": map[string]any{"type": "string"},
			}, []string{"client_id"}),
			Handler: a.toolClientsRotateSecret,
		},
		{
			Name:        "auth_clients_disable",
			Description: "Disable a client. Org is derived from the client row.",
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
// Generate the first EdDSA keypair for an org when none exists. Each
// organization owns its own keys (per-org JWKS). Called from OnMount
// for the default org and from /admin/organizations POST for new ones.

func ensureSigningKey(db *sql.DB, projectID string, orgID int64) error {
	if projectID == "" || orgID <= 0 {
		return nil
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM signing_keys WHERE project_id = ? AND organization_id = ? AND retired_at IS NULL`,
		projectID, orgID,
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
		`INSERT INTO signing_keys(project_id, organization_id, kid, alg, private_pem, public_pem) VALUES(?,?,?,?,?,?)`,
		projectID, orgID, kid, "EdDSA", string(privPEM), string(pubPEM),
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
