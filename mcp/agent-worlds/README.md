# Agent Worlds

Agent Worlds is a project- or globally-installed Apteva sidecar for viewing live agent activity. It provides a project page with four themes selected from one compact toolbar: System Graph, Operations, Pixel Village, and Space Station. Every theme draws the same sanitized scene data and shares focus, selection, camera, and replay state. App labels use the host directory’s `display_name`, resolved by installation ID and shared across all representations and details. The directory is refreshed once per minute; unavailable names fall back to the scene label, and remote identities are never relabeled from local installs. Instance → project → observed team navigation stays inside the current view. Selecting an agent opens its details without filtering peers, changing the page, or resetting the camera. The inspector shows the selected agent’s actual status, project, threads, destinations, and recent activity. Close it with its close button or Escape; Escape navigates up only when details are already closed. Representation switches preserve project/team focus, inspected agent, camera position, destination selection, page, and replay position. Breadcrumbs and Escape move back up.

Global installations enumerate projects through `PlatformClient.ListProjects()` and fan out explicit project filters through the SDK observation APIs, with at most four projects being read concurrently. Project installations stay pinned to their own project. Empty authorized projects remain visible. Failed project reads produce a generic partial-data notice and never expose their metadata. The main source shows main-server agents; isolated runtimes remain separate sources in the same view.

Teams are **observed collaboration groups**, computed once in the shared view model from content-free cross-agent handoff metadata (`target_agent_id`) within the current replay window. The platform does not expose explicit team records. Unlinked agents stay independent, and links never merge agents from different projects. With no recent handoffs, a project shows its agents directly.

## Themes

The panel is split into layers so new looks do not touch data or interaction code:

- **Model** (`ui/world-model.ts`) turns a scene into projects, observed teams, agents, destinations, and events.
- **Layout** decides where things go. Graph themes use the shared graph/board layout. The village uses `ui/pixel/iso/layout.ts`, a seeded island layout; Space Station uses `ui/pixel/layout.ts`, a top-down tile layout of regions, plazas, buildings, paths, and scenery. The same project always gets the same map.
- **Theme** (`ui/themes.ts`, `ui/pixel/themes/*`) only paints. A top-down pixel theme implements `PixelTheme`: ground, props, places, per-place effects, ambient light, and glows. The village's building types live in `ui/pixel/iso/archetypes.ts`.
- **Renderer** — `ui/clean-stages.tsx` draws the SVG graph and board; `ui/pixel/iso/` draws the isometric village and `ui/pixel/render.ts` the station, both on native-resolution canvases.

**System Graph** and **Operations** are clean SVG views with paging. **Pixel Village** and **Space Station** are canvas pixel maps drawn at native resolution and scaled by whole numbers, so pixels stay sharp. At instance scope every project is its own island (or station deck) on one map; selecting it zooms in.

**Pixel Village** is a bright isometric island per project: a green with a well, buildings spread out with room between them, footpaths that wander from every door to the green, a pond, a garden, and trees thickening towards the coast. Each app or integration is an open-walled building whose type comes from its name: a big hall for task apps, an office for CRMs, an archive for storage, a post office for mail and chat, a workshop for build tools, a bank, a studio for media and web, a planner for calendars, a records office for data, a market for integrations, and a cottage otherwise. Types differ in size, wall material, furniture, and wall decor; the roof and sign carry the category colour, so the same kind of app always reads the same. Cards above each building show its name, status, call count, and time since its last call. Walls are depth-sorted per tile so agents are never hidden by a wall they stand in front of.

Agents stroll A* routes between spots on the green, the paths, and the pond shore. A tool call sends the agent along the paths, through the building's door (walls are only crossed at doors), to the station that matches the tool name (`ui/pixel/iso/stations.ts`): in the CRM, contact tools go to the card catalog, deal tools to the pipeline board, and conversation tools to the inbox desk. While it works, a pill above it says what it is doing ("searching contacts"). A director paces busy agents: calls to one station less than 2.5 s apart are one visit, a one-off call elsewhere flies as an envelope, and an agent that falls behind sprints (over 2 s), dashes with shorter stays (over 6 s), and skips visits that are already stale, so the village stays close to live. An agent inside one building walks out of its door before heading to the next. Motion is a pure function of the scene clock, so polling never restarts it, pausing freezes it, and replay reproduces positions. Speech-bubble emotes show thinking, waiting, errors, and handoffs; handoffs also fly a small envelope between agents. **Space Station** keeps the top-down engine: deck plates and corridors over a starfield, modules and docking pods, flight-suited crew, and a lighting cycle. Maps show up to 40 agents and 12 places per village (6 per project in the overview); they are reachable by keyboard and screen readers through hidden buttons. Reduced-motion preferences and hidden tabs pause ambient animation.

