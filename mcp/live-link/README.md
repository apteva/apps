# Live Link

Live Link publishes an Apteva instance through an explicitly selected HTTPS
tunnel provider. The panel shows the active URL, a QR code, lifecycle status,
and bounded run history.

## Providers

| Provider | Account required | URL behavior | Local agent |
|---|---:|---|---|
| Cloudflare Quick | No | Fresh `*.trycloudflare.com` URL per start | `cloudflared` |
| Cloudflare Named | Yes | Stable hostname on a Cloudflare zone | `cloudflared` |
| ngrok | Yes | Random URL, or a configured reserved domain | `ngrok` |
| zrok | Yes | Stable free `*.share.zrok.io` reserved name | `zrok2` |

Binding an integration does not silently switch providers. Select the provider
in the panel, acknowledge that the target will be public, then choose **Go
live**. Provider changes and configuration changes require the current tunnel
to be stopped.

Cloudflare Named setup lists the zones accessible through the bound connection,
validates that the requested hostname belongs to the selected zone, and creates
or reconciles the tunnel ingress and proxied CNAME. Reconfiguration stages the
new hostname before deleting the previous resource. Deleting the hostname is a
separate destructive action; switching back to Quick preserves it.

ngrok reads the bound authtoken just before launch. Set `ngrok_domain` in app
settings only for a domain already reserved on the ngrok account.

zrok uses the `enable_token` stored in a bound zrok connection to enable one
isolated native zrok environment per Live Link install. Configure a reserved
name in the panel; zrok's public namespace maps it to
`https://<name>.share.zrok.io`. Switching providers preserves the name.
The explicit **Release name** action removes the upstream name and local native
environment.

## Security model

- Public exposure is opt-in in the panel. Live Link does not add authentication
  to the target; the target must enforce its own access controls.
- Cloudflare API requests go through Apteva's integration proxy. The account API
  token does not enter this process.
- Cloudflare connector tokens and ngrok authtokens are passed only through the
  selected child process environment. zrok enable tokens are sent to the
  official controller over HTTPS and written only to zrok's required native
  `0600` environment file. No provider credential is placed in argv, logs, run
  history, or the app database.
- The zrok child receives an isolated `HOME` and never receives the enable
  token directly. A connection ID in SQLite prevents a new binding from
  adopting or deleting a name owned by the previous zrok account.
- Inherited credentials for inactive providers are removed before launch.
- Auto-installed agents are version-pinned, downloaded only over HTTPS from an
  allowlisted host, size-bounded, and SHA-256 verified before atomic install.
- Target URLs must be absolute HTTP(S) URLs without embedded credentials.
- HTTP request bodies and retained subprocess diagnostics are bounded; known
  credential values are redacted from diagnostics.

The app can expose any HTTP service reachable from its sidecar. The default is
`APTEVA_GATEWAY_URL`, falling back to `http://localhost:5280`. Treat a custom
`target_url` as security-sensitive configuration.

## Lifecycle and persistence

`runtime_state` stores two independent values:

- `active_provider`: the operator's explicit provider choice.
- `desired_live`: whether the operator last requested live or off.

When `auto_restart_on_boot` is enabled, a live intent is restored after both
clean restarts and crashes. A delayed restart is cancelled during unmount, and
all start, stop, provider, reinstall, configure, and destroy operations are
serialized.

Cloudflare named-tunnel metadata is persisted, but connector tokens are fetched
just in time and never persisted. Each start reconciles remote ingress and DNS,
so configuration drift does not survive unnoticed. Run history is capped at
1,000 rows and queried newest-first.

## Binary resolution

For each provider, Live Link resolves the agent in this order:

1. Explicit `cloudflared_path`, `ngrok_path`, or `zrok2_path` when it exists.
2. The corresponding binary on `$PATH`.
3. A matching managed binary in the app data directory.
4. A pinned and verified download for supported Linux or macOS architectures.

Version `0.6.0` pins cloudflared `2026.7.1`, ngrok `3.39.9`, and zrok `2.0.4`.
The ngrok stable download URL is mutable; a changed upstream archive
intentionally fails checksum verification until a new Live Link version updates
the pin. zrok artifacts use the checksums published with its GitHub release.

## API

HTTP routes are mounted under the app proxy:

- `GET /status` — lifecycle, provider, target, and configuration summary.
- `POST /start` and `POST /stop` — idempotent lifecycle controls.
- `POST /provider` — select one of the four providers.
- `POST /provider/configure` — configure persistent provider resources through
  `{provider, config}`. Cloudflare Named accepts `{zone_id, hostname}`; zrok
  accepts `{name}`.
- `GET /runs` — bounded recent run history.
- `POST /install` — reinstall the selected provider's verified agent.
- `GET /named/zones`, `POST /named/configure`, `GET /named/current` — named
  Cloudflare configuration.
- `POST /destroy` — destroy the requested provider's persistent resource using
  `{provider}`. An omitted provider keeps the legacy Cloudflare Named behavior.

The `/named/*` routes remain compatibility adapters for older panels. There are
no zrok-specific lifecycle routes.

MCP tools expose the same core lifecycle operations as `expose_start`,
`expose_stop`, `expose_status`, and `expose_destroy`.

## Development

From this directory:

```sh
GOWORK=off go test -race ./...
bun test ui/qr.test.ts
```

The shipped panel is `ui/LiveLinkPanel.mjs`, built from
`ui/LiveLinkPanel.tsx`. Rebuild and commit the bundle whenever the source panel
or QR encoder changes.
