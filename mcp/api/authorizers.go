package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Principal is gateway-owned identity, separate from the browser's body.
// Claims come only from the verified provider's authorization response.
type Principal struct {
	Issuer    string         `json:"issuer"`
	Subject   string         `json:"subject"`
	ProjectID string         `json:"project_id"`
	TenantID  string         `json:"tenant_id,omitempty"`
	Claims    map[string]any `json:"claims,omitempty"`
}

type authorizerPolicy struct {
	Kind     string   `json:"kind"`
	Provider string   `json:"provider,omitempty"`
	App      string   `json:"app,omitempty"`
	Path     string   `json:"path,omitempty"`
	Issuer   string   `json:"issuer,omitempty"`
	TenantID string   `json:"tenant_id,omitempty"`
	Claims   []string `json:"claims,omitempty"`
}

var authorizerName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var claimName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func parseAuthPolicy(raw string) (authorizerPolicy, error) {
	obj, err := policyObject(raw)
	if err != nil {
		return authorizerPolicy{}, err
	}
	policy := authorizerPolicy{Kind: "public"}
	decoder := json.NewDecoder(strings.NewReader(defaultJSON(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, errors.New("invalid auth policy: " + err.Error())
	}
	for name, value := range obj {
		switch name {
		case "kind", "provider", "app", "path", "issuer", "tenant_id", "claims":
		default:
			return policy, errors.New("unsupported auth field: " + name)
		}
		if value == nil {
			return policy, errors.New("auth." + name + " cannot be null")
		}
	}
	if len(obj) > 0 && obj["kind"] == nil {
		return policy, errors.New("auth.kind is required")
	}
	switch policy.Kind {
	case "public", "api_key":
		if len(obj) > 1 {
			return policy, errors.New("this auth kind does not accept authorizer settings")
		}
	case "auth_jwt", "authorizer":
		if policy.Kind == "auth_jwt" {
			policy.Provider = "auth"
		}
		switch policy.Provider {
		case "auth":
			if policy.App != "" || policy.Path != "" || policy.Issuer != "" {
				return policy, errors.New("Auth uses its built-in endpoint and issuer")
			}
			if v, ok := obj["provider"]; ok && v != "auth" {
				return policy, errors.New("auth_jwt uses the Auth provider")
			}
		case "app":
			if !authorizerName.MatchString(policy.App) || policy.App == "api" {
				return policy, errors.New("authorizer app must name another installed app")
			}
			if policy.Path == "" || strings.ContainsAny(policy.Path, "%?#\\") || strings.HasPrefix(policy.Path, "//") || validateRouteTargetPath(policy.Path) != nil {
				return policy, errors.New("authorizer path must be a literal absolute app path")
			}
			if !identityString(policy.Issuer) {
				return policy, errors.New("authorizer issuer is required")
			}
		default:
			return policy, errors.New("auth.provider must be auth or app")
		}
		if policy.TenantID != "" && !identityString(policy.TenantID) {
			return policy, errors.New("invalid authorizer tenant_id")
		}
		if len(policy.Claims) > 32 {
			return policy, errors.New("at most 32 authorization claims may be allowed")
		}
		seen := map[string]bool{}
		for _, name := range policy.Claims {
			if !safeClaimName(name) || seen[name] {
				return policy, errors.New("invalid, sensitive, or duplicate authorization claim: " + name)
			}
			seen[name] = true
		}
	default:
		return policy, errors.New("auth.kind must be public, api_key, auth_jwt, or authorizer")
	}
	return policy, nil
}

func identityString(s string) bool {
	return strings.TrimSpace(s) == s && s != "" && len(s) <= 512 && !strings.ContainsAny(s, "\r\n\x00")
}

func safeClaimName(name string) bool {
	if !claimName.MatchString(name) {
		return false
	}
	low := strings.ToLower(name)
	for _, sensitive := range []string{"password", "passwd", "token", "secret", "credential", "session", "cookie", "api_key", "apikey", "private_key"} {
		if strings.Contains(low, sensitive) {
			return false
		}
	}
	switch low {
	case "issuer", "iss", "subject", "sub", "project_id", "tenant_id", "principal", "authorization", "auth":
		return false
	}
	return true
}

func permittedClaims(source map[string]any, names []string) (map[string]any, error) {
	out := map[string]any{}
	for _, name := range names {
		value, ok := source[name]
		if !ok {
			continue
		}
		// Flat scalar claims/arrays only: a permitted claim cannot smuggle a user
		// profile, session object, or nested credentials into the principal.
		if !safeClaimValue(value) {
			return nil, errors.New("invalid authorization claim value")
		}
		out[name] = value
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("authorization claims exceed 16 KiB")
	}
	return out, nil
}
func safeClaimValue(value any) bool {
	switch v := value.(type) {
	case string:
		return len(v) <= 1024
	case bool, json.Number:
		return true
	case []any:
		if len(v) > 256 {
			return false
		}
		for _, item := range v {
			switch item.(type) {
			case string, bool, json.Number:
				if !safeClaimValue(item) {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (a *App) authenticatePrincipal(r *http.Request, projectID string, policy authorizerPolicy) (*Principal, time.Time, error) {
	r, cancel := withAuthDeadline(r)
	defer cancel()
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return nil, time.Time{}, authFailure(401, "missing bearer token", nil)
	}
	if len(token) > 16000 {
		return nil, time.Time{}, authFailure(401, "invalid bearer token", nil)
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		return nil, time.Time{}, authFailure(503, "authentication service unavailable", nil)
	}
	var req *http.Request
	var err error
	if policy.Provider == "app" {
		// Browser credentials are sent only to the configured authorizer, inside
		// an authenticated, project-scoped platform app call. No browser URL or
		// header controls the provider, project, endpoint, or expected issuer.
		outbound := outboundAppToken()
		if outbound == "" {
			return nil, time.Time{}, authFailure(503, "authentication service unavailable", nil)
		}
		input, _ := json.Marshal(map[string]any{"credential": map[string]string{"type": "bearer", "token": token}, "project_id": projectID})
		target := base + "/api/apps/callback/apps/" + policy.App + "/proxy" + policy.Path + "?project_id=" + url.QueryEscape(projectID)
		req, err = http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(input))
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+outbound)
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, base+"/api/apps/auth/me?project_id="+url.QueryEscape(projectID), nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	if err != nil {
		return nil, time.Time{}, authBackendFailure(err)
	}
	req.Header.Set("X-Request-ID", gatewayRequestID(r.Context()))
	resp, err := a.performRequest(req)
	if err != nil {
		return nil, time.Time{}, authBackendFailure(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 401:
		return nil, time.Time{}, authFailure(401, "invalid or expired bearer token", nil)
	case 403:
		return nil, time.Time{}, authFailure(403, "access forbidden", nil)
	case 429, 503:
		return nil, time.Time{}, authFailure(503, "authentication service unavailable", nil)
	case 408, 504:
		return nil, time.Time{}, authFailure(504, "authentication service timed out", nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, time.Time{}, authFailure(502, "authentication service failed", nil)
	}
	raw, err := readBounded(resp.Body, 1<<20)
	if err != nil {
		return nil, time.Time{}, authBackendFailure(err)
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil || out == nil {
		return nil, time.Time{}, invalidAuthorizerResponse()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, time.Time{}, invalidAuthorizerResponse()
	}
	principal := &Principal{ProjectID: projectID}
	var expiry time.Time
	var claims map[string]any
	if policy.Provider == "app" {
		authenticated, _ := out["authenticated"].(bool)
		if !authenticated {
			return nil, time.Time{}, authFailure(401, "authentication rejected", nil)
		}
		identity, _ := out["principal"].(map[string]any)
		principal.Issuer, _ = identity["issuer"].(string)
		principal.Subject, _ = identity["subject"].(string)
		principal.TenantID, _ = identity["tenant_id"].(string)
		providerProject, _ := identity["project_id"].(string)
		if providerProject != projectID || principal.Issuer != policy.Issuer {
			return nil, time.Time{}, authFailure(403, "authorizer identity scope mismatch", nil)
		}
		expires, _ := out["expires_at"].(string)
		expiry, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, time.Time{}, invalidAuthorizerResponse()
		}
		if !expiry.After(time.Now()) {
			return nil, time.Time{}, authFailure(401, "expired authorizer identity", nil)
		}
		claims, _ = identity["claims"].(map[string]any)
	} else {
		user, _ := out["user"].(map[string]any)
		switch id := user["id"].(type) {
		case string:
			principal.Subject = id
		case json.Number:
			if n, e := id.Int64(); e == nil && n > 0 {
				principal.Subject = strconv.FormatInt(n, 10)
			}
		}
		principal.TenantID, _ = out["org"].(string)
		principal.Issuer = "apteva:auth"
		if principal.TenantID != "" {
			principal.Issuer += ":" + principal.TenantID
		}
		// Auth's server-managed authorization object is authoritative. In
		// particular user.metadata is user-editable and must never be copied.
		claims, _ = out["authorization"].(map[string]any)
		expiry = bearerExpiry(r)
	}
	if !identityString(principal.Subject) || (principal.TenantID != "" && !identityString(principal.TenantID)) {
		return nil, time.Time{}, invalidAuthorizerResponse()
	}
	if policy.TenantID != "" && principal.TenantID != policy.TenantID {
		return nil, time.Time{}, authFailure(403, "authorizer tenant mismatch", nil)
	}
	principal.Claims, err = permittedClaims(claims, policy.Claims)
	if err != nil {
		return nil, time.Time{}, invalidAuthorizerResponse()
	}
	return principal, expiry, nil
}

func invalidAuthorizerResponse() error {
	return authFailure(502, "invalid authentication service response", nil)
}

func authPolicySchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Empty route auth inherits API auth; otherwise replaces the full policy. authorizer selects provider auth or a bound app implementing the authorizer contract. Claims are an explicit allowlist; no claims by default.",
		"properties": map[string]any{
			"kind":      map[string]any{"type": "string", "enum": []string{"public", "api_key", "auth_jwt", "authorizer"}},
			"provider":  map[string]any{"type": "string", "enum": []string{"auth", "app"}},
			"app":       map[string]any{"type": "string", "description": "Installed and bound authorizer app name (provider=app)."},
			"path":      map[string]any{"type": "string", "description": "Absolute POST endpoint on the authorizer app (provider=app)."},
			"issuer":    map[string]any{"type": "string", "description": "Required expected issuer (provider=app)."},
			"tenant_id": map[string]any{"type": "string", "description": "Optional required tenant; Auth uses the organization slug."},
			"claims":    map[string]any{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]any{"type": "string"}},
		},
	}
}
