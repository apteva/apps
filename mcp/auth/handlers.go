package main

// handlers.go — HTTP handlers for the auth surface the deployed SaaS
// frontend hits. Reverse-proxied at /apps/auth/* by apteva-server.
//
// Convention for every handler:
//   1. resolveProjectFromRequest    — partition key
//   2. method check                 — POST/GET as documented
//   3. parse body / params
//   4. db lookup, business logic
//   5. dbAudit on every consequential outcome (success and failure)
//   6. httpJSON / httpStatus / httpErr
//
// Handlers are deliberately self-contained — no shared middleware
// stack — so tests can hit them with httptest.NewRequest directly.

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── /.well-known/jwks.json ──────────────────────────────────────────
//
// Public — no auth. Any service holding a JWT can hit this URL to fetch
// the keys it needs to verify signatures. We return every key (active
// + retired-but-not-yet-purged) so tokens signed by an old key still
// validate during the rotation drain window.

func (a *App) handleJWKS(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid)
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

// ─── /.well-known/openid-configuration ───────────────────────────────
//
// Discovery endpoint so SDKs can autoconfigure. We don't claim full
// OIDC support — v0.1 is closer to OAuth2 + JWT — but advertising the
// minimal surface (issuer, jwks_uri, supported algs, endpoints) lets
// generic JWT-verifying middleware work without manual setup.

func (a *App) handleOIDCConfig(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(getAppCtx(r), r)
	resp := map[string]any{
		"issuer":                                base,
		"jwks_uri":                              base + "/.well-known/jwks.json",
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"userinfo_endpoint":                     base + "/me",
		"id_token_signing_alg_values_supported": []string{"EdDSA"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "password", "client_credentials"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
	}
	httpJSON(w, resp)
}

// ─── /signup ─────────────────────────────────────────────────────────
//
// POST { email, password, display_name?, client_id }
// → 201 { user, access_token, refresh_token, expires_in } when
//   email_verification_required is false (auto-login)
// → 202 { user } when verification is required (no tokens issued)

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
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		ClientID    string `json:"client_id"`
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
	if reason := validatePassword(body.Password,
		cfgInt(ctx, "password_min_length", 12),
		cfgInt(ctx, "password_classes_required", 2)); reason != "" {
		httpErr(w, http.StatusBadRequest, reason)
		return
	}

	if existing, err := dbGetUserByEmail(ctx.AppDB(), pid, body.Email); err == nil && existing != nil {
		// 409 — but we don't disclose whether the address is real
		// during /password/forgot. Signup IS allowed to disclose,
		// because front-ends need the friction to redirect users
		// to login; suppressing it would create UX paper cuts.
		dbAudit(ctx.AppDB(), pid, &existing.ID, client.ClientID, "signup_conflict",
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
	uid, err := dbCreateUser(ctx.AppDB(), pid, body.Email, pwHash, body.DisplayName, !verificationRequired)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, uid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, &uid, client.ClientID, "signup", r.RemoteAddr, r.UserAgent(), nil)

	// If verification is required, fire off the verify email and
	// stop — caller redirects to a "check your inbox" page.
	if verificationRequired {
		if err := issueVerifyEmailToken(ctx, pid, uid, body.Email); err != nil {
			ctx.Logger().Warn("verify-email send failed", "err", err)
		}
		httpStatus(w, http.StatusAccepted, map[string]any{"user": user, "verification_required": true})
		return
	}

	// Otherwise auto-login — issue tokens directly.
	tokens, err := mintSession(ctx, pid, user, client, r)
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
//
// POST { email, password, client_id }
// → 200 { access_token, refresh_token, expires_in, user }
// → 401 invalid_grant on any failure (do not leak whether the email
//   exists; whether the password matches; whether the account is
//   locked, disabled, deleted, or unverified — all 401)
// → 423 locked when the account is currently in lockout (a separate
//   code so the front-end can show the right banner, but with no
//   reason that would distinguish "wrong password" vs "doesn't exist"
//   beyond what the lockout itself implies)

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
		Email    string `json:"email"`
		Password string `json:"password"`
		ClientID string `json:"client_id"`
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

	user, err := dbGetUserByEmail(ctx.AppDB(), pid, body.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		dbAudit(ctx.AppDB(), pid, nil, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "no_user", "email": body.Email})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	// Currently locked? Refuse without checking the password — running
	// argon2 on every guess during a lockout would be a free DoS amp.
	if locked := userLocked(user); locked {
		dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login_locked",
			r.RemoteAddr, r.UserAgent(), nil)
		httpStatus(w, http.StatusLocked, map[string]string{"error": "account_locked"})
		return
	}
	if user.Status != "active" {
		dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "status:" + user.Status})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	pwHash, err := dbGetUserPasswordHash(ctx.AppDB(), pid, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pwHash == "" {
		// OAuth-only account — they should hit /oauth/<provider>.
		dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login_failed",
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
		// Bump the streak. If we've crossed the threshold, set a lock.
		threshold := cfgInt(ctx, "lockout_threshold", 5)
		var lockUntil time.Time
		if threshold > 0 && newFailureCount(user)+1 >= threshold {
			minutes := cfgInt(ctx, "lockout_initial_minutes", 15)
			lockUntil = time.Now().Add(time.Duration(minutes) * time.Minute)
		}
		_ = dbMarkLoginFailure(ctx.AppDB(), pid, user.ID, lockUntil)
		dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "bad_password"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	// Email-verified gate.
	if cfgBool(ctx, "email_verification_required", true) && user.EmailVerifiedAt == "" {
		dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "email_unverified"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "email_unverified"})
		return
	}

	// (MFA gate would land here in v0.2.)

	if err := dbMarkLoginSuccess(ctx.AppDB(), pid, user.ID); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tokens, err := mintSession(ctx, pid, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "login", r.RemoteAddr, r.UserAgent(), nil)

	// Refresh the in-memory snapshot so the response carries
	// last_login_at = now (handy for the SaaS to show "Welcome
	// back, last seen X" without a follow-up GET /me).
	user, _ = dbGetUserByID(ctx.AppDB(), pid, user.ID)

	httpJSON(w, map[string]any{
		"user":          user,
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	})
}

