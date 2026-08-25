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
//
//	main.go    — App, manifest, OnMount, route + tool wiring, helpers
//	types.go   — domain types and JSON shapes
//	db.go      — SQL access (no business logic)
//	crypto.go  — argon2id, sha256 token hashing, EdDSA JWT sign/verify
//	handlers.go— HTTP handlers (signup/login/refresh/logout/me/jwks)
//	tools.go   — MCP tool handlers
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
version: 0.9.4
description: |
  Identity layer for Apteva-deployed SaaS, partitioned by Organization
  (row-level multi-tenancy a la Auth0/Clerk/Stytch B2B). One install
  owns N orgs; each org has its own users, clients, signing keys, JWKS,
  and audit log. EdDSA JWTs, refresh-token rotation, email verification,
  password reset, magic links, TOTP MFA. Optional delegated authorization
  is resolved independently by the Apteva server per OAuth client. OAuth
  client origins are reconciled with platform-managed CORS.
author: Apteva
scopes: [project]
min_apteva_version: "0.14.1"
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
    - prefix: /signup
      method: POST
      no_auth: true
    - prefix: /login
      method: POST
      no_auth: true
    - prefix: /password/reset/request
      method: POST
      no_auth: true
    - prefix: /password/reset/confirm
      method: POST
      no_auth: true
    - prefix: /email/verify
      method: POST
      no_auth: true
    - prefix: /email/verification/resend
      method: POST
      no_auth: true
    - prefix: /logout
      method: POST
      no_auth: true
    - prefix: /refresh
      method: POST
      no_auth: true
    - prefix: /me
      method: GET
      no_auth: true
    - prefix: /me/metadata
      method: PATCH
      no_auth: true
    - prefix: /.well-known/jwks.json
      method: GET
      no_auth: true
    - prefix: /.well-known/openid-configuration
      method: GET
      no_auth: true
    - prefix: /orgs/
      method: GET
      no_auth: true
    - prefix: /admin/
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
    - name: auth_users_create
      description: ADMIN — provision a user (requires org). send_password_reset defaults false. No session minted. Supports metadata JSON. For visitor signup use auth_public_signup.
    - name: auth_users_update
      description: ADMIN — update display_name, email_verified, or metadata JSON for one user.
    - name: auth_public_signup
      description: Visitor-facing signup, equivalent to POST /signup. Resolves org from client_id, mints tokens or sends verify email.
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
    - name: auth_users_set_password
      description: Set a new password for one user (requires org).
    - name: auth_roles_list
      description: List roles and their effective permissions (requires org).
    - name: auth_roles_create
      description: Create an organization-scoped role.
    - name: auth_roles_update
      description: Update a role's display data.
    - name: auth_roles_delete
      description: Delete a role and remove its user assignments.
    - name: auth_permissions_list
      description: List the organization's permission catalog.
    - name: auth_permissions_create
      description: Create a namespaced permission.
    - name: auth_permissions_update
      description: Update a permission's display data.
    - name: auth_permissions_delete
      description: Delete a permission and remove it from roles.
    - name: auth_role_permissions_set
      description: Replace a role's permission set atomically.
    - name: auth_user_roles_set
      description: Replace a user's role assignments atomically.
    - name: auth_audit_search
      description: Filter the audit log; org-scoped or project-wide.
    - name: auth_stats
      description: User counts; org-scoped or project-wide.
    - name: auth_clients_list
      description: List OAuth clients; org-scoped or project-wide.
    - name: auth_clients_create
      description: Register a new OAuth client (requires org).
    - name: auth_clients_update
      description: Add or remove allowed browser origins without replacing the client.
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
config_schema:
  - name: app_url
    type: text
    label: Public app URL (override)
    description: Optional. Used in JWT issuer, email links, and OAuth redirects. Leave blank to auto-derive from the platform's public URL; set this only when this auth install fronts a custom domain different from apteva-server. e.g. https://app.example.com
  - name: from_email
    type: text
    label: Outbound from-address
    description: Sender address for transactional email (verify, reset, magic-link). Configure a Messaging-verified address for production portals. When omitted, links are written to the audit log for local development only.
  - name: jwt_access_ttl_seconds
    type: text
    default: "900"
    label: Access token TTL (seconds)
    description: Default 15 minutes. Per-client overrides supported.
  - name: jwt_refresh_ttl_days
    type: text
    default: "30"
    label: Refresh token TTL (days)
    description: Default 30. Per-client overrides supported.
  - name: password_min_length
    type: text
    default: "8"
    label: Password minimum length
    description: Minimum character count for new passwords. Default 8.
  - name: password_classes_required
    type: text
    default: "0"
    label: Password classes required
    description: Of [lower, upper, digit, symbol], how many must appear. 0–4. Default 0 (no character-class requirement).
  - name: lockout_threshold
    type: text
    default: "5"
    label: Failed-login lockout threshold
    description: Consecutive failures before the account locks. 0 disables lockout.
  - name: lockout_initial_minutes
    type: text
    default: "15"
    label: Initial lockout duration (minutes)
    description: Doubles on each subsequent lockout while the failure streak persists.
  - name: email_verification_required
    type: text
    default: "true"
    label: Require verified email to log in
    description: When true, /login refuses unverified accounts. true | false.
  - name: magic_link_enabled
    type: text
    default: "true"
    label: Enable magic-link login
    description: Allows /magic-link/request — passwordless login by emailed token. true | false.
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
		if err := reconcileBrowserOrigins(ctx, pid); err != nil {
			// Auth's database remains authoritative and the same stable keys are
			// retried on the next mount. A CORS control-plane outage must not make
			// the identity service unavailable.
			ctx.Logger().Warn("browser-origin reconciliation incomplete", "error", err)
		}
	}
	ctx.Logger().Info("auth mounted",
		"scope_project_id", pid)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ──────────────────────────────────────────────────────
