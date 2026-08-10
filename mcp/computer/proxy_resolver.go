package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

const proxyProviderRole = "proxy-provider"

type SessionProxyState struct {
	Mode        string `json:"mode"`
	Provider    string `json:"provider,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	Country     string `json:"country,omitempty"`
	StickyScope string `json:"sticky_scope,omitempty"`
}

type resolvedProxyPolicy struct {
	State          SessionProxyState
	Managed        *bool
	ManagedCountry string
	External       *backends.ExternalProxy
}

func validProxyCountry(country string) bool {
	country = strings.ToUpper(strings.TrimSpace(country))
	return len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

func (a *App) resolveSessionProxy(ctx *sdk.AppCtx, args map[string]any, backend string, appContextID string) (resolvedProxyPolicy, error) {
	settings, err := currentSettings(ctx)
	if err != nil {
		return resolvedProxyPolicy{}, err
	}

	mode := normalizeProxyMode(stringArg(args, "proxy_mode"))
	profileRef := strings.TrimSpace(stringArg(args, "proxy_profile"))
	country := strings.ToUpper(strings.TrimSpace(stringArg(args, "proxy_country")))
	sticky := strings.ToLower(strings.TrimSpace(stringArg(args, "proxy_sticky")))
	_, proxyWasSet := args["proxy"]
	if !settings.LockProxyPolicy && mode != "" && proxyWasSet {
		return resolvedProxyPolicy{}, fmt.Errorf("proxy_mode cannot be combined with the legacy proxy boolean")
	}
	explicit := mode != "" || profileRef != "" || country != "" || sticky != "" || proxyWasSet

	if settings.LockProxyPolicy || !explicit {
		mode = settings.DefaultProxyMode
		if mode == "profile" {
			profileRef = settings.DefaultProxyProfile
		} else {
			profileRef = ""
		}
		country = ""
		sticky = ""
	}
	if mode == "" {
		switch {
		case profileRef != "":
			mode = "profile"
		case proxyWasSet:
			if boolArgDefault(args, "proxy", false) {
				mode = "managed"
			} else {
				mode = "direct"
			}
		case country != "":
			mode = "managed"
		default:
			mode = "auto"
		}
	}
	if !validProxyMode(mode) {
		return resolvedProxyPolicy{}, fmt.Errorf("proxy_mode must be auto, direct, managed, or profile")
	}
	if country != "" && !validProxyCountry(country) {
		return resolvedProxyPolicy{}, fmt.Errorf("proxy_country must be a two-letter ISO country code")
	}

	switch mode {
	case "auto":
		if country != "" || profileRef != "" || sticky != "" {
			return resolvedProxyPolicy{}, fmt.Errorf("auto proxy mode cannot include a profile, country, or sticky policy")
		}
		return resolvedProxyPolicy{State: SessionProxyState{Mode: "auto"}}, nil
	case "direct":
		if country != "" || profileRef != "" || sticky != "" {
			return resolvedProxyPolicy{}, fmt.Errorf("direct proxy mode cannot include a profile, country, or sticky policy")
		}
		if backend == "service" {
			return resolvedProxyPolicy{}, fmt.Errorf("backend %q does not expose proxy policy controls", backend)
		}
		off := false
		return resolvedProxyPolicy{State: SessionProxyState{Mode: "direct"}, Managed: &off}, nil
	case "managed":
		if profileRef != "" || sticky != "" {
			return resolvedProxyPolicy{}, fmt.Errorf("managed proxy mode cannot include a proxy profile or sticky policy")
		}
		if backend == "service" {
			return resolvedProxyPolicy{}, fmt.Errorf("backend %q does not expose proxy policy controls", backend)
		}
		if country != "" && backend != "browserbase" && backend != "browser-engine" {
			return resolvedProxyPolicy{}, fmt.Errorf("backend %q does not support country selection for its managed proxy", backend)
		}
		on := true
		return resolvedProxyPolicy{
			State:          SessionProxyState{Mode: "managed", Provider: backend, Country: country},
			Managed:        &on,
			ManagedCountry: country,
		}, nil
	case "profile":
		if profileRef == "" {
			profileRef = settings.DefaultProxyProfile
		}
		if profileRef == "" {
			return resolvedProxyPolicy{}, fmt.Errorf("proxy_profile is required when proxy_mode=profile")
		}
		profile, err := getProxyProfileByReference(appDB(ctx), profileRef)
		if err != nil {
			return resolvedProxyPolicy{}, err
		}
		if !profile.Enabled {
			return resolvedProxyPolicy{}, fmt.Errorf("proxy profile %q is disabled", profile.Name)
		}
		if country == "" {
			country = profile.DefaultCountry
		}
		if sticky == "" {
			sticky = profile.StickyScope
		}
		if sticky != "rotating" && sticky != "session" && sticky != "context" {
			return resolvedProxyPolicy{}, fmt.Errorf("proxy_sticky must be rotating, session, or context")
		}
		if sticky == "context" && appContextID == "" {
			return resolvedProxyPolicy{}, fmt.Errorf("proxy_sticky=context requires an app-managed context_id or context_name")
		}
		binding, err := boundProxyConnection(ctx, profile)
		if err != nil {
			return resolvedProxyPolicy{}, err
		}
		external, err := resolveProxyProvider(ctx, binding, profile, country, sticky, appContextID)
		if err != nil {
			return resolvedProxyPolicy{}, err
		}
		if backend == "browserbase" && !strings.HasPrefix(strings.ToLower(external.Server), "http://") && !strings.HasPrefix(strings.ToLower(external.Server), "https://") {
			return resolvedProxyPolicy{}, fmt.Errorf("proxy profile %q uses %s, but Browserbase external proxies require HTTP or HTTPS", profile.Name, profile.Protocol)
		}
		if backend == "browser-engine" || backend == "service" {
			return resolvedProxyPolicy{}, fmt.Errorf("backend %q does not support external proxy profiles", backend)
		}
		return resolvedProxyPolicy{
			State: SessionProxyState{
				Mode:        "profile",
				Provider:    profile.ProviderSlug,
				ProfileID:   profile.ID,
				ProfileName: profile.Name,
				Country:     country,
				StickyScope: sticky,
			},
			External: external,
		}, nil
	default:
		return resolvedProxyPolicy{}, fmt.Errorf("unsupported proxy mode %q", mode)
	}
}

func getProxyProfileByReference(db *sql.DB, ref string) (*ProxyProfile, error) {
	if db == nil {
		return nil, errors.New("proxy profile catalog unavailable")
	}
	p, err := dbGetProxyProfile(db, ref)
	if errors.Is(err, errProxyProfileNotFound) {
		p, err = dbGetProxyProfileByName(db, ref)
	}
	if errors.Is(err, errProxyProfileNotFound) {
		return nil, fmt.Errorf("proxy profile %q was not found", ref)
	}
	return p, err
}

func boundProxyConnection(ctx *sdk.AppCtx, profile *ProxyProfile) (*sdk.BoundIntegration, error) {
	binding, err := boundProxyConnectionByID(ctx, profile.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("proxy profile %q connection is not bound to Computer", profile.Name)
	}
	if binding.AppSlug != "" && binding.AppSlug != profile.ProviderSlug {
		return nil, fmt.Errorf("proxy profile provider does not match its bound connection")
	}
	return binding, nil
}

func boundProxyConnectionByID(ctx *sdk.AppCtx, connectionID int64) (*sdk.BoundIntegration, error) {
	if ctx == nil {
		return nil, errors.New("proxy provider integration is unavailable")
	}
	for _, binding := range ctx.IntegrationsFor(proxyProviderRole) {
		if binding != nil && binding.ConnectionID == connectionID {
			return binding, nil
		}
	}
	return nil, errors.New("proxy provider connection is not bound to Computer")
}

func resolveProxyProvider(ctx *sdk.AppCtx, binding *sdk.BoundIntegration, profile *ProxyProfile, country, sticky, appContextID string) (*backends.ExternalProxy, error) {
	slug := profile.ProviderSlug
	if binding.AppSlug != "" {
		slug = binding.AppSlug
	}
	switch slug {
	case "dataimpulse":
		return resolveDataImpulseProxy(ctx, binding.ConnectionID, profile, country, sticky, appContextID)
	default:
		return nil, fmt.Errorf("proxy provider %q is not supported by Computer yet", slug)
	}
}

func resolveDataImpulseProxy(ctx *sdk.AppCtx, connectionID int64, profile *ProxyProfile, country, sticky, appContextID string) (*backends.ExternalProxy, error) {
	subuserID, err := strconv.ParseInt(profile.ExternalRef, 10, 64)
	if err != nil || subuserID <= 0 {
		return nil, fmt.Errorf("DataImpulse profile %q requires a numeric sub-user id", profile.Name)
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "get_sub_user", map[string]any{"subuser_id": subuserID})
	if err != nil {
		// Do not forward provider errors verbatim: upstream failures may echo
		// request or account details into an agent-visible tool response.
		return nil, fmt.Errorf("DataImpulse proxy credential lookup failed")
	}
	if result == nil || !result.Success {
		return nil, fmt.Errorf("DataImpulse rejected the proxy credential lookup")
	}
	var payload any
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		return nil, fmt.Errorf("DataImpulse returned an invalid credential response")
	}
	login := findProviderString(payload, "proxy_login", "login", "username", "user")
	password := findProviderString(payload, "proxy_password", "password", "pass")
	if login == "" || password == "" {
		return nil, fmt.Errorf("DataImpulse sub-user response did not include reusable proxy credentials")
	}

	params := make([]string, 0, 2)
	if country != "" {
		params = append(params, "cr."+strings.ToLower(country))
	}
	if sticky != "rotating" {
		identity := newSessionProxyIdentity()
		if sticky == "context" {
			sum := sha256.Sum256([]byte(profile.ID + ":" + appContextID))
			identity = hex.EncodeToString(sum[:10])
		}
		params = append(params, "sessid."+identity)
	}
	if len(params) > 0 {
		separator := "__"
		if strings.Contains(login, "__") {
			separator = ";"
		}
		login += separator + strings.Join(params, ";")
	}

	scheme := profile.Protocol
	port := 823
	if scheme == "https" {
		// DataImpulse's port 823 is an HTTP proxy that supports HTTPS
		// destinations through CONNECT; it is not a TLS-wrapped proxy endpoint.
		scheme = "http"
	} else if scheme == "socks5" {
		port = 824
	}
	server := (&url.URL{Scheme: scheme, Host: "gw.dataimpulse.com:" + strconv.Itoa(port)}).String()
	return &backends.ExternalProxy{Server: server, Username: login, Password: password}, nil
}

func findProviderString(value any, keys ...string) string {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[strings.ToLower(key)] = struct{}{}
	}
	var walk func(any) string
	walk = func(v any) string {
		switch item := v.(type) {
		case map[string]any:
			for key, raw := range item {
				if _, ok := wanted[strings.ToLower(key)]; ok {
					if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
						return strings.TrimSpace(text)
					}
				}
			}
			for _, raw := range item {
				if found := walk(raw); found != "" {
					return found
				}
			}
		case []any:
			for _, raw := range item {
				if found := walk(raw); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return walk(value)
}

func newSessionProxyIdentity() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return strings.TrimPrefix(newSessionID(), "br_")
	}
	return hex.EncodeToString(buf)
}
