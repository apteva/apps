package main

// admin_handlers.go — HTTP routes consumed by the dashboard's AuthPanel.
// All routes live under /admin/* and require the platform's bearer token
// (the SDK's withTokenAuth gates them); the dashboard proxy attaches it
// after authenticating the user's dashboard session. These intentionally
// mirror the MCP tools in tools.go: same project resolution, same audit
// trail, same DB calls — different transport.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// ─── /admin/stats ────────────────────────────────────────────────────

func (a *App) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := dbStats(ctx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, stats)
}

// ─── /admin/users ────────────────────────────────────────────────────

func (a *App) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	createdAfter := r.URL.Query().Get("created_after")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100, 1, 500)

	users, err := dbSearchUsers(ctx.AppDB(), pid, q, status, createdAfter, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if mfaRaw := r.URL.Query().Get("mfa"); mfaRaw != "" {
		want := mfaRaw == "true" || mfaRaw == "1"
		filtered := users[:0]
		for _, u := range users {
			if u.MFAEnabled == want {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}
	httpJSON(w, map[string]any{"users": users, "count": len(users)})
}

func (a *App) handleAdminUsersGetContext(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, uid)
	if err != nil {
		httpErr(w, http.StatusNotFound, "user not found")
		return
	}
	sessions, err := dbListUserSessions(ctx.AppDB(), pid, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	audits, err := dbAuditSearch(ctx.AppDB(), pid, user.ID, "", "", "", 50)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{
		"user":      user,
		"sessions":  sessions,
		"audit_log": audits,
	})
}

func (a *App) handleAdminUsersDisable(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := dbSetUserStatus(ctx.AppDB(), pid, uid, "disabled"); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	revoked, _ := dbRevokeAllUserSessions(ctx.AppDB(), pid, uid)
	dbAudit(ctx.AppDB(), pid, &uid, "", "user_disabled", r.RemoteAddr, r.UserAgent(),
		map[string]any{"reason": body.Reason, "revoked_sessions": revoked})
	httpJSON(w, map[string]any{"ok": true, "revoked_sessions": revoked})
}

func (a *App) handleAdminUsersEnable(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, uid, "active"); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, &uid, "", "user_enabled", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleAdminUsersRevokeSessions(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, &uid, "", "session_revoked", r.RemoteAddr, r.UserAgent(),
		map[string]any{"reason": "admin_revoke_all", "count": n})
	httpJSON(w, map[string]any{"revoked_count": n})
}

// ─── /admin/clients ──────────────────────────────────────────────────

func (a *App) handleAdminClientsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeDisabled := r.URL.Query().Get("include_disabled") == "true"
	clients, err := dbListClients(ctx.AppDB(), pid, includeDisabled)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"clients": clients, "count": len(clients)})
}

