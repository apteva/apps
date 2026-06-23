package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

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
	name := strings.TrimSpace(getStr(args, "name"))
	if name == "" {
		name = "Delegated " + appSlug
		if len(allowedDomains) > 0 {
			name += " (" + strings.Join(allowedDomains, ",") + ")"
		}
	}

	controllerGateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if controllerGateway == "" {
		controllerGateway = "http://127.0.0.1:5280"
	}
	controllerToken := os.Getenv("APTEVA_APP_TOKEN")
	controllerInstallID := os.Getenv("APTEVA_INSTALL_ID")
	if controllerToken == "" || controllerInstallID == "" {
		return nil, errors.New("fleet sidecar is missing APTEVA_APP_TOKEN/APTEVA_INSTALL_ID")
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
		"controller_gateway_url":     controllerGateway,
		"controller_token":           controllerToken,
		"controller_install_id":      controllerInstallID,
		"parent_connection_id":       strconv.FormatInt(parentConnID, 10),
		"allowed_tools":              string(allowedToolsJSON),
		"allowed_domains":            string(allowedDomainsJSON),
		"allowed_from":               string(allowedFromJSON),
		"metadata":                   string(metadataJSON),
	}

	var created struct {
		ID int64 `json:"id"`
	}
	if err := tenantJSON(context.Background(), t.BaseURL, string(tenantKey), http.MethodPost, "/api/connections", map[string]any{
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
		if err := tenantJSON(context.Background(), t.BaseURL, string(tenantKey), http.MethodPut, fmt.Sprintf("/api/apps/installs/%d/bindings", installID), map[string]any{
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
	})
	if err != nil {
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

func (a *App) toolProviderGrantRevoke(_ *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	grantID := getStr(args, "grant_id")
	if tenantID == "" || grantID == "" {
		return nil, errors.New("tenant_id and grant_id are required")
	}
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return nil, err
	}
	grant, err := a.store.getProviderGrant(tenantID, grantID)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	if grant.TenantInstallID > 0 && grant.TenantRole != "" {
		_ = tenantJSON(context.Background(), t.BaseURL, string(key), http.MethodPut, fmt.Sprintf("/api/apps/installs/%d/bindings", grant.TenantInstallID), map[string]any{
			grant.TenantRole: nil,
		}, nil)
	}
	if grant.TenantConnectionID > 0 {
		_ = tenantJSON(context.Background(), t.BaseURL, string(key), http.MethodDelete, fmt.Sprintf("/api/connections/%d", grant.TenantConnectionID), nil, nil)
	}
	if err := a.store.setProviderGrantStatus(tenantID, grantID, "revoked"); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(tenantID, "provider_grant.revoked", "tool:provider_grant_revoke", map[string]any{"grant_id": grantID})
	return map[string]any{"tenant_id": tenantID, "grant_id": grantID, "status": "revoked"}, nil
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
			(tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			updated_at = excluded.updated_at
	`, g.TenantID, g.GrantID, g.AppSlug, g.ParentConnectionID, g.TenantConnectionID, g.TenantInstallID, g.TenantRole, g.Status, string(toolsJSON), string(domainsJSON), string(fromJSON), string(metaJSON), now, now)
	if err != nil {
		return nil, err
	}
	return s.getProviderGrant(g.TenantID, g.GrantID)
}

func (s *store) listProviderGrants(tenantID string) ([]ProviderGrant, error) {
	q := `SELECT id, tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, created_at, updated_at FROM fleet_provider_grants`
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
	row := s.db.QueryRow(`SELECT id, tenant_id, grant_id, app_slug, parent_connection_id, tenant_connection_id, tenant_install_id, tenant_role, status, allowed_tools, allowed_domains, allowed_from, metadata, created_at, updated_at FROM fleet_provider_grants WHERE tenant_id = ? AND grant_id = ?`, tenantID, grantID)
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
	if err := row.Scan(&g.ID, &g.TenantID, &g.GrantID, &g.AppSlug, &g.ParentConnectionID, &g.TenantConnectionID, &g.TenantInstallID, &g.TenantRole, &g.Status, &toolsJSON, &domainsJSON, &fromJSON, &metaJSON, &g.CreatedAt, &g.UpdatedAt); err != nil {
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
