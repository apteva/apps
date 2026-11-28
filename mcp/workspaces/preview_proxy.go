package main

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// handlePreviewProxy serves /p/<workspace_id>/<token>/<path> by proxying to
// the workspace's preview on this machine's loopback (or its Instances
// tunnel), so the link opens from any browser that reaches Apteva.
//
// The route needs no Apteva session: the token in the path is the
// credential, made fresh each time the preview starts. Responses carry a
// Content-Security-Policy sandbox, so the preview's pages run in an opaque
// origin. A preview is code an agent wrote, served from the Apteva origin;
// without the sandbox its scripts could call the Apteva API as whoever
// opened the link.
func (a *App) handlePreviewProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/p/")
	id, rest, _ := strings.Cut(rest, "/")
	token, tail, hasSlash := strings.Cut(rest, "/")
	db := globalCtx.AppDB()
	var p *Preview
	var ws *Workspace
	if id != "" && token != "" {
		var projectID string
		if err := db.QueryRow(`SELECT project_id FROM workspaces WHERE id=?`, id).Scan(&projectID); err == nil {
			ws, _ = getWorkspace(db, projectID, id)
		}
		if ws != nil {
			p, _ = getPreview(db, ws.ID)
		}
	}
	if p == nil || p.AccessToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(p.AccessToken)) != 1 {
		http.Error(w, "preview link not found or no longer valid (it changes each time the preview starts)", http.StatusNotFound)
		return
	}
	public := previewPublicPath(ws.ID, p.AccessToken)
	if !hasSlash {
		// Relative asset links need the trailing slash.
		target := public + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		// Preflight from the sandboxed page itself (origin "null").
		setPreviewResponseHeaders(w.Header())
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
			w.Header().Set("Access-Control-Allow-Headers", headers)
		}
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if (p.Status != "live" && p.Status != "starting") || p.upstreamPort() <= 0 {
		status := p.Status
		if p.Error != "" {
			status += ": " + p.Error
		}
		previewUnavailable(w, fmt.Sprintf("The preview of workspace %s is %s. Start it again with workspace_preview_start.", ws.Name, status))
		return
	}
	upstream := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(p.upstreamPort())}
	proxy := &httputil.ReverseProxy{
		// -1 flushes every write: streamed responses and server-sent events
		// reach the browser as they are produced. Upgrades (websockets) are
		// handled by ReverseProxy itself.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.URL.Path = "/" + tail
			pr.Out.URL.RawPath = ""
			pr.Out.Host = upstream.Host
			scrubPreviewRequest(pr.Out.Header)
			pr.Out.Header.Set("X-Forwarded-Prefix", public)
			pr.SetXForwarded()
		},
		ModifyResponse: func(resp *http.Response) error {
			// Keep the preview's own redirects under its prefix.
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !strings.HasPrefix(loc, public+"/") {
				resp.Header.Set("Location", public+loc)
			}
			// The preview cannot loosen the sandbox or set cookies on the
			// Apteva origin.
			for _, key := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only", "Set-Cookie", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
				resp.Header.Del(key)
			}
			setPreviewResponseHeaders(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			previewUnavailable(w, fmt.Sprintf("The preview of workspace %s is not answering on port %d yet (%v). It may still be starting; check workspace_preview_logs.", ws.Name, p.ContainerPort, err))
		},
	}
	proxy.ServeHTTP(w, r)
}

// setPreviewResponseHeaders sandboxes preview pages into an opaque origin
// (scripts, forms and popups still work; storage and cookies of the Apteva
// origin do not) and lets those pages fetch their own URLs.
func setPreviewResponseHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads allow-pointer-lock allow-presentation")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Cache-Control", "no-store")
}

// scrubPreviewRequest drops credentials and platform metadata: the preview is
// arbitrary code and must not see the user's session or Apteva's headers.
func scrubPreviewRequest(h http.Header) {
	h.Del("Cookie")
	h.Del("Authorization")
	h.Del("Proxy-Authorization")
	for key := range h {
		if strings.HasPrefix(strings.ToLower(key), "x-apteva-") {
			h.Del(key)
		}
	}
}

func previewUnavailable(w http.ResponseWriter, message string) {
	setPreviewResponseHeaders(w.Header())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Retry-After", "3")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte(message + "\n"))
}
