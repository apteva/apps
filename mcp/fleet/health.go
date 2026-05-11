package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
	for _, t := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Skip tenants that aren't expected to respond. Stopped /
		// suspended are intentional; failed had a real error already
		// surfaced — re-poll only after the operator starts it.
		switch t.Status {
		case StatusDeleted, StatusStopped, StatusSuspended, StatusFailed, StatusStarting:
			continue
		}
		a.probeOnce(ctx, app, t)
	}
	return nil
}

func (a *App) probeOnce(ctx context.Context, app *sdk.AppCtx, t *Tenant) {
	_, enc, err := a.store.get(t.ID)
	if err != nil {
		return
	}
	key, err := a.keys.open(enc)
	if err != nil {
		app.Logger().Error("fleet: decrypt key", "tenant", t.ID, "err", err)
		return
	}
	ok, version, body, err := probeHealth(ctx, t.BaseURL, string(key))
	if err != nil {
		// Record the error in last_health so operators can see why.
		_ = a.store.updateHealth(t.ID, false, "", []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		a.bumpFailures(app, t)
		return
	}
	if !ok {
		_ = a.store.updateHealth(t.ID, false, "", body)
		a.bumpFailures(app, t)
		return
	}
	_ = a.store.updateHealth(t.ID, true, version, body)
	if t.Status == StatusDisconnected {
		_ = a.store.setStatus(t.ID, StatusActive, "worker:health_poller")
	}
}

// bumpFailures counts consecutive failures by reading the recent
// events tail. Cheap relative to the 60s tick.
func (a *App) bumpFailures(app *sdk.AppCtx, t *Tenant) {
	_ = a.store.recordEvent(t.ID, "health_failed", "worker:health_poller", nil)
	if t.Status == StatusActive {
		evts, err := a.store.recentEvents(t.ID, failuresToDisconnect)
		if err != nil {
			return
		}
		fails := 0
		for _, e := range evts {
			if e.Kind == "health_failed" {
				fails++
			} else {
				break // any non-failure breaks the streak
			}
		}
		if fails >= failuresToDisconnect {
			_ = a.store.setStatus(t.ID, StatusDisconnected, "worker:health_poller")
			app.Logger().Warn("fleet: tenant disconnected", "tenant", t.ID, "consecutive_failures", fails)
		}
	}
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
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return false, "", body, nil
	}
	var parsed struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(body, &parsed)
	return true, parsed.Version, body, nil
}
