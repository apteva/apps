package main

import (
	"context"
	"encoding/json"
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
	project, e := a.panelProject(r)
	if e != nil {
		http.Error(w, "project not allowed", 403)
		return
	}
	policy, e := a.phonePolicy(project)
	if e != nil {
		http.Error(w, "access unavailable", 503)
		return
	}
	var provider *phoneAuthProvider
	for i := range policy.Providers {
		if policy.Providers[i].ID == r.URL.Query().Get("auth_provider") {
			provider = &policy.Providers[i]
			break
		}
	}
	if provider == nil || !validPhoneProvider(*provider) {
		http.Error(w, "unknown authentication provider", 401)
		return
	}
	bearer := r.Header.Get("Authorization")
	if !strings.HasPrefix(bearer, "Bearer ") || len(bearer) < 16 || len(bearer) > 16384 {
		http.Error(w, "user session required", 401)
		return
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
		http.Error(w, "application-user action not allowed", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", provider.URL, nil)
	if e != nil {
		http.Error(w, "identity provider unavailable", 503)
		return
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
		http.Error(w, "identity provider unavailable", 503)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		http.Error(w, "user session invalid or revoked", 401)
		return
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	dec.UseNumber()
	var data map[string]any
	if dec.Decode(&data) != nil {
		http.Error(w, "invalid identity response", 401)
		return
	}
	identity := phoneIdentity{IssuerApp: provider.IssuerApp, IssuerInstallID: provider.IssuerInstallID, SubjectType: "user"}
	if provider.Format == "apteva-auth" {
		user, ok := data["user"].(map[string]any)
		if !ok || identityScalar(user["project_id"]) != project {
			http.Error(w, "identity project mismatch", 403)
			return
		}
		identity.SubjectID = identityScalar(user["id"])
		identity.OrganizationID = identityScalar(user["organization_id"])
	} else {
		identity.SubjectID = identityScalar(data["sub"])
		identity.OrganizationID = identityScalar(data["organization_id"])
	}
	if !identity.valid() {
		http.Error(w, "verified user identity required", 401)
		return
	}
	p, e := a.phonePrincipal(project, identity)
	if e != nil || p.Revision != policy.Revision {
		http.Error(w, "Telephony access denied", 403)
		return
	}
	if action == "call.takeover" && !p.Supervisor {
		http.Error(w, "supervisor permission required", 403)
		return
	}
	clone = clone.WithContext(context.WithValue(clone.Context(), phonePrincipalKey{}, p))
	switch {
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
