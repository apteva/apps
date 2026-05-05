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
	"errors"
	"log"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: mqtt
display_name: MQTT Broker
version: 0.1.0
description: Embedded MQTT broker. LAN message bus for IoT devices; bridges to the platform event bus.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, net.egress]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: mqtt_publish,           description: "Publish one message." }
    - { name: mqtt_topics_recent,     description: "Recent topics seen." }
    - { name: mqtt_subscribe,         description: "Bridge a topic pattern to the platform event bus." }
    - { name: mqtt_subscribe_list,    description: "List bus subscriptions." }
    - { name: mqtt_subscribe_delete,  description: "Delete a bus subscription." }
    - { name: mqtt_users_add,         description: "Add a broker user." }
    - { name: mqtt_users_list,        description: "List broker users." }
    - { name: mqtt_users_delete,      description: "Delete a broker user." }
    - { name: mqtt_users_set_enabled, description: "Enable / disable a user." }
    - { name: mqtt_devices,           description: "List HA-discovered devices." }
    - { name: mqtt_status,            description: "Broker runtime status." }
  ui_panels:
    - slot: project.page
      label: MQTT
      icon: radio
      entry: /ui/MQTTPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/mqtt
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/mqtt.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	ctx    *sdk.AppCtx
	broker *Broker
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

	if err := a.seedDefaultUserIfNeeded(); err != nil {
		ctx.Logger().Warn("seed default user", "err", err.Error())
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

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "broker",
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.broker.Serve(ctx)
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

var _ = log.Println // reserve log for future use