//
// Reverse-proxied at /apps/auth/* by apteva-server. The deployed SaaS
// frontend hits these directly; the dashboard panels also use them.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Per-org discovery (v0.4.0).
		{Pattern: "/orgs/", Handler: a.handleOrgPublic, NoAuth: true},
		{Pattern: "/orgs/{slug}/.well-known/jwks.json", Handler: a.handleJWKS, NoAuth: true},
		{Pattern: "/orgs/{slug}/.well-known/openid-configuration", Handler: a.handleOIDCConfig, NoAuth: true},

		// Legacy discovery — resolves to the default org. Scheduled for
		// removal in v0.5.0; old SaaS code keeps working for one release
		// window so callers can update their JWT verifier configuration.
		{Pattern: "/.well-known/jwks.json", Handler: a.handleJWKS, NoAuth: true},
		{Pattern: "/.well-known/openid-configuration", Handler: a.handleOIDCConfig, NoAuth: true},

		// Public auth endpoints — tenant resolved from client_id at runtime.
		{Pattern: "/signup", Handler: a.handleSignup, NoAuth: true},
		{Pattern: "/login", Handler: a.handleLogin, NoAuth: true},
		{Method: "POST", Pattern: "/password/reset/request", Handler: a.handlePasswordResetRequest, NoAuth: true},
		{Method: "POST", Pattern: "/password/reset/confirm", Handler: a.handlePasswordResetConfirm, NoAuth: true},
		{Method: "POST", Pattern: "/email/verify", Handler: a.handleEmailVerify, NoAuth: true},
		{Method: "POST", Pattern: "/email/verification/resend", Handler: a.handleEmailVerificationResend, NoAuth: true},
		{Pattern: "/logout", Handler: a.handleLogout, NoAuth: true},
		{Pattern: "/refresh", Handler: a.handleRefresh, NoAuth: true},
		{Pattern: "/me", Handler: a.handleMe, NoAuth: true},
		{Method: "PATCH", Pattern: "/me/metadata", Handler: a.handleMeMetadata, NoAuth: true},

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
		{Method: "POST", Pattern: "/admin/users/{id}/set_password", Handler: a.handleAdminUsersSetPassword},
		{Method: "PUT", Pattern: "/admin/users/{id}/roles", Handler: a.handleAdminUserRolesSet},
		{Method: "GET", Pattern: "/admin/roles", Handler: a.handleAdminRolesList},
		{Method: "POST", Pattern: "/admin/roles", Handler: a.handleAdminRolesCreate},
		{Method: "PATCH", Pattern: "/admin/roles/{id}", Handler: a.handleAdminRolesUpdate},
		{Method: "DELETE", Pattern: "/admin/roles/{id}", Handler: a.handleAdminRolesDelete},
		{Method: "PUT", Pattern: "/admin/roles/{id}/permissions", Handler: a.handleAdminRolePermissionsSet},
		{Method: "GET", Pattern: "/admin/permissions", Handler: a.handleAdminPermissionsList},
		{Method: "POST", Pattern: "/admin/permissions", Handler: a.handleAdminPermissionsCreate},
		{Method: "PATCH", Pattern: "/admin/permissions/{id}", Handler: a.handleAdminPermissionsUpdate},
		{Method: "DELETE", Pattern: "/admin/permissions/{id}", Handler: a.handleAdminPermissionsDelete},
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
			Name:        "auth_users_create",
			Description: "Provision a user. Requires organization_id/slug + email. Optional: password (omit for passwordless/invite), display_name, email_verified (default true), send_password_reset (default false — no email is sent unless set, so bulk email imports stay silent). Returns the user + password_reset_sent. ADMIN tool — does not mint a session and does not check password policy. For visitor-facing signup that issues tokens + verification email, use auth_public_signup.",
			InputSchema: schemaObject(merge(map[string]any{
				"email":               map[string]any{"type": "string"},
				"password":            map[string]any{"type": "string"},
				"display_name":        map[string]any{"type": "string"},
				"email_verified":      map[string]any{"type": "boolean"},
				"send_password_reset": map[string]any{"type": "boolean"},
				"metadata":            map[string]any{"type": "object"},
			}), []string{"email"}),
			Handler: a.toolUsersCreate,
		},
		{
			Name:        "auth_users_update",
			Description: "Update a user. Requires organization_id/slug + user_id. Optional: display_name, email_verified, metadata (JSON object).",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":        map[string]any{"type": "integer"},
				"display_name":   map[string]any{"type": "string"},
				"email_verified": map[string]any{"type": "boolean"},
				"metadata":       map[string]any{"type": "object"},
			}), []string{"user_id"}),
			Handler: a.toolUsersUpdate,
		},
		{
			Name:        "auth_public_signup",
			Description: "Visitor-facing signup, identical to POST /signup. Resolves the org from client_id (or organization_slug when the client spans multiple orgs), validates the password against the configured policy, creates the user, issues a verification email when email_verification_required is true, otherwise mints an access + refresh token pair. Returns {user, access_token, refresh_token, expires_in, verification_required}. Use this from agent-driven onboarding (content forms, chat signup) — the auth_users_create tool is the ADMIN alternative for bulk provisioning without password policy / token issuance / verify email.",
			InputSchema: schemaObject(map[string]any{
				"email":             map[string]any{"type": "string"},
				"password":          map[string]any{"type": "string"},
				"display_name":      map[string]any{"type": "string"},
				"client_id":         map[string]any{"type": "string"},
				"organization_slug": map[string]any{"type": "string"},
				"ip":                map[string]any{"type": "string"},
				"user_agent":        map[string]any{"type": "string"},
			}, []string{"email", "password"}),
			Handler: a.toolPublicSignup,
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
			Name:        "auth_users_set_password",
			Description: "Set a new password for a user. Requires organization_id/slug. Validates the configured password policy. Args: user_id, password, revoke_sessions (default true).",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id":         map[string]any{"type": "integer"},
				"password":        map[string]any{"type": "string"},
				"revoke_sessions": map[string]any{"type": "boolean"},
			}), []string{"user_id", "password"}),
			Handler: a.toolUsersSetPassword,
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

		// ─── Roles and permissions ────────────────────────────────
		{
			Name:        "auth_roles_list",
			Description: "List organization-scoped roles and their effective permission keys. Requires organization_id/slug.",
			InputSchema: schemaObject(orgSelector, nil),
			Handler:     a.toolRolesList,
		},
		{
			Name:        "auth_roles_create",
			Description: "Create a role. Requires organization_id/slug, key, and optional name/description. The key is immutable.",
			InputSchema: schemaObject(merge(map[string]any{
				"key":         map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}), []string{"key"}),
			Handler: a.toolRolesCreate,
		},
		{
			Name:        "auth_roles_update",
			Description: "Update a role's display name or description. Requires organization_id/slug + role_id.",
			InputSchema: schemaObject(merge(map[string]any{
				"role_id":     map[string]any{"type": "integer"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}), []string{"role_id"}),
			Handler: a.toolRolesUpdate,
		},
		{
			Name:        "auth_roles_delete",
			Description: "Delete a role and remove all of its user assignments. Affected users receive a new authorization_version.",
			InputSchema: schemaObject(merge(map[string]any{
				"role_id": map[string]any{"type": "integer"},
			}), []string{"role_id"}),
			Handler: a.toolRolesDelete,
		},
		{
			Name:        "auth_permissions_list",
			Description: "List the organization's permission catalog. Requires organization_id/slug.",
			InputSchema: schemaObject(orgSelector, nil),
			Handler:     a.toolPermissionsList,
		},
		{
			Name:        "auth_permissions_create",
			Description: "Create a namespaced permission (for example resources:read). Requires organization_id/slug + key.",
			InputSchema: schemaObject(merge(map[string]any{
				"key":         map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}), []string{"key"}),
			Handler: a.toolPermissionsCreate,
		},
		{
			Name:        "auth_permissions_update",
			Description: "Update a permission's display name or description. Its machine key is immutable.",
			InputSchema: schemaObject(merge(map[string]any{
				"permission_id": map[string]any{"type": "integer"},
				"name":          map[string]any{"type": "string"},
				"description":   map[string]any{"type": "string"},
			}), []string{"permission_id"}),
			Handler: a.toolPermissionsUpdate,
		},
		{
			Name:        "auth_permissions_delete",
			Description: "Delete a permission and remove it from every role. Affected users receive a new authorization_version.",
			InputSchema: schemaObject(merge(map[string]any{
				"permission_id": map[string]any{"type": "integer"},
			}), []string{"permission_id"}),
			Handler: a.toolPermissionsDelete,
		},
		{
			Name:        "auth_role_permissions_set",
			Description: "Atomically replace a role's complete permission set. Requires role_id and permission_ids.",
			InputSchema: schemaObject(merge(map[string]any{
				"role_id": map[string]any{"type": "integer"},
				"permission_ids": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "integer"},
				},
			}), []string{"role_id", "permission_ids"}),
			Handler: a.toolRolePermissionsSet,
		},
		{
			Name:        "auth_user_roles_set",
			Description: "Atomically replace a user's complete role set and advance authorization_version when it changes.",
			InputSchema: schemaObject(merge(map[string]any{
				"user_id": map[string]any{"type": "integer"},
				"role_ids": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "integer"},
				},
			}), []string{"user_id", "role_ids"}),
			Handler: a.toolUserRolesSet,
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
			Name:        "auth_clients_update",
			Description: "Add or remove allowed browser origins on an existing OAuth client without changing its client ID or invalidating sessions.",
			InputSchema: schemaObject(map[string]any{
				"client_id":              map[string]any{"type": "string"},
				"add_allowed_origins":    map[string]any{"type": "array"},
				"remove_allowed_origins": map[string]any{"type": "array"},
			}, []string{"client_id"}),
			Handler: a.toolClientsUpdate,
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
