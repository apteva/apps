package main

import (
	"embed"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

//go:embed ui/join.js
var guestScript []byte

//go:embed ui/frontend.json ui/frontend/*.mjs
var frontendAssets embed.FS

func handleFrontendAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpErr(w, http.StatusMethodNotAllowed, "GET or HEAD")
		return
	}
	asset, err := frontendAssets.ReadFile(strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/ui/frontend.json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet {
		_, _ = w.Write(asset)
	}
}

func (a *App) allowJoinOrigin(r *http.Request) bool {
	if requireSameOrigin(r) == nil {
		return true
	}
	// Cross-origin admission accepts only JSON invite redemption. It does
	// not rely on cookies, and the gateway owns the CORS response headers.
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || origin != u.Scheme+"://"+u.Host {
		return false
	}
	for _, allowed := range strings.Split(globalCtx.Config().Get("allowed_embed_origins"), ",") {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}
	return false
}