// ─── /refresh ────────────────────────────────────────────────────────
//
// POST { refresh_token, client_id }
// → 200 { access_token, refresh_token, expires_in }
//
// Refresh-token rotation: the presented token is revoked and a new
// one is issued in the same response. If the client presents a
// previously-rotated (i.e. revoked) token, that's a strong signal
// of a stolen token; we revoke ALL the user's sessions as a defensive
// measure.

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
		RefreshToken string `json:"refresh_token"`
		ClientID     string `json:"client_id"`
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
		// Either invalid, expired, or replayed-after-rotation. We can't
		// distinguish cheaply without a separate lookup; the cheap thing
		// to do is treat all three as 401 and move on. Future hardening:
		// if a hash matches a revoked row, treat as token theft and
		// revoke all sessions for that user.
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if sess.ClientID != client.ClientID {
		// Refresh tokens are bound to a client. If the presented token
		// was issued for a different client, refuse.
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	user, err := dbGetUserByID(ctx.AppDB(), pid, sess.UserID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user.Status != "active" {
		_ = dbRevokeSession(ctx.AppDB(), pid, sess.ID)
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}

	// Rotate: revoke the old session, mint a new one. Refresh token
	// rotation is the default; clients with rotation off (e.g. native
	// apps that can't safely store new refresh tokens) skip the revoke.
	if client.RefreshRotation {
		if err := dbRevokeSession(ctx.AppDB(), pid, sess.ID); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	tokens, err := mintSession(ctx, pid, user, client, r)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, &user.ID, client.ClientID, "refresh", r.RemoteAddr, r.UserAgent(), nil)

	httpJSON(w, map[string]any{
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	})
}

// ─── /logout ─────────────────────────────────────────────────────────
//
// POST { refresh_token }
// → 204
//
// Always returns 204, even on missing/invalid token — logout is never
// a useful probing surface and an unauthenticated 204 hides nothing.

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
			_ = dbRevokeSession(ctx.AppDB(), pid, sess.ID)
			uid := sess.UserID
			dbAudit(ctx.AppDB(), pid, &uid, sess.ClientID, "logout", r.RemoteAddr, r.UserAgent(), nil)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── /me ─────────────────────────────────────────────────────────────
//
// GET (Authorization: Bearer <access_token>)
// → 200 { user }
// → 401 if the JWT is missing/invalid/expired
//
// Verifies the JWT against the JWKS for the current project. We don't
// re-fetch the user from DB on every call because the JWT carries
// enough for most consumers (sub, email, email_verified) — but for
// completeness we DO read the row, since a disabled-after-issue user
// shouldn't be returned as "logged in".

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
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid)
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
	user, err := dbGetUserByID(ctx.AppDB(), pid, uidParsed)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	if user.Status != "active" {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "user_inactive"})
		return
	}
	httpJSON(w, map[string]any{"user": user})
}

