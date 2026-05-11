# VPN

Backend-agnostic VPN orchestrator. Provisions a VPN server on an Instances host, generates keys, pushes daemon config, polls peer stats. v0.1 ships **WireGuard**; the internal `Backend` interface accommodates OpenVPN / strongSwan IKEv2 later without changes to the MCP tool surface.

## How it fits together

```
agent (chat / scenario)
   │
   ▼ MCP
vpn sidecar  ──► instances.instance_run_command  ──► Linux host (apt install wireguard,
   │            instances.instance_upload_file               sysctl, systemd, wg-quick)
   │
   └── SQLite (server row, peer rows, stats updated by the poller)

peer device (Mac/Win/iOS/Android/Linux)
   │
   ▼ UDP 51820
host  ── kernel WireGuard ── MASQUERADE eth0 ── internet
```

## Tools

| Tool | What it does |
|---|---|
| `vpn_status` | Backend, host, endpoint, port, install timestamp, peer counts, last poll result. |
| `vpn_install` | One-time: install daemon, generate server keys, push config, enable service. |
| `vpn_uninstall` | Stop service, remove configs. Refuses if peers exist unless `force=true`. |
| `vpn_peer_add` | Allocate IP, generate credentials, hot-reload daemon. Returns config + QR. |
| `vpn_peer_remove` | Revoke peer, re-render daemon config without it. |
| `vpn_peer_list` | All peers with handshake / rx / tx / status. |
| `vpn_peer_config` | Re-emit config + QR for an existing peer (credentials are stored). |
| `vpn_announce` | Force an immediate stats poll. |

## Adding a backend

1. Create `backend/<name>/<name>.go` implementing `backend.Backend`.
2. Add the option to `apteva.yaml` `config_schema.backend.options`.
3. Wire it into `pickBackend` in `orchestrator.go`.

No changes to `main.go`, MCP tools, or the panel — those are protocol-neutral by construction.

## Client setup

Every modern OS has a native WireGuard client. The `.conf` returned by `vpn_peer_add` is the universal format:

| OS | App | Source |
|---|---|---|
| macOS | WireGuard | Mac App Store |
| iOS | WireGuard | App Store |
| Windows | WireGuard | wireguard.com/install |
| Android | WireGuard | Play Store / F-Droid |
| Linux | `wg-quick` / NetworkManager | distro packages |

Paste the `config` text into *Add empty tunnel*, or save as `<name>.conf` and double-click to import.

## Operational notes

- **One server per install.** Each install owns one host_id and one server row. Run two installs if you need two separate VPNs.
- **Peer privkeys are stored server-side.** Lets `vpn_peer_config` re-emit. Threat model: a DB read also yields the server key, so storing peer keys adds nothing on top.
- **No transcoding / no protocol bridging.** Each install commits to one backend at install time.
- **Distro coverage:** v0.1 targets Debian / Ubuntu (Hetzner default images). Alpine and RHEL deferred.
