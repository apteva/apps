package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// httpClient is reused across handlers, the health poller, and the
// readiness probe. The short timeout keeps a wedged tenant from
// blocking the parent.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// -- tenant_create: spawn a fresh local tenant ---------------------------

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	slug := strings.ToLower(strings.TrimSpace(getStr(args, "slug")))
	owner := getStr(args, "owner_email")
	if slug == "" || owner == "" {
		return nil, errors.New("slug and owner_email are required")
	}
	if _, _, err := a.store.getBySlug(slug); err == nil {
		return nil, fmt.Errorf("slug %q already in use", slug)
	}
	configDir, err := slugDataDir(slug)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configDir); err == nil {
		return nil, fmt.Errorf("data dir already exists: %s", configDir)
	}
	port, err := allocatePort()
	if err != nil {
		return nil, err
	}

	// Insert in "starting" status so a concurrent caller observes the
	// in-progress provision and doesn't reuse the slug.
	t := &Tenant{
		Slug:       slug,
		Kind:       KindLocal,
		BaseURL:    fmt.Sprintf("http://localhost:%d", port),
		ConfigDir:  configDir,
		OwnerEmail: owner,
		Status:     StatusStarting,
	}
	stub, err := a.keys.seal([]byte("pending"))
	if err != nil {
		return nil, err
	}
	if err := a.store.insert(t, stub); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "spawn_start", "user", map[string]any{"port": port, "config_dir": configDir})

	// Spawn — 30s readiness budget. Boot timeout = tenant marked failed.
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	apiKey, proc, err := a.spawnTenant(spawnCtx, slug, configDir, getStr(args, "apteva_bin"), port)
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		_ = a.store.recordEvent(t.ID, "spawn_failed", "user", map[string]any{"error": err.Error()})
		// Best-effort cleanup of the half-built data dir.
		_ = os.RemoveAll(configDir)
		_ = a.store.hardDelete(t.ID)
		return nil, err
	}

	// Replace the stub api_key with the real one.
	enc, err := a.keys.seal([]byte(apiKey))
	if err != nil {
		return nil, err
	}
	if _, err := a.store.db.Exec(`UPDATE fleet_tenants SET api_key_enc = ?, status = ?, updated_at = ? WHERE id = ?`,
		enc, StatusActive, time.Now().UTC(), t.ID); err != nil {
		return nil, err
	}

	a.procMu.Lock()
	a.procs[slug] = proc
	a.procMu.Unlock()

	_ = a.store.recordEvent(t.ID, "spawned", "user", map[string]any{"port": port})
	ctx.Logger().Info("fleet: tenant spawned", "tenant", t.ID, "slug", slug, "port", port)
	return map[string]any{
		"tenant_id": t.ID,
		"slug":      slug,
		"base_url":  t.BaseURL,
		"status":    StatusActive,
	}, nil
}

// -- tenant_connect: register a pre-existing apteva ----------------------

func (a *App) toolConnect(_ *sdk.AppCtx, args map[string]any) (any, error) {
	baseURL := strings.TrimRight(getStr(args, "base_url"), "/")
	apiKey := getStr(args, "api_key")
	owner := getStr(args, "owner_email")
	slug := getStr(args, "slug")
	if baseURL == "" || apiKey == "" || owner == "" {
		return nil, errors.New("base_url, api_key, owner_email are required")
	}
	if slug == "" {
		slug = deriveSlug(baseURL)
	}
	if _, _, err := a.store.getBySlug(slug); err == nil {
		return nil, fmt.Errorf("slug %q already in use", slug)
	}
	ok, version, body, err := probeHealth(context.Background(), baseURL, apiKey)
	if err != nil {
		return nil, fmt.Errorf("health probe failed: %w", err)
	}
	if !ok {
		return nil, errors.New("tenant /api/health returned non-OK; check base_url and api_key")
	}
	enc, err := a.keys.seal([]byte(apiKey))
	if err != nil {
		return nil, err
	}
	t := &Tenant{
		Slug:           slug,
		Kind:           KindRemote,
		BaseURL:        baseURL,
		OwnerEmail:     owner,
		CurrentVersion: version,
		Status:         StatusActive,
	}
	if err := a.store.insert(t, enc); err != nil {
		return nil, err
	}
	_ = a.store.updateHealth(t.ID, true, version, body)
	_ = a.store.recordEvent(t.ID, "connected", "user", map[string]any{"slug": slug})
	return map[string]any{"tenant_id": t.ID, "slug": t.Slug, "status": t.Status}, nil
}

// -- tenant_list / get ---------------------------------------------------

func (a *App) toolList(_ *sdk.AppCtx, args map[string]any) (any, error) {
	filter := map[string]string{
		"status":      getStr(args, "status"),
		"owner_email": getStr(args, "owner_email"),
		"version":     getStr(args, "version"),
		"kind":        getStr(args, "kind"),
	}
	list, err := a.store.list(filter)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tenants": list, "count": len(list)}, nil
}

func (a *App) toolGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	events, err := a.store.recentEvents(id, 20)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tenant": t, "events": events}, nil
}

