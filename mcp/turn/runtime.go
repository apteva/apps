package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const runtimePrefix = "/_apteva/runtime/app/"

var runtimeCapabilities = map[string]any{"passive_start": true, "drain": true, "replica_safe": false, "worker_coordination": false, "routing": "stateless"}

type ownership struct {
	Active       bool   `json:"active"`
	DeploymentID string `json:"deployment_id"`
	Generation   int64  `json:"generation"`
	ExpiresAt    int64  `json:"expires_at"`
}

func secureEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

func (a *App) prepareRuntime() error {
	mode, id, gen := os.Getenv("APTEVA_RUNTIME_MODE"), os.Getenv("APTEVA_DEPLOYMENT_ID"), os.Getenv("APTEVA_DEPLOYMENT_GENERATION")
	if mode == "" && id == "" && gen == "" {
		return nil
	}
	n, err := strconv.ParseInt(gen, 10, 64)
	if mode != "standby" || id == "" || len(id) > 128 || err != nil || n < 1 {
		return errors.New("invalid managed TURN deployment identity or startup mode")
	}
	if !secureGateway(os.Getenv("APTEVA_GATEWAY_URL")) {
		return errors.New("managed TURN requires an HTTPS gateway or loopback tunnel")
	}
	if os.Getenv("APTEVA_APP_TOKEN") == "" {
		return errors.New("managed TURN requires an app credential")
	}
	a.managed = true
	a.deployment = id
	a.generation = n
	a.state = "standby"
	return nil
}

func secureGateway(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
}

func (a *App) admittingLocked(now time.Time) bool {
	return a.state == "active" && (!a.managed || now.Before(a.leaseExpiry))
}
func (a *App) trafficAllowedLocked(now time.Time) bool {
	return (a.state == "active" || a.state == "draining") && (!a.managed || now.Before(a.leaseExpiry))
}

func (a *App) fetchOwnership(ctx context.Context) (ownership, int, error) {
	var own ownership
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if !secureGateway(base) {
		return own, 0, errors.New("secure runtime gateway required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/apps/callback/runtime/ownership", nil)
	if err != nil {
		return own, 0, errors.New("runtime ownership request failed")
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if token == "" {
		return own, 0, errors.New("runtime ownership credential missing")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.http.Do(req)
	if err != nil {
		return own, 0, errors.New("runtime ownership unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return own, resp.StatusCode, errors.New("runtime ownership rejected")
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&own)
	return own, resp.StatusCode, err
}

func (a *App) startOwnershipWatcher() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			own, status, err := a.fetchOwnership(ctx)
			if a.applyOwnership(own, status, err, time.Now()) {
				if a.tlsManager != nil && a.tlsManager.cancel != nil {
					a.tlsManager.cancel()
				}
				a.stopNetwork()
				return
			}
		}
	}()
}

// Returns true when this execution must permanently release every socket.
// Unknown ownership on a first passive start is expected; an old owner losing
// its lease must never continue forwarding media or accept a later reactivation.
func (a *App) applyOwnership(own ownership, status int, err error, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == "stopped" || a.state == "fenced" {
		return a.state == "fenced"
	}
	previouslyOwned := !a.leaseExpiry.IsZero()
	if err != nil {
		if previouslyOwned && (!now.Before(a.leaseExpiry) || status == 401 || status == 403 || status == 409) {
			a.state = "fenced"
			return true
		}
		return false
	}
	valid := own.Active && own.DeploymentID == a.deployment && own.Generation == a.generation && own.ExpiresAt > now.Unix() && own.ExpiresAt <= now.Add(time.Minute).Unix()
	if !valid {
		if previouslyOwned {
			a.state = "fenced"
			return true
		}
		return false
	}
	a.leaseExpiry = time.Unix(own.ExpiresAt, 0)
	if a.armed && a.state == "standby" {
		a.state = "active"
	}
	return false
}

func (a *App) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, runtimePrefix)
	if r.Method == http.MethodGet {
		switch action {
		case "describe":
			writeJSON(w, 200, runtimeCapabilities)
		case "status":
			writeJSON(w, 200, a.status())
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	if action != "activate" && action != "drain" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		DeploymentID string `json:"deployment_id"`
		Generation   int64  `json:"generation"`
	}
	if decodeBody(w, r, &req) != nil {
		http.Error(w, "invalid runtime request", 400)
		return
	}
	if a.managed {
		if req.DeploymentID != a.deployment || req.Generation != a.generation {
			http.Error(w, "deployment identity mismatch", 409)
			return
		}
	} else {
		// The controller can adopt an existing legacy installation before moving
		// it. Authenticate that identity with the gateway before changing state.
		own, _, err := a.fetchOwnership(r.Context())
		if err != nil || req.DeploymentID == "" || own.DeploymentID != req.DeploymentID || own.Generation != req.Generation {
			http.Error(w, "legacy deployment identity unavailable or mismatched", 409)
			return
		}
	}
	a.mu.Lock()
	if a.state == "fenced" || a.state == "stopped" {
		a.mu.Unlock()
		http.Error(w, "execution requires a restart", 409)
		return
	}
	if action == "activate" {
		a.armed = true
		if !a.managed || time.Now().Before(a.leaseExpiry) {
			a.state = "active"
		} else {
			a.state = "standby"
		}
		a.mu.Unlock()
		writeJSON(w, 200, map[string]any{"activation_armed": true})
		return
	}
	a.armed = false
	a.state = "draining"
	count := len(a.relays)
	drained := count == 0
	if drained {
		a.state = "drained"
	}
	a.mu.Unlock()
	writeJSON(w, 200, map[string]any{"draining": true, "drained": drained, "active_work": count})
}
