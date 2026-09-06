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

func authOrg(ctx *sdk.AppCtx, scope GameScope) string {
	g, err := getGame(ctx.AppDB(), scope.ProjectID, scope.GameID)
	if err != nil {
		return ""
	}
	return g.AuthOrganization
}

// identitySubject hashes a raw device or custom id before it leaves the
// app: Auth only ever stores the digest, so a leaked identity table
// cannot be replayed as device ids.
func identitySubject(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

func callAuth(ctx *sdk.AppCtx, scope GameScope, tool string, in map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	if in == nil {
		in = map[string]any{}
	}
	in["_project_id"] = scope.ProjectID
	if _, ok := in["organization_slug"]; !ok {
		in["organization_slug"] = authOrg(ctx, scope)
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
func ensureAuthClient(ctx *sdk.AppCtx, scope GameScope) (string, error) {
	provisioningMu.Lock()
	defer provisioningMu.Unlock()
	db := ctx.AppDB()
	if v, err := dbGetSetting(db, scope, settingAuthClient); err != nil {
		return "", err
	} else if v != "" {
		return v, nil
	}
	g, err := getGame(db, scope.ProjectID, scope.GameID)
	if err != nil {
		return "", err
	}
	if !g.Legacy {
		var orgs struct {
			Organizations []struct {
				Slug string `json:"slug"`
			} `json:"organizations"`
		}
		if err := callAuth(ctx, scope, "auth_orgs_list", nil, &orgs); err != nil {
			return "", err
		}
		found := false
		for _, org := range orgs.Organizations {
			if org.Slug == g.AuthOrganization {
				found = true
			}
		}
		if !found {
			var out any
			if err := callAuth(ctx, scope, "auth_orgs_create", map[string]any{"slug": g.AuthOrganization, "name": g.Name}, &out); err != nil {
				return "", err
			}
		}
	}
	name := "games-" + g.ID
	var clients struct {
		Clients []struct {
			ClientID string `json:"client_id"`
			Name     string `json:"name"`
			Status   string `json:"status"`
		} `json:"clients"`
	}
	if err := callAuth(ctx, scope, "auth_clients_list", nil, &clients); err != nil {
		return "", err
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	for _, c := range clients.Clients {
		if c.Name == name && c.Status != "disabled" {
			out.ClientID = c.ClientID
			break
		}
	}
	if out.ClientID == "" {
		if err := callAuth(ctx, scope, "auth_clients_create", map[string]any{"name": name, "type": "native"}, &out); err != nil {
			return "", err
		}
	}
	if out.ClientID == "" {
		return "", errors.New("auth_clients_create returned no client_id")
	}
	if err := dbSetSetting(db, scope, settingAuthClient, out.ClientID); err != nil {
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

func authLoginIdentity(ctx *sdk.AppCtx, scope GameScope, provider, subject, displayName, ip, ua string) (*authLoginResult, error) {
	clientID, err := ensureAuthClient(ctx, scope)
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
	if err := callAuth(ctx, scope, "auth_public_login_identity", in, &out); err != nil {
		return nil, err
	}
	if out.User.ID == 0 || out.AccessToken == "" {
		return nil, errors.New("auth returned no session")
	}
	return &out, nil
}

// authResolveIdentity maps a provider subject to an Auth user id without
// minting a session. 0 means unknown.
func authResolveIdentity(ctx *sdk.AppCtx, scope GameScope, provider, subject string) (int64, error) {
	var out struct {
		Identities []struct {
			UserID int64 `json:"user_id"`
		} `json:"identities"`
	}
	if err := callAuth(ctx, scope, "auth_identities_list", map[string]any{"provider": provider, "provider_user_id": subject}, &out); err != nil {
		return 0, err
	}
	if len(out.Identities) == 0 {
		return 0, nil
	}
	return out.Identities[0].UserID, nil
}

func authLinkIdentity(ctx *sdk.AppCtx, scope GameScope, authUserID int64, provider, subject string) error {
	var out map[string]any
	return callAuth(ctx, scope, "auth_identities_link", map[string]any{
		"user_id": authUserID, "provider": provider, "provider_user_id": subject,
	}, &out)
}

func authDisableUser(ctx *sdk.AppCtx, scope GameScope, authUserID int64, reason string) error {
	var out map[string]any
	return callAuth(ctx, scope, "auth_users_disable", map[string]any{"user_id": authUserID, "reason": reason}, &out)
}

func authEnableUser(ctx *sdk.AppCtx, scope GameScope, authUserID int64) error {
	var out map[string]any
	return callAuth(ctx, scope, "auth_users_enable", map[string]any{"user_id": authUserID}, &out)
}

func authRevokeSessions(ctx *sdk.AppCtx, scope GameScope, authUserID int64) error {
	var out map[string]any
	return callAuth(ctx, scope, "auth_users_revoke_sessions", map[string]any{"user_id": authUserID}, &out)
}

// ─── JWKS cache + JWT verification ───────────────────────────────────

type jwksCache struct {
	mu                   sync.Mutex
	keys                 map[string]ed25519.PublicKey
	issuer               string
	fetched, lastAttempt time.Time
	inFlight             chan struct{}
}

var cachesMu sync.Mutex
var gameKeyCaches = map[GameScope]*jwksCache{}

func resetJWKSCache() {
	cachesMu.Lock()
	defer cachesMu.Unlock()
	gameKeyCaches = map[GameScope]*jwksCache{}
}
func scopeKeys(scope GameScope) *jwksCache {
	cachesMu.Lock()
	defer cachesMu.Unlock()
	c := gameKeyCaches[scope]
	if c == nil {
		c = &jwksCache{}
		gameKeyCaches[scope] = c
	}
	return c
}
func (c *jwksCache) key(ctx *sdk.AppCtx, scope GameScope, kid string) (ed25519.PublicKey, string, error) {
	for {
		c.mu.Lock()
		key := c.keys[kid]
		issuer := c.issuer
		age := time.Since(c.fetched)
		if key != nil && age < jwksTTL {
			c.mu.Unlock()
			return key, issuer, nil
		}
		if c.inFlight != nil {
			done := c.inFlight
			c.mu.Unlock()
			if key != nil && age < 2*jwksTTL {
				return key, issuer, nil
			}
			<-done
			continue
		}
		if time.Since(c.lastAttempt) < jwksMissBackoff {
			c.mu.Unlock()
			if key != nil && age < 2*jwksTTL {
				return key, issuer, nil
			}
			return nil, "", errors.New("signing key unavailable")
		}
		c.inFlight = make(chan struct{})
		c.lastAttempt = time.Now()
		c.mu.Unlock()
		var out struct {
			Issuer string `json:"issuer"`
			Keys   []struct {
				Kid string `json:"kid"`
				X   string `json:"x"`
				Kty string `json:"kty"`
				Crv string `json:"crv"`
				Use string `json:"use"`
			} `json:"keys"`
		}
		err := callAuth(ctx, scope, "auth_jwks_get", nil, &out)
		c.mu.Lock()
		if err == nil && out.Issuer != "" {
			keys := map[string]ed25519.PublicKey{}
			for _, k := range out.Keys {
				raw, e := base64.RawURLEncoding.DecodeString(k.X)
				if e == nil && len(raw) == ed25519.PublicKeySize && k.Kty == "OKP" && k.Crv == "Ed25519" && (k.Use == "" || k.Use == "sig") {
					keys[k.Kid] = ed25519.PublicKey(raw)
				}
			}
			c.keys = keys
			c.issuer = out.Issuer
			c.fetched = time.Now()
		} else if err == nil {
			err = errors.New("Auth JWKS has no issuer")
		}
		close(c.inFlight)
		c.inFlight = nil
		key = c.keys[kid]
		issuer = c.issuer
		age = time.Since(c.fetched)
		c.mu.Unlock()
		if key != nil && age < 2*jwksTTL {
			return key, issuer, nil
		}
		if err != nil {
			return nil, "", err
		}
		return nil, "", errors.New("unknown signing key")
	}
}

type playerClaims struct {
	AuthUserID int64
	Kind       string
	Org        string
	ExpiresAt  time.Time
}

// verifyPlayerToken checks an Auth access token: EdDSA signature against
// the cached JWKS, expiry, subject, and organization.
func verifyPlayerToken(ctx *sdk.AppCtx, scope GameScope, token string) (*playerClaims, error) {
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
	pub, issuer, err := scopeKeys(scope).key(ctx, scope, h.Kid)
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
		Sub  string          `json:"sub"`
		Kind string          `json:"kind"`
		Org  string          `json:"org"`
		Exp  int64           `json:"exp"`
		Iss  string          `json:"iss"`
		Aud  json.RawMessage `json:"aud"`
		Azp  string          `json:"azp"`
		Nbf  int64           `json:"nbf"`
	}
	if err := json.Unmarshal(cb, &c); err != nil {
		return nil, errors.New("malformed token")
	}
	if c.Exp <= 0 || time.Now().Unix() >= c.Exp {
		return nil, errors.New("token expired")
	}
	uid, err := strconv.ParseInt(c.Sub, 10, 64)
	if err != nil || uid <= 0 {
		return nil, errors.New("token has no subject")
	}
	if c.Org == "" || c.Org != authOrg(ctx, scope) {
		return nil, errors.New("token issued for another organization")
	}
	clientID, err := dbGetSetting(ctx.AppDB(), scope, settingAuthClient)
	if err != nil {
		return nil, err
	}
	audiences := []string{}
	var aud string
	if json.Unmarshal(c.Aud, &aud) == nil {
		audiences = append(audiences, aud)
	} else {
		_ = json.Unmarshal(c.Aud, &audiences)
	}
	validAudience := false
	for _, a := range audiences {
		if a == clientID && a != "" {
			validAudience = true
		}
	}
	if !validAudience || c.Azp != clientID || c.Iss != issuer || issuer == "" || c.Nbf > time.Now().Unix() {
		return nil, errors.New("token is not valid for this game")
	}
	return &playerClaims{AuthUserID: uid, Kind: c.Kind, Org: c.Org, ExpiresAt: time.Unix(int64(c.Exp), 0)}, nil
}

func gameIdentity(ctx *sdk.AppCtx, scope GameScope, raw string) string {
	g, err := getGame(ctx.AppDB(), scope.ProjectID, scope.GameID)
	if err != nil {
		return ""
	}
	if g.Legacy {
		return identitySubject(raw)
	}
	return identitySubject(scope.GameID + "\x00" + strings.TrimSpace(raw))
}
