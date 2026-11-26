package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// handleGlobalOverview serves the All projects Home widget from a global
// installation. The project set comes from the platform for this viewer (the
// request's user session), never from the browser; a selected project must be
// in that set. Delegated application users never get the cross-project view.
func (a *App) handleGlobalOverview(w http.ResponseWriter, r *http.Request) {
	if delegatedRequest(r) {
		http.Error(w, "all projects overview is unavailable to delegated application users", http.StatusForbidden)
		return
	}
	if requestUserID(r) <= 0 {
		http.Error(w, "authenticated user required", http.StatusUnauthorized)
		return
	}
	viewer := a.ctx.WithUserSession(r)
	if viewer == nil || viewer.PlatformAPI() == nil {
		http.Error(w, "platform unavailable", http.StatusServiceUnavailable)
		return
	}
	out, err := a.overviewGlobalVia(viewer.PlatformAPI(), r.URL.Query().Get("project_id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errProjectNotVisible) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeOverviewJSON(w, out)
}

// requestUserID is the platform user the proxy stamped on the request.
func requestUserID(r *http.Request) int64 {
	raw := r.Header.Get("X-User-ID")
	if raw == "" {
		raw = r.Header.Get("X-Apteva-User-ID")
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id
}

// delegatedRequest reports an application-user token: only those carry issuer
// markers. The platform strips client-supplied identity headers.
func delegatedRequest(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("X-Apteva-Issuer-App")) != "" ||
		strings.TrimSpace(r.Header.Get("X-Apteva-Issuer-Install-ID")) != ""
}

func writeOverviewJSON(w http.ResponseWriter, out *processOverview) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
