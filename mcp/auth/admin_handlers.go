package main

// admin_handlers.go — HTTP routes consumed by the dashboard's AuthPanel.
// All routes live under /admin/* and require the platform's bearer token
// (the SDK's withTokenAuth gates them); the dashboard proxy attaches it
// after authenticating the user's dashboard session.
//
// v0.4.0: every admin operation resolves to an organization. Reads
// accept `?organization_id=` or `?organization_slug=` and roll up
// project-wide when omitted. Mutations require an org identifier.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// ─── admin org resolution ────────────────────────────────────────────

// adminOrgInner parses ?organization_id= (or ?organization_slug=) from
// the request. require=true → 400 if missing. Returns (org, projectID,
// ok). When require=false and no param given, returns (nil, pid, true)
// meaning "roll up across all orgs in the project" — used by the
// Overview tab for project-wide stats and audit feed.
func adminOrgInner(w http.ResponseWriter, r *http.Request, require bool) (*Organization, string, bool) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return nil, "", false
	}
	ctx := getAppCtx(r)
	if slug := r.URL.Query().Get("organization_slug"); slug != "" {
		o, err := dbGetOrgBySlug(ctx.AppDB(), pid, slug)
		if err != nil {
			httpErr(w, http.StatusNotFound, "unknown organization_slug")
			return nil, "", false
		}
		return o, pid, true
	}
	if v := r.URL.Query().Get("organization_id"); v != "" {
		id, _ := strconv.ParseInt(v, 10, 64)
		if id <= 0 {
			httpErr(w, http.StatusBadRequest, "invalid organization_id")
			return nil, "", false
		}
		o, err := dbGetOrgByID(ctx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, http.StatusNotFound, "unknown organization_id")
			return nil, "", false
		}
		return o, pid, true
	}
	if require {
		httpErr(w, http.StatusBadRequest, "organization_id or organization_slug required")
		return nil, "", false
	}
	return nil, pid, true
}

func orgID(o *Organization) int64 {
	if o == nil {
		return 0
	}
	return o.ID
}

// ─── /admin/organizations CRUD ───────────────────────────────────────

func (a *App) handleAdminOrgsList(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	orgs, err := dbListOrgs(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"organizations": orgs, "count": len(orgs)})
}

func (a *App) handleAdminOrgsCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Slug  string `json:"slug"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.Slug = strings.ToLower(strings.TrimSpace(body.Slug))
	body.Name = strings.TrimSpace(body.Name)
	if body.Slug == "" || body.Name == "" {
		httpErr(w, http.StatusBadRequest, "slug and name required")
		return
	}
	if !isSlugValid(body.Slug) {
		httpErr(w, http.StatusBadRequest, "slug must be lowercase letters/digits/hyphens (3-32 chars)")
		return
	}
	if existing, _ := dbGetOrgBySlug(ctx.AppDB(), pid, body.Slug); existing != nil {
		httpErr(w, http.StatusConflict, "slug already in use")
		return
	}
	id, err := dbCreateOrg(ctx.AppDB(), pid, body.Slug, body.Name, body.Color)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Seed an initial EdDSA keypair so /signup against this org works
	// immediately — no first-call lazy seed.
	if err := ensureSigningKey(ctx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, "seed signing key: "+err.Error())
		return
	}
	org, _ := dbGetOrgByID(ctx.AppDB(), pid, id)
	dbAudit(ctx.AppDB(), pid, id, nil, "", "org_created", r.RemoteAddr, r.UserAgent(),
		map[string]any{"slug": body.Slug, "name": body.Name})
	httpStatus(w, http.StatusCreated, map[string]any{"organization": org})
}

func (a *App) handleAdminOrgsPatch(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name            *string `json:"name"`
		Color           *string `json:"color"`
		PolicyOverrides *string `json:"policy_overrides"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == nil && body.Color == nil && body.PolicyOverrides == nil {
		httpErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if err := dbUpdateOrg(ctx.AppDB(), pid, id, body.Name, body.Color, body.PolicyOverrides); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	org, err := dbGetOrgByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "org not found")
		return
	}
	dbAudit(ctx.AppDB(), pid, id, nil, "", "org_updated", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"organization": org})
}

