package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"
)

// Security is API-owned, never read from GraphQL variables or provider claims.
// Existing APIs remain platform-only. Public execution requires explicit Auth.
type securityPolicy struct {
	Mode        string              `json:"mode"`
	TenantID    string              `json:"tenant_id,omitempty"`
	Environment string              `json:"environment,omitempty"`
	Claims      []string            `json:"claims,omitempty"`
	Permissions []string            `json:"permissions,omitempty"`
	Fields      map[string][]string `json:"fields,omitempty"`
}
type requestIdentity struct {
	Subject     string
	Issuer      string
	Project     string
	API         string
	Tenant      string
	Claims      map[string]any
	Permissions []string
	Expires     time.Time
	RequestID   string
}
type identityKey struct{}

func unauthenticated() error {
	return &graphqlError{Code: "unauthenticated", Message: "verified user authentication required"}
}
func securityIdentity(ctx context.Context) *requestIdentity {
	value, _ := ctx.Value(identityKey{}).(*requestIdentity)
	return value
}

var securityFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*$`)
var securityClaimPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func parseSecurity(raw any) (securityPolicy, error) {
	var p securityPolicy
	b, err := json.Marshal(raw)
	if err != nil || len(b) > 32<<10 {
		return p, invalid("invalid security policy")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return p, invalid("invalid security policy fields")
	}
	if p.Mode != "platform" && p.Mode != "auth" {
		return p, invalid("security.mode must be platform or auth")
	}
	if p.Mode == "platform" {
		if p.TenantID != "" || p.Environment != "" || len(p.Claims)+len(p.Permissions)+len(p.Fields) > 0 {
			return p, invalid("platform mode cannot configure user policies")
		}
		return p, nil
	}
	if !identityText(p.TenantID) {
		return p, invalid("Auth tenant_id is required")
	}
	if p.Environment != "development" && p.Environment != "staging" && p.Environment != "production" {
		return p, invalid("a fixed environment is required")
	}
	if len(p.Claims) > 32 || len(p.Permissions) > 64 || len(p.Fields) > 256 {
		return p, invalid("security policy exceeds limits")
	}
	for _, claim := range p.Claims {
		if !safeIdentityClaim(claim) {
			return p, invalid("invalid or sensitive claim name")
		}
	}
	for field, perms := range p.Fields {
		if !securityFieldPattern.MatchString(field) || len(perms) > 64 {
			return p, invalid("invalid field policy")
		}
		for _, perm := range perms {
			if !identityText(perm) {
				return p, invalid("invalid permission")
			}
		}
	}
	for _, perm := range p.Permissions {
		if !identityText(perm) {
			return p, invalid("invalid permission")
		}
	}
	return p, nil
}
func identityText(s string) bool {
	return s != "" && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func safeIdentityClaim(s string) bool {
	if !securityClaimPattern.MatchString(s) {
		return false
	}
	low := strings.ToLower(s)
	for _, part := range []string{"password", "passwd", "token", "secret", "credential", "session", "cookie", "apikey", "api_key", "private_key"} {
		if strings.Contains(low, part) {
			return false
		}
	}
	return !slices.Contains([]string{"issuer", "iss", "subject", "sub", "project_id", "tenant_id", "principal", "authorization", "auth", "function_ids"}, low)
}
func safeIdentityValue(v any) bool {
	switch x := v.(type) {
	case string:
		return len(x) <= 1024
	case bool, json.Number:
		return true
	case []any:
		if len(x) > 256 {
			return false
		}
		for _, item := range x {
			switch item.(type) {
			case string, bool, json.Number:
				if !safeIdentityValue(item) {
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
func getSecurity(db *sql.DB, project, api string) (securityPolicy, error) {
	var raw string
	err := db.QueryRow(`SELECT policy_json FROM graphql_security WHERE project_id=? AND api_slug=?`, project, normalizeAPISlug(api)).Scan(&raw)
	if err == sql.ErrNoRows {
		return securityPolicy{Mode: "platform"}, nil
	}
	if err != nil {
		return securityPolicy{}, err
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return securityPolicy{}, internal("invalid stored security policy")
	}
	return parseSecurity(value)
}
func setSecurity(db *sql.DB, project, api string, raw any) (securityPolicy, error) {
	p, err := parseSecurity(raw)
	if err != nil {
		return p, err
	}
	if _, err = resolveGraphQLAPI(db, project, api); err != nil {
		return p, err
	}
	b, _ := json.Marshal(p)
	_, err = db.Exec(`INSERT INTO graphql_security(project_id,api_slug,policy_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(project_id,api_slug) DO UPDATE SET policy_json=excluded.policy_json,updated_at=excluded.updated_at`, project, normalizeAPISlug(api), string(b), nowUTC())
	return p, err
}

// Auth /me validates the session signature, project, and revocation. Only its
// server-managed authorization object supplies claims (never user.metadata).
func (a *App) authenticateGraphQL(r *http.Request, project, api string, p securityPolicy) (*requestIdentity, error) {
	header := strings.Fields(r.Header.Get("Authorization"))
	if len(header) != 2 || !strings.EqualFold(header[0], "Bearer") || len(header[1]) > 16000 {
		return nil, unauthenticated()
	}
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		return nil, internal("authentication service unavailable")
	}
	authCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(authCtx, http.MethodGet, base+"/api/apps/auth/me?project_id="+url.QueryEscape(project), nil)
	if err != nil {
		return nil, internal("authentication service unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+header[1])
	client := *a.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, internal("authentication service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return nil, unauthenticated()
	}
	if resp.StatusCode == 403 {
		return nil, forbidden("identity scope denied")
	}
	if resp.StatusCode != 200 {
		return nil, internal("authentication service unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, internal("invalid authentication response")
	}
	var out struct {
		User struct {
			ID json.Number `json:"id"`
		} `json:"user"`
		Org           string         `json:"org"`
		Authorization map[string]any `json:"authorization"`
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if decoder.Decode(&out) != nil {
		return nil, internal("invalid authentication response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, internal("invalid authentication response")
	}
	id, err := out.User.ID.Int64()
	if err != nil || id <= 0 {
		return nil, unauthenticated()
	}
	if out.Org != p.TenantID {
		return nil, forbidden("identity tenant mismatch")
	}
	// Read exp only AFTER Auth has validated this exact credential. It bounds
	// execution; it is never used as proof of signature or user identity.
	parts := strings.Split(header[1], ".")
	if len(parts) != 3 {
		return nil, unauthenticated()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, unauthenticated()
	}
	var token struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &token) != nil || token.Exp <= time.Now().Unix() {
		return nil, unauthenticated()
	}
	identity := &requestIdentity{Subject: strconv.FormatInt(id, 10), Issuer: "apteva:auth:" + out.Org, Project: project, API: normalizeAPISlug(api), Tenant: out.Org, Claims: map[string]any{}, Expires: time.Unix(token.Exp, 0), RequestID: uuid.NewString()}
	for _, name := range p.Claims {
		if value, ok := out.Authorization[name]; ok {
			if !safeIdentityValue(value) {
				return nil, internal("invalid authorization claim")
			}
			identity.Claims[name] = value
		}
	}
	if perms, ok := out.Authorization["permissions"].([]any); ok {
		for _, value := range perms {
			if perm, ok := value.(string); ok {
				identity.Permissions = append(identity.Permissions, perm)
			}
		}
	}
	claims, _ := json.Marshal(identity.Claims)
	if len(claims) > 16<<10 {
		return nil, internal("authorization claims exceed limit")
	}
	return identity, nil
}

func authorizeIdentity(ctx context.Context, project, api string, p securityPolicy, environment string) error {
	if p.Mode == "platform" {
		return nil
	}
	i := securityIdentity(ctx)
	if i == nil || !time.Now().Before(i.Expires) {
		return unauthenticated()
	}
	if i.Project != project || i.API != normalizeAPISlug(api) || i.Tenant != p.TenantID || i.Issuer != "apteva:auth:"+p.TenantID || normalizeEnvironment(environment) != p.Environment {
		return forbidden("identity or environment scope denied")
	}
	return requirePermissions(i, p.Permissions)
}
func requirePermissions(i *requestIdentity, perms []string) error {
	if len(perms) == 0 {
		return nil
	}
	if i == nil {
		return unauthenticated()
	}
	for _, perm := range perms {
		if !slices.Contains(i.Permissions, perm) {
			return forbidden("field access denied")
		}
	}
	return nil
}

// Preflight the whole operation before any resolver or mutation runs. This
// includes nested scalar fields that use the optimized projection/batch paths.
func authorizeSelection(ctx context.Context, p securityPolicy, selection ast.SelectionSet) error {
	for _, item := range selection {
		switch f := item.(type) {
		case *ast.Field:
			if f.ObjectDefinition != nil {
				if err := requirePermissions(securityIdentity(ctx), p.Fields[f.ObjectDefinition.Name+"."+f.Name]); err != nil {
					return err
				}
			}
			if err := authorizeSelection(ctx, p, f.SelectionSet); err != nil {
				return err
			}
		case *ast.InlineFragment:
			if err := authorizeSelection(ctx, p, f.SelectionSet); err != nil {
				return err
			}
		case *ast.FragmentSpread:
			if f.Definition != nil {
				if err := authorizeSelection(ctx, p, f.Definition.SelectionSet); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// A separate Auth-only entry point preserves the platform gate on the existing
// /graphql, /admin and MCP surfaces. No anonymous fallback, even on default APIs.
func (a *App) handlePublicGraphQL(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	project := a.ctx.CurrentProject()
	if project == "" {
		writeGraphQLError(w, 403, forbidden("authenticated endpoints require a project installation"))
		return
	}
	slug := strings.TrimPrefix(r.URL.Path, "/public/graphql/")
	if validateAPISlug(slug) != nil {
		writeGraphQLError(w, 404, invalid("API not found"))
		return
	}
	if r.Method != http.MethodPost {
		writeGraphQLError(w, 405, invalid("POST required"))
		return
	}
	if _, err := resolveGraphQLAPI(a.ctx.AppDB(), project, slug); err != nil {
		writeGraphQLError(w, 404, invalid("API not found"))
		return
	}
	p, err := getSecurity(a.ctx.AppReadDB(), project, slug)
	if err != nil || p.Mode != "auth" {
		writeGraphQLError(w, 403, forbidden("user-authenticated API is not enabled"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	identity, err := a.authenticateGraphQL(r, project, slug, p)
	if err != nil {
		status := 401
		if errorCode(err) == "permission_denied" {
			status = 403
		}
		if errorCode(err) == "internal_error" {
			status = 503
		}
		writeGraphQLError(w, status, err)
		return
	}
	ctx, cancelExpiry := context.WithDeadline(ctx, identity.Expires)
	defer cancelExpiry()
	ctx = context.WithValue(ctx, identityKey{}, identity)
	clone := r.Clone(ctx)
	clone.URL.Path = "/graphql/" + slug
	// The authenticated environment is pinned by API policy, not browser input.
	clone.Header.Set("X-GraphQL-Environment", p.Environment)
	w.Header().Set("X-Request-ID", identity.RequestID)
	a.handleGraphQL(w, clone)
}