## Sources

- **Main server:** `AppCtx.TelemetryAPI()` supplies project agents and retained activity through the generic observation API. Runtime agents are shown in their own sources.
- **Environments:** `ListObservedRuntimes` and `GetObservedRuntime` expose authorized runtimes across installations. Discovery does not depend on the Environments app or grant execution access.
- **Remote server:** optionally configure `remote_url`, `remote_api_key`, and `remote_label`. `sdk.NewTelemetryClient` uses the same inventory and history contracts as the local client. Set `remote_project_id` to narrow discovery. Without it, remote project IDs come from the SDK observation directory; remote empty projects and human-readable project names are not exposed by that directory. The key stays on the backend. The old `remote_environments_install_id` setting is no longer used.

All sources use the same scene loader. Each nonempty project scene refresh makes one inventory request and one metadata history query, regardless of agent count. The SDK project directory is also read once. Thread and attachment inventories come from `GetAgentTopology` and are cached for 45 seconds per viewer, server credential, project, and agent (maximum 1,024 entries); this replaces app-owned runtime-catalog and integration discovery. App and integration destinations keep their project identity, even when names repeat across projects. The query reads at most 500 matching project/runtime events from the last 15 minutes; the scene displays up to 240 events per project belonging to its current agents. The main-server query shares this bounded project window with runtimes, so a busy runtime can reduce the main-source history shown. There is no background collector, app-owned event log, or per-agent history polling. Replay uses server-retained history and survives app restarts. The panel polls scenes every 2.5 seconds after the previous request finishes, pauses scene refreshes for replay, and skips background polling in hidden tabs. Source/project changes cancel outstanding requests.

The app requests `platform.telemetry.observe`, `platform.runtimes.observe`, and `platform.instances.read`. Activity is requested with the `metadata` projection and decoded using `TelemetryStreamEvent.Metadata()`; raw conversation content is unnecessary. Tool activity covers the recent history window; thread inventory comes from the generic topology API. Missing topology is reported and does not hide the agent scene.

Event labels deliberately omit message text, thoughts, prompts, and tool arguments. A graph or exported clip conveys activity without copying private conversation content.

## Build

`go.mod` pins the published `github.com/apteva/app-sdk` release; build with `GOWORK=off` to check the pin. The server must expose the generic telemetry observation endpoints and permissions described above.

```sh
cd apps
bun run scripts/build-panels.ts --app agent-worlds
cd mcp/agent-worlds
GOWORK=off go test -race ./...
bun run typecheck
bun test
```

Scroll over the visualization to zoom around the pointer, drag to pan, or pinch with two fingers. Camera controls provide zoom buttons and a fit/reset action. When the visualization has keyboard focus, use arrows to pan, +/− to zoom, and 0 or Home to fit. The same camera controller serves every theme and preserves the visible camera in exports. Navigating to another project, team, agent, page, or source resets the camera to fit. Source, focus, portrait, refresh, and export controls are available in the toolbar’s ⋯ menu.

The panel exports a PNG or a 12-second WebM clip of the selected preset. Video capture requires browser `MediaRecorder` and canvas capture support.


## Publishing

Version 0.4.1 is a stable release, distributed through its tagged marketplace manifest and checksummed macOS/Linux native packages for arm64 and amd64. The tag also supports standalone source builds with `GOWORK=off go build .`, using the bundled SDK dependency. Native package builds use that same standalone source path. The release updates Worlds only.

Compatibility requires the generic telemetry observation API: project agent/runtime inventory, bounded retained telemetry queries, and agent topology, with `platform.telemetry.observe`, `platform.runtimes.observe`, and `platform.instances.read`. The existing local server provides these APIs; this app release does not update the server. Older servers without those endpoints need a compatible server build.