// -- tenant_start / stop -------------------------------------------------

func (a *App) toolStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if t.Kind != KindLocal {
		return nil, fmt.Errorf("tenant %s is kind=%s; tenant_start is local-only (use tenant_run_remote against the remote endpoint)", id, t.Kind)
	}
	port, err := portFromBaseURL(t.BaseURL)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("cannot derive port from base_url %q", t.BaseURL)
	}
	if portInUse(port) {
		// Already running — make the registry agree.
		_ = a.store.setStatus(t.ID, StatusActive, "user")
		return map[string]any{"tenant_id": t.ID, "status": StatusActive, "note": "process already listening on port"}, nil
	}
	_ = a.store.setStatus(t.ID, StatusStarting, "user")
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	apiKey, proc, err := a.spawnTenant(spawnCtx, t.Slug, t.ConfigDir, "", port)
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		return nil, err
	}
	// api_key may have rotated if the operator wiped apteva.json — re-seal.
	enc, _ := a.keys.seal([]byte(apiKey))
	_, _ = a.store.db.Exec(`UPDATE fleet_tenants SET api_key_enc = ?, status = ?, updated_at = ? WHERE id = ?`,
		enc, StatusActive, time.Now().UTC(), t.ID)
	a.procMu.Lock()
	a.procs[t.Slug] = proc
	a.procMu.Unlock()
	_ = a.store.recordEvent(t.ID, "started", "user", nil)
	ctx.Logger().Info("fleet: tenant started", "tenant", t.ID, "port", port)
	return map[string]any{"tenant_id": t.ID, "status": StatusActive}, nil
}

func (a *App) toolStop(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if t.Kind != KindLocal {
		// Remote tenant — registry only.
		_ = a.store.setStatus(t.ID, StatusSuspended, "user")
		return map[string]any{"tenant_id": t.ID, "status": StatusSuspended}, nil
	}
	a.procMu.Lock()
	proc := a.procs[t.Slug]
	delete(a.procs, t.Slug)
	a.procMu.Unlock()
	if proc != nil {
		_ = stopProcess(proc, 10*time.Second)
	}
	_ = a.store.setStatus(t.ID, StatusStopped, "user")
	_ = a.store.recordEvent(t.ID, "stopped", "user", nil)
	return map[string]any{"tenant_id": t.ID, "status": StatusStopped}, nil
}

// -- tenant_delete -------------------------------------------------------

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	confirm, _ := args["confirm"].(bool)
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}

	if t.Kind == KindLocal {
		a.procMu.Lock()
		proc := a.procs[t.Slug]
		delete(a.procs, t.Slug)
		a.procMu.Unlock()
		if proc != nil {
			_ = stopProcess(proc, 10*time.Second)
		}
		if !confirm {
			// Process stopped but data dir preserved — let the operator
			// recover by hand if delete was a mistake.
			_ = a.store.setStatus(t.ID, StatusStopped, "user")
			return map[string]any{
				"tenant_id": t.ID,
				"status":    StatusStopped,
				"note":      "process stopped; data dir preserved at " + t.ConfigDir + ". Re-run with confirm=true to wipe and remove.",
			}, nil
		}
		if t.ConfigDir != "" {
			if err := os.RemoveAll(t.ConfigDir); err != nil {
				ctx.Logger().Error("fleet: rm config dir", "tenant", t.ID, "err", err)
				return nil, fmt.Errorf("remove data dir: %w", err)
			}
		}
	}

	if err := a.store.hardDelete(t.ID); err != nil {
		return nil, err
	}
	return map[string]any{"tenant_id": t.ID, "status": StatusDeleted}, nil
}

// -- tenant_support_login ------------------------------------------------

func (a *App) toolSupportLogin(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	reason := getStr(args, "reason")
	if reason == "" {
		return nil, errors.New("reason is required for an audit trail on the tenant")
	}
	t, enc, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"reason": reason, "ttl_seconds": 900})
	req, _ := http.NewRequest(http.MethodPost, t.BaseURL+"/api/admin/support_session", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("tenant %s lacks /api/admin/support_session — upgrade tenant apteva-server", id)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tenant returned %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse tenant response: %w", err)
	}
	_ = a.store.recordEvent(id, "support_login", "user", map[string]any{"reason": reason, "expires_at": parsed.ExpiresAt})
	return parsed, nil
}

// -- HTTP routes ---------------------------------------------------------

func (a *App) httpList(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.list(map[string]string{
		"status":      r.URL.Query().Get("status"),
		"owner_email": r.URL.Query().Get("owner_email"),
		"version":     r.URL.Query().Get("version"),
		"kind":        r.URL.Query().Get("kind"),
	})
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": list, "count": len(list)})
}

func (a *App) httpGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	t, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, err)
		return
	}
	events, _ := a.store.recentEvents(id, 20)
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t, "events": events})
}

// -- shared helpers ------------------------------------------------------

func getStr(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func deriveSlug(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "tenant"
	}
	host := u.Hostname()
	if i := strings.Index(host, "."); i > 0 {
		return host[:i]
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
