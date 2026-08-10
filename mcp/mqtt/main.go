// MQTT — embedded MQTT broker as an Apteva sidecar. See apteva.yaml
// for the public surface. This file wires mochi-mqtt to the SDK +
// the platform event bus.
//
// Layout:
//   main.go        lifecycle, MCP tools, HTTP routes, manifest constant
//   broker.go      mochi-mqtt setup, auth/ACL hooks, listener
//   bus.go         inline subscriber that bridges MQTT ↔ platform bus
//   discovery.go   HA-convention device parser (homeassistant/+/+/config)
//   users.go       DB layer for mqtt_users, ACL evaluation, bcrypt
//   subscriptions.go  DB layer for persisted bus-bridge subs

package main

import (
	"context"
	_ "embed"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

type App struct {
	ctx                 *sdk.AppCtx
	broker              *Broker
	messageLogCh        chan messageRecord
	aclLogCh            chan aclRecord
	droppedLogs         atomic.Uint64
	droppedEvents       atomic.Uint64
	rateLimitedMessages atomic.Uint64
	authRejected        atomic.Uint64
	aclRejected         atomic.Uint64
	eventsEmitted       atomic.Uint64
	startedAt           time.Time
	eventLimiter        *tokenBucket
	clientConnectedAt   sync.Map
	clientIdentities    sync.Map
	usersMu             sync.RWMutex
	users               map[string]*MQTTUser
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("mqtt: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("mqtt: requires a db block")
	}
	a.ctx = ctx
	a.startedAt = time.Now().UTC()
	a.messageLogCh = make(chan messageRecord, 2048)
	a.aclLogCh = make(chan aclRecord, 2048)
	a.eventLimiter = newTokenBucket(
		clampConfigInt(ctx, "max_event_per_second", 1000, 0, 1000000),
		clampConfigInt(ctx, "max_event_burst", 2000, 1, 10000000),
	)

	if err := a.seedDefaultUserIfNeeded(); err != nil {
		ctx.Logger().Warn("seed default user", "err", err.Error())
	}
	if err := a.reloadUserCache(); err != nil {
		return err
	}

	br, err := NewBroker(a)
	if err != nil {
		return err
	}
	a.broker = br
	ctx.Logger().Info("mqtt mounted", "port", br.Port())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.broker != nil {
		return a.broker.Close()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{
			Event: "mqtt.publish_request",
			Handler: func(ctx *sdk.AppCtx, event sdk.Event) error {
				return a.handleOutboundPublishRequest(ctx, event.Data)
			},
		},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "broker",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.broker.Serve(ctx)
			},
		},
		{
			Name: "persistence",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runPersistence(ctx)
			},
		},
		{
			Name: "retention-sweep",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runRetentionSweep(ctx)
			},
		},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/clients", Handler: a.handleClients},
		{Pattern: "/messages", Handler: a.handleMessages},
		{Pattern: "/users", Handler: a.handleUsers},
		{Pattern: "/users/", Handler: a.handleUserItem},
		{Pattern: "/subscriptions", Handler: a.handleSubscriptions},
		{Pattern: "/subscriptions/", Handler: a.handleSubscriptionItem},
		{Pattern: "/retained", Handler: a.handleRetained},
		{Pattern: "/retained/", Handler: a.handleRetainedItem},
		{Pattern: "/devices", Handler: a.handleDevices},
		{Pattern: "/test_publish", Handler: a.handleTestPublish},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return a.mcpTools()
}

func main() {
	sdk.Run(&App{})
}
