# Agent Worlds

Agent Worlds is a project-scoped Apteva sidecar for viewing live agent activity. It provides a project page with four presets: System Graph, Operations, Pixel Village, and Conversation. Every preset uses the same sanitized scene data, so changing views never changes the source.

## Sources

- **Main server:** agents come from `ListAgents`; a filtered `platform.telemetry.read` subscription supplies live events. The app keeps the most recent 600 events in memory. Main-server replay begins when the app starts, because the app telemetry bridge is ephemeral.
- **Environments:** active isolated runtimes in the current project come from `RuntimeAPI`. The panel polls each runtime's recent agent telemetry. No environment agent is started or changed by this app.
- **Remote server:** optionally configure `remote_url`, `remote_api_key`, and `remote_label` on this install. The app reads the remote server's `/api/agents` and `/api/telemetry` endpoints using the key on the backend. When the remote server has Environments installed, it also discovers running environment definitions and reads their inspection feed. Set `remote_project_id` and `remote_environments_install_id` when needed to identify the remote installation. The key is never returned by the app API. Use one Agent Worlds installation per remote server you want to configure.

Event labels deliberately omit message text, thoughts, prompts, and tool arguments. A graph or exported clip conveys activity without copying private conversation content.

## Build

```sh
cd apps
bun run scripts/build-panels.ts --app agent-worlds
cd mcp/agent-worlds
go test ./...
```

The panel exports a PNG or a 12-second WebM clip of the selected preset. Video capture requires browser `MediaRecorder` and canvas capture support.
