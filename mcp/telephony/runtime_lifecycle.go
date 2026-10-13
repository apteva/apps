package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const runtimeAppPrefix = "/_apteva/runtime/app/"

var errRuntimeAdmission = errors.New("Telephony execution is not admitting new work")

type telephonyRuntime struct {
	mu           sync.Mutex
	deployment   string
	generation   int64
	state        string
	fenced       bool
	armed        bool
	initialized  bool
	initializing bool
	initWG       sync.WaitGroup
	expires      time.Time
	work         int
	endpoints    map[string]string
	cancel       context.CancelFunc
	done         chan struct{}
	mediaStopped bool
	http         *http.Client
	initialize   func() error
	lastError    string
}

type runtimeOwnership struct {
	Active     bool   `json:"active"`
	Deployment string `json:"deployment_id"`
	Generation int64  `json:"generation"`
	Expires    int64  `json:"expires_at"`
}

func (a *App) prepareRuntime(ctx *sdk.AppCtx) error {
	mode, id, gen := os.Getenv("APTEVA_RUNTIME_MODE"), os.Getenv("APTEVA_DEPLOYMENT_ID"), os.Getenv("APTEVA_DEPLOYMENT_GENERATION")
	if mode == "" && id == "" && gen == "" {
		return nil
	}
	n, err := strconv.ParseInt(gen, 10, 64)
	if mode != "standby" || id == "" || len(id) > 128 || err != nil || n <= 0 {
		return errors.New("invalid managed Telephony deployment identity/mode")
	}
	endpoints, err := parseRuntimeEndpoints(os.Getenv("APTEVA_RUNTIME_ENDPOINTS"))
	if err != nil {
		return err
	}
	a.runtime = &telephonyRuntime{deployment: id, generation: n, state: "standby", endpoints: endpoints, http: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return nil
}

// HTTP calls are bounded and never run on the audio frame path.
func (rt *telephonyRuntime) callback(ctx context.Context, method, path string, body any, out any) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if !validRuntimeURL(base, false) {
		return errors.New("managed runtime requires a secure reachable gateway")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api/apps/callback/runtime/"+path, reader)
	if err != nil {
		return err
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if token == "" {
		return errors.New("managed runtime credential unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rt.http.Do(req)
	if err != nil {
		return errors.New("runtime callback temporarily unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runtimeCallbackError{Status: resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
}
func (a *App) startRuntimeOwnership(ctx *sdk.AppCtx) {
	rt := a.runtime
	if rt == nil {
		return
	}
	lifetime, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	rt.done = make(chan struct{})
	go func() {
		defer close(rt.done)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				a.refreshRuntimeOwnership(lifetime)
				if !a.runtimeOwned() {
					rt.mu.Lock()
					fenced := rt.fenced && !rt.mediaStopped
					if fenced {
						rt.mediaStopped = true
					}
					rt.mu.Unlock()
					if fenced {
						a.stopCarrierBridges()
						a.stopSIPGateway()
					}
				}
			}
		}
	}()
}
func (a *App) refreshRuntimeOwnership(ctx context.Context) {
	rt := a.runtime
	if rt == nil {
		return
	}
	var own runtimeOwnership
	err := rt.callback(ctx, "GET", "ownership", nil, &own)
	now := time.Now()
	rt.mu.Lock()
	if err != nil {
		rt.lastError = err.Error()
		var callbackErr runtimeCallbackError
		// A first managed installation has no active pointer until Server commits
		// it. Its ownership callback legitimately returns 409 in passive startup.
		waitingForFirstOwner := errors.As(err, &callbackErr) && callbackErr.Status == 409 && !rt.initialized && !rt.initializing && rt.expires.IsZero()
		if permanentRuntimeFailure(err) && !waitingForFirstOwner || !rt.expires.IsZero() && !now.Before(rt.expires) {
			rt.state = "fenced"
			rt.fenced = true
		}
		rt.mu.Unlock()
		return
	}
	matching := own.Active && own.Deployment == rt.deployment && own.Generation == rt.generation && own.Expires > now.Unix() && own.Expires <= now.Add(time.Minute).Unix()
	if !matching {
		if rt.initialized || rt.initializing {
			rt.state = "fenced"
			rt.fenced = true
		}
		rt.expires = time.Time{}
		rt.mu.Unlock()
		return
	}
	rt.expires = time.Unix(own.Expires, 0)
	rt.lastError = ""
	if rt.fenced {
		rt.mu.Unlock()
		return
	}
	if !rt.armed {
		rt.mu.Unlock()
		return
	}
	if rt.initialized {
		if rt.state == "standby" {
			rt.state = "active"
		}
		rt.mu.Unlock()
		return
	}
	if rt.initializing {
		rt.mu.Unlock()
		return
	}
	rt.state = "activating"
	rt.initializing = true
	rt.work++
	rt.initWG.Add(1)
	rt.mu.Unlock()
	// Keep renewing/fencing ownership while local state validation runs. A slow
	// initialization must not stall the ownership watcher past its lease horizon.
	go rt.initializeOwned()
}

func (rt *telephonyRuntime) initializeOwned() {
	defer rt.initWG.Done()
	err := rt.initialize()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.initializing = false
	rt.work--
	if rt.fenced {
		return
	}
	if err != nil {
		rt.lastError = err.Error()
		rt.state = "error"
		rt.armed = false
		return
	}
	rt.initialized = true
	if time.Now().Before(rt.expires) {
		if rt.armed {
			rt.state = "active"
		} else {
			rt.state = "draining"
		}
	} else {
		rt.state = "fenced"
		rt.fenced = true
	}
}
func (a *App) runtimeBegin(newAdmission bool) (func(), error) {
	rt := a.runtime
	if rt == nil {
		state := a.legacyAdmission.Load()
		if state == 2 || state == 1 && newAdmission {
			return nil, errRuntimeAdmission
		}
		a.legacyWork.Add(1)
		state = a.legacyAdmission.Load()
		if state == 2 || state == 1 && newAdmission {
			a.legacyWork.Add(-1)
			return nil, errRuntimeAdmission
		}
		return a.runtimeEnd, nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !time.Now().Before(rt.expires) || !rt.initialized || rt.state != "active" && !(rt.state == "draining" && !newAdmission) {
		return nil, errRuntimeAdmission
	}
	rt.work++
	return a.runtimeEnd, nil
}
func (a *App) runtimeEnd() {
	if a.runtime == nil {
		a.legacyWork.Add(-1)
		return
	}
	a.runtime.mu.Lock()
	a.runtime.work--
	a.runtime.mu.Unlock()
}

func (a *App) runtimeInitializationAllowed() bool {
	if a.runtime == nil {
		return true
	}
	rt := a.runtime
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return !rt.fenced && time.Now().Before(rt.expires)
}
func (a *App) runtimeOwned() bool {
	rt := a.runtime
	if rt == nil {
		return true
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.initialized && time.Now().Before(rt.expires) && (rt.state == "active" || rt.state == "draining")
}
func (a *App) runtimeHTTP(next http.HandlerFunc, newAdmission bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		done, err := a.runtimeBegin(r.Method != "GET" && r.Method != "HEAD" && (newAdmission || runtimeNewHTTP(r.URL.Path)))
		if err != nil {
			w.Header().Set("Retry-After", "1")
			writeJSONStatus(w, 503, map[string]any{"code": "runtime_not_admitting"})
			return
		}
		defer done()
		// A managed socket must cease forwarding when its verified ownership ends.
		// Cancellation/forced-close is monitored separately from audio processing.
		if a.runtime != nil && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			guarded, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(guarded)
			w = runtimeSocketResponse{ResponseWriter: w, ctx: guarded}
			go func() {
				t := time.NewTicker(time.Second)
				defer t.Stop()
				for {
					select {
					case <-guarded.Done():
						return
					case <-t.C:
						if !a.runtimeOwned() {
							cancel()
							return
						}
					}
				}
			}()
		}
		next(w, r)
	}
}
func runtimeNewHTTP(path string) bool {
	// Provider inbound routes also receive events for existing calls. Admission
	// for those routes is checked only when recordInboundCall creates a new call.
	return strings.Contains(path, "/softphone/place") || strings.HasPrefix(path, "/numbers/") || strings.HasPrefix(path, "/routing/") || strings.HasPrefix(path, "/access/") || path == runtimeBindingsPath || path == "/recording-settings" || path == "/call-control-settings"
}
func (a *App) handleRuntimeLifecycle(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" || !secureEqual(r.Header.Get("Authorization"), "Bearer "+token) {
		http.Error(w, "runtime credential required", 403)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, runtimeAppPrefix)
	if action == "describe" && r.Method == "GET" {
		writeJSON(w, map[string]any{"passive_start": true, "drain": true, "replica_safe": false, "worker_coordination": false, "routing": "key"})
		return
	}
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var request struct {
		Deployment string `json:"deployment_id"`
		Generation int64  `json:"generation"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request) != nil {
		http.Error(w, "invalid runtime request", 400)
		return
	}
	rt := a.runtime
	if rt == nil {
		a.handleLegacyRuntimeLifecycle(w, r, action, request.Deployment, request.Generation)
		return
	}
	if request.Deployment != rt.deployment || request.Generation != rt.generation {
		http.Error(w, "deployment identity mismatch", 409)
		return
	}
	switch action {
	case "activate":
		rt.mu.Lock()
		if rt.fenced {
			rt.mu.Unlock()
			http.Error(w, "fenced execution requires a restart", 409)
			return
		}
		rt.armed = true
		if rt.state == "draining" || rt.state == "drained" {
			if rt.initializing {
				rt.state = "activating"
			} else {
				rt.state = "standby"
			}
		}
		rt.mu.Unlock()
		writeJSON(w, map[string]any{"activation_armed": true, "deployment_id": rt.deployment, "generation": rt.generation})
	case "drain":
		rt.mu.Lock()
		rt.armed = false
		if rt.state == "active" || rt.state == "activating" {
			rt.state = "draining"
		}
		rt.mu.Unlock()
		counts, err := a.runtimeDrainStatus()
		if err != nil {
			http.Error(w, "drain state unavailable", 503)
			return
		}
		writeJSON(w, counts)
	default:
		http.NotFound(w, r)
	}
}
func (a *App) runtimeDrainStatus() (map[string]any, error) {
	rt := a.runtime
	var calls, imports int
	err := a.db().db.QueryRow(`SELECT COUNT(*) FROM calls WHERE ended_at='' AND status NOT IN ('completed','failed','no-answer','no_answer','busy','canceled','cancelled')`).Scan(&calls)
	if err != nil {
		return nil, err
	}
	err = a.db().db.QueryRow(`SELECT COUNT(*) FROM recordings WHERE storage_status='importing' OR provider_status='in-progress'`).Scan(&imports)
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	writers := a.recordingWriters.Load()
	drained := calls == 0 && imports == 0 && writers == 0 && rt.work == 0 && (rt.state == "draining" || rt.state == "drained" || rt.state == "standby" || rt.state == "fenced" || rt.state == "error")
	if drained {
		rt.state = "drained"
	}
	return map[string]any{"draining": true, "drained": drained, "active_work": calls, "pending_writes": rt.work + int(writers), "details": map[string]any{"active_calls": calls, "recording_imports": imports, "recording_writers": writers}}, nil
}

func (a *App) runtimeTool(name string, next func(context.Context, *sdk.AppCtx, map[string]any) (any, error)) func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return func(c context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
		newWork := true
		if name == "telephony_hangup" || name == "telephony_reject_call" || name == "telephony_answer_call" || strings.HasSuffix(name, "_get") || strings.HasSuffix(name, "_list") || name == "telephony_active_calls" || name == "telephony_pending_calls" {
			newWork = false
		}
		done, err := a.runtimeBegin(newWork)
		if err != nil {
			return nil, err
		}
		defer done()
		return next(c, ctx, args)
	}
}

// A legacy local process has no deployment environment yet. Server adopts it
// before moving it. Validate that adoption against its authenticated callback,
// then drain the same tracked work without converting it into a second owner.
func (a *App) handleLegacyRuntimeLifecycle(w http.ResponseWriter, r *http.Request, action, id string, generation int64) {
	if id == "" || generation < 0 {
		http.Error(w, "invalid legacy deployment", 400)
		return
	}
	client := &telephonyRuntime{http: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var own runtimeOwnership
	if err := client.callback(r.Context(), "GET", "ownership", nil, &own); err != nil || own.Deployment != id || own.Generation != generation {
		http.Error(w, "legacy deployment is not current", 409)
		return
	}
	a.legacyLifecycleMu.Lock()
	defer a.legacyLifecycleMu.Unlock()
	if a.legacyDeployment != "" && (a.legacyDeployment != id || a.legacyGeneration != generation) {
		http.Error(w, "legacy identity mismatch", 409)
		return
	}
	a.legacyDeployment = id
	a.legacyGeneration = generation
	switch action {
	case "activate":
		a.legacyAdmission.Store(0)
		writeJSON(w, map[string]any{"activation_armed": true, "admission_active": true})
	case "drain":
		a.legacyAdmission.CompareAndSwap(0, 1)
		var calls, imports int
		if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM calls WHERE ended_at='' AND status NOT IN ('completed','failed','no-answer','no_answer','busy','canceled','cancelled')`).Scan(&calls); err != nil {
			http.Error(w, "drain state unavailable", 503)
			return
		}
		if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM recordings WHERE storage_status='importing' OR provider_status='in-progress'`).Scan(&imports); err != nil {
			http.Error(w, "drain state unavailable", 503)
			return
		}
		drained := calls == 0 && imports == 0 && a.recordingWriters.Load() == 0 && a.legacyWork.Load() == 0
		if drained {
			a.legacyAdmission.Store(2)
			if a.legacyWork.Load() != 0 {
				a.legacyAdmission.Store(1)
				drained = false
			}
		}
		writeJSON(w, map[string]any{"draining": true, "drained": drained, "active_work": calls, "pending_writes": a.legacyWork.Load() + a.recordingWriters.Load()})
	default:
		http.NotFound(w, r)
	}
}

type runtimeCallbackError struct{ Status int }

func (e runtimeCallbackError) Error() string {
	return fmt.Sprintf("runtime callback returned HTTP %d", e.Status)
}
func permanentRuntimeFailure(err error) bool {
	var e runtimeCallbackError
	return errors.As(err, &e) && (e.Status == 401 || e.Status == 403 || e.Status == 409)
}
