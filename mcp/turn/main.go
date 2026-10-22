package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/pion/turn/v5"
	"golang.org/x/time/rate"
)

//go:embed apteva.yaml
var manifestYAML []byte

var errNotAdmitting = errors.New("TURN execution is not admitting new sessions")
var errRateLimited = errors.New("credential request rate exceeded")

type App struct {
	cfg         config
	project     string
	mu          sync.Mutex
	state       string
	managed     bool
	deployment  string
	generation  int64
	armed       bool
	leaseExpiry time.Time
	server      *turn.Server
	udp         net.PacketConn
	tcp         *clientListener
	tls         *clientListener
	relays      map[*relayConn]struct{}
	pool        []*relayPort
	poolReaders sync.WaitGroup
	users       map[string]int
	issuance    *rate.Limiter
	stopOnce    sync.Once
	cancel      context.CancelFunc
	done        chan struct{}
	http        *http.Client
	stats       trafficStats
	peerFilter  func(net.IP) bool
}

type trafficStats struct {
	sent, received, dropped atomic.Uint64
	issued                  atomic.Uint64
}

func main() { sdk.Run(&App{}) }
func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if os.Getenv("APTEVA_APP_TOKEN") == "" {
		return errors.New("TURN requires APTEVA_APP_TOKEN for authenticated control access")
	}
	cfg, err := parseConfig(ctx.Config())
	if err != nil {
		return err
	}
	if err = a.initialize(cfg, ctx.CurrentProject()); err != nil {
		return err
	}
	if err = a.prepareRuntime(); err != nil {
		return err
	}
	if err = a.startNetwork(); err != nil {
		a.stopNetwork()
		return err
	}
	if a.managed {
		a.startOwnershipWatcher()
	}
	return nil
}

func (a *App) initialize(cfg config, project string) error {
	a.cfg = cfg
	a.project = project
	a.state = "active"
	a.relays = make(map[*relayConn]struct{})
	a.users = make(map[string]int)
	a.issuance = rate.NewLimiter(10, 20)
	a.http = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.cancel != nil {
		a.cancel()
		<-a.done
	}
	a.mu.Lock()
	a.state = "stopped"
	a.mu.Unlock()
	a.stopNetwork()
	return nil
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/ready", Handler: a.handleReady, NoAuth: true},
		{Method: "GET", Pattern: "/status", Handler: a.protected(a.handleStatus)},
		{Method: "POST", Pattern: "/credentials", Handler: a.protected(a.handleCredentials)},
		{Method: "POST", Pattern: "/settings/validate", Handler: a.protected(a.handleValidateSettings)},
		{Pattern: "/_apteva/runtime/app/", Handler: a.protected(a.handleLifecycle)},
	}
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	ready := a.readyLocked(time.Now())
	a.mu.Unlock()
	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]bool{"ready": ready})
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "turn_status", Description: "Read TURN relay status and aggregate counters.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.status(), nil }},
		{Name: "turn_credentials", Description: "Issue temporary credentials for an authorized project/session.", Exposure: sdk.ToolExposureAppOnly,
			InputSchema: map[string]any{"type": "object", "required": []string{"session_id"}, "additionalProperties": false, "properties": map[string]any{"session_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "ttl_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 86400}}},
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				session, _ := args["session_id"].(string)
				raw, err := json.Marshal(args)
				if err != nil {
					return nil, errors.New("invalid credential arguments")
				}
				var req credentialRequest
				if err = json.Unmarshal(raw, &req); err != nil {
					return nil, errors.New("invalid credential arguments")
				}
				req.ProjectID = ctx.CurrentProject()
				req.SessionID = session
				return a.issueCredentials(req, time.Now())
			}},
	}
}

func (a *App) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token := os.Getenv("APTEVA_APP_TOKEN")
		if token == "" || !secureEqual(r.Header.Get("Authorization"), "Bearer "+token) {
			http.Error(w, "app authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.status())
}
func (a *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	var req credentialRequest
	if err := decodeBody(w, r, &req); err != nil {
		http.Error(w, "invalid credential request", 400)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = a.project
	}
	out, err := a.issueCredentials(req, time.Now())
	if err != nil {
		code := 400
		if errors.Is(err, errNotAdmitting) {
			code = 503
		}
		if errors.Is(err, errRateLimited) {
			code = 429
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, 200, out)
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("exactly one JSON object required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *App) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	return map[string]any{
		"state":              a.state,
		"admitting":          a.admittingLocked(now),
		"ready":              a.readyLocked(now),
		"active_allocations": len(a.relays),
		"max_allocations":    a.cfg.maxAllocations,
		"public_host":        a.cfg.publicHost,
		"public_ip":          a.cfg.publicIP.String(),
		"port":               turnPort,
		"transports":         a.cfg.transports(now),
		"relay_port_min":     relayPortMin,
		"relay_port_max":     relayPortMax,
		"bytes_sent":         a.stats.sent.Load(),
		"bytes_received":     a.stats.received.Load(),
		"bytes_dropped":      a.stats.dropped.Load(),
		"credentials_issued": a.stats.issued.Load(),
		"tls":                a.cfg.tlsValid(now),
		"tls_port":           tlsPort,
		"tls_certificate":    a.cfg.certificateStatus(now),
		"configuration":      a.cfg.publicSettings(),
	}
}

func (a *App) readyLocked(now time.Time) bool {
	return a.server != nil && a.state != "stopped" && a.state != "fenced" &&
		(!a.managed || a.leaseExpiry.IsZero() || now.Before(a.leaseExpiry)) &&
		(a.cfg.certificate == nil || a.cfg.tlsValid(now))
}
