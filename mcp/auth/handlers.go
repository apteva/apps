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
	"net/url"
	"os"
	"strconv"
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
		"issuer": base, "jwks_uri": base + "/.well-known/jwks.json",
		"userinfo_endpoint": platformBase + "/me", "login_endpoint": platformBase + "/login", "refresh_endpoint": platformBase + "/refresh",
		"protocol": "apteva-auth", "oauth_supported": false, "mfa_supported": false, "magic_link_supported": false,
		"access_token_signing_alg_values_supported": []string{"EdDSA"},
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
	var body signupRequest
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.IP = r.RemoteAddr
	body.UserAgent = r.UserAgent()
	body.Origin = r.Header.Get("Origin")
	if id, secret, ok := r.BasicAuth(); ok {
		if id != body.ClientID {
			httpErr(w, 401, "invalid_client")
			return
		}
		body.ClientSecret = secret
	}

	res, status, err := performSignup(ctx, pid, body, mintSessionFor(r))
	if err != nil {
		httpErr(w, status, err.Error())
		return
	}
	if res.VerificationRequired {
		httpStatus(w, status, res)
		return
	}
	resp := map[string]any{
		"user":          res.User,
		"access_token":  res.AccessToken,
		"refresh_token": res.RefreshToken,
		"expires_in":    res.ExpiresIn,
		"token_type":    "Bearer",
	}
	if res.AptevaAccessToken != "" {
		resp["apteva_access_token"] = res.AptevaAccessToken
		resp["apteva_expires_in"] = res.AptevaExpiresIn
	}
	if res.Authorization != nil {
		resp["authorization"] = res.Authorization
	}
	httpStatus(w, status, resp)
}

// signupRequest is the shared input to performSignup — used by both
// the /signup HTTP handler (json-decoded body) and the
// auth_public_signup MCP tool (args-decoded body). IP / UserAgent are
// set by the caller from r.RemoteAddr / r.UserAgent() in the HTTP
// path; MCP callers either pass them through from the originating
// request or accept the "mcp" defaults.
type signupRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	DisplayName      string `json:"display_name"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret"`
	OrganizationSlug string `json:"organization_slug"`
	ContinueURL      string `json:"continue_url"`
	IP               string `json:"-"`
	UserAgent        string `json:"-"`
	Origin           string `json:"-"`
}

// signupResult mirrors what /signup writes to the response body, but
// as a typed value the MCP tool can return without re-serializing.
// VerificationRequired splits the two HTTP statuses (202 vs 201):
// when true, the access/refresh tokens are empty and the verify-email
// has been (or attempted to be) sent.
type signupResult struct {
	User                 *User                 `json:"user"`
	Authorization        *AuthorizationContext `json:"authorization,omitempty"`
	AccessToken          string                `json:"access_token,omitempty"`
	RefreshToken         string                `json:"refresh_token,omitempty"`
	ExpiresIn            int                   `json:"expires_in,omitempty"`
	AptevaAccessToken    string                `json:"apteva_access_token,omitempty"`
	AptevaExpiresIn      int                   `json:"apteva_expires_in,omitempty"`
	VerificationRequired bool                  `json:"verification_required,omitempty"`
	DeliveryError        string                `json:"delivery_error,omitempty"`
}

// mintSessionFor returns a closure that calls mintSession with the
// originating HTTP request — letting performSignup stay HTTP-agnostic
// (the MCP path supplies a closure backed by a synthetic request that
// carries just the IP/UA the agent passed in).
type sessionMinter func(ctx *sdk.AppCtx, tx *sql.Tx, pid string, org *Organization, user *User, client *Client) (tokenPair, error)

func mintSessionFor(r *http.Request) sessionMinter {
	return func(ctx *sdk.AppCtx, tx *sql.Tx, pid string, org *Organization, user *User, client *Client) (tokenPair, error) {
		return mintSessionTx(ctx, tx, pid, org.ID, user.ID, client.ClientID, r, "", time.Time{}, "")
	}
}

