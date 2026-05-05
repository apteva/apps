// Computer v0.1 — UI-only sidecar app.
//
// Today this app contributes one operator panel + four chat-attachable
// UI components. There are no MCP tools and no DB; the agent's
// browser_session / computer_use calls remain core built-ins. The
// operator panel and the LiveCard component talk to apteva-server
// endpoints (/api/browsers, /api/instances/{id}/computer/stream, ...)
// to fetch live data; this sidecar just serves the static UI bundles
// the platform mounts under /apps/computer/.
//
// A later release will lift the browser backends out of core/ and
// register them as MCP tools here. The manifest will gain entries;
// nothing about today's UI surface needs to change because the
// components consume server endpoints, not direct tool returns.
package main

import (
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────
// Embedded so a built sidecar binary still self-describes if loaded
// without its source tree. Keep in sync with apteva.yaml — the
// platform reads the on-disk yaml at install time.

const manifestYAML = `schema: apteva-app/v1
name: computer
display_name: Computer
version: 0.1.0
description: |
  Watch and steer the agent's browser. Operator panel + chat
  components. v0.1 is UI-only; backends and tools land in a
  later release.
scopes: [project, global]
provides:
  http_routes:
    - prefix: /
  ui_panels:
    - slot: project.page
      label: Browsers
      icon: monitor
      entry: /ui/ComputerPanel.mjs
  ui_components:
    - name: browser-card
      entry: /ui/BrowserCard.mjs
      slots: [chat.message_attachment]
    - name: screenshot-with-som
      entry: /ui/ScreenshotCard.mjs
      slots: [chat.message_attachment]
    - name: live-view
      entry: /ui/LiveCard.mjs
      slots: [chat.message_attachment]
    - name: navigation-timeline
      entry: /ui/TimelineCard.mjs
      slots: [chat.message_attachment]
runtime:
  kind: source
  port: 8080
  health_check: /health
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	ctx.Logger().Info("computer mounted (UI-only, no MCP tools yet)")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) MCPTools() []sdk.Tool              { return nil }

// HTTPRoutes — only /health here. The UI bundles under /ui/* are
// served by the platform's static handler against this app's bundle
// directory; a custom Go handler would shadow that and break panel
// loading. /health is the only route we own.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/health", Handler: handleHealth},
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func main() { sdk.Run(&App{}) }