func (a *App) handleAdminClientsCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name              string   `json:"name"`
		Type              string   `json:"type"`
		RedirectURIs      []string `json:"redirect_uris"`
		AllowedOrigins    []string `json:"allowed_origins"`
		AllowedGrantTypes []string `json:"allowed_grant_types"`
		RequireMFA        bool     `json:"require_mfa"`
		JWTAudience       string   `json:"jwt_audience"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == "" || body.Type == "" {
		httpErr(w, http.StatusBadRequest, "name and type required")
		return
	}
	switch body.Type {
	case "spa", "web", "native", "m2m":
	default:
		httpErr(w, http.StatusBadRequest, "unknown type (spa | web | native | m2m)")
		return
	}
	grants := body.AllowedGrantTypes
	if len(grants) == 0 {
		grants = defaultedGrants(body.Type, nil)
	}
	c := Client{
		ClientID:                "akc_" + mustSlug(16),
		Name:                    body.Name,
		Type:                    body.Type,
		RedirectURIs:            body.RedirectURIs,
		AllowedOrigins:          body.AllowedOrigins,
		AllowedGrantTypes:       grants,
		TokenEndpointAuthMethod: defaultAuthMethod(body.Type),
		RequirePKCE:             body.Type == "spa" || body.Type == "native",
		RequireMFA:              body.RequireMFA,
		JWTAudience:             body.JWTAudience,
		RefreshRotation:         true,
	}
	var secret, secretHash string
	if body.Type == "web" || body.Type == "m2m" {
		secret = mustSlug(32)
		secretHash = hashToken(secret)
		c.TokenEndpointAuthMethod = "client_secret_post"
	}
	if _, err := dbCreateClient(ctx.AppDB(), pid, c, secretHash); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, nil, c.ClientID, "client_created", r.RemoteAddr, r.UserAgent(),
		map[string]any{"name": body.Name, "type": body.Type})
	out := map[string]any{"client": c, "client_id": c.ClientID}
	if secret != "" {
		out["client_secret"] = secret
		out["note"] = "store this secret — it will not be shown again"
	}
	httpStatus(w, http.StatusCreated, out)
}

func (a *App) handleAdminClientsRotate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID := r.PathValue("client_id")
	if clientID == "" {
		httpErr(w, http.StatusBadRequest, "client_id required")
		return
	}
	secret := mustSlug(32)
	if err := dbUpdateClientSecret(ctx.AppDB(), pid, clientID, hashToken(secret)); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, nil, clientID, "client_secret_rotated", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{
		"client_id":     clientID,
		"client_secret": secret,
		"note":          "store this secret — it will not be shown again",
	})
}

func (a *App) handleAdminClientsDisable(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID := r.PathValue("client_id")
	if clientID == "" {
		httpErr(w, http.StatusBadRequest, "client_id required")
		return
	}
	if err := dbDisableClient(ctx.AppDB(), pid, clientID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, nil, clientID, "client_disabled", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"ok": true})
}

// ─── /admin/audit ────────────────────────────────────────────────────

func (a *App) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var uid int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		uid, _ = strconv.ParseInt(v, 10, 64)
	}
	event := r.URL.Query().Get("event")
	since := r.URL.Query().Get("since")
	until := r.URL.Query().Get("until")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100, 1, 500)
	events, err := dbAuditSearch(ctx.AppDB(), pid, uid, event, since, until, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"events": events, "count": len(events)})
}

// ─── /admin/oidc ─────────────────────────────────────────────────────
//
// Surfaces the public discovery URLs and signing-key metadata the
// Endpoints tab renders. Convenience over hitting jwks + oidc-config
// separately, and adds key ages the public discovery doc doesn't expose.

func (a *App) handleAdminOIDC(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	base := publicBaseURL(ctx, r)
	type keyInfo struct {
		Kid       string `json:"kid"`
		Alg       string `json:"alg"`
		CreatedAt string `json:"created_at"`
		RetiredAt string `json:"retired_at,omitempty"`
	}
	rows, err := ctx.AppDB().Query(
		`SELECT kid, alg, IFNULL(created_at,''), IFNULL(retired_at,'')
		   FROM signing_keys WHERE project_id = ?
		   ORDER BY created_at DESC`, pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var keys []keyInfo
	for rows.Next() {
		var k keyInfo
		if err := rows.Scan(&k.Kid, &k.Alg, &k.CreatedAt, &k.RetiredAt); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		keys = append(keys, k)
	}
	httpJSON(w, map[string]any{
		"issuer":                  base,
		"jwks_uri":                base + "/.well-known/jwks.json",
		"openid_configuration":    base + "/.well-known/openid-configuration",
		"authorization_endpoint":  base + "/oauth/authorize",
		"token_endpoint":          base + "/oauth/token",
		"userinfo_endpoint":       base + "/me",
		"signing_keys":            keys,
		"app_url_configured":      strings.TrimSpace(cfgStr(ctx, "app_url", "")) != "",
		"verification_required":   cfgBool(ctx, "email_verification_required", true),
		"magic_link_enabled":      cfgBool(ctx, "magic_link_enabled", true),
		"access_ttl_seconds":      cfgInt(ctx, "jwt_access_ttl_seconds", 900),
		"refresh_ttl_days":        cfgInt(ctx, "jwt_refresh_ttl_days", 30),
		"password_min_length":     cfgInt(ctx, "password_min_length", 12),
		"password_classes":        cfgInt(ctx, "password_classes_required", 2),
		"lockout_threshold":       cfgInt(ctx, "lockout_threshold", 5),
		"lockout_initial_minutes": cfgInt(ctx, "lockout_initial_minutes", 15),
	})
}

// ─── helpers ─────────────────────────────────────────────────────────

func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, errors.New(name + " required")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid " + name)
	}
	return n, nil
}

func parseIntDefault(s string, dflt, min, max int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return dflt
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
