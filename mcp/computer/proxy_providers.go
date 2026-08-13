package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

var (
	proxyCountryToken = regexp.MustCompile(`(?i)_country-[^_]+`)
	proxySessionToken = regexp.MustCompile(`(?i)_session-[^_]+`)
)

func resolveIPRoyalProxy(ctx *sdk.AppCtx, connectionID int64, profile *ProxyProfile, country, sticky, appContextID string) (*backends.ExternalProxy, error) {
	if profile.ExternalRef == "" {
		return nil, fmt.Errorf("IPRoyal profile %q requires a residential sub-user hash", profile.Name)
	}
	portKind := "http|https"
	if profile.Protocol == "socks5" {
		portKind = "socks5"
	}
	input := map[string]any{
		"format":       "{hostname}:{port}:{username}:{password}",
		"hostname":     "geo.iproyal.com",
		"port":         portKind,
		"rotation":     map[bool]string{true: "random", false: "sticky"}[sticky == "rotating"],
		"proxy_count":  1,
		"subuser_hash": profile.ExternalRef,
	}
	if country != "" {
		input["location"] = "_country-" + strings.ToLower(country)
	}
	if sticky != "rotating" {
		input["lifetime"] = "24h"
	}
	payload, err := executeProxyProviderTool(ctx, connectionID, "IPRoyal", "generate_proxy_list", input)
	if err != nil {
		return nil, err
	}
	proxy, err := providerProxyFromPayload(payload, profile.Protocol, ipRoyalDefaultPort(profile.Protocol))
	if err != nil {
		return nil, fmt.Errorf("IPRoyal returned no usable proxy credentials")
	}
	if sticky == "context" {
		// IPRoyal requires an exactly eight-character alphanumeric session id.
		proxy.Password = replaceOrAppendProxyToken(proxy.Password, proxySessionToken, "_session-"+stableProxyIdentity(profile.ID, appContextID)[:8])
	}
	return proxy, nil
}

func resolveProxyCheapProxy(ctx *sdk.AppCtx, connectionID int64, profile *ProxyProfile, country, sticky, appContextID string) (*backends.ExternalProxy, error) {
	if profile.ExternalRef == "" {
		return nil, fmt.Errorf("Proxy-Cheap profile %q requires an order id", profile.Name)
	}
	payload, err := executeProxyProviderTool(ctx, connectionID, "Proxy-Cheap", "get_order_proxies", map[string]any{
		"order_id": profile.ExternalRef,
	})
	if err != nil {
		return nil, err
	}
	proxy, err := providerProxyFromPayload(payload, profile.Protocol)
	if err != nil {
		return nil, fmt.Errorf("Proxy-Cheap returned no usable proxy credentials")
	}
	// Rotating-residential routing is encoded in the generated password.
	// Preserve the provider-issued base secret and add only safe policy tokens.
	if country != "" {
		proxy.Password = replaceOrAppendProxyToken(proxy.Password, proxyCountryToken, "_country-"+strings.ToUpper(country))
	} else {
		proxy.Password = removeProxyToken(proxy.Password, proxyCountryToken)
	}
	if sticky != "rotating" {
		proxy.Password = replaceOrAppendProxyToken(proxy.Password, proxySessionToken, "_session-"+proxySessionIdentity(profile.ID, sticky, appContextID))
	} else {
		proxy.Password = removeProxyToken(proxy.Password, proxySessionToken)
	}
	return proxy, nil
}

func proxySessionIdentity(profileID, sticky, appContextID string) string {
	if sticky == "context" {
		return stableProxyIdentity(profileID, appContextID)[:8]
	}
	return newSessionProxyIdentity()[:8]
}

func stableProxyIdentity(profileID, appContextID string) string {
	sum := sha256.Sum256([]byte(profileID + ":" + appContextID))
	return hex.EncodeToString(sum[:10])
}

func executeProxyProviderTool(ctx *sdk.AppCtx, connectionID int64, provider, tool string, input map[string]any) (any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s proxy integration is unavailable", provider)
	}
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, tool, input)
	if err != nil || result == nil || !result.Success {
		return nil, fmt.Errorf("%s proxy credential lookup failed", provider)
	}
	var payload any
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		return nil, fmt.Errorf("%s returned an invalid credential response", provider)
	}
	return payload, nil
}

func replaceOrAppendProxyToken(value string, pattern *regexp.Regexp, replacement string) string {
	if pattern.MatchString(value) {
		return pattern.ReplaceAllString(value, replacement)
	}
	return value + replacement
}

