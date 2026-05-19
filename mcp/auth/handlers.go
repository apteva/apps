package main

// handlers.go — HTTP handlers for the auth surface the deployed SaaS
// frontend hits. Reverse-proxied at /apps/auth/* by apteva-server.
//
// In v0.4.0 every request resolves to an Organization (the row-level
// partition above users):
//
//   /signup, /login, /refresh, /logout — org comes from the client row
//   /me                                — org comes from the JWT's iss
//   /orgs/{slug}/.well-known/*         — org comes from the URL path
//   /.well-known/*                     — legacy alias, resolves to the
//                                        default org (one release window;
//                                        scheduled removal in v0.5.0)
//
// Handlers are deliberately self-contained — no shared middleware
// stack — so tests can hit them with httptest.NewRequest directly.

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// peekJWTOrg pulls the `org` claim out of a JWT's payload WITHOUT
// verifying the signature — we only need it to pick which org's keys
// to load for the actual verify step. The signature check still
// happens. Returns "" if the token has no `org` claim (legacy v0.3.x
// token from the default org).
func peekJWTOrg(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse payload: %w", err)
	}
	s, _ := m["org"].(string)
	return s, nil
}

// ─── /orgs/{slug}/.well-known/jwks.json (+ legacy /.well-known/) ─────
//
// Public — no auth. Per-org JWKS so a leaked private key in one org
// can't validate tokens for another.