// performSignup runs the public signup flow shared by /signup and the
// auth_public_signup MCP tool. Returns (result, http-status-to-use,
// error). Status is meaningful even on error so the HTTP path can
// surface 400/409/500 to the caller; the MCP path treats any non-nil
// err as the tool error (status is ignored on the MCP side).
func performSignup(ctx *sdk.AppCtx, pid string, body signupRequest, mint sessionMinter) (*signupResult, int, error) {
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	if body.Email == "" || body.Password == "" {
		return nil, http.StatusBadRequest, errors.New("email and password required")
	}
	client, clientErr := requireClient(ctx, pid, body.ClientID)
	if clientErr != nil {
		return nil, http.StatusBadRequest, clientErr
	}
	if err := authenticateClient(ctx, pid, client, nil, body.ClientSecret); err != nil {
		return nil, 401, err
	}
	if err := validateEmail(body.Email); err != nil {
		return nil, 400, err
	}
	if len(body.DisplayName) > 256 {
		return nil, 400, errors.New("display_name too long")
	}
	if err := consumeRate(ctx.AppDB(), pid+":signup:"+client.ClientID, 600, time.Hour); err != nil {
		return nil, 429, err
	}
	if err := requireAllowedOrigin(client, body.Origin); err != nil {
		return nil, http.StatusForbidden, err
	}
	if err := validateRecoveryContinueURL(client, body.ContinueURL); err != nil {
		return nil, http.StatusBadRequest, err
	}
	org, orgErr := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if orgErr != nil {
		return nil, http.StatusBadRequest, orgErr
	}
	if err := checkPasswordPolicy(ctx, org, body.Password); err != nil {
		return nil, 400, err
	}
	policy, _ := effectivePolicy(ctx, org)
	if client.RequireMFA {
		return nil, 403, errors.New("mfa_required: MFA authentication is not implemented")
	}
	if existing, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, body.Email); err == nil && existing != nil {
		dbAudit(ctx.AppDB(), pid, org.ID, &existing.ID, client.ClientID, "signup_conflict",
			body.IP, body.UserAgent, nil)
		return nil, http.StatusConflict, errors.New("email already registered")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, http.StatusInternalServerError, err
	}

	pwHash, err := hashPassword(body.Password)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	tx, err := beginAuthTx(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return nil, 500, err
	}
	defer tx.Rollback()
	org, err = dbGetOrgByID(tx, pid, org.ID)
	if err != nil || org.Status != "active" {
		return nil, 403, errors.New("organization inactive")
	}
	policy, err = effectivePolicy(ctx, org)
	if err != nil {
		return nil, 400, err
	}
	if err = checkPasswordPolicy(ctx, org, body.Password); err != nil {
		return nil, 400, err
	}
	verificationRequired := policy.Verify
	uid, err := dbCreateUser(tx, pid, org.ID, body.Email, pwHash, body.DisplayName, !verificationRequired, "{}")
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	user, err := dbGetUserByID(tx, pid, org.ID, uid)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	dbAudit(tx, pid, org.ID, &uid, client.ClientID, "signup", body.IP, body.UserAgent, nil)

	if verificationRequired {
		if err = tx.Commit(); err != nil {
			return nil, 500, err
		}
		deliveryError := ""
		if err := issueVerifyEmailTokenForClient(ctx, pid, org, uid, body.Email, recoveryLinkOptions{
			ClientID: client.ClientID, ContinueURL: body.ContinueURL,
		}); err != nil {
			ctx.Logger().Warn("verify-email send failed")
			deliveryError = "Account created; verification email was not delivered. Retry verification after email delivery is configured."
		}
		return &signupResult{User: user, VerificationRequired: true, DeliveryError: deliveryError}, http.StatusAccepted, nil
	}

	tokens, err := mint(ctx, tx, pid, org, user, client)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	if err = tx.Commit(); err != nil {
		return nil, 500, err
	}
	aptevaToken, err := mintAptevaDelegatedToken(pid, org, user, client)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
		aptevaToken = nil
	}
	aptevaAccessToken := ""
	aptevaExpiresIn := 0
	if aptevaToken != nil {
		aptevaAccessToken = aptevaToken.AccessToken
		aptevaExpiresIn = aptevaToken.ExpiresIn
	}
	return &signupResult{
		User:              user,
		Authorization:     &tokens.authorization,
		AccessToken:       tokens.access,
		RefreshToken:      tokens.refresh,
		ExpiresIn:         tokens.expiresIn,
		AptevaAccessToken: aptevaAccessToken,
		AptevaExpiresIn:   aptevaExpiresIn,
	}, http.StatusCreated, nil
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
		ClientSecret     string `json:"client_secret"`
		OrganizationSlug string `json:"organization_slug"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	client, clientErr := requireClient(ctx, pid, body.ClientID)
	if clientErr != nil {
		httpErr(w, http.StatusBadRequest, clientErr.Error())
		return
	}
	if err := requireAllowedOrigin(client, r.Header.Get("Origin")); err != nil {
		httpStatus(w, http.StatusForbidden, map[string]string{"error": "origin_not_allowed"})
		return
	}
	org, orgErr := resolveOrgForRequest(ctx, pid, client, body.OrganizationSlug)
	if orgErr != nil {
		httpErr(w, http.StatusBadRequest, orgErr.Error())
		return
	}

	if err := authenticateClient(ctx, pid, client, r, body.ClientSecret); err != nil {
		httpErr(w, 401, err.Error())
		return
	}
	policy, err := effectivePolicy(ctx, org)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := consumeRate(ctx.AppDB(), pid+":login:"+client.ClientID+":"+strings.ToLower(strings.TrimSpace(body.Email)), 30, time.Minute); err != nil {
		httpErr(w, 429, "rate_limited")
		return
	}

	user, err := dbGetUserByEmail(ctx.AppDB(), pid, org.ID, body.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if user == nil {
		_, _ = verifyPassword(dummyPasswordHash(), body.Password)
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
		if err := markLoginFailure(ctx, pid, org, user, policy); err != nil {
			httpErr(w, 500, "login unavailable")
			return
		}
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "bad_password"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_grant"})
		return
	}
	if policy.Verify && user.EmailVerifiedAt == "" {
		dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login_failed",
			r.RemoteAddr, r.UserAgent(), map[string]any{"reason": "email_unverified"})
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "email_unverified"})
		return
	}

	if err := sessionEligibility(ctx, org, user, client); err != nil {
		httpErr(w, 403, err.Error())
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
	aptevaToken, err := mintAptevaDelegatedToken(pid, org, user, client)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
		aptevaToken = nil
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "login", r.RemoteAddr, r.UserAgent(), nil)

	user, _ = dbGetUserByID(ctx.AppDB(), pid, org.ID, user.ID)

	resp := map[string]any{
		"user":          user,
		"authorization": tokens.authorization,
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	}
	if aptevaToken != nil {
		resp["apteva_access_token"] = aptevaToken.AccessToken
		resp["apteva_expires_in"] = aptevaToken.ExpiresIn
	}
	httpJSON(w, resp)
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
		ClientSecret     string `json:"client_secret"`
		OrganizationSlug string `json:"organization_slug"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
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
	if err := requireAllowedOrigin(client, r.Header.Get("Origin")); err != nil {
		httpStatus(w, http.StatusForbidden, map[string]string{"error": "origin_not_allowed"})
		return
	}

	if err := authenticateClient(ctx, pid, client, r, body.ClientSecret); err != nil {
		httpErr(w, 401, err.Error())
		return
	}
	tokens, org, user, err := refreshSession(ctx, pid, client, body.RefreshToken, strings.ToLower(strings.TrimSpace(body.OrganizationSlug)), r)
	if err != nil {
		httpErr(w, 401, err.Error())
		return
	}
	aptevaToken, err := mintAptevaDelegatedToken(pid, org, user, client)
	if err != nil {
		ctx.Logger().Warn("delegated user token mint failed", "err", err)
		aptevaToken = nil
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, client.ClientID, "refresh", r.RemoteAddr, r.UserAgent(), nil)

	resp := map[string]any{
		"user":          user,
		"authorization": tokens.authorization,
		"access_token":  tokens.access,
		"refresh_token": tokens.refresh,
		"expires_in":    tokens.expiresIn,
		"token_type":    "Bearer",
	}
	if aptevaToken != nil {
		resp["apteva_access_token"] = aptevaToken.AccessToken
		resp["apteva_expires_in"] = aptevaToken.ExpiresIn
	}
	httpJSON(w, resp)
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
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, 400, "invalid JSON")
		return
	}
	if body.RefreshToken != "" {
		var family string
		if err := ctx.AppDB().QueryRow(`SELECT IFNULL(family_id,'') FROM sessions WHERE project_id=? AND refresh_token_hash=?`, pid, hashToken(body.RefreshToken)).Scan(&family); err == nil && family != "" {
			if err := revokeFamily(ctx.AppDB(), pid, family); err != nil {
				httpErr(w, 500, "logout unavailable")
				return
			}
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
	org, user, ok := a.authenticatedBearerUser(w, r, ctx, pid)
	if !ok {
		return
	}
	authorization, err := dbAuthorizationContext(ctx.AppDB(), pid, org, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"user": user, "org": org.Slug, "authorization": authorization})
}

