package main

// tools.go — MCP tool handlers. Agents call these; the dashboard panels
// call the HTTP routes in handlers.go. Both end up at the same db
// layer through resolveProjectFromArgs / resolveProjectFromRequest.
//
// Convention: each tool returns a JSON-encodable map (or struct). The
// SDK marshals it. Errors are returned as the second value and become
// MCP tool errors.

import (
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// ─── auth_users_search / get / get_context ───────────────────────────

func (a *App) toolUsersSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, _ := args["q"].(string)
	status, _ := args["status"].(string)
	createdAfter, _ := args["created_after"].(string)
	limit := intArg(args, "limit", 50, 1, 200)

	users, err := dbSearchUsers(ctx.AppDB(), pid, q, status, createdAfter, limit)
	if err != nil {
		return nil, err
	}
	if mfaFilter, ok := args["mfa"].(bool); ok {
		// In-memory filter — the DB query already paged us down to
		// `limit`, so anything beyond that needs a smarter query.
		// v0.1 keeps it simple; revisit when limit×mfa becomes a
		// performance issue.
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
	user, err := lookupUserByArgs(ctx, pid, args)
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
	user, err := lookupUserByArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	sessions, err := dbListUserSessions(ctx.AppDB(), pid, user.ID)
	if err != nil {
		return nil, err
	}
	audits, err := dbAuditSearch(ctx.AppDB(), pid, user.ID, "", "", "", 20)
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
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	n, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, uid)
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, &uid, "", "session_revoked", "", "agent",
		map[string]any{"reason": "admin_revoke_all", "count": n})
	return map[string]any{"revoked_count": n}, nil
}

func (a *App) toolUsersDisable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, uid, "disabled"); err != nil {
		return nil, err
	}
	if _, err := dbRevokeAllUserSessions(ctx.AppDB(), pid, uid); err != nil {
		return nil, err
	}
	reason, _ := args["reason"].(string)
	dbAudit(ctx.AppDB(), pid, &uid, "", "user_disabled", "", "agent",
		map[string]any{"reason": reason})
	return map[string]any{"ok": true}, nil
}

func (a *App) toolUsersEnable(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	if err := dbSetUserStatus(ctx.AppDB(), pid, uid, "active"); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, &uid, "", "user_enabled", "", "agent", nil)
	return map[string]any{"ok": true}, nil
}

// ─── auth_audit_search / stats ────────────────────────────────────────

func (a *App) toolAuditSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	uid, _ := intReq(args, "user_id")
	event, _ := args["event"].(string)
	since, _ := args["since"].(string)
	until, _ := args["until"].(string)
	limit := intArg(args, "limit", 100, 1, 500)
	events, err := dbAuditSearch(ctx.AppDB(), pid, uid, event, since, until, limit)
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
	stats, err := dbStats(ctx.AppDB(), pid)
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
	includeDisabled, _ := args["include_disabled"].(bool)
	cs, err := dbListClients(ctx.AppDB(), pid, includeDisabled)
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
		ClientID:                 "akc_" + mustSlug(16),
		Name:                     name,
		Type:                     typ,
		RedirectURIs:             stringArrArg(args, "redirect_uris"),
		AllowedOrigins:           stringArrArg(args, "allowed_origins"),
		AllowedGrantTypes:        defaultedGrants(typ, stringArrArg(args, "allowed_grant_types")),
		TokenEndpointAuthMethod:  defaultAuthMethod(typ),
		RequirePKCE:              typ == "spa" || typ == "native",
		RequireMFA:               boolArg(args, "require_mfa", false),
		JWTAudience:              stringArg(args, "jwt_audience", ""),
		RefreshRotation:          true,
	}
	// Confidential clients (web, m2m) get a one-time secret. Public
	// clients (spa, native) cannot keep a secret and don't get one.
	var secret string
	var secretHash string
	if typ == "web" || typ == "m2m" {
		secret = mustSlug(32)
		secretHash = hashToken(secret)
		c.TokenEndpointAuthMethod = "client_secret_post"
	}
	if _, err := dbCreateClient(ctx.AppDB(), pid, c, secretHash); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, nil, c.ClientID, "client_created", "", "agent",
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
	secret := mustSlug(32)
	if err := dbUpdateClientSecret(ctx.AppDB(), pid, clientID, hashToken(secret)); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, nil, clientID, "client_secret_rotated", "", "agent", nil)
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
	if err := dbDisableClient(ctx.AppDB(), pid, clientID); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, nil, clientID, "client_disabled", "", "agent", nil)
	return map[string]any{"ok": true}, nil
}

// ─── arg coercion helpers ────────────────────────────────────────────

func lookupUserByArgs(ctx *sdk.AppCtx, projectID string, args map[string]any) (*User, error) {
	if id, ok := intReq(args, "id"); ok {
		return dbGetUserByID(ctx.AppDB(), projectID, id)
	}
	if email, _ := args["email"].(string); email != "" {
		return dbGetUserByEmail(ctx.AppDB(), projectID, email)
	}
	return nil, errors.New("id or email required")
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

// mustSlug — the random helpers in crypto.go return errors only when
// crypto/rand fails (essentially never on a working OS). At call sites
// where we'd just panic on that anyway, mustSlug is the convenience.
func mustSlug(n int) string {
	s, err := randSlug(n)
	if err != nil {
		panic("randSlug: " + err.Error())
	}
	return s
}
