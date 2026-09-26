package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Administrator-approved online identity providers. The endpoint must validate
// the current session on every request, including logout/disable/revocation.
// No JWT claim or user-supplied identity is trusted by Telephony.
type phoneAuthProvider struct {
	ID              string   `json:"id"`
	IssuerApp       string   `json:"issuer_app"`
	IssuerInstallID string   `json:"issuer_install_id"`
	URL             string   `json:"url"`
	Format          string   `json:"format"` // apteva-auth (/me) or userinfo ({sub,organization_id})
	Actions         []string `json:"actions"`
}

func validPhoneProvider(p phoneAuthProvider) bool {
	u, e := url.Parse(p.URL)
	return e == nil && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) && p.ID != "" && p.IssuerApp != "" && p.IssuerInstallID != "" && (p.Format == "apteva-auth" || p.Format == "userinfo")
}
func phoneProviderStillAllows(policy phonePolicy, selected phoneAuthProvider, action string) bool {
	for _, current := range policy.Providers {
		if current.ID != selected.ID || current.URL != selected.URL || current.Format != selected.Format ||
			current.IssuerApp != selected.IssuerApp || current.IssuerInstallID != selected.IssuerInstallID {
			continue
		}
		for _, allowed := range current.Actions {
			if allowed == action {
				return true
			}
		}
	}
	return false
}
func identityScalar(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	}
	return ""
}

// /user/ is explicitly public at the gateway, but this handler authenticates
// every data request. It never falls back to trusted-operator access.
func (a *App) handleApplicationSession(w http.ResponseWriter, r *http.Request) {
	clone, status, err := a.authenticateApplicationSession(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	switch {
	case clone.URL.Path == "/calls/events":
		clone = clone.WithContext(context.WithValue(clone.Context(), phoneStreamRequestKey{}, r))
		a.handleCallNotifications(w, clone)
	case clone.URL.Path == "/calls":
		a.handleListCalls(w, clone)
	case strings.HasPrefix(clone.URL.Path, "/calls/"):
		a.handleCallAction(w, clone)
	case strings.HasPrefix(clone.URL.Path, "/softphone/"):
		a.handleSoftphoneAction(w, clone)
	default:
		http.NotFound(w, clone)
	}
}
func (a *App) authenticateApplicationSession(r *http.Request) (*http.Request, int, error) {
	project, e := a.panelProject(r)
	if e != nil {
		return nil, 403, errors.New("project not allowed")
	}
	policy, e := a.phonePolicy(project)
	if e != nil {
		return nil, 503, errors.New("access unavailable")
	}
	var provider *phoneAuthProvider
	for i := range policy.Providers {
		if policy.Providers[i].ID == r.URL.Query().Get("auth_provider") {
			provider = &policy.Providers[i]
			break
		}
	}
	if provider == nil || !validPhoneProvider(*provider) {
		return nil, 401, errors.New("unknown authentication provider")
	}
	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") || len(bearer) < 16 || len(bearer) > 16384 {
		return nil, 401, errors.New("user session required")
	}
	// Authorize the route before contacting the provider. Admin/MCP/media routes
	// cannot be reached by adding /user/ to their path.
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	clone.URL.Path = strings.TrimPrefix(r.URL.Path, "/user")
	action := phoneAction(clone)
	allowed := false
	for _, v := range provider.Actions {
		if v == action && action != "" {
			allowed = true
		}
	}
	if !allowed {
		return nil, 403, errors.New("application-user action not allowed")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", provider.URL, nil)
	if e != nil {
		return nil, 503, errors.New("identity provider unavailable")
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Accept", "application/json")
	// Forward the original origin so Auth's configured client-origin checks still
	// apply. Never forward cookies, caller-supplied identity, or arbitrary headers.
	if origin := r.Header.Get("Origin"); origin != "" {
		req.Header.Set("Origin", origin)
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return nil, 503, errors.New("identity provider unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 401, errors.New("user session invalid or revoked")
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	dec.UseNumber()
	var data map[string]any
	if dec.Decode(&data) != nil {
		return nil, 401, errors.New("invalid identity response")
	}
	identity := phoneIdentity{IssuerApp: provider.IssuerApp, IssuerInstallID: provider.IssuerInstallID, SubjectType: "user"}
	if provider.Format == "apteva-auth" {
		user, ok := data["user"].(map[string]any)
		if !ok || identityScalar(user["project_id"]) != project {
			return nil, 403, errors.New("identity project mismatch")
		}
		identity.SubjectID = identityScalar(user["id"])
		identity.OrganizationID = identityScalar(user["organization_id"])
	} else {
		identity.SubjectID = identityScalar(data["sub"])
		identity.OrganizationID = identityScalar(data["organization_id"])
	}
	if !identity.valid() {
		return nil, 401, errors.New("verified user identity required")
	}
	// Authentication may take seconds. A policy write during that interval
	// must not revoke this request unless it changes this provider/action or
	// the caller's own access. Derive both decisions from one fresh snapshot.
	freshPolicy, e := a.phonePolicy(project)
	if e != nil {
		return nil, 503, errors.New("access unavailable")
	}
	if !phoneProviderStillAllows(freshPolicy, *provider, action) {
		return nil, 403, errors.New("authentication provider access changed")
	}
	p, e := phonePrincipalFromPolicy(project, identity, freshPolicy)
	if e != nil {
		return nil, 403, errors.New("Telephony access denied")
	}
	if action == "call.takeover" && !p.Supervisor {
		return nil, 403, errors.New("supervisor permission required")
	}
	clone = clone.WithContext(context.WithValue(clone.Context(), phonePrincipalKey{}, p))
	return clone, 0, nil
}