// ─── /me/metadata ───────────────────────────────────────────────────

func (a *App) handleMeMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		httpErr(w, http.StatusMethodNotAllowed, "PATCH only")
		return
	}
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	org, user, ok := a.authenticatedBearerUser(w, r, ctx, pid)
	if !ok {
		return
	}
	var body struct {
		Metadata *json.RawMessage `json:"metadata"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Metadata == nil {
		httpErr(w, http.StatusBadRequest, "metadata required")
		return
	}
	metadataJSON, code, err := normalizeMetadataRaw(body.Metadata)
	if err != nil {
		httpErr(w, code, err.Error())
		return
	}
	if err := dbUpdateUserProfile(ctx.AppDB(), pid, org.ID, user.ID, nil, nil, &metadataJSON); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, user.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &user.ID, "", "user_metadata_updated", r.RemoteAddr, r.UserAgent(), map[string]any{
		"self_service": true,
	})
	httpJSON(w, map[string]any{"user": updated, "org": org.Slug})
}

func (a *App) authenticatedBearerUser(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string) (*Organization, *User, bool) {
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(authz, prefix) {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "missing_bearer"})
		return nil, nil, false
	}
	token := strings.TrimPrefix(authz, prefix)
	if len(token) > 16000 {
		httpErr(w, 401, "invalid_token")
		return nil, nil, false
	}

	// Peek the org slug out of the unverified payload so we know which
	// org's keys to load. Then verify against those keys — a token
	// claiming "org": "acme" signed with internal's key won't validate.
	orgSlug, err := peekJWTOrg(token)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": err.Error()})
		return nil, nil, false
	}
	if orgSlug == "" {
		// Legacy v0.3.x token — no org claim. Resolve to default org for
		// the deprecation window.
		orgSlug = "default"
	}
	org, err := dbGetOrgBySlug(ctx.AppDB(), pid, orgSlug)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": "unknown org"})
		return nil, nil, false
	}
	keys, err := dbAllSigningKeys(ctx.AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	claims, err := jwtVerify(token, func(kid string) (ed25519.PublicKey, bool) {
		k, ok := keys[kid]
		return k, ok
	})
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "detail": err.Error()})
		return nil, nil, false
	}
	subRaw, _ := claims["sub"].(string)
	uidParsed, _ := parseUint(subRaw)
	if uidParsed == 0 {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return nil, nil, false
	}
	user, err := dbGetUserByID(ctx.AppDB(), pid, org.ID, uidParsed)
	if err != nil {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return nil, nil, false
	}
	if user.Status != "active" {
		httpStatus(w, http.StatusUnauthorized, map[string]string{"error": "user_inactive"})
		return nil, nil, false
	}
	if org.Status != "active" {
		httpErr(w, 401, "organization_inactive")
		return nil, nil, false
	}
	cid, _ := claims["azp"].(string)
	sid, _ := claims["sid"].(string)
	iss, _ := claims["iss"].(string)
	aud, _ := claims["aud"].(string)
	client, err := dbGetClientByClientID(ctx.AppDB(), pid, cid)
	if err != nil || sid == "" || claims["token_use"] != "access" || iss != orgBaseURL(ctx, r, org) {
		httpErr(w, 401, "invalid_token")
		return nil, nil, false
	}
	expectedAud := client.JWTAudience
	if expectedAud == "" {
		expectedAud = cid
	}
	if aud != expectedAud || sessionEligibility(ctx, org, user, client) != nil {
		httpErr(w, 401, "invalid_token")
		return nil, nil, false
	}
	var active int
	err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM auth_session_families WHERE id=? AND project_id=? AND organization_id=? AND user_id=? AND client_id=? AND revoked_at IS NULL AND expires_at>?`, sid, pid, org.ID, user.ID, cid, rfc3339(time.Now())).Scan(&active)
	if err != nil || active != 1 {
		httpErr(w, 401, "session_revoked")
		return nil, nil, false
	}
	tokenVersion, hasVersion := jwtInt64Claim(claims, "authorization_version")
	if (hasVersion && tokenVersion != user.AuthorizationVersion) ||
		!hasVersion {
		httpStatus(w, http.StatusUnauthorized, map[string]string{
			"error":  "stale_authorization",
			"detail": "refresh the session to receive current roles and permissions",
		})
		return nil, nil, false
	}
	return org, user, true
}

