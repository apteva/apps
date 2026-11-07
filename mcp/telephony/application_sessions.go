package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
	return e == nil && u.Host != "" && u.User == nil && u.Fragment == "" && phoneProviderURLAllowed(u) && p.ID != "" && p.IssuerApp != "" && p.IssuerInstallID != "" && (p.Format == "apteva-auth" || p.Format == "userinfo")
}

// phoneProviderOnGateway reports whether u has exactly the scheme and host of
// the platform gateway. The Auth app is reached through the gateway
// (/api/apps/auth/_install/N/me), which is plain-HTTP loopback on local
// installs, so the gateway origin is the only non-public target allowed.
func phoneProviderOnGateway(u *url.URL) bool {
	g, e := url.Parse(strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"))
	return e == nil && g.Host != "" && (g.Scheme == "http" || g.Scheme == "https") &&
		strings.EqualFold(g.Scheme, u.Scheme) && strings.EqualFold(g.Host, u.Host)
}

func phoneProviderIPDisallowed(ip net.IP) bool {
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// phoneProviderURLAllowed: the gateway origin, or public HTTPS. Hostnames are
// re-checked at dial time (phoneProviderTransport) so DNS cannot point a
// public-looking name at an internal address.
func phoneProviderURLAllowed(u *url.URL) bool {
	if phoneProviderOnGateway(u) {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && phoneProviderIPDisallowed(ip) {
		return false
	}
	return true
}

// Shared so connections to the provider are reused; the dialer refuses
// loopback/private/link-local targets after resolution.
var phoneProviderTransport = providerMediaTransport()

func phoneProviderClient(u *url.URL) *http.Client {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if !phoneProviderOnGateway(u) {
		client.Transport = phoneProviderTransport
	}
	return client
}

// phoneProviderIssuerConflict reports whether another provider claims the
// same issuer through a different identity endpoint, which would let one
// endpoint vouch for identities another owns. Aliases that use the exact same
// URL and format are the same authority and stay valid; each keeps its own ID
// and actions. URLs are compared exactly: paths and queries matter, and hosts
// or resolved addresses are never treated as equivalent.
func phoneProviderIssuerConflict(policy phonePolicy, selected phoneAuthProvider) bool {
	for _, other := range policy.Providers {
		if other.ID != selected.ID && other.IssuerApp == selected.IssuerApp && other.IssuerInstallID == selected.IssuerInstallID &&
			(other.URL != selected.URL || other.Format != selected.Format) {
			return true
		}
	}
	return false
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
	policy, e := a.cachedPhonePolicy(project)
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
	if provider == nil || !validPhoneProvider(*provider) || phoneProviderIssuerConflict(*policy, *provider) {
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
	resp, e := phoneProviderClient(req.URL).Do(req)
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
	freshPolicy, e := a.cachedPhonePolicy(project)
	if e != nil {
		return nil, 503, errors.New("access unavailable")
	}
	if !phoneProviderStillAllows(*freshPolicy, *provider, action) || phoneProviderIssuerConflict(*freshPolicy, *provider) {
		return nil, 403, errors.New("authentication provider access changed")
	}
	p, e := phonePrincipalFromPolicy(project, identity, *freshPolicy)
	if e != nil {
		return nil, 403, errors.New("Telephony access denied")
	}
	p.AuthProvider = provider
	for _, v := range provider.Actions {
		p.ListenScope = p.ListenScope || v == "call.listen"
		p.CoachScope = p.CoachScope || v == "call.coach"
	}
	if (action == "call.takeover" || action == "call.listen" || action == "call.coach") && !p.Supervisor {
		return nil, 403, errors.New("supervisor permission required")
	}
	clone = clone.WithContext(context.WithValue(clone.Context(), phonePrincipalKey{}, p))
	return clone, 0, nil
}
