package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// REST surface — mirror of the MCP tools.
//
//   /api/certs                          collection
//   /api/certs/<id-or-fqdn>             item; sub-actions: /renew, /revoke

func (a *App) handleCertsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListCerts(w, r)
	case http.MethodPost:
		a.httpIssueCert(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleCertItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/certs/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "id or fqdn required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	key := parts[0]
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := lookupCertByKey(pid, key)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	switch {
	case tail == "":
		a.httpCertDetail(w, r, c)
	case tail == "renew":
		a.httpCertRenew(w, r, c)
	case tail == "revoke":
		a.httpCertRevoke(w, r, c)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

// handleMeta exposes the certs app's resolved config so the panel can
// show a one-line status without talking to other apps directly:
// which challenge type is in effect, and — for dns-01 — whether the
// Domains app is bound and which apexes it manages. Mirrors the
// deploy app's /api/_meta pattern.
func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{
		"challenge_type":    a.selectChallengeType(globalCtx, pid),
		"domains_available": false,
		"domains":           []string{},
		"cert_output_dir":   a.certOutputDir,
	}
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := callDomainsTool(globalCtx, pid, "domain_list", map[string]any{}, &resp); err == nil {
		names := make([]string, 0, len(resp.Domains))
		for _, d := range resp.Domains {
			if d.Name != "" {
				names = append(names, d.Name)
			}
		}
		out["domains"] = names
		out["domains_available"] = len(names) > 0
	}
	httpJSON(w, out)
}

// ─── Collection ────────────────────────────────────────────────────

func (a *App) httpListCerts(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	include := r.URL.Query().Get("include_revoked") == "1"
	rows, err := dbListCerts(globalCtx.AppDB(), pid, include)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"certs": rows, "count": len(rows)})
}

func (a *App) httpIssueCert(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		FQDN string `json:"fqdn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.FQDN) == "" {
		httpErr(w, http.StatusBadRequest, "fqdn required")
		return
	}
	c, err := dbInsertOrTouchCert(globalCtx.AppDB(), pid, body.FQDN)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emit("certs.issuance.requested", map[string]any{"cert_id": c.ID, "fqdn": c.FQDN})
	a.kickIssuance(globalCtx, pid, body.FQDN)
	httpJSON(w, map[string]any{"cert": c})
}

// ─── Item ──────────────────────────────────────────────────────────

func (a *App) httpCertDetail(w http.ResponseWriter, r *http.Request, c *Cert) {
	switch r.Method {
	case http.MethodGet:
		httpJSON(w, map[string]any{"cert": c})
	case http.MethodDelete:
		if err := dbSetCertStatus(globalCtx.AppDB(), c.ID, "revoked", ""); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = removeCertFiles(a.certOutputDir, c.FQDN)
		emit("certs.revoked", map[string]any{"cert_id": c.ID, "fqdn": c.FQDN})
		httpJSON(w, map[string]any{"revoked": true, "id": c.ID})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or DELETE")
	}
}

func (a *App) httpCertRenew(w http.ResponseWriter, r *http.Request, c *Cert) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	a.kickIssuance(globalCtx, c.ProjectID, c.FQDN)
	out, _ := dbGetCert(globalCtx.AppDB(), c.ID)
	httpJSON(w, map[string]any{"cert": out})
}

func (a *App) httpCertRevoke(w http.ResponseWriter, r *http.Request, c *Cert) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if err := dbSetCertStatus(globalCtx.AppDB(), c.ID, "revoked", ""); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = removeCertFiles(a.certOutputDir, c.FQDN)
	emit("certs.revoked", map[string]any{"cert_id": c.ID, "fqdn": c.FQDN})
	httpJSON(w, map[string]any{"revoked": true, "id": c.ID})
}

// ─── helpers ──────────────────────────────────────────────────────

func lookupCertByKey(projectID, key string) (*Cert, error) {
	if id, err := strconv.ParseInt(key, 10, 64); err == nil && id > 0 {
		c, err := dbGetCert(globalCtx.AppDB(), id)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, errNotFound("cert", key)
		}
		return c, nil
	}
	c, err := dbGetCertByFQDN(globalCtx.AppDB(), projectID, key)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errNotFound("cert", key)
	}
	return c, nil
}

type notFoundErr struct{ kind, key string }

func (e *notFoundErr) Error() string      { return e.kind + " " + e.key + " not found" }
func errNotFound(kind, key string) error  { return &notFoundErr{kind: kind, key: key} }

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
