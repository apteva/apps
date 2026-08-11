package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type portalPageConfig struct {
	Community string `json:"community"`
	ProjectID string `json:"project_id"`
	Product   string `json:"product,omitempty"`
	Course    string `json:"course,omitempty"`
	Offer     string `json:"offer,omitempty"`
	Intent    string `json:"intent,omitempty"`
	Auth      string `json:"auth,omitempty"`
}

// httpPortalHostPage serves the portal SPA directly on a community's native
// ingress hostname. The hostname is the tenant selector, so customers never
// need a community slug or project id in the public URL.
func (a *App) httpPortalHostPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(r.Host))
	if colon := strings.LastIndexByte(hostname, ':'); colon > 0 {
		hostname = hostname[:colon]
	}
	community, err := communityForPortalHostname(globalCtx.AppDB(), "", hostname)
	if err != nil {
		http.Error(w, "portal unavailable", http.StatusInternalServerError)
		return
	}
	if community == nil {
		http.NotFound(w, r)
		return
	}
	config, ok := portalConfigForPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	config.Community = community.Slug
	config.ProjectID = community.ProjectID
	config.Offer = strings.TrimSpace(r.URL.Query().Get("offer"))

	index, err := readCommunityPortalIndex()
	if err != nil {
		globalCtx.Logger().Error("read community portal index", "err", err.Error())
		http.Error(w, "portal unavailable", http.StatusServiceUnavailable)
		return
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		http.Error(w, "portal unavailable", http.StatusInternalServerError)
		return
	}
	index = bytes.ReplaceAll(index, []byte(`href="./`), []byte(`href="/ui/portal/dist/`))
	index = bytes.ReplaceAll(index, []byte(`src="./`), []byte(`src="/ui/portal/dist/`))
	injection := []byte(`<script>window.__COMMUNITY_PORTAL__=` + string(encoded) + `;</script>`)
	if bytes.Contains(index, []byte("</head>")) {
		index = bytes.Replace(index, []byte("</head>"), append(injection, []byte("</head>")...), 1)
	} else {
		index = append(injection, index...)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprint(len(index)))
		return
	}
	_, _ = w.Write(index)
}

func portalConfigForPath(path string) (portalPageConfig, bool) {
	clean := strings.Trim(strings.TrimSpace(path), "/")
	if clean == "" {
		return portalPageConfig{}, true
	}
	parts := strings.Split(clean, "/")
	if len(parts) == 1 {
		switch parts[0] {
		case "login":
			return portalPageConfig{Auth: "login"}, true
		case "signup":
			return portalPageConfig{Auth: "signup"}, true
		case "forgot-password":
			return portalPageConfig{Auth: "forgot"}, true
		case "reset-password":
			return portalPageConfig{Auth: "reset"}, true
		}
	}
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return portalPageConfig{}, false
	}
	slug, err := url.PathUnescape(parts[1])
	if err != nil || strings.Contains(slug, "/") {
		return portalPageConfig{}, false
	}
	switch parts[0] {
	case "products":
		return portalPageConfig{Product: slug}, true
	case "checkout":
		return portalPageConfig{Product: slug, Intent: "buy"}, true
	case "courses":
		return portalPageConfig{Course: slug}, true
	default:
		return portalPageConfig{}, false
	}
}

func readCommunityPortalIndex() ([]byte, error) {
	candidates := []string{}
	if uiDir := strings.TrimSpace(os.Getenv("APTEVA_UI_DIR")); uiDir != "" {
		candidates = append(candidates, filepath.Join(uiDir, "portal", "dist", "index.html"))
	}
	candidates = append(candidates,
		filepath.Join("ui", "portal", "dist", "index.html"),
		filepath.Join("mcp", "community", "ui", "portal", "dist", "index.html"),
	)
	var joined error
	for _, candidate := range candidates {
		contents, err := os.ReadFile(candidate)
		if err == nil {
			return contents, nil
		}
		joined = errors.Join(joined, fmt.Errorf("%s: %w", candidate, err))
	}
	return nil, joined
}

func (a *App) httpPortalGatewayBridge(w http.ResponseWriter, r *http.Request) {
	if !allowedCommunityPortalGatewayPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	rawGateway := strings.TrimSpace(os.Getenv("APTEVA_GATEWAY_URL"))
	if rawGateway == "" {
		rawGateway = "http://127.0.0.1:5280"
	}
	target, err := url.Parse(rawGateway)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		http.Error(w, "portal gateway unavailable", http.StatusServiceUnavailable)
		return
	}
	visitorAuthorization := strings.TrimSpace(r.Header.Get("X-Apteva-Original-Authorization"))
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(out *http.Request) {
		originalDirector(out)
		out.Host = target.Host
		for key := range out.Header {
			if strings.HasPrefix(strings.ToLower(key), "x-apteva-") {
				out.Header.Del(key)
			}
		}
		if visitorAuthorization == "" {
			out.Header.Del("Authorization")
		} else {
			out.Header.Set("Authorization", visitorAuthorization)
		}
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, proxyErr error) {
		globalCtx.Logger().Warn("community portal gateway failed", "err", proxyErr.Error())
		http.Error(rw, "portal service unavailable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

func allowedCommunityPortalGatewayPath(path string) bool {
	return strings.HasPrefix(path, "/api/apps/community/") ||
		strings.HasPrefix(path, "/api/apps/auth/") ||
		strings.HasPrefix(path, "/api/apps/storage/public/") ||
		path == "/api/app-events/community"
}