func (a *App) handleJWKS(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	org, err := orgFromRequest(ctx, r, pid)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jwks := struct {
		Keys []jwk `json:"keys"`
	}{}
	for kid, pub := range keys {
		jwks.Keys = append(jwks.Keys, jwkFromEd25519(kid, pub))
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpJSON(w, jwks)
}

// ─── /orgs/{slug}/.well-known/openid-configuration (+ legacy) ────────

func (a *App) handleOIDCConfig(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	org, err := orgFromRequest(ctx, r, pid)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	base := orgBaseURL(ctx, r, org)
	platformBase := platformBaseURL(ctx, r)
	resp := map[string]any{
		"issuer":                                base,
		"jwks_uri":                              base + "/.well-known/jwks.json",
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        platformBase + "/oauth/token",
		"userinfo_endpoint":                     platformBase + "/me",
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "password", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
	}
	httpJSON(w, resp)
}

// ─── /signup ─────────────────────────────────────────────────────────

func (a *App) handleSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Email            string `json:"email"`
		Password         string `json:"password"`
		DisplayName      string `json:"display_name"`
		ClientID         string `json:"client_id"`
		OrganizationSlug string `json:"organization_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || body.Password == "" {
		httpErr(w, http.StatusBadRequest, "email and password required")
		return
	}
	client, clientErr := requireClient(ctx, pid, body.ClientID)
	if clientErr != nil {
		httpErr(w, http.StatusBadRequest, clientErr.Error())
		return
	}
	org, orgErr := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if orgErr != nil {
		httpErr(w, http.StatusBadRequest, orgErr.Error())
		return
	}
	if reason := validatePassword(body.Password,
		cfgInt(ctx, "password_min_length", 12),
		cfgInt(ctx, "password_classes_required", 2)); reason != "" {
		httpErr(w, http.StatusBadRequest, reason)
		return
	}

	if existing, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, body.Email); err == nil && existing != nil {
		dbAudit(ctx.AppDB(), pid, org.ID, &existing.ID, client.ClientID, "signup_conflict",
			r.RemoteAddr, r.UserAgent(), nil)
		httpErr(w, http.StatusConflict, "email already registered")
		return
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	pwHash, err := hashPassword(body.Password)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	verificationRequired := cfgBool(ctx, "email_verification_required", true)
	uid, err := dbCreateUser(ctx.AppDB(), pid, org.ID, body.Email, pwHash, body.DisplayName, !verificationRequired)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &uid, client.ClientID, "signup", r.RemoteAddr, r.UserAgent(), nil)

	if verificationRequired {
		if err := issueVerifyEmailToken(ctx, pid, org, uid, body.Email); err != nil {
			ctx.Logger().Warn("verify-email send failed", "err", err)
		}
		httpStatus(w, http.StatusAccepted, map[string]any{"user": user, "verification_required": true})
		return
	}

	tokens, err := mintSession(ctx, pid, org, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpStatus(w, http.StatusCreated, map[string]any{
		"user":          user,
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	})
}

// ─── /login ──────────────────────────────────────────────────────────

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Email            string `json:"email"`
		Password         string `json:"password"`
		ClientID         string `json:"client_id"`
		OrganizationSlug string `json:"organization_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	client, clientErr := requireClient(ctx, pid, body.ClientID)
	if clientErr != nil {
		httpErr(w, http.StatusBadRequest, clientErr.Error())
		return
	}
	org, orgErr := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if orgErr != nil {
		httpErr(w, http.StatusBadRequest, orgErr.Error())
		return
	}

	user, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, body.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		dbAudit(ctx.AppDB(), pid, org.ID, nil, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "no_user", "email": body.Email})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if locked := userLocked(user); locked {
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_locked",
			r.RemoteAddr, r.UserAgent(), nil)
		httpStatus(w, http.StatusLocked, map[string]string{"error": "account_locked"})
		return
	}
	if user.Status != "active" {
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "status:" + user.Status})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	pwHash, err := dbGetUserPasswordHash(ctx.AppDB(), pid, org.ID, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pwHash == "" {
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "no_password"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	ok, err := verifyPassword(pwHash, body.Password)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		threshold := cfgInt(ctx, "lockout_threshold", 5)
		var lockUntil time.Time
		if threshold > 0 && newFailureCount(user)+1 >= threshold {
			minutes := cfgInt(ctx, "lockout_initial_minutes", 15)
			lockUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		}
		_ = dbMarkLoginFailure(ctx.AppDB(), pid, org.ID, user.ID, lockUntil)
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "bad_password"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if cfgBool(ctx, "email_verification_required", true) && user.EmailVerifiedAt == "" {
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "email_unverified"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "email_unverified"})
		return
	}

	if err := dbMarkLoginSuccess(ctx.AppDB(), pid, org.ID, user.ID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tokens, err := mintSession(ctx, pid, org, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login", r.RemoteAddr, r.UserAgent(), nil)

	user, _ = dbGetUserByID(ctx.AppDB(), pid, org.ID, user.ID)

	httpJSON(w, map[string]any{
		"user":          user,
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	})
}

// ─── /refresh ────────────────────────────────────────────────────────

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		RefreshToken     string `json:"refresh_token"`
		ClientID         string `json:"client_id"`
		OrganizationSlug string `json:"organization_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.RefreshToken == "" {
		httpErr(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	client, clientErr := requireClient(ctx, pid, body.ClientID)
	if clientErr != nil {
		httpErr(w, http.StatusBadRequest, clientErr.Error())
		return
	}

	hash := hashToken(body.RefreshToken)
	sess, err := dbFindActiveSessionByRefresh(ctx.AppDB(), pid, hash)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess == nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if sess.ClientID != client.ClientID {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	// The session carries the org it was minted under. For single-org
	// clients we cross-check that the session's org matches the
	// client's org (defense-in-depth — they're the same partition,
	// mismatching would be a strong signal of cross-org replay). For
	// multi-org clients we accept whichever org the session was issued
	// for, but if the caller passed organization_slug we also verify
	// it matches the session — a stale-but-correct hint shouldn't
	// override the session, but a *wrong* one is a red flag.
	if client.OrganizationID > 0 && sess.OrganizationID != client.OrganizationID {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	org, err := dbGetOrgByID(ctx.AppDB(), pid, sess.OrganizationID)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if org.Status != "active" {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if hint := strings.TrimSpace(strings.ToLower(body.OrganizationSlug)); hint != "" && hint != org.Slug {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, sess.UserID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user.Status != "active" {
		_ = dbRevokeSession(ctx.AppDB(), pid, org.ID, sess.ID)
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	if client.RefreshRotation {
		if err := dbRevokeSession(ctx.AppDB(), pid, org.ID, sess.ID); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	tokens, err := mintSession(ctx, pid, org, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "refresh", r.RemoteAddr, r.UserAgent(), nil)

	httpJSON(w, map[string]any{
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	})
}

// ─── /logout ─────────────────────────────────────────────────────────

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.RefreshToken != "" {
		hash := hashToken(body.RefreshToken)
		if sess, err := dbFindActiveSessionByRefresh(ctx.AppDB(), pid, hash); err == nil && sess != nil {
			_ = dbRevokeSession(ctx.AppDB(), pid, sess.OrganizationID, sess.ID)
			uid := sess.UserID
			dbAudit(ctx.AppDB(), pid, sess.OrganizationID, &uid, sess.ClientID, "logout", r.RemoteAddr, r.UserAgent(), nil)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── /me ─────────────────────────────────────────────────────────────
//
// JWTs carry an `org` claim (slug) so we know which org's JWKS to
// verify against. Iss is also org-prefixed but we don't trust strings
// before signature check — the kid in the header is what selects the
// pubkey, and the kid is per-org-unique because keys are per-org.

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "missing_bearer"})
		return
	}
	token := strings.TrimPrefix(authz, prefix)

	// Peek the org slug out of the unverified payload so we know which
	// org's keys to load. Then verify against those keys — a token
	// claiming "org": "acme" signed with internal's key won't validate.
	orgSlug, err := peekJWTOrg(token)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": err.Error()})
		return
	}
	if orgSlug == "" {
		// Legacy v0.3.x token — no org claim. Resolve to default org for
		// the deprecation window.
		orgSlug = "default"
	}
	org, err := dbGetOrgBySlug(ctx.AppDB(), pid, orgSlug)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": "unknown org"})
		return
	}
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	claims, err := jwtVerify(token, func(kid string) (ed25519.PublicKey, bool) {
		k, ok := keys[kid]
		return k, ok
	})
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": err.Error()})
		return
	}
	subRaw, _ := claims["sub"].(string)
	uidParsed, _ := parseUint(subRaw)
	if uidParsed == 0 {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uidParsed)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	if user.Status != "active" {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "user_inactive"})
		return
	}
	httpJSON(w, map[string]any{"user": user, "org": org.Slug})
}

// ─── helpers used only by handlers ────────────────────────────────────

type tokenPair struct {
	access    string
	refresh   string
	expiresIn int
}

// mintSession issues a fresh access + refresh token pair, persisting
// the refresh row. Uses the org's per-org signing key; the JWT carries
// the org slug in the new `org` claim and in the `iss` URL.
func mintSession(ctx *sdk.AppCtx, projectID string, org *Organization, user *User, client *Client, r *http.Request) (tokenPair, error) {
	kid, priv, err := dbActiveSigningKey(ctx.AppDB(), projectID, org.ID)
	if err != nil {
		return tokenPair{}, err
	}

	accessTTL := time.Duration(cfgInt(ctx, "jwt_access_ttl_seconds", 900)) * time.Second
	if client.AccessTokenTTLSeconds > 0 {
		accessTTL = time.Duration(client.AccessTokenTTLSeconds) * time.Second
	}
	refreshTTLDays := cfgInt(ctx, "jwt_refresh_ttl_days", 30)
	refreshTTL := time.Duration(refreshTTLDays) * 24 * time.Hour
	if client.RefreshTokenTTLSeconds > 0 {
		refreshTTL = time.Duration(client.RefreshTokenTTLSeconds) * time.Second
	}

	now := time.Now()
	aud := client.JWTAudience
	if aud == "" {
		aud = client.ClientID
	}
	claims := jwtClaims{
		Iss:   orgBaseURL(ctx, r, org),
		Sub:   uintToStr(user.ID),
		Aud:   aud,
		Azp:   client.ClientID,
		Iat:   now.Unix(),
		Exp:   now.Add(accessTTL).Unix(),
		Email: user.Email,
		EVer:  user.EmailVerifiedAt != "",
		Extra: map[string]any{"org": org.Slug},
	}
	access, err := jwtSign(priv, kid, claims)
	if err != nil {
		return tokenPair{}, err
	}
	refresh, err := randSlug(32)
	if err != nil {
		return tokenPair{}, err
	}
	expiresAt := now.Add(refreshTTL)
	if _, err := dbCreateSession(ctx.AppDB(), projectID, org.ID, user.ID, client.ClientID,
		hashToken(refresh), r.UserAgent(), r.RemoteAddr, expiresAt); err != nil {
		return tokenPair{}, err
	}
	return tokenPair{access: access, refresh: refresh, expiresIn: int(accessTTL.Seconds())}, nil
}

// requireClient looks up the client by id. Does NOT resolve the org —
// see resolveOrgForRequest for that, since the org might come from the
// client row (single-org client) OR from a body/query parameter
// (multi-org client). When client_id is omitted and exactly one active
// client exists across the project, that's used — preserves the v0.1.x
// convenience for new installs without registered clients.
func requireClient(ctx *sdk.AppCtx, projectID, clientID string) (*Client, error) {
	if clientID == "" {
		clients, err := dbListClients(ctx.AppDB(), projectID, 0, false)
		if err != nil {
			return nil, err
		}
		if len(clients) == 1 {
			c := clients[0]
			return &c, nil
		}
		return nil, errors.New("client_id required (multiple clients registered)")
	}
	c, err := dbGetClientByClientID(ctx.AppDB(), projectID, clientID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("unknown client_id")
		}
		return nil, err
	}
	if c.DisabledAt != "" {
		return nil, errors.New("client disabled")
	}
	return c, nil
}

// resolveOrgForRequest picks the Organization for a public-endpoint
// request. Two paths:
//
//  1. Single-org client (client.OrganizationID > 0) — org comes from
//     the client row. The SaaS doesn't need to send anything extra;
//     bodyOrgSlug is ignored even if present (the client's org wins
//     so a typo can't accidentally land users in the wrong pool).
//  2. Multi-org client (client.OrganizationID == 0) — org must be
//     supplied by the caller via the request body's organization_slug
//     field. Missing or unknown → error.
//
// Either way the caller gets the resolved Organization with status
// checked. Archived orgs error.
func resolveOrgForRequest(ctx *sdk.AppCtx, projectID string, client *Client, bodyOrgSlug string) (*Organization, error) {
	if client == nil {
		return nil, errors.New("client required")
	}
	var org *Organization
	if client.OrganizationID > 0 {
		o, err := dbGetOrgByID(ctx.AppDB(), projectID, client.OrganizationID)
		if err != nil {
			return nil, fmt.Errorf("client org missing: %w", err)
		}
		org = o
	} else {
		slug := strings.TrimSpace(strings.ToLower(bodyOrgSlug))
		if slug == "" {
			return nil, errors.New("organization_slug required (this client is multi-organization)")
		}
		o, err := dbGetOrgBySlug(ctx.AppDB(), projectID, slug)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("unknown organization_slug")
			}
			return nil, err
		}
		org = o
	}
	if org.Status != "active" {
		return nil, errors.New("organization archived")
	}
	return org, nil
}

// orgFromRequest resolves the org for routes whose URL carries the slug
// (`/orgs/{slug}/...`). The legacy `/.well-known/*` paths fall through
// to the default org for one release.
func orgFromRequest(ctx *sdk.AppCtx, r *http.Request, projectID string) (*Organization, error) {
	if slug := r.PathValue("slug"); slug != "" {
		o, err := dbGetOrgBySlug(ctx.AppDB(), projectID, slug)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("unknown organization")
			}
			return nil, err
		}
		return o, nil
	}
	// Legacy: no slug — resolve to default org. Audit so we can see
	// when callers are still using the deprecated paths.
	o, err := dbGetOrgBySlug(ctx.AppDB(), projectID, "default")
	if err != nil {
		return nil, errors.New("default organization missing (apply migration 002_organizations.sql)")
	}
	return o, nil
}

func userLocked(u *User) bool {
	if u == nil || u.LockedUntil == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, u.LockedUntil)
	if err != nil {
		return false
	}
	return t.After(time.Now())
}

func newFailureCount(u *User) int {
	if u == nil || globalCtx == nil {
		return 0
	}
	var n int
	_ = globalCtx.AppDB().QueryRow(
		`SELECT failed_login_count FROM users WHERE id = ?`, u.ID).Scan(&n)
	return n
}

// platformBaseURL — the URL the SaaS frontend hits for /signup, /login,
// /token, /me, etc. Resolution: explicit app_url override → SDK
// PlatformInfo → request host. NB: this is *not* the JWT issuer;
// orgBaseURL is, because issuers must be org-prefixed in v0.4.0.
func platformBaseURL(ctx *sdk.AppCtx, r *http.Request) string {
	if ctx != nil {
		if v := cfgStr(ctx, "app_url", ""); v != "" {
			return strings.TrimRight(v, "/")
		}
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			return strings.TrimRight(info.PublicURL, "/")
		}
	}
	if r == nil {
		return ""
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// orgBaseURL — the JWT issuer string + the prefix for org-scoped
// discovery URLs. `{platform_base}/orgs/{slug}`. JWT verifiers should
// pin to this so a token from org B can't pass for org A even if
// signing keys somehow collide.
func orgBaseURL(ctx *sdk.AppCtx, r *http.Request, org *Organization) string {
	base := platformBaseURL(ctx, r)
	if org == nil {
		return base
	}
	return base + "/orgs/" + org.Slug
}

// publicBaseURL — kept for tests and any caller that doesn't have an
// org in hand. Returns the platform base (not org-prefixed).
func publicBaseURL(ctx *sdk.AppCtx, r *http.Request) string {
	return platformBaseURL(ctx, r)
}

func parseUint(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
		if n < 0 {
			return 0, false
		}
	}
	return n, true
}

func uintToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
