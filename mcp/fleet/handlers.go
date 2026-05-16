package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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

// publicBaseURL rewrites a stored loopback URL to one operators can
// open from outside the fleet host. Storage keeps the loopback form
// because fleet's INTERNAL calls (auto-setup orchestrator, health
// poller, run_remote) all run on the same machine as the tenant —
// loopback is the most reliable path. The public form is only for
// what we display + return to operators.
//
// Only the host is rewritten (port preserved); if the stored host
// is already non-local (a tenant connected via tenant_connect against
// a remote apteva-server) we pass it through unchanged.
func (a *App) publicBaseURL(stored string) string {
	if a == nil || a.publicHost == "" || a.publicHost == "localhost" {
		return stored
	}
	u, err := url.Parse(stored)
	if err != nil {
		return stored
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return stored
	}
	if u.Port() != "" {
		u.Host = a.publicHost + ":" + u.Port()
	} else {
		u.Host = a.publicHost
	}
	return u.String()
}

// publicTenantView returns the tenant shape we send out to operators
// with base_url rewritten to the public form. We can't mutate the
// pointer Tenant in place because the same row may be cached / shared
// across callers and we don't want to corrupt the canonical stored
// URL — copy the struct first.
func (a *App) publicTenantView(t *Tenant) *Tenant {
	if t == nil {
		return nil
	}
	cp := *t
	cp.BaseURL = a.publicBaseURL(t.BaseURL)
	return &cp
}

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
	// Hosted dispatch: when instance_id > 0, fleet drives a remote
	// VPS (via the Instances integration) instead of spawning a
	// local process. The hosted path shares nothing with the local
	// path other than the auto-setup orchestrator — keeping it in
	// hostedproc.go makes the divergence explicit.
	if id := int64Arg(args, "instance_id"); id > 0 {
		return a.toolCreateHosted(ctx, args, slug, owner, id)
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

	// Resolve which apteva binary to spawn under. v0.4 default: pin
	// fresh tenants to the current npm latest so a tenant's version
	// isn't a side effect of the operator's $PATH. Override per-create
	// with apteva_version ("host" to fall back to PATH) or apteva_bin
	// (literal path); fleet-wide via FLEET_DEFAULT_APTEVA_VERSION env.
	// Install can take 10-20s on cold cache — extend the create budget
	// so a tenant_create that has to first download apteva@latest doesn't
	// race the spawn timeout.
	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 60*time.Second)
	spawnBin, resolvedVer, err := a.resolveSpawnBin(resolveCtx,
		getStr(args, "apteva_bin"), getStr(args, "apteva_version"))
	resolveCancel()
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		_ = a.store.recordEvent(t.ID, "spawn_failed", "user", map[string]any{"stage": "resolve_bin", "error": err.Error()})
		_ = os.RemoveAll(configDir)
		_ = a.store.hardDelete(t.ID)
		return nil, fmt.Errorf("resolve apteva binary: %w", err)
	}
	// Pin target_version BEFORE spawn so an auto-respawn after a
	// crash mid-create still uses the same binary (tryRespawn reads
	// tenantAptevaBin(target_version)).
	if resolvedVer != "" {
		_ = a.store.setTargetVersion(t.ID, resolvedVer)
	}

	// Spawn — 60s budget (server + core boot can run 10-30s on cold
	// disk). Boot timeout = tenant marked failed + data dir removed.
	spawnCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	setupToken, proc, err := a.spawnTenant(spawnCtx, slug, configDir, spawnBin, port, true /* freshSetup */)
	if err != nil {
		_ = a.store.setStatus(t.ID, StatusFailed, "user")
		_ = a.store.recordEvent(t.ID, "spawn_failed", "user", map[string]any{"error": err.Error()})
		_ = os.RemoveAll(configDir)
		_ = a.store.hardDelete(t.ID)
		return nil, err
	}

	a.procMu.Lock()
	a.procs[slug] = proc
	a.procMu.Unlock()

	_ = a.store.recordEvent(t.ID, "spawned", "user", map[string]any{"port": port})

	// Auto-setup path: run register → login → mint_key. On success
	// the tenant flips straight to active and we surface the admin
	// credentials + api_key in the response (one-shot reveal). On
	// failure we persist the setup_token and fall back to the manual
	// setup_pending flow so the operator can finish by hand.
	autoSetup, err := a.autoSetupTenant(context.Background(), t.BaseURL, setupToken, owner, "")
	if err != nil {
		ctx.Logger().Warn("fleet: auto-setup failed, falling back to setup_pending", "tenant", t.ID, "err", err)
		setupTokenEnc, sealErr := a.keys.seal([]byte(setupToken))
		if sealErr != nil {
			_ = stopProcess(proc, 2*time.Second)
			return nil, sealErr
		}
		if _, dbErr := a.store.db.Exec(
			`UPDATE fleet_tenants SET setup_token_enc = ?, status = ?, updated_at = ? WHERE id = ?`,
			setupTokenEnc, StatusSetupPending, time.Now().UTC(), t.ID,
		); dbErr != nil {
			_ = stopProcess(proc, 2*time.Second)
			return nil, dbErr
		}
		_ = a.store.recordEvent(t.ID, "auto_setup_failed", "user", map[string]any{"error": err.Error()})
		publicURL := a.publicBaseURL(t.BaseURL)
		return map[string]any{
			"tenant_id":         t.ID,
			"slug":              slug,
			"base_url":          publicURL,
			"status":            StatusSetupPending,
			"setup_url":         publicURL + "/?setup=1",
			"setup_token":       setupToken,
			"auto_setup_error":  err.Error(),
			"next_steps":        "Auto-setup failed (see auto_setup_error). Manual recovery: open setup_url, register admin with the setup_token, generate an api_key, call tenant_attach_key.",
		}, nil
	}

	// Persist the freshly-minted api_key + flip status to active.
	apiKeyEnc, err := a.keys.seal([]byte(autoSetup.APIKey))
	if err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return nil, err
	}
	if err := a.store.attachAPIKey(t.ID, apiKeyEnc); err != nil {
		_ = stopProcess(proc, 2*time.Second)
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "auto_setup_complete", "user", map[string]any{"admin_email": owner})
	ctx.Logger().Info("fleet: tenant auto-setup complete", "tenant", t.ID, "slug", slug, "port", port)

	return map[string]any{
		"tenant_id":      t.ID,
		"slug":           slug,
		"base_url":       a.publicBaseURL(t.BaseURL),
		"status":         StatusActive,
		"admin_email":    owner,
		"admin_password": autoSetup.Password,
		"api_key":        autoSetup.APIKey,
		"next_steps":     "Save admin_password and api_key — they're shown ONCE. The admin_password lets the operator (or the client) log into the tenant dashboard at base_url; api_key is the long-lived bearer fleet uses internally.",
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
	out := make([]*Tenant, len(list))
	for i, t := range list {
		out[i] = a.publicTenantView(t)
	}
	return map[string]any{"tenants": out, "count": len(out)}, nil
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
//
// The tenant's base_url and setup_url are rewritten through
// publicBaseURL so operators see the host's public IP/hostname
// rather than the loopback form fleet uses internally.
func (a *App) decorateView(t *Tenant, events []Event) map[string]any {
	pub := a.publicTenantView(t)
	out := map[string]any{"tenant": pub, "events": events}
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
	out["setup_url"] = pub.BaseURL + "/?setup=1"
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

	// Hosted: re-spawn on the remote VPS. freshSetup=false → no
	// token scrape; the api_key is still valid against the existing
	// data dir.
	if t.IsHosted() {
		info, infoErr := a.getInstanceInfo(ctx, t.InstanceID)
		if infoErr != nil {
			return nil, infoErr
		}
		prevStatus := t.Status
		_ = a.store.setStatus(t.ID, StatusStarting, "user")
		_, _, spawnErr := a.spawnHostedTenant(ctx, hostedSpawnSpec{
			InstanceID: t.InstanceID,
			InstanceIP: info.PublicIPv4,
			Slug:       t.Slug,
			Port:       port,
			AptevaVer:  t.TargetVersion,
			FreshSetup: false,
		})
		if spawnErr != nil {
			_ = a.store.setStatus(t.ID, StatusFailed, "user")
			return nil, spawnErr
		}
		newStatus := StatusActive
		if prevStatus == StatusSetupPending {
			newStatus = StatusSetupPending
		}
		_ = a.store.setStatus(t.ID, newStatus, "user")
		_ = a.store.recordEvent(t.ID, "started", "user",
			map[string]any{"instance_id": t.InstanceID, "port": port})
		return map[string]any{"tenant_id": t.ID, "status": newStatus}, nil
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
	_, proc, err := a.spawnTenant(spawnCtx, t.Slug, t.ConfigDir, tenantAptevaBin(t.TargetVersion), port, false /* freshSetup */)
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

func (a *App) toolStop(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	// Hosted: ask the VPS to stop. Same kill-by-port+pgrp semantics
	// as local, just dispatched over SSH via instance_run_command.
	if t.IsHosted() {
		port, _ := portFromBaseURL(t.BaseURL)
		if err := stopHostedTenant(ctx, t.InstanceID, port, 10*time.Second); err != nil {
			return nil, fmt.Errorf("stop hosted: %w", err)
		}
		_ = a.store.setStatus(t.ID, StatusStopped, "user")
		_ = a.store.recordEvent(t.ID, "stopped", "user", nil)
		return map[string]any{"tenant_id": t.ID, "status": StatusStopped}, nil
	}
	// Local: stopTenantBy handles both paths: in-memory handle and
	// orphan-pid-by-port (fleet was upgraded since the tenant spawned).
	port, _ := portFromBaseURL(t.BaseURL)
	if err := a.stopTenantBy(t.Slug, port, 10*time.Second); err != nil {
		return nil, fmt.Errorf("stop: %w", err)
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
		port, _ := portFromBaseURL(t.BaseURL)
		// Stop the process — branch on local vs hosted, same as toolStop.
		if t.IsHosted() {
			_ = stopHostedTenant(ctx, t.InstanceID, port, 10*time.Second)
		} else {
			_ = a.stopTenantBy(t.Slug, port, 10*time.Second)
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
		// Wipe data dir — local fs for local tenants, SSH rm for hosted.
		if t.ConfigDir != "" {
			if t.IsHosted() {
				if err := destroyHostedTenant(ctx, t.InstanceID, t.Slug); err != nil {
					ctx.Logger().Error("fleet: rm remote config dir", "tenant", t.ID, "err", err)
					return nil, fmt.Errorf("remove remote data dir: %w", err)
				}
			} else {
				if err := os.RemoveAll(t.ConfigDir); err != nil {
					ctx.Logger().Error("fleet: rm config dir", "tenant", t.ID, "err", err)
					return nil, fmt.Errorf("remove data dir: %w", err)
				}
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

// intArg accepts int / int64 / float64 (the JSON decoder yields
// float64 for unmarshalled numbers). Returns def when missing or a
// non-numeric type.
func intArg(m map[string]any, key string, def int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

func int64Arg(m map[string]any, key string) int64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int:
			return int64(n)
		case int64:
			return n
		}
	}
	return 0
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

// -- Auto-setup orchestrator --------------------------------------------
//
// Runs the three HTTP steps every fresh apteva tenant needs to become
// usable from fleet's perspective:
//
//   1. POST /api/auth/register with X-Setup-Token → creates the admin
//      user (the server flips out of setup mode on success).
//   2. POST /api/auth/login (cookie jar captures the session cookie).
//   3. POST /api/auth/keys with session cookie → mints the api_key.
//
// Each step is a hard error: a partial success (e.g. registered admin
// but key minting failed) is surfaced so the caller can decide whether
// to roll back or fall back to manual setup_pending mode.

type autoSetupResult struct {
	APIKey   string
	Password string
}

func (a *App) autoSetupTenant(parent context.Context, baseURL, setupToken, email, password string) (*autoSetupResult, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	if password == "" {
		// 16 random bytes → 32 hex chars. Well past the server's
		// 8-char minimum and indistinguishable enough that the
		// operator can't memorise it (the point — they should treat
		// it as one-shot output to be saved somewhere).
		password = randomPassword()
	}

	// Step 1: register the admin. The server enforces password >= 8
	// chars; randomPassword() comfortably clears that.
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/register", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Setup-Token", setupToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("register: %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	// Step 2: log in with a cookie jar so /api/auth/keys sees a
	// session. Using a per-call jar (not the shared httpClient) so
	// concurrent setups don't share cookies.
	jar, _ := cookiejar.New(nil)
	authedClient := &http.Client{Timeout: httpClient.Timeout, Jar: jar}
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err = authedClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("login: %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	// Step 3: mint the api_key. The "fleet" name surfaces in the
	// tenant's API Keys list so the operator can later identify
	// which key is fleet-owned vs human-created.
	keyBody, _ := json.Marshal(map[string]string{"name": "fleet"})
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/keys", strings.NewReader(string(keyBody)))
	req.Header.Set("Content-Type", "application/json")
	resp, err = authedClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("keys: %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	var keyResp struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&keyResp); err != nil {
		return nil, fmt.Errorf("keys: parse: %w", err)
	}
	if keyResp.Key == "" {
		return nil, errors.New("keys: server returned empty api_key")
	}
	return &autoSetupResult{APIKey: keyResp.Key, Password: password}, nil
}

func randomPassword() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is a process-level disaster; we don't
		// have a sensible fallback that's actually secure. Panic
		// rather than silently emit a predictable password.
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// -- tenant_reveal_api_key / tenant_reset_admin_password ---------------
//
// Two operator recovery paths bolted onto the auto-setup credentials
// flow. The api_key is persisted (sealed via keyring) so we can return
// it verbatim. The admin password is NOT — auto-setup discards it
// after registering. Reset uses the api_key (admin-grade) to:
//   1. GET <base>/api/auth/me              → resolve user_id
//   2. PATCH <base>/api/users/<id>/password → set a new random password
//      (this also revokes every session for that user)
// and returns the freshly-minted password to the caller. The
// operator's recourse for "I forgot the password" is therefore "rotate
// it" rather than "show me the one I lost", which matches how managed
// instance products usually work.

func (a *App) toolRevealAPIKey(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, enc, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt key: %w", err)
	}
	_ = a.store.recordEvent(t.ID, "api_key_revealed", "tool:reveal_api_key", nil)
	return map[string]any{
		"tenant_id": t.ID,
		"slug":      t.Slug,
		"base_url":  a.publicBaseURL(t.BaseURL),
		"api_key":   string(key),
	}, nil
}

func (a *App) toolResetAdminPassword(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, enc, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt key: %w", err)
	}
	apiKey := string(key)

	// Resolve the user id via /api/auth/me — the api_key belongs to
	// the admin we registered at tenant_create, so /me identifies the
	// right target without us hardcoding a "primary admin" notion.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL+"/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+apiKey)
	meResp, err := httpClient.Do(meReq)
	if err != nil {
		return nil, fmt.Errorf("auth/me: %w", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode >= 300 {
		body, _ := io.ReadAll(meResp.Body)
		return nil, fmt.Errorf("auth/me: %d: %s", meResp.StatusCode, strings.TrimSpace(string(body)))
	}
	var me struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(meResp.Body).Decode(&me); err != nil {
		return nil, fmt.Errorf("auth/me decode: %w", err)
	}
	if me.ID == 0 {
		return nil, errors.New("auth/me returned no user id")
	}

	newPassword := randomPassword()
	body, _ := json.Marshal(map[string]string{"new_password": newPassword})
	resetReq, _ := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("%s/api/users/%d/password", t.BaseURL, me.ID),
		strings.NewReader(string(body)))
	resetReq.Header.Set("Authorization", "Bearer "+apiKey)
	resetReq.Header.Set("Content-Type", "application/json")
	resetResp, err := httpClient.Do(resetReq)
	if err != nil {
		return nil, fmt.Errorf("reset password: %w", err)
	}
	defer resetResp.Body.Close()
	if resetResp.StatusCode >= 300 {
		rbody, _ := io.ReadAll(resetResp.Body)
		return nil, fmt.Errorf("reset password: %d: %s", resetResp.StatusCode, strings.TrimSpace(string(rbody)))
	}
	_ = a.store.recordEvent(t.ID, "admin_password_reset", "tool:reset_admin_password",
		map[string]any{"user_id": me.ID, "email": me.Email})
	return map[string]any{
		"tenant_id":      t.ID,
		"slug":           t.Slug,
		"base_url":       a.publicBaseURL(t.BaseURL),
		"admin_email":    me.Email,
		"admin_password": newPassword,
		"note":           "All existing sessions for this user have been revoked.",
	}, nil
}

// HTTP wrappers — same handlers, panel-friendly entry.

func (a *App) httpRevealAPIKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("POST"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	res, err := a.toolRevealAPIKey(nil, map[string]any{"tenant_id": id})
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *App) httpResetAdminPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("POST"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	res, err := a.toolResetAdminPassword(nil, map[string]any{"tenant_id": id})
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// -- Project resolution -------------------------------------------------
//
// Fleet's manifest declares scopes: [project, global]. The cross-app
// calls fleet makes (domains_records_set, cert_issue, …) target apps
// that ARE project-scoped — so we must thread a project id through.
// Same pattern as deploy: APTEVA_PROJECT_ID env when scope=project,
// _project_id arg when scope=global. See
// feedback_project_id_global_calls in user memory.

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when fleet is global-scoped")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string")
}

// -- tenant_attach_domain / tenant_detach_domain -----------------------

func (a *App) toolAttachDomain(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	projectID, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	spec := attachDomainSpec{
		FQDN:   getStr(args, "fqdn"),
		Target: getStr(args, "target"),
		Type:   getStr(args, "type"),
	}
	if ttlF, ok := args["ttl"].(float64); ok {
		spec.TTL = int(ttlF)
	}
	if err := a.attachDomain(globalCtx, projectID, t, spec); err != nil {
		return nil, err
	}
	out, _, _ := a.store.get(id)
	return map[string]any{"tenant": a.publicTenantView(out)}, nil
}

func (a *App) toolDetachDomain(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	projectID, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	t, _, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	derr := a.detachDomain(globalCtx, projectID, t)
	out, _, _ := a.store.get(id)
	res := map[string]any{"tenant": a.publicTenantView(out), "detached": true}
	if derr != nil {
		res["registrar_error"] = derr.Error()
	}
	return res, nil
}

// toolSetTargetVersion just records desired version without applying.
// The auto-update worker (4c, deferred) would consume this; for now
// it lets the panel show "pinned to vX" intent.
func (a *App) toolSetTargetVersion(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	version := strings.TrimSpace(getStr(args, "version"))
	if err := a.store.setTargetVersion(id, version); err != nil {
		return nil, err
	}
	t, _, _ := a.store.get(id)
	return map[string]any{"tenant": a.publicTenantView(t)}, nil
}

// -- HTTP variants for the panel ---------------------------------------

func (a *App) httpAttachDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("POST"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	projectID, err := resolveProjectFromRequest(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	t, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		FQDN   string `json:"fqdn"`
		Target string `json:"target"`
		Type   string `json:"type"`
		TTL    int    `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	if err := a.attachDomain(globalCtx, projectID, t, attachDomainSpec{
		FQDN: body.FQDN, Target: body.Target, Type: body.Type, TTL: body.TTL,
	}); err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	out, _, _ := a.store.get(id)
	writeJSON(w, http.StatusOK, map[string]any{"tenant": a.publicTenantView(out)})
}

func (a *App) httpDetachDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("POST"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	projectID, err := resolveProjectFromRequest(r)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	t, _, err := a.store.get(id)
	if err != nil {
		writeJSONErr(w, http.StatusNotFound, err)
		return
	}
	derr := a.detachDomain(globalCtx, projectID, t)
	out, _, _ := a.store.get(id)
	res := map[string]any{"tenant": a.publicTenantView(out), "detached": true}
	if derr != nil {
		res["registrar_error"] = derr.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

// httpMeta — panel calls this once per load + on relevant events.
// Reports integration availability + the registered-domains picker
// list + cert status per tenant FQDN + the latest npm version. The
// panel doesn't talk to domains/certs/routes/npm directly.
func (a *App) httpMeta(w http.ResponseWriter, r *http.Request) {
	projectID, _ := resolveProjectFromRequest(r) // soft — empty is OK
	out := map[string]any{
		"domains_available": a.domainsAvailable(globalCtx),
		"certs_available":   a.certsAvailable(globalCtx),
		"routes_available":  a.routesAvailable(globalCtx),
		"public_host":       a.publicHost,
		"domains":           []any{},
		"certs":             map[string]any{},
	}
	if a.domainsAvailable(globalCtx) {
		var resp struct {
			Domains []struct {
				Name string `json:"name"`
			} `json:"domains"`
		}
		if err := callDomainsTool(globalCtx, projectID, "domain_list", map[string]any{}, &resp); err == nil {
			names := make([]map[string]any, 0, len(resp.Domains))
			for _, d := range resp.Domains {
				names = append(names, map[string]any{"name": d.Name})
			}
			out["domains"] = names
		}
	}
	if a.certsAvailable(globalCtx) {
		var resp struct {
			Certs []struct {
				FQDN      string `json:"fqdn"`
				Status    string `json:"status"`
				ExpiresAt string `json:"expires_at,omitempty"`
				Error     string `json:"error,omitempty"`
			} `json:"certs"`
		}
		if err := callCertsTool(globalCtx, projectID, "cert_list", map[string]any{}, &resp); err == nil {
			byFQDN := make(map[string]any, len(resp.Certs))
			for _, c := range resp.Certs {
				byFQDN[c.FQDN] = map[string]any{
					"status":     c.Status,
					"expires_at": c.ExpiresAt,
					"error":      c.Error,
				}
			}
			out["certs"] = byFQDN
		}
	}
	// npm latest — best-effort; failure leaves the field absent so
	// the panel doesn't render an empty version pill.
	if v, err := npmLatestVersion(r.Context()); err == nil {
		out["apteva_latest"] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// httpUpdate — POST /tenants/<id>/update?project_id=...  body: {version?}
func (a *App) httpUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErr(w, http.StatusMethodNotAllowed, errors.New("POST"))
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/tenants/")
	if i := strings.Index(id, "/"); i > 0 {
		id = id[:i]
	}
	var body struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	res, err := a.toolUpdate(nil, map[string]any{
		"tenant_id": id,
		"version":   body.Version,
	})
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