func (a *App) handleOrgPublic(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

// ─── helpers used only by handlers ────────────────────────────────────

type tokenPair struct {
	access        string
	refresh       string
	expiresIn     int
	authorization AuthorizationContext
}

// mintSession issues a fresh access + refresh token pair, persisting
// the refresh row. Uses the org's per-org signing key; the JWT carries
// the org slug in the new `org` claim and in the `iss` URL.

func jwtInt64Claim(claims map[string]any, key string) (int64, bool) {
	return strictID(claims[key], false)
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

func requireAllowedOrigin(client *Client, origin string) error {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return nil
	}
	if client == nil {
		return errors.New("client required")
	}
	for _, allowed := range client.AllowedOrigins {
		allowed = strings.TrimRight(strings.TrimSpace(allowed), "/")
		if allowed == "*" || allowed == origin {
			return nil
		}
	}
	return errors.New("origin not allowed")
}

func validateRecoveryContinueURL(client *Client, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("continue_url must be an absolute http(s) URL")
	}
	origin := u.Scheme + "://" + u.Host
	if client == nil {
		return errors.New("client required")
	}
	for _, allowed := range client.AllowedOrigins {
		if strings.TrimRight(strings.TrimSpace(allowed), "/") == origin || strings.TrimSpace(allowed) == "*" {
			return nil
		}
	}
	for _, redirect := range client.RedirectURIs {
		r, parseErr := url.Parse(redirect)
		if parseErr == nil && r.Scheme+"://"+r.Host == origin {
			return nil
		}
	}
	return errors.New("continue_url origin is not allowed for this client")
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
	if bodyOrgSlug != "" && strings.ToLower(strings.TrimSpace(bodyOrgSlug)) != org.Slug {
		return nil, errors.New("organization does not match client")
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

func platformBaseURL(ctx *sdk.AppCtx, r *http.Request) string {
	if ctx != nil {
		if v := cfgStr(ctx, "app_url", ""); v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return ""
			}
			return strings.TrimRight(v, "/")
		}
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && info.PublicURL != "" {
			base := strings.TrimRight(info.PublicURL, "/") + "/api/apps/auth"
			if id, ok := parseUint(os.Getenv("APTEVA_INSTALL_ID")); ok {
				base += "/_install/" + uintToStr(id)
			}
			return base
		}
	}
	return ""
}

// orgBaseURL — the JWT issuer string + the prefix for org-scoped
// discovery URLs. `{platform_base}/orgs/{slug}`. JWT verifiers should
// pin to this so a token from org B can't pass for org A even if
// signing keys somehow collide.
func orgBaseURL(ctx *sdk.AppCtx, r *http.Request, org *Organization) string {
	base := platformBaseURL(ctx, r)
	if base == "" {
		return ""
	}
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
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil && n > 0
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
