# Instances 0.19.0

## Public SSH for tunnel machines

`instance_public_ssh_set(id, enabled, allowed_cidrs?, max_connections?)`, or
`public_ssh: true` on `instance_register`, gives a reverse-tunnel machine a
public port from `reverse_tunnel_public_ports` (default `23000-23999`).
Instances accepts connections on it and copies them to the tunnel's loopback
port, so the tunnel key stays limited to that loopback port.

- Before the port opens, the machine's SSH server must accept keys only.
  Instances checks over SSH and, with sudo, adds
  `/etc/ssh/sshd_config.d/00-apteva-keys-only.conf`; without sudo it returns a
  command to run on the machine. Registration with `public_ssh` does this in
  the setup command, before the tunnel starts.
- The port is opened in ufw or firewalld when active. A cloud firewall in
  front of the server must allow the range.
- Limits: allowed source CIDRs, 1-100 concurrent connections (default 10),
  20 new connections per minute per source. New connections are logged.
- Restored on start. Reported as `public_ssh` on `instance_get` /
  `instance_list`; changes emit `instance.public_ssh_changed`. Destroy closes
  the port.
- `reverse_tunnel_public_host` defaults to `reverse_tunnel_host`.

## Fixes

- A registration that fails before returning its command deletes its tunnel
  row and key, instead of leaving them to be authorized by the next key sync.
- `instance_destroy` returns `cleanup_command` for registered machines (shown
  in the panel): it stops the tunnel job and removes its key, the management
  key and the sudo rule.
- Re-running the setup command on a Mac waits for launchd to unload the old
  tunnel job before loading the new one.
- The Remote Login check uses the registered SSH port.
- The tunnel account also gets `PermitOpen none`; every setting is checked in
  the effective configuration. A 0.18.0 block is rebuilt once.
- Machines never checked before the upgrade record their first presence state
  without an event.

## Hardening

- The setup command prints the machine's SSH host key fingerprints.
  `instance_wait_ready(expected_host_key)` accepts only those keys instead of
  trusting the first one. Pinned machines negotiate the pinned key type.
- The tunnel keys file names its install; Instances refuses to replace another
  install's file on the same server.

## Upgrade

Migration 016 adds the public SSH columns, `presence_known`,
`ssh_host_key_expected` and `install_identity`. The first tunnel setup after
upgrading rebuilds the sshd block and reloads sshd.

## Testing

Go suite, Docker OpenSSH end-to-end test (`RUN_TUNNEL_E2E=1`) and panel tests
pass. The public SSH proxy is tested in-process. The macOS command has not run
on a real Mac.
