package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
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
	mutationMu sync.Mutex
	outboxMu   sync.Mutex
	secretKey  []byte
	outboxWake chan struct{}
	ctx        *sdk.AppCtx
	projectID  string

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

func (a *App) PreserveJSONNumbers() bool { return true }

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
	a.ctx = ctx.WithProject(projectID)
	a.projectID = projectID
	a.waiters = make(map[string][]chan struct{})
	a.outboxWake = make(chan struct{}, 1)
	if err := a.validateLimits(); err != nil {
		return err
	}
	if os.Getenv("APTEVA_APP_TOKEN") == "" {
		return errors.New("authenticated management requires an install token")
	}
	if err := a.loadRecoveryKey(); err != nil {
		return err
	}
	if err := a.migrateTransport(); err != nil {
		return err
	}

	if err := a.refreshBrokerStatus(); err != nil {
		return fmt.Errorf("devices: MQTT dependency unavailable: %w", err)
	}
	var subscription map[string]any
	if err := a.ctx.PlatformAPI().CallAppResult("mqtt", "mqtt_subscribe_ensure", map[string]any{
		"topic_pattern": a.topicRoot() + "devices/+/+",
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
		{Name: "reconciliation", Run: a.runReconciliation},
		{Name: "connection-reconciliation", Schedule: "@every 30s", Run: func(_ context.Context, _ *sdk.AppCtx) error { return a.reconcileConnectedClients() }},
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
			ids, err := a.expireCommands(time.Now().UTC())
			if err != nil {
				a.ctx.Logger().Warn("devices mark timeouts", "err", err.Error())
				continue
			}
			_ = ids
		}
	}
}

func (a *App) runTelemetryPrune(_ context.Context, _ *sdk.AppCtx) error {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	for _, spec := range []struct {
		table, column, config string
		days                  int
	}{{"device_outbox", "created_at", "event_retention_days", 90}, {"device_usage", "created_at", "usage_retention_days", 365}} {
		extra := ""
		if spec.table == "device_outbox" {
			extra = " AND delivered_at IS NOT NULL"
		}
		cutoff := formatTime(now.Add(-time.Duration(a.limit(spec.config, spec.days)) * 24 * time.Hour))
		kind := "events"
		if spec.table == "device_usage" {
			kind = "usage"
		}
		if _, err = tx.Exec(`INSERT INTO device_retention(project_id,kind,through_seq) SELECT ?,?,COALESCE(MAX(seq),0) FROM `+spec.table+` WHERE project_id=? AND `+spec.column+`<?`+extra+` ON CONFLICT(project_id,kind)DO UPDATE SET through_seq=MAX(device_retention.through_seq,excluded.through_seq)`, a.projectID, kind, a.projectID, cutoff); err != nil {
			return err
		}

		if _, err = tx.Exec(`DELETE FROM `+spec.table+` WHERE project_id=? AND `+spec.column+`<?`+extra, a.projectID, cutoff); err != nil {
			return err
		}
	}
	cutoff := formatTime(now.Add(-time.Duration(a.limit("telemetry_retention_hours", 168)) * time.Hour))
	if _, err = tx.Exec(`INSERT INTO device_retention(project_id,kind,through_seq)SELECT ?,'data',COALESCE(MAX(id),0) FROM device_messages WHERE project_id=? AND received_at<? ON CONFLICT(project_id,kind)DO UPDATE SET through_seq=MAX(device_retention.through_seq,excluded.through_seq)`, a.projectID, a.projectID, cutoff); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM device_messages WHERE project_id=? AND received_at<?`, a.projectID, cutoff); err != nil {
		return err
	}
	// Completed command rows retain idempotency identities for the configured
	// window. Active dispatch and pending lifecycle intent are never pruned.
	commandCutoff := formatTime(now.Add(-time.Duration(a.limit("command_retention_days", 90)) * 24 * time.Hour))
	if _, err = tx.Exec(`DELETE FROM device_commands WHERE completed_at<? AND device_id IN(SELECT id FROM devices WHERE project_id=?)`, commandCutoff, a.projectID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM device_events WHERE created_at<? AND device_id IN(SELECT id FROM devices WHERE project_id=?)`, commandCutoff, a.projectID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM device_telemetry WHERE received_at<? AND device_id IN(SELECT id FROM devices WHERE project_id=?)`, cutoff, a.projectID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM device_responses WHERE project_id=? AND created_at<?`, a.projectID, commandCutoff); err != nil {
		return err
	}
	cutoffOperations := formatTime(now.Add(-time.Duration(a.limit("operation_retention_days", 90)) * 24 * time.Hour))
	if _, err = tx.Exec(`DELETE FROM device_operations WHERE project_id=? AND status='completed' AND completed_at<?`, a.projectID, cutoffOperations); err != nil {
		return err
	}
	return tx.Commit()
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