func removeProxyToken(value string, pattern *regexp.Regexp) string {
	return pattern.ReplaceAllString(value, "")
}

func externalProxyScheme(protocol string) string {
	if strings.EqualFold(protocol, "socks5") {
		return "socks5"
	}
	// HTTPS here describes HTTPS destinations through CONNECT, not a
	// TLS-wrapped proxy gateway, for every currently supported provider.
	return "http"
}

func ipRoyalDefaultPort(protocol string) int {
	if strings.EqualFold(protocol, "socks5") {
		return 32325
	}
	return 12321
}

func providerProxyFromPayload(payload any, protocol string, zeroPortFallback ...int) (*backends.ExternalProxy, error) {
	var walk func(any) (*backends.ExternalProxy, bool)
	walk = func(value any) (*backends.ExternalProxy, bool) {
		switch item := value.(type) {
		case string:
			proxy, err := parseProviderProxyLine(item, protocol, zeroPortFallback...)
			return proxy, err == nil
		case map[string]any:
			host := firstProviderString(item, "host", "hostname", "server", "proxy", "proxy_host", "proxy_address", "ip")
			port := firstProviderPort(item, "port", "proxy_port")
			username := firstProviderString(item, "username", "user", "login", "proxy_username", "proxy_login")
			password := firstProviderString(item, "password", "pass", "proxy_password")
			port = providerPortOrFallback(port, zeroPortFallback...)
			if host != "" && port > 0 && username != "" && password != "" {
				server, err := providerServerURL(host, port, protocol)
				if err == nil {
					return &backends.ExternalProxy{Server: server, Username: username, Password: password}, true
				}
			}
			for _, child := range item {
				if proxy, ok := walk(child); ok {
					return proxy, true
				}
			}
		case []any:
			for _, child := range item {
				if proxy, ok := walk(child); ok {
					return proxy, true
				}
			}
		}
		return nil, false
	}
	if proxy, ok := walk(payload); ok {
		return proxy, nil
	}
	return nil, errors.New("proxy credentials not found")
}

func parseProviderProxyLine(raw, protocol string, zeroPortFallback ...int) (*backends.ExternalProxy, error) {
	raw = strings.TrimSpace(strings.Trim(raw, `"`))
	if raw == "" {
		return nil, errors.New("empty proxy line")
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || parsed.Port() == "" || parsed.User == nil {
			return nil, errors.New("invalid proxy URL")
		}
		password, ok := parsed.User.Password()
		if !ok {
			return nil, errors.New("proxy password missing")
		}
		username := parsed.User.Username()
		port, err := strconv.Atoi(parsed.Port())
		if err != nil {
			return nil, errors.New("invalid proxy port")
		}
		port = providerPortOrFallback(port, zeroPortFallback...)
		server, err := providerServerURL(parsed.Hostname(), port, protocol)
		if err != nil {
			return nil, errors.New("invalid proxy URL")
		}
		return &backends.ExternalProxy{Server: server, Username: username, Password: password}, nil
	}
	parts := strings.SplitN(raw, ":", 4)
	if len(parts) != 4 {
		return nil, errors.New("unknown proxy credential format")
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, errors.New("invalid proxy port")
	}
	port = providerPortOrFallback(port, zeroPortFallback...)
	if port <= 0 || port > 65535 {
		return nil, errors.New("invalid proxy port")
	}
	server, err := providerServerURL(parts[0], port, protocol)
	if err != nil || parts[2] == "" || parts[3] == "" {
		return nil, errors.New("invalid proxy credential line")
	}
	return &backends.ExternalProxy{Server: server, Username: parts[2], Password: parts[3]}, nil
}

func providerPortOrFallback(port int, zeroPortFallback ...int) int {
	if port == 0 && len(zeroPortFallback) > 0 {
		return zeroPortFallback[0]
	}
	return port
}

func providerServerURL(host string, port int, protocol string) (string, error) {
	host = strings.TrimSpace(host)
	if strings.Contains(host, "://") {
		parsed, err := url.Parse(host)
		if err != nil {
			return "", err
		}
		host = parsed.Hostname()
	}
	if host == "" || port <= 0 || port > 65535 {
		return "", errors.New("invalid proxy server")
	}
	return (&url.URL{Scheme: externalProxyScheme(protocol), Host: net.JoinHostPort(host, strconv.Itoa(port))}).String(), nil
}

func firstProviderString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstProviderPort(item map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := item[key].(type) {
		case float64:
			if int(value) == int(value) {
				return int(value)
			}
		case string:
			if port, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return port
			}
		}
	}
	return 0
}
