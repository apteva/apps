package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var providerGrantIDRE = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,160}$`)

func (a *App) toolProviderGrant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	appSlug := strings.TrimSpace(getStr(args, "app_slug"))
	parentConnID := int64Arg(args, "parent_connection_id")
	projectID := strings.TrimSpace(firstArg(args, "project_id", "_project_id"))
	if tenantID == "" || appSlug == "" || parentConnID <= 0 || projectID == "" {
		return nil, errors.New("tenant_id, app_slug, parent_connection_id, and project_id are required")
	}
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "provider grant")
	if err != nil {
		return nil, err
	}
	defer done()
	if t.Status == StatusSetupPending {
		return nil, errors.New("tenant is in setup_pending — finish admin registration first")
	}
	tenantKey, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}

	installID := int64Arg(args, "tenant_install_id")
	role := strings.TrimSpace(getStr(args, "tenant_role"))
	allowedTools := stringListArg(args, "allowed_tools")
	allowedDomains := normaliseProviderDomains(stringListArg(args, "allowed_domains"))
	allowedFrom := stringListArg(args, "allowed_from")
	if len(allowedFrom) == 0 {
		allowedFrom = stringListArg(args, "allowed_from_addresses")
	}
	grantID := strings.TrimSpace(getStr(args, "grant_id"))
	if grantID == "" {
		grantID = fmt.Sprintf("fleet:%s:%s:%d", tenantID, appSlug, parentConnID)
	}
	if !providerGrantIDRE.MatchString(grantID) {
		return nil, errors.New("grant_id must contain only letters, digits, colon, underscore, or hyphen")
	}
	name := strings.TrimSpace(getStr(args, "name"))
	if name == "" {
		name = "Delegated " + appSlug
		if len(allowedDomains) > 0 {
			name += " (" + strings.Join(allowedDomains, ",") + ")"
		}
	}

	scopedToken, tokenHash, err := newProviderGrantToken()
	if err != nil {
		return nil, err
	}
	controllerExecuteURL, err := a.providerGrantExecuteURL(ctx, tenantID, grantID)
	if err != nil {
		return nil, err
	}

	metadata := map[string]any{}
	if raw, ok := args["metadata"].(map[string]any); ok {
		metadata = raw
	}
	metadata["controller_app"] = "fleet"
	metadata["created_by"] = "tenant_provider_grant"
	metadataJSON, _ := json.Marshal(metadata)
	allowedToolsJSON, _ := json.Marshal(allowedTools)
	allowedDomainsJSON, _ := json.Marshal(allowedDomains)
	allowedFromJSON, _ := json.Marshal(allowedFrom)

	creds := map[string]string{
		"_apteva_delegated_provider": "1",
		"grant_id":                   grantID,
		"resource":                   "provider.connection",
		"controller_execute_url":     controllerExecuteURL,
		"controller_token":           scopedToken,
		"parent_connection_id":       fmt.Sprintf("%d", parentConnID),
		"allowed_tools":              string(allowedToolsJSON),
		"allowed_domains":            string(allowedDomainsJSON),
		"allowed_from":               string(allowedFromJSON),
		"metadata":                   string(metadataJSON),
	}

	var created struct {
		ID int64 `json:"id"`
	}
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return nil, err
	}
	if err := tenantJSON(context.Background(), baseURL, string(tenantKey), http.MethodPost, "/api/connections", map[string]any{
		"app_slug":    appSlug,
		"name":        name,
		"auth_type":   "delegated",
		"credentials": creds,
		"project_id":  projectID,
		"created_via": "app_install",
		"auto_mcp":    false,
	}, &created); err != nil {
		return nil, fmt.Errorf("create tenant delegated connection: %w", err)
	}
	if created.ID <= 0 {
		return nil, errors.New("tenant did not return connection id")
	}

	if installID > 0 && role != "" {
		if err := tenantJSON(context.Background(), baseURL, string(tenantKey), http.MethodPut, fmt.Sprintf("/api/apps/installs/%d/bindings", installID), map[string]any{
			role: created.ID,
		}, nil); err != nil {
			return nil, fmt.Errorf("bind tenant install role: %w", err)
		}
	}
	grant, err := a.store.upsertProviderGrant(ProviderGrant{
		TenantID:           tenantID,
		GrantID:            grantID,
		AppSlug:            appSlug,
		ParentConnectionID: parentConnID,
		TenantConnectionID: created.ID,
		TenantInstallID:    installID,
		TenantRole:         role,
		Status:             "active",
		AllowedTools:       allowedTools,
		AllowedDomains:     allowedDomains,
		AllowedFrom:        allowedFrom,
		Metadata:           metadata,
		TokenHash:          tokenHash,
	})
	if err != nil {
		_ = tenantJSON(context.Background(), baseURL, string(tenantKey), http.MethodDelete, fmt.Sprintf("/api/connections/%d", created.ID), nil, nil)
		return nil, err
	}
	_ = a.store.recordEvent(tenantID, "provider_grant.created", "tool:provider_grant", map[string]any{
		"grant_id": grantID, "app_slug": appSlug, "parent_connection_id": parentConnID,
		"tenant_connection_id": created.ID, "tenant_install_id": installID, "tenant_role": role,
	})
	if ctx != nil {
		ctx.Logger().Info("fleet: provider grant created", "tenant", tenantID, "grant_id", grantID, "app_slug", appSlug)
	}
	return map[string]any{"grant": grant, "tenant_connection_id": created.ID, "bound": installID > 0 && role != ""}, nil
}

func (a *App) toolProviderGrantList(_ *sdk.AppCtx, args map[string]any) (any, error) {
	grants, err := a.store.listProviderGrants(getStr(args, "tenant_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"grants": grants}, nil
}

func (a *App) toolProviderGrantRevoke(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	grantID := getStr(args, "grant_id")
	if tenantID == "" || grantID == "" {
		return nil, errors.New("tenant_id and grant_id are required")
	}
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return nil, err
	}
	done, err := a.beginTenantOperation(t.ID, "revoke provider grant")
	if err != nil {
		return nil, err
	}
	defer done()
	grant, err := a.store.getProviderGrant(tenantID, grantID)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return nil, err
	}
	if grant.TenantInstallID > 0 && grant.TenantRole != "" {
		_ = tenantJSON(context.Background(), baseURL, string(key), http.MethodPut, fmt.Sprintf("/api/apps/installs/%d/bindings", grant.TenantInstallID), map[string]any{
			grant.TenantRole: nil,
		}, nil)
	}
	if grant.TenantConnectionID > 0 {
		_ = tenantJSON(context.Background(), baseURL, string(key), http.MethodDelete, fmt.Sprintf("/api/connections/%d", grant.TenantConnectionID), nil, nil)
	}
	if err := a.store.setProviderGrantStatus(tenantID, grantID, "revoked"); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(tenantID, "provider_grant.revoked", "tool:provider_grant_revoke", map[string]any{"grant_id": grantID})
	return map[string]any{"tenant_id": tenantID, "grant_id": grantID, "status": "revoked"}, nil
}

func newProviderGrantToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate provider grant token: %w", err)
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func (a *App) providerGrantExecuteURL(ctx *sdk.AppCtx, tenantID, grantID string) (string, error) {
	base := strings.TrimRight(a.publicTransferBaseURL(ctx), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("platform public_url is required for provider grants")
	}
	if u.Scheme != "https" && !allowInsecurePublicURL() {
		return "", errors.New("provider grants require an HTTPS platform public_url")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/apps/fleet/provider-grants/" + url.PathEscape(tenantID) + "/" + url.PathEscape(grantID) + "/execute"
	q := u.Query()
	if installID := myInstallID(); installID > 0 {
		q.Set("install_id", fmt.Sprintf("%d", installID))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (a *App) httpProviderGrantExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/provider-grants/")
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != "execute" {
		http.NotFound(w, r)
		return
	}
	grant, err := a.store.getProviderGrant(parts[0], parts[1])
	if err != nil || grant.Status != "active" || grant.TokenHash == "" {
		http.Error(w, "grant unavailable", http.StatusForbidden)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	sum := sha256.Sum256([]byte(token))
	want, err := hex.DecodeString(grant.TokenHash)
	if err != nil || len(want) != len(sum) || subtle.ConstantTimeCompare(want, sum[:]) != 1 {
		http.Error(w, "invalid grant token", http.StatusForbidden)
		return
	}
	var payload struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, tenantControlMaxBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&payload); err != nil || strings.TrimSpace(payload.Tool) == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if payload.Input == nil {
		payload.Input = map[string]any{}
	}
	if err := validateProviderGrantExecution(grant, payload.Tool, payload.Input); err != nil {
		_ = a.store.recordEvent(grant.TenantID, "provider_grant.denied", "tenant", map[string]any{"grant_id": grant.GrantID, "tool": payload.Tool, "error": err.Error()})
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	ctx := platformContext(nil)
	if ctx == nil || ctx.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(grant.ParentConnectionID, payload.Tool, payload.Input)
	if err != nil {
		_ = a.store.recordEvent(grant.TenantID, "provider_grant.error", "tenant", map[string]any{"grant_id": grant.GrantID, "tool": payload.Tool, "error": err.Error()})
		http.Error(w, "provider execution failed", http.StatusBadGateway)
		return
	}
	_ = a.store.recordEvent(grant.TenantID, "provider_grant.executed", "tenant", map[string]any{"grant_id": grant.GrantID, "tool": payload.Tool})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}

func validateProviderGrantExecution(grant *ProviderGrant, tool string, input map[string]any) error {
	if grant == nil || grant.Status != "active" {
		return errors.New("provider grant is not active")
	}
	if len(grant.AllowedTools) > 0 && !providerListContainsFold(grant.AllowedTools, tool) {
		return fmt.Errorf("tool %q is not allowed by this provider grant", tool)
	}
	if !strings.EqualFold(grant.AppSlug, "aws-ses") {
		return nil
	}
	for _, key := range []string{"EmailIdentity", "Identity", "MailFromDomain"} {
		if value := providerString(input[key]); value != "" && len(grant.AllowedDomains) > 0 && !providerIdentityCovered(value, grant.AllowedDomains) {
			return fmt.Errorf("identity %q is outside delegated domains", value)
		}
	}
	for _, key := range []string{"FromEmailAddress", "From", "Source", "from"} {
		value := providerString(input[key])
		if value == "" {
			continue
		}
		if len(grant.AllowedFrom) > 0 && !providerFromCovered(value, grant.AllowedFrom) {
			return fmt.Errorf("from address %q is outside delegated grant", value)
		}
		if len(grant.AllowedFrom) == 0 && len(grant.AllowedDomains) > 0 && !providerIdentityCovered(value, grant.AllowedDomains) {
			return fmt.Errorf("from address %q is outside delegated domains", value)
		}
	}
	return nil
}

func providerListContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func providerString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func providerIdentityCovered(value string, domains []string) bool {
	domain := strings.ToLower(strings.TrimSuffix(providerEmailDomain(value), "."))
	for _, allowed := range domains {
		allowed = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(allowed, "*.")))
		if domain == allowed || strings.HasSuffix(domain, "."+allowed) {
			return true
		}
	}
	return false
}

func providerEmailDomain(value string) string {
	if addr, err := mail.ParseAddress(strings.TrimSpace(value)); err == nil {
		value = addr.Address
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		return value[at+1:]
	}
	return value
}

func providerFromCovered(value string, patterns []string) bool {
	address := strings.ToLower(strings.TrimSpace(value))
	if parsed, err := mail.ParseAddress(address); err == nil {
		address = strings.ToLower(parsed.Address)
	}
	for _, pattern := range patterns {
		p := strings.ToLower(strings.TrimSpace(pattern))
		switch {
		case p == "*":
			return true
		case strings.HasPrefix(p, "*@") && strings.HasSuffix(address, p[1:]):
			return true
		case strings.HasPrefix(p, "@") && strings.HasSuffix(address, p):
			return true
		case address == p:
			return true
		}
	}
	return false
}

func tenantJSON(ctx context.Context, baseURL, apiKey, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tenant returned %d: %s", resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) upsertProviderGrant(g ProviderGrant) (*ProviderGrant, error) {
	now := time.Now().UTC()
	toolsJSON, _ := json.Marshal(g.AllowedTools)
	domainsJSON, _ := json.Marshal(g.AllowedDomains)
	fromJSON, _ := json.Marshal(g.AllowedFrom)
	metaJSON, _ := json.Marshal(g.Metadata)
	_, err := s.db.Exec(`
		INSERT INTO fleet_provider_grants
			(tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, token_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, grant_id) DO UPDATE SET
			app_slug = excluded.app_slug,
			parent_connection_id = excluded.parent_connection_id,
			tenant_connection_id = excluded.tenant_connection_id,
			tenant_install_id = excluded.tenant_install_id,
			tenant_role = excluded.tenant_role,
			status = excluded.status,
			allowed_tools = excluded.allowed_tools,
			allowed_domains = excluded.allowed_domains,
			allowed_from = excluded.allowed_from,
			metadata = excluded.metadata,
			token_hash = excluded.token_hash,
			updated_at = excluded.updated_at
	`, g.TenantID, g.GrantID, g.AppSlug, g.ParentConnectionID, g.TenantConnectionID, g.TenantInstallID, g.TenantRole, g.Status, string(toolsJSON), string(domainsJSON), string(fromJSON), string(metaJSON), g.TokenHash, now, now)
	if err != nil {
		return nil, err
	}
	return s.getProviderGrant(g.TenantID, g.GrantID)
}

func (s *store) listProviderGrants(tenantID string) ([]ProviderGrant, error) {
	q := `SELECT id, tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, token_hash, created_at, updated_at FROM fleet_provider_grants`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id = ?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderGrant
	for rows.Next() {
		g, err := scanProviderGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *store) getProviderGrant(tenantID, grantID string) (*ProviderGrant, error) {
	row := s.db.QueryRow(`SELECT id, tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, token_hash, created_at, updated_at FROM fleet_provider_grants WHERE tenant_id = ? AND grant_id = ?`, tenantID, grantID)
	g, err := scanProviderGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (s *store) setProviderGrantStatus(tenantID, grantID, status string) error {
	_, err := s.db.Exec(`UPDATE fleet_provider_grants SET status = ?, updated_at = ? WHERE tenant_id = ? AND grant_id = ?`, status, time.Now().UTC(), tenantID, grantID)
	return err
}

type providerGrantScanner interface {
	Scan(dest ...any) error
}

func scanProviderGrant(row providerGrantScanner) (ProviderGrant, error) {
	var g ProviderGrant
	var toolsJSON, domainsJSON, fromJSON, metaJSON string
	if err := row.Scan(&g.ID, &g.TenantID, &g.GrantID, &g.AppSlug, &g.ParentConnectionID, &g.TenantConnectionID, &g.TenantInstallID, &g.TenantRole, &g.Status, &toolsJSON, &domainsJSON, &fromJSON, &metaJSON, &g.TokenHash, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return g, err
	}
	_ = json.Unmarshal([]byte(toolsJSON), &g.AllowedTools)
	_ = json.Unmarshal([]byte(domainsJSON), &g.AllowedDomains)
	_ = json.Unmarshal([]byte(fromJSON), &g.AllowedFrom)
	var meta any
	if json.Unmarshal([]byte(metaJSON), &meta) == nil {
		g.Metadata = meta
	}
	return g, nil
}

func stringListArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return compactProviderStrings(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return compactProviderStrings(out)
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return compactProviderStrings(strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\t'
		}))
	default:
		return nil
	}
}

func compactProviderStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normaliseProviderDomains(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(v, "."), "*.")))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstArg(args map[string]any, names ...string) string {
	for _, name := range names {
		if s := strings.TrimSpace(getStr(args, name)); s != "" {
			return s
		}
	}
	return ""
}
