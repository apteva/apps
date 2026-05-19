package main

// tools.go — MCP tool handlers. Agents call these; the dashboard panels
// call the HTTP routes in admin_handlers.go. Both end up at the same
// db layer.
//
// v0.4.0: every user-facing tool takes organization_id or
// organization_slug. Reads roll up project-wide when omitted; mutations
// error. New auth_orgs_* tools manage the partition itself.

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// ─── auth_orgs_* ─────────────────────────────────────────────────────

func (a *App) toolOrgsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	orgs, err := dbListOrgs(ctx.AppDB(), pid, includeArchived)
	if err != nil {
		return nil, err
	}
	return map[string]any{"organizations": orgs, "count": len(orgs)}, nil
}

func (a *App) toolOrgsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	slug, _ := args["slug"].(string)
	name, _ := args["name"].(string)
	color, _ := args["color"].(string)
	if slug == "" || name == "" {
		return nil, errors.New("slug and name required")
	}
	if !isSlugValid(slug) {
		return nil, errors.New("slug must be lowercase letters/digits/hyphens (3-32 chars)")
	}
	if existing, _ := dbGetOrgBySlug(ctx.AppDB(), pid, slug); existing != nil {
		return nil, errors.New("slug already in use")
	}
	id, err := dbCreateOrg(ctx.AppDB(), pid, slug, name, color)
	if err != nil {
		return nil, err
	}
	if err := ensureSigningKey(ctx.AppDB(), pid, id); err != nil {
		return nil, fmt.Errorf("seed signing key: %w", err)
	}
	org, _ := dbGetOrgByID(ctx.AppDB(), pid, id)
	dbAudit(ctx.AppDB(), pid, id, nil, "", "org_created", "", "agent",
		map[string]any{"slug": slug, "name": name})
	return map[string]any{"organization": org}, nil
}

func (a *App) toolOrgsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	var name, color, policy *string
	if v, ok := args["name"].(string); ok && v != "" {
		name = &v
	}
	if v, ok := args["color"].(string); ok {
		color = &v
	}
	if v, ok := args["policy_overrides"].(string); ok {
		policy = &v
	}
	if err := dbUpdateOrg(ctx.AppDB(), pid, org.ID, name, color, policy); err != nil {
		return nil, err
	}
	updated, _ := dbGetOrgByID(ctx.AppDB(), pid, org.ID)
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "org_updated", "", "agent", nil)
	return map[string]any{"organization": updated}, nil
}

func (a *App) toolOrgsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if org.Slug == "default" {
		return nil, errors.New("cannot archive the default organization")
	}
	if err := dbArchiveOrg(ctx.AppDB(), pid, org.ID); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "org_archived", "", "agent", nil)
	return map[string]any{"ok": true}, nil
}

// ─── auth_users_search / get / get_context ───────────────────────────

func (a *App) toolUsersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	orgID := orgIDFromArgsOptional(ctx, pid, args)
	q, _ := args["q"].(string)
	status, _ := args["status"].(string)
	createdAfter, _ := args["created_after"].(string)
	limit := intArg(args, "limit", 50, 1, 200)

	users, err := dbSearchUsers(ctx.AppDB(), pid, orgID, q, status, createdAfter, limit)
	if err != nil {
		return nil, err
	}
	if mfaFilter, ok := args["mfa"].(bool); ok {
		filtered := users[:0]
		for _, u := range users {
			if u.MFAEnabled == mfaFilter {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}
	return map[string]any{"users": users, "count": len(users)}, nil
}

func (a *App) toolUsersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	user, err := lookupUserByArgs(ctx, pid, org.ID, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": user}, nil
}

func (a *App) toolUsersGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	user, err := lookupUserByArgs(ctx, pid, org.ID, args)
	if err != nil {
		return nil, err
	}
	sessions, err := dbListUserSessions(ctx.AppDB(), pid, org.ID, user.ID)
	if err != nil {
		return nil, err
	}
	audits, err := dbAuditSearch(ctx.AppDB(), pid, org.ID, user.ID, "", "", "", 20)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"user":      user,
		"sessions":  sessions,
		"audit_log": audits,
	}, nil
}

