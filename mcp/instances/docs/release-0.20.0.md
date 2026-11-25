# Instances 0.20.0

## Apps on registered machines

A registered Linux machine, such as a Raspberry Pi behind a router, can now
host Apteva apps through the native backend. The main server reaches the app
over Instances' SSH connection, through the reverse tunnel for machines behind
a router, so the machine needs no public address.

- New setup option `apps`, shown as **Run Apteva apps on this machine** when
  registering (Linux only; refused for macOS). The enrollment command grants
  the SSH account passwordless sudo, as the other software options do. Setup
  adds an "App hosting" step that checks sudo gives root and that systemd is
  running, reported as `setup.verified.apps`.
- Native backend commands from a non-root account run through `sudo -n`: the
  deploy check, release upload, start (credentials still travel over stdin),
  state transfer and the inspection shell. Machines that log in as `root` are
  unchanged.
- If a machine without root access is picked as a deploy target, the server
  gets an error naming the `apps` option instead of "target SSH/systemd/gateway
  validation failed".

The app on the machine calls the main server back at its gateway URL, which
must be HTTPS or a loopback address.

## Testing

`RUN_TUNNEL_E2E=1 go test -run TestReverseTunnelEndToEnd` now also logs in
through a reverse tunnel as the non-root account and checks the refusal
without sudo, then, with the enrollment's sudo rule: the deploy check, a root
write fed over stdin, the inspection shell, the state transport and an HTTP
request through the private control port. A full app deploy was not run:
the containers have no systemd.
