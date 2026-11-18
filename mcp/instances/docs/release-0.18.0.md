# Instances 0.18.0

Instances now manages Macs and machines behind a router.

## Registration

`instance_register` takes `connect` (`direct`, `vpn` or `reverse_tunnel`) and
`platform` (`linux` or `macos`). It returns one command that detects the OS
when it runs. Impossible combinations are refused early: a VPN or Docker on a
Mac, or `ssh_host` with a tunnel.

## macOS

The command turns on Remote Login, creates a hidden SSH account with a random
password, authorizes the management key, keeps the Mac awake (`pmset`) and
turns off automatic macOS upgrades. It warns when the Mac has a battery. The
account is made an administrator only when software setup is selected.

Setup steps on macOS: `baseline` (power settings, Command Line Tools) and
`runtimes` (Homebrew, Node.js, Go). Docker is refused on macOS.

## Reverse tunnels

The machine keeps a restricted `ssh -R` tunnel open to this server (launchd on
macOS, systemd on Linux). Each machine has its own tunnel key, limited to
`permitlisten="127.0.0.1:<port>"`; listeners stay on the server's loopback
address. There is no public port. The first tunnel registration creates the
`apteva-tunnel` account and a validated `Match User` block in `sshd_config`.
Configure with `reverse_tunnel_host`, `reverse_tunnel_ssh_port` and
`reverse_tunnel_ports`. Instances must run directly on Linux with root or
passwordless sudo, not in a container.

`instance_destroy` removes the key, closes the live session and deletes the
stored key without contacting the machine.

## Online status

`connected` and `last_seen_at` on `instance_get` / `instance_list` (with a
`connected` filter, also on `GET /api/instances`). Checked about every 45 s
without logging in. Changes emit `instance.connected` /
`instance.disconnected`.

## Upgrade

Migration 015 adds `connect_mode`, `connected`, `last_seen_at` and the
`reverse_tunnels` table. Existing VPN-enrolled hosts are labelled `vpn`.

## Testing

Unit tests cover the commands, registration, ports, resume, destroy, presence
and migrations. `RUN_TUNNEL_E2E=1` runs real OpenSSH in Docker. The macOS
command branch has not yet run on a real Mac.

See `docs/macos-and-reverse-tunnels.md`.