// ─── auth_users_revoke_sessions / disable / enable ───────────────────

func (a *App) toolUsersRevokeSessions(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	n, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "session_revoked", "", "agent",
		map[string]any{"reason": "admin_revoke_all", "count": n})
	return map[string]any{"revoked_count": n}, nil
}

func (a *App) toolUsersDisable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, org.ID, uid, "disabled"); err != nil {
		return nil, err
	}
	if _, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, org.ID, uid); err != nil {
		return nil, err
	}
	reason, _ := args["reason"].(string)
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_disabled", "", "agent",
		map[string]any{"reason": reason})
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersEnable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, org.ID, uid, "active"); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_enabled", "", "agent", nil)
	return map[string]any{"ok": true}, nil
}

// ─── auth_audit_search / stats ────────────────────────────────────────

func (a *App) toolAuditSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	orgID := orgIDFromArgsOptional(ctx, pid, args)
	uid, _ := intReq(args, "user_id")
	event, _ := args["event"].(string)
	since, _ := args["since"].(string)
	until, _ := args["until"].(string)
	limit := intArg(args, "limit", 100, 1, 500)
	events, err := dbAuditSearch(ctx.AppDB(), pid, orgID, uid, event, since, until, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"events": events, "count": len(events)}, nil
}

func (a *App) toolStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	orgID := orgIDFromArgsOptional(ctx, pid, args)
	stats, err := dbStats(ctx.AppDB(), pid, orgID)
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// ─── auth_clients_list / create / rotate_secret / disable ────────────

func (a *App) toolClientsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	orgID := orgIDFromArgsOptional(ctx, pid, args)
	includeDisabled, _ := args["include_disabled"].(bool)
	cs, err := dbListClients(ctx.AppDB(), pid, orgID, includeDisabled)
	if err != nil {
		return nil, err
	}
	return map[string]any{"clients": cs, "count": len(cs)}, nil
}

func (a *App) toolClientsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	name, _ := args["name"].(string)
	typ, _ := args["type"].(string)
	if name == "" || typ == "" {
		return nil, errors.New("name and type required")
	}
	switch typ {
	case "spa", "web", "native", "m2m":
	default:
		return nil, fmt.Errorf("unknown type %q (spa | web | native | m2m)", typ)
	}
	c := Client{
		ClientID:                "akc_" + mustSlug(16),
		Name:                    name,
		Type:                    typ,
		RedirectURIs:            stringArrArg(args, "redirect_uris"),
		AllowedOrigins:          stringArrArg(args, "allowed_origins"),
		AllowedGrantTypes:       defaultedGrants(typ, stringArrArg(args, "allowed_grant_types")),
		TokenEndpointAuthMethod: defaultAuthMethod(typ),
		RequirePKCE:             typ == "spa" || typ == "native",
		RequireMFA:              boolArg(args, "require_mfa", false),
		JWTAudience:             stringArg(args, "jwt_audience", ""),
		RefreshRotation:         true,
	}
	var secret string
	var secretHash string
	if typ == "web" || typ == "m2m" {
		secret = mustSlug(32)
		secretHash = hashToken(secret)
		c.TokenEndpointAuthMethod = "client_secret_post"
	}
	if _, err := dbCreateClient(ctx.AppDB(), pid, org.ID, c, secretHash); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, c.ClientID, "client_created", "", "agent",
		map[string]any{"name": name, "type": typ})
	out := map[string]any{
		"client":    c,
		"client_id": c.ClientID,
	}
	if secret != "" {
		out["client_secret"] = secret
		out["note"] = "store this secret — it will not be shown again"
	}
	return out, nil
}

func (a *App) toolClientsRotateSecret(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	clientID, _ := args["client_id"].(string)
	if clientID == "" {
		return nil, errors.New("client_id required")
	}
	// client_id is globally unique — we look it up first to capture the
	// org for the audit row.
	c, err := dbGetClientByClientID(ctx.AppDB(), pid, clientID)
	if err != nil {
		return nil, errors.New("unknown client_id")
	}
	secret := mustSlug(32)
	if err := dbUpdateClientSecret(ctx.AppDB(), pid, clientID, hashToken(secret)); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, c.OrganizationID, nil, clientID, "client_secret_rotated", "", "agent", nil)
	return map[string]any{
		"client_id":     clientID,
		"client_secret": secret,
		"note":          "store this secret — it will not be shown again",
	}, nil
}

