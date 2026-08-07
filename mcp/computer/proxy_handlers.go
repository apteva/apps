package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func newProxyProfileID() string {
	return "px_" + strings.TrimPrefix(newContextID(), "ctx_")
}

func (a *App) toolProxyProfileList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	profiles, err := dbListProxyProfiles(appDB(ctx), true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"profiles": profiles}, nil
}

func (a *App) proxyConnections(ctx *sdk.AppCtx) []map[string]any {
	if ctx == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0)
	for _, binding := range ctx.IntegrationsFor(proxyProviderRole) {
		if binding == nil || binding.ConnectionID <= 0 {
			continue
		}
		name := binding.AppSlug
		if connection, err := ctx.PlatformAPI().GetConnection(binding.ConnectionID); err == nil && connection != nil {
			name = connection.Name
		}
		out = append(out, map[string]any{
			"connection_id": binding.ConnectionID,
			"provider_slug": binding.AppSlug,
			"name":          name,
			"default":       binding.IsDefault,
		})
	}
	return out
}

func validateProxyProfileConnection(ctx *sdk.AppCtx, profile *ProxyProfile) error {
	binding, err := boundProxyConnection(ctx, profile)
	if err != nil || !profile.Enabled {
		return err
	}
	_, err = resolveProxyProvider(ctx, binding, profile, profile.DefaultCountry, "rotating", "profile-validation")
	return err
}

func proxyProfileFromBody(body map[string]any) ProxyProfile {
	connectionID, _ := int64Arg(body, "connection_id")
	return ProxyProfile{
		Name:           stringArg(body, "name"),
		ProviderSlug:   stringArg(body, "provider_slug"),
		ConnectionID:   connectionID,
		ExternalRef:    stringArg(body, "external_ref"),
		PoolType:       stringArg(body, "pool_type"),
		Protocol:       stringArg(body, "protocol"),
		DefaultCountry: stringArg(body, "default_country"),
		StickyScope:    stringArg(body, "sticky_scope"),
		Enabled:        boolArgDefault(body, "enabled", true),
	}
}

func (a *App) handleProxyProfilesCollection(w http.ResponseWriter, r *http.Request) {
	ctx := appCtxForRequest(r, nil)
	switch r.Method {
	case http.MethodGet:
		profiles, err := dbListProxyProfiles(appDB(ctx), false)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"profiles": profiles, "connections": a.proxyConnections(ctx)})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
			return
		}
		profile := proxyProfileFromBody(body)
		if err := normalizeProxyProfile(&profile); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateProxyProfileConnection(ctx, &profile); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := dbCreateProxyProfile(appDB(ctx), profile)
		if err != nil {
			httpErr(w, http.StatusBadRequest, proxyProfileConflictError(err).Error())
			return
		}
		writeJSON(w, map[string]any{"profile": created})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProxyProfileItem(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/proxy-profiles/")
	id = strings.Trim(id, "/")
	if id == "" {
		httpErr(w, http.StatusBadRequest, "proxy profile id required")
		return
	}
	ctx := appCtxForRequest(r, nil)
	switch r.Method {
	case http.MethodGet:
		profile, err := dbGetProxyProfile(appDB(ctx), id)
		if err != nil {
			httpErr(w, http.StatusNotFound, "proxy profile not found")
			return
		}
		writeJSON(w, map[string]any{"profile": profile})
	case http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
			return
		}
		candidate, err := dbGetProxyProfile(appDB(ctx), id)
		if err != nil {
			httpErr(w, http.StatusNotFound, "proxy profile not found")
			return
		}
		applyProxyProfilePatch(candidate, body)
		if err := normalizeProxyProfile(candidate); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateProxyProfileConnection(ctx, candidate); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		updated, err := dbUpdateProxyProfile(appDB(ctx), id, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, proxyProfileConflictError(err).Error())
			return
		}
		writeJSON(w, map[string]any{"profile": updated})
	case http.MethodDelete:
		settings, _ := currentSettings(ctx)
		if settings.DefaultProxyProfile == id {
			_, _ = dbUpdateSettings(appDB(ctx), map[string]any{
				"default_proxy_mode":       "auto",
				"default_proxy_profile_id": "",
			})
		}
		if err := dbDeleteProxyProfile(appDB(ctx), id); err != nil {
			if errors.Is(err, errProxyProfileNotFound) {
				httpErr(w, http.StatusNotFound, "proxy profile not found")
				return
			}
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"deleted": true})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleProxyConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, map[string]any{"connections": a.proxyConnections(appCtxForRequest(r, nil))})
}

type safeProxyResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *App) handleProxyResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	connectionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("connection_id")), 10, 64)
	if err != nil || connectionID <= 0 {
		httpErr(w, http.StatusBadRequest, "valid connection_id required")
		return
	}
	ctx := appCtxForRequest(r, nil)
	binding, err := boundProxyConnectionByID(ctx, connectionID)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if binding.AppSlug != "dataimpulse" {
		httpErr(w, http.StatusBadRequest, fmt.Sprintf("proxy provider %q does not support resource discovery", binding.AppSlug))
		return
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_sub_users", map[string]any{})
	if err != nil || result == nil || !result.Success {
		httpErr(w, http.StatusBadGateway, "proxy provider resource lookup failed")
		return
	}
	var payload any
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		httpErr(w, http.StatusBadGateway, "proxy provider returned an invalid resource list")
		return
	}
	writeJSON(w, map[string]any{"resources": safeProxyResources(payload)})
}

func safeProxyResources(payload any) []safeProxyResource {
	out := make([]safeProxyResource, 0)
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			id, hasSubuserID := safeResourceID(item["subuser_id"])
			if !hasSubuserID {
				if _, looksLikeSubuser := item["label"]; looksLikeSubuser {
					id, hasSubuserID = safeResourceID(item["id"])
				}
			}
			if hasSubuserID && !seen[id] {
				name := "Sub-user " + id
				if label, ok := item["label"].(string); ok && strings.TrimSpace(label) != "" {
					name = strings.TrimSpace(label)
				} else if label, ok := item["name"].(string); ok && strings.TrimSpace(label) != "" {
					name = strings.TrimSpace(label)
				}
				seen[id] = true
				out = append(out, safeProxyResource{ID: id, Name: name})
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(payload)
	return out
}

func safeResourceID(value any) (string, bool) {
	switch id := value.(type) {
	case string:
		id = strings.TrimSpace(id)
		if id == "" {
			return "", false
		}
		return id, true
	case float64:
		if id > 0 && id == float64(int64(id)) {
			return strconv.FormatInt(int64(id), 10), true
		}
	case json.Number:
		if n, err := id.Int64(); err == nil && n > 0 {
			return strconv.FormatInt(n, 10), true
		}
	}
	return "", false
}
