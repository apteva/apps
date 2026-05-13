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
//
// New contract (v0.2 admin-driven bootstrap): spawn the child with a
// fleet-minted APTEVA_SETUP_TOKEN, wait for /api/health to come up,
// return the setup info to the caller. NO admin is auto-registered —
// the operator finishes registration in the browser, then calls
// tenant_attach_key to hand fleet the resulting api_key.

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

	// Insert in "starting" so a concurrent caller doesn't reuse the
	// slug while we're spawning. api_key_enc holds a sentinel; the
	// real setup_token_enc is filled in after spawn (the apteva CLI
	// is what mints it — see spawnTenant comment for why fleet can't
	// inject one in advance).
	t := &Tenant{
		Slug:       slug,
		Kind:       KindLocal,
		BaseURL:    fmt.Sprintf("http://localhost:%d", port),
		ConfigDir:  configDir,
		OwnerEmail: owner,
		Status:     StatusStarting,
	}
	apiKeyStub, err := a.keys.seal([]byte("pending"))
	if err != nil {
		return nil, err
	}
	if err := a.store.insert(t, apiKeyStub, nil); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "spawn_start", "user", map[string]any{"port": port, "config_dir": configDir})

	// Spawn — 60s budget (server + core boot can run 10-30s on cold
	// disk). Boot timeout = tenant marked failed + data dir removed.
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	setupToken, proc, err := a.spawnTenant(spawnCtx, slug, configDir, getStr(args, "apteva_bin"), port, true /* freshSetup */)
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		_ = a.store.recordEvent(t.ID, "spawn_failed", "user", map[string]any{"error": err.Error()})
		_ = os.RemoveAll(configDir)
		_ = a.store.hardDelete(t.ID)
		return nil, err
	}

	// Seal the scraped token and persist it now that we have it.
	setupTokenEnc, err := a.keys.seal([]byte(setupToken))
	if err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return nil, err
	}
	if _, err := a.store.db.Exec(
		`UPDATE fleet_tenants SET setup_token_enc = ?, status = ?, updated_at = ? WHERE id = ?`,
		setupTokenEnc, StatusSetupPending, time.Now().UTC(), t.ID,
	); err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return nil, err
	}

	a.procMu.Lock()
	a.procs[slug] = proc
	a.procMu.Unlock()

	_ = a.store.recordEvent(t.ID, "spawned", "user", map[string]any{"port": port})
	ctx.Logger().Info("fleet: tenant spawned (setup_pending)", "tenant", t.ID, "slug", slug, "port", port)

	return map[string]any{
		"tenant_id":   t.ID,
		"slug":        slug,
		"base_url":    t.BaseURL,
		"status":      StatusSetupPending,
		"setup_url":   t.BaseURL + "/?setup=1",
		"setup_token": setupToken,
		"next_steps": "1. Open setup_url and register an admin (email + password); paste setup_token when asked. " +
			"2. In the tenant dashboard, create an API key (e.g. named 'fleet'). " +
			"3. Call tenant_attach_key with the tenant_id and the new api_key to finish linking.",
	}, nil
}

// -- tenant_attach_key: complete the admin-driven bootstrap ---------------

func (a *App) toolAttachKey(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	apiKey := getStr(args, "api_key")
	if id == "" || apiKey == "" {
		return nil, errors.New("tenant_id and api_key are required")
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	if t.Status == StatusActive {
		return nil, errors.New("tenant already linked; use a fresh tenant_create or rotate manually")
	}
	if t.Status != StatusSetupPending && t.Status != StatusStarting {
		return nil, fmt.Errorf("tenant in status %q is not awaiting a key", t.Status)
	}

	// Validate the key by hitting /api/health with auth. We accept
	// any 200 — health is unauthenticated, so a wrong key still
	// returns 200 with the version body. The real "is this an admin
	// key?" check is /api/auth/status which 401s on bad keys.
	if err := verifyAPIKey(context.Background(), t.BaseURL, apiKey); err != nil {
		return nil, fmt.Errorf("verify api_key: %w", err)
	}

	enc, err := a.keys.seal([]byte(apiKey))
	if err != nil {
		return nil, err
	}
	if err := a.store.attachAPIKey(t.ID, enc); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "key_attached", "user", nil)
	ctx.Logger().Info("fleet: tenant key attached", "tenant", t.ID)

	// Best-effort: refresh last_seen + version now so the operator
	// sees the row as live immediately rather than waiting up to 60s
	// for the next health poller pass.
	if ok, version, body, herr := probeHealth(context.Background(), t.BaseURL, apiKey); herr == nil && ok {
		_ = a.store.updateHealth(t.ID, true, version, body)
	}

	return map[string]any{"tenant_id": t.ID, "status": StatusActive}, nil
}

// verifyAPIKey GETs /api/auth/status with the supplied bearer and
// asserts a 200. 401 here means the key is bad or doesn't resolve to
// a user; any other non-2xx is treated as a transient failure rather
// than a hard reject so we don't lose a perfectly good key when the
// tenant is briefly slow.
func verifyAPIKey(ctx context.Context, baseURL, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/auth/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("tenant rejected the key (401) — wrong key, or it isn't tied to a registered user")
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tenant returned %d on auth status: %s", resp.StatusCode, string(body))
	}
	return nil
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
	if err := a.store.insert(t, enc, nil); err != nil {
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
	return a.decorateView(t, events), nil
}

// decorateView builds the operator-facing get response. For tenants
// still in setup_pending it surfaces the decrypted setup_token + URL
// so the operator can recover the info on refresh without re-running
// tenant_create. Once attached (status flips, token is NULLed) these
// fields naturally fall away.
func (a *App) decorateView(t *Tenant, events []Event) map[string]any {
	out := map[string]any{"tenant": t, "events": events}
	if t.Status != StatusSetupPending {
		return out
	}
	enc, err := a.store.getSetupToken(t.ID)
	if err != nil || len(enc) == 0 {
		return out
	}
	tok, err := a.keys.open(enc)
	if err != nil {
		return out
	}
	out["setup_token"] = string(tok)
	out["setup_url"] = t.BaseURL + "/?setup=1"
	return out
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
	// Re-spawning a previously-bootstrapped tenant: the server already
	// has a users table, so registration isn't in setup mode and we
	// don't need to scrape a token. The stored api_key remains valid.
	prevStatus := t.Status
	_ = a.store.setStatus(t.ID, StatusStarting, "user")
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, proc, err := a.spawnTenant(spawnCtx, t.Slug, t.ConfigDir, "", port, false /* freshSetup */)
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		return nil, err
	}
	// If we interrupted mid-setup (never attached a key), preserve
	// the setup_pending status so the operator can still complete it.
	// Otherwise flip to active.
	newStatus := StatusActive
	if prevStatus == StatusSetupPending {
		newStatus = StatusSetupPending
	}
	_ = a.store.setStatus(t.ID, newStatus, "user")
	a.procMu.Lock()
	a.procs[t.Slug] = proc
	a.procMu.Unlock()
	_ = a.store.recordEvent(t.ID, "started", "user", nil)
	ctx.Logger().Info("fleet: tenant started", "tenant", t.ID, "port", port, "status", newStatus)
	return map[string]any{"tenant_id": t.ID, "status": newStatus}, nil
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
	if t.Status == StatusSetupPending {
		return nil, errors.New("tenant is in setup_pending — finish admin registration and call tenant_attach_key before minting a support session")
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
	writeJSON(w, http.StatusOK, a.decorateView(t, events))
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
