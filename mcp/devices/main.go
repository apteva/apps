package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
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
	ctx       *sdk.AppCtx
	projectID string

	brokerMu sync.RWMutex
	broker   brokerStatus

	waitMu  sync.Mutex
	waiters map[string][]chan struct{}
}

type brokerStatus struct {
	Endpoint       string `json:"endpoint"`
	AdvertisedHost string `json:"advertised_host"`
	AdvertisedPort int    `json:"advertised_port"`
	TLS            bool   `json:"tls"`
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("devices: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("devices: requires a db block")
	}
	projectID := strings.TrimSpace(ctx.CurrentProject())
	if projectID == "" {
		return errors.New("devices: project-scoped install required")
	}
	a.ctx = ctx
	a.projectID = projectID
	a.waiters = make(map[string][]chan struct{})

	if err := a.refreshBrokerStatus(); err != nil {
		return fmt.Errorf("devices: MQTT dependency unavailable: %w", err)
	}
	var subscription map[string]any
	if err := ctx.PlatformAPI().CallAppResult("mqtt", "mqtt_subscribe_ensure", map[string]any{
		"topic_pattern": "devices/+/+",
		"bus_topic":     "devices.message",
	}, &subscription); err != nil {
		return fmt.Errorf("devices: ensure MQTT device bridge: %w", err)
	}
	if err := a.reconcileRetained(); err != nil {
		ctx.Logger().Warn("devices reconcile retained state", "err", err.Error())
	}
	if err := a.reconcileConnectedClients(); err != nil {
		ctx.Logger().Warn("devices reconcile MQTT clients", "err", err.Error())
	}
	ctx.Logger().Info("devices mounted", "project_id", projectID, "mqtt_endpoint", a.currentBroker().Endpoint)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) MCPTools() []sdk.Tool           { return a.mcpTools() }
func (a *App) HTTPRoutes() []sdk.Route        { return a.httpRoutes() }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "mqtt.devices.message", Handler: a.handleMQTTMessage},
		{Event: "mqtt.client.connected", Handler: a.handleMQTTConnected},
		{Event: "mqtt.client.disconnected", Handler: a.handleMQTTDisconnected},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "command-timeouts", Run: a.runCommandTimeouts},
		{Name: "telemetry-prune", Schedule: "@every 1h", Run: a.runTelemetryPrune},
	}
}

func (a *App) runCommandTimeouts(ctx context.Context, _ *sdk.AppCtx) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			ids, err := markTimedOutCommands(a.ctx.AppDB(), time.Now().UTC())
			if err != nil {
				a.ctx.Logger().Warn("devices mark timeouts", "err", err.Error())
				continue
			}
			a.emitTimedOutCommands(ids)
		}
	}
}

func (a *App) runTelemetryPrune(_ context.Context, _ *sdk.AppCtx) error {
	hours := configInt(a.ctx, "telemetry_retention_hours", 168, 1, 8760)
	maxRows := configInt(a.ctx, "telemetry_max_rows_per_device", 10000, 100, 1000000)
	if err := pruneTelemetry(a.ctx.AppDB(), time.Now().UTC().Add(-time.Duration(hours)*time.Hour), maxRows); err != nil {
		return err
	}
	days := configInt(a.ctx, "command_retention_days", 90, 1, 3650)
	return pruneHistory(a.ctx.AppDB(), time.Now().UTC().Add(-time.Duration(days)*24*time.Hour))
}

func (a *App) emitTimedOutCommands(ids []string) {
	for _, id := range ids {
		payload := map[string]any{"command_id": id, "status": "timed_out"}
		if command, err := getCommand(a.ctx.AppDB(), id); err == nil {
			payload["device_id"] = command.DeviceID
			payload["operation"] = command.Operation
			insertEvent(a.ctx.AppDB(), command.DeviceID, "command.timed_out", payload)
		}
		a.ctx.Emit("devices.command.timed_out", payload)
		a.notifyWaiters(id)
	}
}

func (a *App) currentBroker() brokerStatus {
	a.brokerMu.RLock()
	defer a.brokerMu.RUnlock()
	return a.broker
}

func (a *App) setBroker(s brokerStatus) {
	a.brokerMu.Lock()
	a.broker = s
	a.brokerMu.Unlock()
}

func (a *App) registerWaiter(commandID string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	a.waitMu.Lock()
	a.waiters[commandID] = append(a.waiters[commandID], ch)
	a.waitMu.Unlock()
	return ch, func() {
		a.waitMu.Lock()
		list := a.waiters[commandID]
		for i, item := range list {
			if item == ch {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(a.waiters, commandID)
		} else {
			a.waiters[commandID] = list
		}
		a.waitMu.Unlock()
	}
}

func (a *App) notifyWaiters(commandID string) {
	a.waitMu.Lock()
	list := a.waiters[commandID]
	delete(a.waiters, commandID)
	for _, ch := range list {
		close(ch)
	}
	a.waitMu.Unlock()
}

func configInt(ctx *sdk.AppCtx, key string, def, min, max int) int {
	if ctx == nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get(key)))
	if err != nil || n < min || n > max {
		return def
	}
	return n
}

func main() { sdk.Run(&App{}) }