func (a *App) handleAdminOrgsArchive(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	org, err := dbGetOrgByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusNotFound, "org not found")
		return
	}
	if org.Slug == "default" {
		httpErr(w, http.StatusBadRequest, "cannot archive the default organization")
		return
	}
	if err := dbArchiveOrg(ctx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, id, nil, "", "org_archived", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"ok": true})
}

// ─── /admin/stats ────────────────────────────────────────────────────

func (a *App) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, false)
	if !ok {
		return
	}
	stats, err := dbStats(getAppCtx(r).AppDB(), pid, orgID(org))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, stats)
}

// ─── /admin/users ────────────────────────────────────────────────────

func (a *App) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	createdAfter := r.URL.Query().Get("created_after")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100, 1, 500)

	users, err := dbSearchUsers(getAppCtx(r).AppDB(), pid, orgID(org), q, status, createdAfter, limit)
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

func (a *App) handleAdminUsersCreate(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	var body struct {
		Email             string `json:"email"`
		Password          string `json:"password"`
		DisplayName       string `json:"display_name"`
		EmailVerified     *bool  `json:"email_verified"`
		SendPasswordReset bool   `json:"send_password_reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" {
		httpErr(w, http.StatusBadRequest, "email required")
		return
	}
	if existing, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, body.Email); err == nil && existing != nil {
		httpErr(w, http.StatusConflict, "email already registered in this organization")
		return
	}
	verified := true
	if body.EmailVerified != nil {
		verified = *body.EmailVerified
	}
	var pwHash string
	if body.Password != "" {
		if reason := validatePassword(body.Password,
			cfgInt(ctx, "password_min_length", 12),
			cfgInt(ctx, "password_classes_required", 2)); reason != "" {
			httpErr(w, http.StatusBadRequest, reason)
			return
		}
		h, hashErr := hashPassword(body.Password)
		if hashErr != nil {
			httpErr(w, http.StatusInternalServerError, hashErr.Error())
			return
		}
		pwHash = h
	}
	uid, err := dbCreateUser(ctx.AppDB(), pid, org.ID, body.Email, pwHash, body.DisplayName, verified)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_created_admin", r.RemoteAddr, r.UserAgent(),
		map[string]any{"email": body.Email, "via": "admin_panel"})

	out := map[string]any{"user": user}
	if pwHash == "" || body.SendPasswordReset {
		if err := issueResetToken(ctx, pid, org, uid, body.Email); err != nil {
			ctx.Logger().Warn("password-reset issue failed", "err", err, "user_id", uid)
		} else {
			out["password_reset_sent"] = true
		}
	}
	httpStatus(w, http.StatusCreated, out)
}

func (a *App) handleAdminUsersPatch(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		DisplayName   *string `json:"display_name"`
		EmailVerified *bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.DisplayName == nil && body.EmailVerified == nil {
		httpErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if err := dbUpdateUserProfile(ctx.AppDB(), pid, org.ID, uid, body.DisplayName, body.EmailVerified); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusNotFound, "user not found")
		return
	}
	changes := map[string]any{}
	if body.DisplayName != nil {
		changes["display_name"] = *body.DisplayName
	}
	if body.EmailVerified != nil {
		changes["email_verified"] = *body.EmailVerified
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_updated_admin", r.RemoteAddr, r.UserAgent(), changes)
	httpJSON(w, map[string]any{"user": user})
}

func (a *App) handleAdminUsersSendPasswordReset(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusNotFound, "user not found")
		return
	}
	if err := issueResetToken(ctx, pid, org, uid, user.Email); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleAdminUsersGetContext(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusNotFound, "user not found")
		return
	}
	sessions, err := dbListUserSessions(ctx.AppDB(), pid, org.ID, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	audits, err := dbAuditSearch(ctx.AppDB(), pid, org.ID, user.ID, "", "", "", 50)
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
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := dbSetUserStatus(ctx.AppDB(), pid, org.ID, uid, "disabled"); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	revoked, _ := dbRevokeAllUserSessions(ctx.AppDB(), pid, org.ID, uid)
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_disabled", r.RemoteAddr, r.UserAgent(),
		map[string]any{"reason": body.Reason, "revoked_sessions": revoked})
	httpJSON(w, map[string]any{"ok": true, "revoked_sessions": revoked})
}

func (a *App) handleAdminUsersEnable(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, org.ID, uid, "active"); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_enabled", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleAdminUsersRevokeSessions(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "session_revoked", r.RemoteAddr, r.UserAgent(),
		map[string]any{"reason": "admin_revoke_all", "count": n})
	httpJSON(w, map[string]any{"revoked_count": n})
}

// ─── /admin/clients ──────────────────────────────────────────────────

func (a *App) handleAdminClientsList(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, false)
	if !ok {
		return
	}
	includeDisabled := r.URL.Query().Get("include_disabled") == "true"
	clients, err := dbListClients(getAppCtx(r).AppDB(), pid, orgID(org), includeDisabled)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"clients": clients, "count": len(clients)})
}

// handleAdminClientsCreate — accepts an optional organization_id /
// organization_slug. When omitted, the client is created as
// **multi-organization**: usable by every org in the project, with the
// SaaS sending organization_slug on every public call. Use this when
// one SaaS frontend deployment serves many customer orgs (Auth0
// "Organizations" / Stytch B2B pattern). When set, the client is bound
// to that single org (v0.4.0 default behaviour).
func (a *App) handleAdminClientsCreate(w http.ResponseWriter, r *http.Request) {
	// adminOrgInner with require=false: missing org param is fine — we
	// interpret it as "create a multi-org client".
	org, pid, ok := adminOrgInner(w, r, false)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
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
	if _, err := dbCreateClient(ctx.AppDB(), pid, orgID(org), c, secretHash); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	auditMeta := map[string]any{"name": body.Name, "type": body.Type}
	if org == nil {
		auditMeta["scope"] = "multi_organization"
	}
	dbAudit(ctx.AppDB(), pid, orgID(org), nil, c.ClientID, "client_created", r.RemoteAddr, r.UserAgent(), auditMeta)
	out := map[string]any{"client": c, "client_id": c.ClientID, "multi_organization": org == nil}
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
	// Look up the client first so the audit row carries the right org.
	c, err := dbGetClientByClientID(ctx.AppDB(), pid, clientID)
	if err != nil {
		httpErr(w, http.StatusNotFound, "client not found")
		return
	}
	secret := mustSlug(32)
	if err := dbUpdateClientSecret(ctx.AppDB(), pid, clientID, hashToken(secret)); err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, c.OrganizationID, nil, clientID, "client_secret_rotated", r.RemoteAddr, r.UserAgent(), nil)
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
	c, err := dbGetClientByClientID(ctx.AppDB(), pid, clientID)
	if err != nil {
		httpErr(w, http.StatusNotFound, "client not found")
		return
	}
	if err := dbDisableClient(ctx.AppDB(), pid, clientID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, c.OrganizationID, nil, clientID, "client_disabled", r.RemoteAddr, r.UserAgent(), nil)
	httpJSON(w, map[string]any{"ok": true})
}

// ─── /admin/audit ────────────────────────────────────────────────────

func (a *App) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, false)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	var uid int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		uid, _ = strconv.ParseInt(v, 10, 64)
	}
	event := r.URL.Query().Get("event")
	since := r.URL.Query().Get("since")
	until := r.URL.Query().Get("until")
	limit := parseIntDefault(r.URL.Query().Get("limit"), 100, 1, 500)
	events, err := dbAuditSearch(ctx.AppDB(), pid, orgID(org), uid, event, since, until, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"events": events, "count": len(events)})
}

// ─── /admin/oidc — per-org discovery snapshot ────────────────────────

func (a *App) handleAdminOIDC(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	ctx := getAppCtx(r)
	base := orgBaseURL(ctx, r, org)
	type keyInfo struct {
		Kid       string `json:"kid"`
		Alg       string `json:"alg"`
		CreatedAt string `json:"created_at"`
		RetiredAt string `json:"retired_at,omitempty"`
	}
	rows, err := ctx.AppDB().Query(
		`SELECT kid, alg, IFNULL(created_at,''), IFNULL(retired_at,'')
		   FROM signing_keys WHERE project_id = ? AND organization_id = ?
		   ORDER BY created_at DESC`, pid, org.ID)
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
		"organization":            org,
		"issuer":                  base,
		"jwks_uri":                base + "/.well-known/jwks.json",
		"openid_configuration":    base + "/.well-known/openid-configuration",
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

func isSlugValid(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for i, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return false
		}
		if (c == '-') && (i == 0 || i == len(s)-1) {
			return false
		}
	}
	return true
}
