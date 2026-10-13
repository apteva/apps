package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// Named bases are supplied by the execution backend. Unknown names are left to
// other consumers; recognized Telephony bases must be unambiguous secure URLs.
func validRuntimeURL(raw string, websocket bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "..") {
		return false
	}
	if websocket && (u.Scheme == "wss" || u.Scheme == "ws" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return true
	}
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}
func parseRuntimeEndpoints(raw string) (map[string]string, error) {
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	if len(raw) > 32<<10 || json.Unmarshal([]byte(raw), &out) != nil {
		return nil, errors.New("invalid APTEVA_RUNTIME_ENDPOINTS")
	}
	for _, key := range []string{"browser", "media", "signaling", "callbacks", "carrier_media"} {
		if value := out[key]; value != "" && !validRuntimeURL(value, true) {
			return nil, errors.New("invalid managed Telephony endpoint: " + key)
		}
	}
	// Direct browser authorization must precede the SDK's protected handlers.
	// Both transports use this ingress; WebRTC still selects its own UDP path.
	return out, nil
}
func (a *App) runtimeEndpoint(name, path string) string {
	if a.runtime == nil {
		return ""
	}
	base := a.runtime.endpoints[name]
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + path
}
func runtimeHTTPURL(raw string) string {
	if strings.HasPrefix(raw, "wss://") {
		return "https://" + strings.TrimPrefix(raw, "wss://")
	}
	if strings.HasPrefix(raw, "ws://") {
		return "http://" + strings.TrimPrefix(raw, "ws://")
	}
	return raw
}

// HTTPS ingress may be split by backend, but every browser base must route
// through the same scoped authorization prefix on this single execution.
func (a *App) runtimeBrowserBase(name string) string {
	base := a.runtimeEndpoint(name, runtimeBrowserPrefix)
	if base == "" {
		base = a.runtimeEndpoint("browser", runtimeBrowserPrefix)
	}
	return runtimeHTTPURL(base)
}

func websocketURL(raw string) string {
	if strings.HasPrefix(raw, "https://") {
		return "wss://" + strings.TrimPrefix(raw, "https://")
	}
	if strings.HasPrefix(raw, "http://") {
		return "ws://" + strings.TrimPrefix(raw, "http://")
	}
	return raw
}
