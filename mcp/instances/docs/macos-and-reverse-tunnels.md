# Macs, machines behind a router, and online status

Instances manages machines only: it connects them, sets them up, keeps them
reachable and monitors them.

## Registering a machine

`instance_register` returns one command. It detects the OS when it runs.

| `connect` | Use for | Needs |
|---|---|---|
| `direct` (default) | Machines this server can reach over SSH | `ssh_host` |
| `vpn` | Linux machines on the bound WireGuard VPN app | VPN app installed; Ubuntu/Debian only |
| `reverse_tunnel` | Linux or macOS machines behind a router | `reverse_tunnel_host` setting; outbound internet on the machine |

`platform` (`linux` or `macos`) is optional. It adjusts the instructions and
refuses impossible combinations early, such as a VPN or Docker on a Mac.

### On macOS the command:

- Turns on Remote Login. If macOS blocks this, the command says what to do
  once: turn on Remote Login in System Settings > General > Sharing, or give
  Terminal Full Disk Access. Then run the command again.
- Creates the SSH account if it does not exist. The account is hidden from the
  login window and has a random password that is never shown.
- Authorizes the management key with safe permissions.
- Adds the account to `com.apple.access_ssh`, the group macOS checks when
  Remote Login is limited to some users.
- Only when software setup is selected: makes the account an administrator
  (Homebrew requires one) and adds a passwordless sudo rule under
  `/etc/sudoers.d/`.
- Keeps the Mac up: `pmset` sleep 0, disk sleep 0, automatic restart after a
  power failure, and wake for network. Automatic macOS upgrades are turned off;
  security updates still install.
- Warns when the Mac has a battery: a laptop sleeps when its lid closes,
  whatever the power settings.
- Prints the same `SSH authorized` line as on Linux.

### macOS setup steps

- **baseline**: checks the power settings, installs the Xcode Command Line
  Tools unattended, and creates `/var/lib/apteva-instances`.
- **runtimes**: installs Homebrew, then Node.js (with npm) and Go. Homebrew
  refuses to run as root, so this needs a non-root SSH account. Homebrew's
  directory is added to the account's `~/.zshenv`, and a fresh SSH session
  must find `node`, `npm` and `go` before setup succeeds.
- **docker**: refused on macOS, with a clear message.
- Monitoring already supports macOS (LaunchDaemon collector).

Macs cannot be targets for Server-managed native deployments, which need Linux
with systemd. Commands, file transfer, metrics and monitoring work.

## Reverse tunnels

The machine keeps
`ssh -N -R 127.0.0.1:<port>:127.0.0.1:<machine ssh port> apteva-tunnel@<server>`
open. It runs as a launchd daemon on macOS and a systemd unit on Linux, with
keepalives and automatic restarts. Instances records the machine as
`ssh_host=127.0.0.1`, `ssh_port=<port>`, so commands, uploads, metrics and
monitoring work unchanged.

### Settings

- `reverse_tunnel_host`: the public hostname or IP of this server. Empty
  disables reverse tunnels.
- `reverse_tunnel_ssh_port`: default 22.
- `reverse_tunnel_ports`: default `22000-22999`.

Each setting has an `APTEVA_INSTANCES_TUNNEL_*` environment override.

### Server requirements

Instances must run directly on a Linux server, not in a container, with root
or passwordless sudo. Inside a container, the tunnel would listen on the
container's loopback address instead of the server's, so registration refuses.

### One-time server setup

The first tunnel registration sets up the server through local host 0:

- Creates a system account, `apteva-tunnel`, with no login shell.
- Appends a marked `Match User apteva-tunnel` block to the end of
  `/etc/ssh/sshd_config`. The block allows key-only logins, remote forwarding
  only, no terminal and no `GatewayPorts`, forces `/bin/false` as the command,
  and closes dead sessions within about 90 s.
- Validates the new configuration with `sshd -t` before installing it, keeps a
  backup at `sshd_config.apteva-backup`, then reloads sshd.
- Confirms the effective settings with `sshd -T`.
- Refuses when `AllowUsers` or `AllowGroups` would block the tunnel account.

### Keys

- Each machine gets its own tunnel key, separate from its management key and
  stored in the encrypted SSH vault. The key's options are
  `restrict,port-forwarding,command="/bin/false",permitlisten="127.0.0.1:<port>"`,
  so it can only open its own loopback listener.
- The tunnel keys live in a root-owned file, `/etc/apteva-tunnel/authorized_keys`.
  Instances rewrites it from the database, so it always matches the registered
  machines exactly.
- The machine pins this server's host keys under a fixed alias:
  `StrictHostKeyChecking=yes`, with no trust on first use.
- Because the command contains the tunnel key, it reports
  `contains_credentials: true`.

### Removal

`instance_destroy` removes the key, ends the machine's live session (which
frees its port) and deletes the vault entry. It never contacts the machine.
The machine's job keeps retrying and is refused.

A session is matched to its port through `ss -p`. Where process details are
hidden, every `apteva-tunnel` session is ended instead: the account runs
nothing else, and other machines reconnect within about 10 s.

There is no public port. Tunnel listeners always stay on the server's loopback
address.

## Online status

`connected` and `last_seen_at` appear on `instance_get` and `instance_list`;
`instance_list` and `GET /api/instances` accept a `connected` filter.

The check runs about every 45 seconds, 8 machines at a time, and never logs in:

- If Instances already holds an SSH connection to the machine, it sends a
  keepalive on that connection.
- Otherwise it opens a TCP connection and reads the SSH greeting. For a
  tunnel, the greeting travels the whole path to the machine's sshd.

A change emits `instance.connected` or `instance.disconnected`. The local host
is always connected.

## Testing

`go test ./...` covers the command builders, registration, port allocation,
resume, destroy, presence and migrations.

`RUN_TUNNEL_E2E=1 go test -run TestReverseTunnelEndToEnd` runs the server
setup and the machine enrollment against real OpenSSH in Docker. It checks:

- SSH reaches the machine through the tunnel.
- The listener is loopback only.
- The tunnel key cannot run commands, listen on another port, listen publicly
  or forward outbound.
- Destroy closes the session and keeps the machine locked out.

The macOS command branch is syntax-checked only; it has not run on a real Mac.
