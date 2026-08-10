package main

import (
	_ "embed"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx            *sdk.AppCtx
	store          *store
	connectors     *connectorManager
	usage          *usageBuffer
	maxTunnels     int
	maxRequestBody int64
	requestTimeout time.Duration
	maxConcurrent  int
	mutationMu     sync.Mutex
}

func (a *App) Manifest() sdk.Manifest {
	manifest, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *manifest
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("Tunnel requires its app database")
	}
	a.ctx = ctx
	a.store = &store{db: ctx.AppDB()}
	a.connectors = newConnectorManager()
	a.usage = newUsageBuffer(a.store)
	go a.usage.run()
	a.maxTunnels = configInt(ctx, "max_tunnels_per_project", 5, 1, 1000)
	a.maxRequestBody = int64(configInt(ctx, "max_request_bytes", 10<<20, 64<<10, 100<<20))
	a.requestTimeout = time.Duration(configInt(ctx, "request_timeout_seconds", 60, 5, 600)) * time.Second
	a.maxConcurrent = configInt(ctx, "max_concurrent_requests_per_tunnel", 32, 1, 1000)
	return nil
}

func configInt(ctx *sdk.AppCtx, name string, fallback, min, max int) int {
	value := strings.TrimSpace(ctx.Config().Get(name))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return fallback
	}
	return parsed
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.connectors != nil {
		a.connectors.closeAll()
	}
	if a.usage != nil {
		a.usage.stop()
	}
	return nil
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/health", Handler: a.handleHealth},
		{Method: http.MethodGet, Pattern: "/v1/connect", Handler: a.handleConnect, NoAuth: true},
		{Method: http.MethodGet, Pattern: "/admin/config", Handler: a.handleGetConfig},
		{Method: http.MethodPost, Pattern: "/admin/config", Handler: a.handleConfigureDomain},
		{Method: http.MethodGet, Pattern: "/admin/tunnels", Handler: a.handleListTunnels},
		{Method: http.MethodPost, Pattern: "/admin/tunnels", Handler: a.handleCreateTunnel},
		{Method: http.MethodPost, Pattern: "/admin/tunnels/{id}/rotate", Handler: a.handleRotateTunnel},
		{Method: http.MethodDelete, Pattern: "/admin/tunnels/{id}", Handler: a.handleDeleteTunnel},
		{Method: http.MethodGet, Pattern: "/admin/stats", Handler: a.handleStats},
		{Pattern: "/", Handler: a.handlePublic},
	}
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() {
	sdk.Run(&App{})
}