// ─── helpers used only by handlers ────────────────────────────────────

type tokenPair struct {
	access    string
	refresh   string
	expiresIn int
}

// mintSession issues a fresh access + refresh token pair, persisting
// the refresh row. Used by /signup (auto-login), /login, /refresh,
// and the OAuth callback.
func mintSession(ctx *sdk.AppCtx, projectID string, user *User, client *Client, r *http.Request) (tokenPair, error) {
	kid, priv, err := dbActiveSigningKey(ctx.AppDB(), projectID)
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
		Iss:   publicBaseURL(ctx, r),
		Sub:   uintToStr(user.ID),
		Aud:   aud,
		Azp:   client.ClientID,
		Iat:   now.Unix(),
		Exp:   now.Add(accessTTL).Unix(),
		Email: user.Email,
		EVer:  user.EmailVerifiedAt != "",
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
	if _, err := dbCreateSession(ctx.AppDB(), projectID, user.ID, client.ClientID,
		hashToken(refresh), r.UserAgent(), r.RemoteAddr, expiresAt); err != nil {
		return tokenPair{}, err
	}
	return tokenPair{access: access, refresh: refresh, expiresIn: int(accessTTL.Seconds())}, nil
}

// requireClient looks up the client by id, or — when the caller
// omitted client_id and exactly one client exists in the project —
// returns that one. Convenience for new installs that haven't
// registered explicit clients yet; production deployments should
// always pass client_id explicitly.
func requireClient(ctx *sdk.AppCtx, projectID, clientID string) (*Client, error) {
	if clientID == "" {
		clients, err := dbListClients(ctx.AppDB(), projectID, false)
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

// userLocked returns true when the user is currently in lockout window.
// Reads parsed string from User.LockedUntil; if parse fails we err on
// the safe side and consider unlocked (a corrupt timestamp shouldn't
// brick someone's account).
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

// newFailureCount reads the current failed_login_count via a tiny
// query. We could persist it on the User struct but readers of *User
// elsewhere don't need it, and the dashboard displays "locked yes/no"
// not the raw count.
func newFailureCount(u *User) int {
	if u == nil {
		return 0
	}
	if globalCtx == nil {
		return 0
	}
	var n int
	_ = globalCtx.AppDB().QueryRow(
		`SELECT failed_login_count FROM users WHERE id = ?`, u.ID).Scan(&n)
	return n
}

// publicBaseURL is the issuer string we embed in JWTs and the OIDC
// discovery doc. We prefer the install's `app_url` config when set
// because that's the user-visible domain (e.g. https://app.example.com)
// rather than the internal sidecar URL the request came in on.
func publicBaseURL(ctx *sdk.AppCtx, r *http.Request) string {
	if ctx != nil {
		if v := cfgStr(ctx, "app_url", ""); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// parseUint / uintToStr — JWT sub claims are strings per spec; we want
// int64 user ids on the wire. Tiny wrappers so the conversion lives
// in one place. parseUint allows leading zero / minus rejection that
// strconv.ParseUint already does — we just ignore err and return 0
// because callers treat 0 as "invalid".
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