func (a *App) toolClientsDisable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	clientID, _ := args["client_id"].(string)
	if clientID == "" {
		return nil, errors.New("client_id required")
	}
	c, err := dbGetClientByClientID(ctx.AppDB(), pid, clientID)
	if err != nil {
		return nil, errors.New("unknown client_id")
	}
	if err := dbDisableClient(ctx.AppDB(), pid, clientID); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, c.OrganizationID, nil, clientID, "client_disabled", "", "agent", nil)
	return map[string]any{"ok": true}, nil
}

// ─── arg coercion helpers ────────────────────────────────────────────

func lookupUserByArgs(ctx *sdk.AppCtx, projectID string, orgID int64, args map[string]any) (*User, error) {
	if id, ok := intReq(args, "id"); ok {
		return dbGetUserByID(ctx.AppDB(), projectID, orgID, id)
	}
	if email, _ := args["email"].(string); email != "" {
		return dbGetUserByEmail(ctx.AppDB(), projectID, orgID, email)
	}
	return nil, errors.New("id or email required")
}

// orgFromArgs — required-org resolver. Accepts organization_id or
// organization_slug. Returns an error if neither was supplied — used
// by every mutation tool.
func orgFromArgs(ctx *sdk.AppCtx, projectID string, args map[string]any) (*Organization, error) {
	if slug, _ := args["organization_slug"].(string); slug != "" {
		o, err := dbGetOrgBySlug(ctx.AppDB(), projectID, slug)
		if err != nil {
			return nil, errors.New("unknown organization_slug")
		}
		return o, nil
	}
	if id, ok := intReq(args, "organization_id"); ok {
		o, err := dbGetOrgByID(ctx.AppDB(), projectID, id)
		if err != nil {
			return nil, errors.New("unknown organization_id")
		}
		return o, nil
	}
	return nil, errors.New("organization_id or organization_slug required")
}

// orgIDFromArgsOptional — optional-org resolver. Returns 0 (= roll up
// project-wide) when neither id nor slug is supplied. Used by read
// tools where omitting org means "across the whole project".
func orgIDFromArgsOptional(ctx *sdk.AppCtx, projectID string, args map[string]any) int64 {
	if slug, _ := args["organization_slug"].(string); slug != "" {
		if o, err := dbGetOrgBySlug(ctx.AppDB(), projectID, slug); err == nil {
			return o.ID
		}
	}
	if id, ok := intReq(args, "organization_id"); ok {
		return id
	}
	return 0
}

func intArg(args map[string]any, key string, dflt, min, max int) int {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case float64:
			n := int(x)
			if n < min {
				return min
			}
			if n > max {
				return max
			}
			return n
		case int:
			if x < min {
				return min
			}
			if x > max {
				return max
			}
			return x
		}
	}
	return dflt
}

func intReq(args map[string]any, key string) (int64, bool) {
	switch x := args[key].(type) {
	case float64:
		if x <= 0 {
			return 0, false
		}
		return int64(x), true
	case int:
		if x <= 0 {
			return 0, false
		}
		return int64(x), true
	case int64:
		if x <= 0 {
			return 0, false
		}
		return x, true
	}
	return 0, false
}

func boolArg(args map[string]any, key string, dflt bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return dflt
}

func stringArg(args map[string]any, key, dflt string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return dflt
}

func stringArrArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func defaultedGrants(typ string, requested []string) []string {
	if len(requested) > 0 {
		return requested
	}
	switch typ {
	case "spa", "web", "native":
		return []string{"authorization_code", "refresh_token"}
	case "m2m":
		return []string{"client_credentials"}
	}
	return []string{"authorization_code", "refresh_token"}
}

func defaultAuthMethod(typ string) string {
	switch typ {
	case "spa", "native":
		return "none"
	case "web", "m2m":
		return "client_secret_post"
	}
	return "none"
}

func mustSlug(n int) string {
	s, err := randSlug(n)
	if err != nil {
		panic("randSlug: " + err.Error())
	}
	return s
}
