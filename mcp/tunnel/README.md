# Apteva Tunnel

Apteva Tunnel is a self-hosted HTTP tunnel service. It gives projects stable
public HTTPS URLs beneath an operator-controlled domain and carries requests to
a local HTTP service over an outbound WebSocket connection.

It does not use ngrok, zrok, Cloudflare Tunnel, or an Apteva-operated relay.
Every installation controls its own domain, DNS, certificates, credentials,
database, and traffic.

## How it works

1. Install Tunnel as a global Apteva app.
2. Optionally bind the Domains app.
3. Configure a base domain such as `tunnel.example.com`.
4. Tunnel publishes `*.tunnel.example.com` to the Apteva instance, or shows the
   exact DNS record to create manually.
5. Reserving `demo` creates the exact server-native ingress route
   `demo.tunnel.example.com → app://tunnel`. The Apteva server obtains an
   ordinary certificate for that hostname through HTTP-01; a wildcard
   certificate is not required.
6. The connector opens an outbound `wss://` connection to the app and forwards
   public HTTP requests to a local target.

Tunnel requires Apteva 0.29.2 or newer. That platform version adds the
route-level `ingress_auth=app_token` boundary used to authenticate public
host-routed requests to the Tunnel sidecar without making its admin API public.

The first release buffers individual HTTP request and response bodies, with a
configurable size limit. WebSocket targets, raw TCP, streaming bodies, and
multi-edge clustering are intentionally outside the v0.1 protocol.

## Connector

Build the connector:

```sh
go build -o apteva-tunnel ./cmd/apteva-tunnel
```

Write the one-time token returned when creating or rotating a tunnel to a
private file:

```sh
umask 077
printf '%s' 'TOKEN_RETURNED_BY_TUNNEL' > tunnel.token
```

Run:

```sh
./apteva-tunnel \
  --server https://instance.example/api/apps/tunnel \
  --token-file ./tunnel.token \
  --target http://127.0.0.1:5280
```

`APTEVA_TUNNEL_SERVER` and `APTEVA_TUNNEL_TOKEN` are supported for supervised
deployments. Prefer `--token-file` for interactive use so credentials do not
appear in process arguments.

## DNS and deployment

The wildcard DNS record points at the Apteva instance, but each reservation is
an exact ingress hostname. This works with the existing server-native router
and HTTP-01 certificate manager.

For a larger paid service, run dedicated edge instances and add shared
connection state before horizontal scaling. The v0.1 connector is intentionally
bound to the app instance that accepted its WebSocket.
