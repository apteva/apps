package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const delegatedMaxTTL = 60 * time.Second

var errDelegatedSession = errors.New("invalid_session")
var errDelegatedPolicy = errors.New("delegated_access_denied")

// Policy identifiers are selected by Auth from trusted configuration, never
// accepted as authorization from a browser. Every required role AND permission
// must be present. A profile permits explicit selection when several qualify.
type delegatedBinding struct {
	ProjectID        string   `json:"project_id"`
	OrganizationSlug string   `json:"organization_slug"`
	ClientID         string   `json:"client_id"`
	Profile          string   `json:"profile"`
	PolicyClientID   string   `json:"policy_client_id"`
	Roles            []string `json:"roles"`
	Permissions      []string `json:"permissions"`
}
type aptevaDelegatedToken struct {
	AccessToken   string `json:"access_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int    `json:"expires_in"`
	ExpiresAt     string `json:"expires_at"`
	KeyPrefix     string `json:"key_prefix"`
	ProjectID     string `json:"project_id"`
	OAuthClientID string `json:"oauth_client_id"`
	Subject       struct {
		Type             string `json:"type"`
		ID               string `json:"id"`
		OrganizationID   string `json:"organization_id"`
		OrganizationSlug string `json:"organization_slug"`
	} `json:"subject"`
}

func delegatedBindings(ctx *sdk.AppCtx) ([]delegatedBinding, error) {
	raw := cfgStr(ctx, "delegated_token_bindings", "[]")
	if len(raw) > 65536 {
		return nil, errors.New("invalid delegated bindings")
	}
	var bindings []delegatedBinding
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bindings); err != nil {
		return nil, errors.New("invalid delegated bindings")
	}
	if dec.Decode(new(any)) != io.EOF || len(bindings) > 100 {
		return nil, errors.New("invalid delegated bindings")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.ProjectID == "" || b.OrganizationSlug == "" || b.ClientID == "" || b.Profile == "" || b.PolicyClientID == "" || len(b.Roles)+len(b.Permissions) == 0 {
			return nil, errors.New("incomplete delegated binding")
		}
		key := b.ProjectID + "\x00" + b.OrganizationSlug + "\x00" + b.ClientID + "\x00" + b.Profile
		if seen[key] {
			return nil, errors.New("duplicate delegated profile")
		}
		seen[key] = true
		for _, required := range append(append([]string{}, b.Roles...), b.Permissions...) {
			if required == "" || required == "*" {
				return nil, errors.New("invalid delegated requirement")
			}
		}
	}
	return bindings, nil
}
func hasAll(actual, required []string) bool {
	values := map[string]bool{}
	for _, value := range actual {
		values[value] = true
	}
	for _, value := range required {
		if !values[value] {
			return false
		}
	}
	return true
}

// Validation and the bounded mint call run under the same SQLite writer
// reservation as logout, account disablement and RBAC mutations. A revocation
// that commits first prevents minting; a previously issued token lasts at most
// its returned expiry. The platform has no per-Auth-session revocation API.
func mintAptevaDelegatedToken(ctx *sdk.AppCtx, pid string, pair tokenPair) (*aptevaDelegatedToken, error) {
	bindings, err := delegatedBindings(ctx)
	if err != nil || len(bindings) == 0 {
		return nil, err
	}
	token, r := pair.access, pair.request
	if r == nil || len(token) == 0 || len(token) > 16000 {
		return nil, errDelegatedSession
	}
	slug, err := peekJWTOrg(token)
	if err != nil || slug == "" {
		return nil, errDelegatedSession
	}
	org, err := dbGetOrgBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, errDelegatedSession
	}
	tx, err := beginAuthTx(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	org, err = dbGetOrgByID(tx, pid, org.ID)
	if err != nil {
		return nil, errDelegatedSession
	}
	keys, err := dbAllSigningKeys(tx, pid, org.ID)
	if err != nil {
		return nil, err
	}
	claims, err := jwtVerify(token, func(kid string) (ed25519.PublicKey, bool) { key, ok := keys[kid]; return key, ok })
	if err != nil {
		return nil, errDelegatedSession
	}
	sub, _ := claims["sub"].(string)
	uid, validID := parseUint(sub)
	if !validID || uid == 0 {
		return nil, errDelegatedSession
	}
	cid, _ := claims["azp"].(string)
	sid, _ := claims["sid"].(string)
	client, err := dbGetClientByClientID(tx, pid, cid)
	if err != nil {
		return nil, errDelegatedSession
	}
	user, err := dbGetUserByID(tx, pid, org.ID, uid)
	if err != nil || sessionEligibility(ctx, org, user, client) != nil {
		return nil, errDelegatedSession
	}
	aud := client.JWTAudience
	if aud == "" {
		aud = cid
	}
	version, ok := jwtInt64Claim(claims, "authorization_version")
	if !ok || version != user.AuthorizationVersion || claims["iss"] != orgBaseURL(ctx, r, org) || claims["aud"] != aud || claims["token_use"] != "access" || sid == "" {
		return nil, errDelegatedSession
	}
	if requireAllowedOrigin(client, r.Header.Get("Origin")) != nil {
		return nil, errDelegatedPolicy
	}
	var expiry string
	err = tx.QueryRow(`SELECT expires_at FROM auth_session_families WHERE id=? AND project_id=? AND organization_id=? AND user_id=? AND client_id=? AND revoked_at IS NULL`, sid, pid, org.ID, uid, cid).Scan(&expiry)
	sessionExpiry, parseErr := time.Parse(time.RFC3339, expiry)
	if err != nil || parseErr != nil || !sessionExpiry.After(time.Now()) {
		return nil, errDelegatedSession
	}
	authorization, err := dbAuthorizationContext(tx, pid, org, uid)
	if err != nil {
		return nil, err
	}
	profile := r.URL.Query().Get("delegated_profile")
	var selected *delegatedBinding
	for i := range bindings {
		b := &bindings[i]
		if b.ProjectID != pid || b.OrganizationSlug != org.Slug || b.ClientID != cid || (profile != "" && profile != b.Profile) {
			continue
		}
		if !hasAll(authorization.Roles, b.Roles) || !hasAll(authorization.Permissions, b.Permissions) {
			continue
		}
		if selected != nil {
			return nil, errDelegatedPolicy
		}
		selected = b
	}
	if selected == nil {
		return nil, errDelegatedPolicy
	}
	// Bound key creation even for authenticated callers. Commit the counter only
	// after a successful call; HTTP routes also have the common IP limiter.
	if err := consumeRate(tx, pid+":delegated:"+sid, 12, time.Minute); err != nil {
		return nil, errDelegatedPolicy
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	issuer := os.Getenv("APTEVA_APP_TOKEN")
	if base == "" || issuer == "" {
		return nil, errors.New("delegated gateway unavailable")
	}
	raw, err := json.Marshal(map[string]any{
		"project_id": pid, "oauth_client_id": selected.PolicyClientID,
		"subject_type": "user", "subject_id": uintToStr(uid),
		"subject_email": user.Email, "organization_id": uintToStr(org.ID),
		"organization_slug": org.Slug, "allowed_origins": client.AllowedOrigins,
		"expires_in": 60,
	})
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base+"/api/apps/callback/delegated-keys/mint", bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("delegated gateway unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+issuer)
	req.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, errors.New("delegated mint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("delegated mint rejected")
	}
	var minted aptevaDelegatedToken
	if err = json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&minted); err != nil {
		return nil, errors.New("invalid delegated response")
	}
	expiresAt, err := time.Parse(time.RFC3339, minted.ExpiresAt)
	now := time.Now().UTC()
	remaining := expiresAt.Sub(now)
	if err != nil || remaining < time.Second || remaining > delegatedMaxTTL || minted.ExpiresIn < 1 || minted.ExpiresIn > 60 || remaining > time.Duration(minted.ExpiresIn)*time.Second+time.Second || expiresAt.After(sessionExpiry) || minted.AccessToken == "" || len(minted.AccessToken) > 12000 || minted.TokenType != "Bearer" || minted.ProjectID != pid || minted.OAuthClientID != selected.PolicyClientID || minted.Subject.Type != "user" || minted.Subject.ID != uintToStr(uid) || minted.Subject.OrganizationID != uintToStr(org.ID) || minted.Subject.OrganizationSlug != org.Slug {
		return nil, errors.New("invalid delegated response or lifetime")
	}
	if err = requestCtx.Err(); err != nil {
		return nil, errors.New("delegated mint cancelled")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// Advertise the conservative remaining lifetime, not the requested lifetime.
	minted.ExpiresIn = int(remaining.Seconds())
	return &minted, nil
}

func (a *App) handleDelegatedToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, "project required")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == r.Header.Get("Authorization") {
		httpErr(w, 401, "missing_bearer")
		return
	}
	minted, err := mintAptevaDelegatedToken(getAppCtx(r), pid, tokenPair{access: token, request: r})
	if errors.Is(err, errDelegatedSession) {
		httpErr(w, 401, "invalid_session")
		return
	}
	if errors.Is(err, errDelegatedPolicy) || (err == nil && minted == nil) {
		httpErr(w, 403, "delegated_access_denied")
		return
	}
	if err != nil {
		httpErr(w, 503, "delegated_token_unavailable")
		return
	}
	httpJSON(w, map[string]any{"apteva_access_token": minted.AccessToken, "apteva_expires_in": minted.ExpiresIn, "apteva_expires_at": minted.ExpiresAt})
}
