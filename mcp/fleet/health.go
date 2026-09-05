package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// runHealthPoller is the @every 60s worker registered in main.go. One
// pass per fire — the scheduler handles cadence. The worker iterates
// every active tenant, probes /api/health with the stored api_key,
// and updates last_seen / current_version. After failuresToDisconnect
// consecutive failures we transition the tenant to disconnected; a
// later successful probe flips it back to active.
const failuresToDisconnect = 5

func (a *App) runHealthPoller(ctx context.Context, app *sdk.AppCtx) error {
	tenants, err := a.store.list(map[string]string{}) // every non-deleted
	if err != nil {
		return err
	}
	jobs := make(chan *Tenant)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tenant := range jobs {
				a.probeOnce(ctx, app, tenant)
			}
		}()
	}
	for _, t := range tenants {
		select {
		case jobs <- t:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

func (a *App) probeOnce(ctx context.Context, app *sdk.AppCtx, t *Tenant) {
	done, err := a.beginTenantOperation(t.ID, "health probe")
	if err != nil {
		return
	}
	defer done()
	t, _, err = a.store.get(t.ID)
	if err != nil {
		return
	}
	if t.Status != StatusActive && t.Status != StatusDisconnected {
		return
	}
	a.probeOnceLocked(ctx, app, t)
}
func (a *App) probeOnceLocked(ctx context.Context, app *sdk.AppCtx, t *Tenant) {
	// Local-on-parent tenants: port-presence pre-check. When the
	// port is empty the process is gone — kick a respawn before the
	// HTTP probe runs (which would just timeout and add 60s of
	// latency). Hosted tenants on a remote VPS skip this branch:
	// portInUse only sees the parent's loopback. We rely on the
	// regular HTTP probe + tryRespawnHosted for those (TODO in 0.6.x);
	// for now hosted tenants follow the same disconnect-after-5
	// pattern as kind=remote.
	if t.Kind == KindLocal && !t.IsHosted() {
		if port, _ := portFromBaseURL(t.BaseURL); port > 0 && !portInUse(port) {
			a.tryRespawnLocked(ctx, t)
			return // come back next tick to evaluate health
		}
	}
	_, enc, err := a.store.get(t.ID)
	if err != nil {
		return
	}
	key, err := a.keys.open(enc)
	if err != nil {
		app.Logger().Error("fleet: decrypt key", "tenant", t.ID, "err", err)
		return
	}
	baseURL := ""
	var baseErr error
	if t.IngressMode == IngressDirect {
		baseURL = "https://" + t.Domain
	} else {
		baseURL, baseErr = a.internalTenantBaseURL(app, t)
	}
	if baseErr != nil {
		_ = a.store.updateHealth(t.ID, false, "", []byte(fmt.Sprintf(`{"error":%q}`, baseErr.Error())))
		a.bumpFailures(app, t)
		a.maybeRespawnHosted(ctx, app, t)
		return
	}
	if t.IsHosted() && t.IngressMode != IngressDirect && a.takeHostedTunnelChanged(t.InstanceID, portFromTenant(t)) {
		if err := a.refreshTenantIngressTargets(app, t, baseURL); err != nil {
			a.markHostedTunnelDirty(t.InstanceID, portFromTenant(t))
			app.Logger().Warn("fleet: refresh hosted ingress target", "tenant", t.ID, "err", err)
		}
	}
	ok, version, body, err := probeHealth(ctx, baseURL, string(key))
	if err != nil {
		// Record the error in last_health so operators can see why.
		_ = a.store.updateHealth(t.ID, false, "", []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		if !a.maybeRespawnHosted(ctx, app, t) {
			a.bumpFailures(app, t)
		}
		return
	}
	if !ok {
		_ = a.store.updateHealth(t.ID, false, "", body)
		a.bumpFailures(app, t)
		return
	}
	_ = a.store.updateHealth(t.ID, true, version, body)
	// Healthy probe → reset the auto-respawn counter so the next
	// blip starts fresh from 0.
	_ = a.store.resetRespawn(t.ID)
	if t.Status == StatusDisconnected {
		_ = a.store.setStatus(t.ID, StatusActive, "worker:health_poller")
	}
}

func portFromTenant(t *Tenant) int {
	if t == nil {
		return 0
	}
	port, _ := portFromBaseURL(t.BaseURL)
	return port
}

func (a *App) maybeRespawnHosted(ctx context.Context, app *sdk.AppCtx, t *Tenant) bool {
	if t == nil || !t.IsHosted() {
		return false
	}
	port, _ := portFromBaseURL(t.BaseURL)
	alive, err := hostedPortListening(app, t.InstanceID, port)
	if err != nil || alive {
		return false
	}
	a.tryRespawnHostedLocked(ctx, app, t)
	return true
}

// Failure streaks are durable state, independent of event retention.
func (a *App) bumpFailures(app *sdk.AppCtx, t *Tenant) {
	var count int
	err := a.store.db.QueryRow(`UPDATE fleet_tenant_state SET health_failures=health_failures+1 WHERE tenant_id=? RETURNING health_failures`, t.ID).Scan(&count)
	if err != nil {
		return
	}
	_ = a.store.recordEvent(t.ID, "health_failed", "worker:health_poller", map[string]any{"consecutive_failures": count})
	if count >= failuresToDisconnect && t.Status == StatusActive {
		_ = a.store.setStatus(t.ID, StatusDisconnected, "worker:health_poller")
	}
}

func healthVersion(body []byte) string {
	var parsed struct {
		Version string `json:"version"`
		Apteva  string `json:"apteva"`
	}
	_ = json.Unmarshal(body, &parsed)
	if parsed.Apteva != "" {
		return strings.TrimPrefix(strings.TrimSpace(parsed.Apteva), "v")
	}
	return strings.TrimPrefix(strings.TrimSpace(parsed.Version), "v")
}

func requireRuntimeHealthVersion(ctx context.Context, baseURL, requested string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/api/health", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("runtime health returned HTTP %d", resp.StatusCode)
	}
	actual := healthVersion(body)
	requested = strings.TrimPrefix(strings.TrimSpace(requested), "v")
	if actual != requested {
		if actual == "" {
			actual = "unknown"
		}
		return body, fmt.Errorf("requested Apteva %s, but launched runtime reports %s", requested, actual)
	}
	return body, nil
}

// probeHealth GETs <base>/api/health with Bearer auth. Returns
// (ok, version, raw body, err). The version field of the response is
// not strictly required to exist — empty string is fine.
func probeHealth(ctx context.Context, baseURL, apiKey string) (bool, string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/health", nil)
	if err != nil {
		return false, "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, "", nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return false, "", body, nil
	}
	// apteva's /api/health returns {apteva, build, cli, core, dashboard,
	// integrations, ok}. We prefer `apteva` (the canonical name since
	// 0.10ish); fall back to `version` for any older / alternate shape.
	// Either field absent → empty string → store keeps the prior value
	// (COALESCE NULLIF in store.updateHealth), so we never overwrite
	// good data with "".
	var health struct {
		OK *bool `json:"ok"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		return false, "", body, err
	}
	if health.OK == nil || !*health.OK {
		return false, "", body, nil
	}
	return true, healthVersion(body), body, nil
}
