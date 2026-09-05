package main

// auth_client.go — the bridge to the Auth app.
//
// Games never stores credentials. Login, linking, disabling, and JWKS
// all go through Auth's MCP tools over the platform's app-to-app bridge
// (ctx.PlatformAPI().CallAppResult). Access tokens are Auth's EdDSA
// JWTs; games verifies them locally against Auth's JWKS, cached per
// process and refreshed on an unknown key id.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	authApp           = "auth"
	settingAuthClient = "auth_client_id"
	jwksTTL           = 10 * time.Minute
	jwksMissBackoff   = 15 * time.Second
)

func authOrg(ctx *sdk.AppCtx) string { return cfgStr(ctx, "auth_organization_slug", "default") }

// identitySubject hashes a raw device or custom id before it leaves the
// app: Auth only ever stores the digest, so a leaked identity table
// cannot be replayed as device ids.
func identitySubject(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

func callAuth(ctx *sdk.AppCtx, pid, tool string, in map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	if in == nil {
		in = map[string]any{}
	}
	in["_project_id"] = pid
	if _, ok := in["organization_slug"]; !ok {
		in["organization_slug"] = authOrg(ctx)
	}
	if err := ctx.PlatformAPI().CallAppResult(authApp, tool, in, out); err != nil {
		return fmt.Errorf("auth.%s: %w", tool, err)
	}
	return nil
}

// ensureAuthClient registers one native OAuth client for this install
// in the configured Auth organization and remembers its id. The client
// is the "title" from Auth's point of view: sessions, rate limits, and
// audit rows hang off it.
func ensureAuthClient(ctx *sdk.AppCtx, pid string) (string, error) {
	db := ctx.AppDB()
	if v, err := dbGetSetting(db, pid, settingAuthClient); err != nil {
		return "", err
	} else if v != "" {
		return v, nil
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := callAuth(ctx, pid, "auth_clients_create", map[string]any{"name": "games", "type": "native"}, &out); err != nil {
		return "", err
	}
	if out.ClientID == "" {
		return "", errors.New("auth_clients_create returned no client_id")
	}
	if err := dbSetSetting(db, pid, settingAuthClient, out.ClientID); err != nil {
		return "", err
	}
	return out.ClientID, nil
}

type authUser struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Status      string `json:"status"`
}

type authLoginResult struct {
	User             authUser `json:"user"`
	Created          bool     `json:"created"`
	AccessToken      string   `json:"access_token"`
	RefreshToken     string   `json:"refresh_token"`
	ExpiresIn        int      `json:"expires_in"`
	TokenType        string   `json:"token_type"`
	ClientID         string   `json:"client_id"`
	OrganizationSlug string   `json:"organization_slug"`
}

func authLoginIdentity(ctx *sdk.AppCtx, pid, provider, subject, displayName, ip, ua string) (*authLoginResult, error) {
	clientID, err := ensureAuthClient(ctx, pid)
	if err != nil {
		return nil, err
	}
	in := map[string]any{
		"client_id": clientID, "provider": provider, "provider_user_id": subject,
		"ip": ip, "user_agent": ua,
	}
	if displayName != "" {
		in["display_name"] = displayName
	}
	var out authLoginResult
	if err := callAuth(ctx, pid, "auth_public_login_identity", in, &out); err != nil {
		return nil, err
	}
	if out.User.ID == 0 || out.AccessToken == "" {
		return nil, errors.New("auth returned no session")
	}
	return &out, nil
}

// authResolveIdentity maps a provider subject to an Auth user id without
// minting a session. 0 means unknown.
func authResolveIdentity(ctx *sdk.AppCtx, pid, provider, subject string) (int64, error) {
	var out struct {
		Identities []struct {
			UserID int64 `json:"user_id"`
		} `json:"identities"`
	}
	if err := callAuth(ctx, pid, "auth_identities_list", map[string]any{"provider": provider, "provider_user_id": subject}, &out); err != nil {
		return 0, err
	}
	if len(out.Identities) == 0 {
		return 0, nil
	}
	return out.Identities[0].UserID, nil
}

func authLinkIdentity(ctx *sdk.AppCtx, pid string, authUserID int64, provider, subject string) error {
	var out map[string]any
	return callAuth(ctx, pid, "auth_identities_link", map[string]any{
		"user_id": authUserID, "provider": provider, "provider_user_id": subject,
	}, &out)
}

func authDisableUser(ctx *sdk.AppCtx, pid string, authUserID int64, reason string) error {
	var out map[string]any
	return callAuth(ctx, pid, "auth_users_disable", map[string]any{"user_id": authUserID, "reason": reason}, &out)
}

func authEnableUser(ctx *sdk.AppCtx, pid string, authUserID int64) error {
	var out map[string]any
	return callAuth(ctx, pid, "auth_users_enable", map[string]any{"user_id": authUserID}, &out)
}

func authRevokeSessions(ctx *sdk.AppCtx, pid string, authUserID int64) error {
	var out map[string]any
	return callAuth(ctx, pid, "auth_users_revoke_sessions", map[string]any{"user_id": authUserID}, &out)
}

// ─── JWKS cache + JWT verification ───────────────────────────────────

type jwksCache struct {
	mu       sync.Mutex
	keys     map[string]ed25519.PublicKey
	fetched  time.Time
	lastMiss time.Time
}

var playerKeys = &jwksCache{}

func resetJWKSCache() { playerKeys = &jwksCache{} }

func (c *jwksCache) key(ctx *sdk.AppCtx, pid, kid string) (ed25519.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok && time.Since(c.fetched) < jwksTTL {
		return k, nil
	}
	if _, ok := c.keys[kid]; !ok && c.keys != nil && time.Since(c.lastMiss) < jwksMissBackoff {
		return nil, errors.New("unknown signing key")
	}
	var out struct {
		Keys []struct {
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := callAuth(ctx, pid, "auth_jwks_get", nil, &out); err != nil {
		// A transient Auth outage must not log every player out: a key
		// we already hold keeps verifying until the fetch succeeds.
		if k, ok := c.keys[kid]; ok {
			return k, nil
		}
		return nil, err
	}
	keys := make(map[string]ed25519.PublicKey, len(out.Keys))
	for _, k := range out.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys[k.Kid] = ed25519.PublicKey(raw)
	}
	c.keys = keys
	c.fetched = time.Now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	c.lastMiss = time.Now()
	return nil, errors.New("unknown signing key")
}

type playerClaims struct {
	AuthUserID int64
	Kind       string
	Org        string
	ExpiresAt  time.Time
}

// verifyPlayerToken checks an Auth access token: EdDSA signature against
// the cached JWKS, expiry, subject, and organization.
func verifyPlayerToken(ctx *sdk.AppCtx, pid, token string) (*playerClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed token")
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, errors.New("malformed token")
	}
	if h.Alg != "EdDSA" {
		return nil, fmt.Errorf("unexpected alg %q", h.Alg)
	}
	pub, err := playerKeys.key(ctx, pid, h.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed token")
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("signature mismatch")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed token")
	}
	var c struct {
		Sub  string  `json:"sub"`
		Kind string  `json:"kind"`
		Org  string  `json:"org"`
		Exp  float64 `json:"exp"`
	}
	if err := json.Unmarshal(cb, &c); err != nil {
		return nil, errors.New("malformed token")
	}
	if c.Exp > 0 && float64(time.Now().Unix()) >= c.Exp {
		return nil, errors.New("token expired")
	}
	uid, err := strconv.ParseInt(c.Sub, 10, 64)
	if err != nil || uid <= 0 {
		return nil, errors.New("token has no subject")
	}
	if c.Org != "" && c.Org != authOrg(ctx) {
		return nil, errors.New("token issued for another organization")
	}
	return &playerClaims{AuthUserID: uid, Kind: c.Kind, Org: c.Org, ExpiresAt: time.Unix(int64(c.Exp), 0)}, nil
}
