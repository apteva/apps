---
name: how-to-use-cdn
description: |
  CDN's mental model + conventions. Load when working with custom
  hostnames, vanity URLs, or asking how files / pages get exposed
  publicly on a non-platform domain. Covers zones, the local-mode
  v0.1 model, linking storage / media-studio / podcast / deploy to
  a zone, and the trade-offs vs. just using the platform's own
  PublicURL. Triggers on: "custom domain", "vanity URL", "files on
  acme.com", "publish on", "expose under", "behind a domain", or
  any cdn_* tool call.
command: /cdn
metadata:
  category: infrastructure
---

# How to use CDN

CDN is the app that turns a hostname like `files.acme.com` into a
working public URL. It doesn't store files, doesn't run the origin,
doesn't cache bytes (in v0.1) — it's an orchestrator that wires up
three other apps so the platform's HostRouter can reverse-proxy by
Host header.

## Mental model

- **A zone is one hostname → origin URL mapping.** Created via
  `cdn_zone_create`, removed via `cdn_zone_delete`, queried via
  `cdn_zone_list` and `cdn_zone_get`. Idempotent — re-creating the
  same zone returns the existing row.
- **Zones live alongside, not instead of, the platform's PublicURL.**
  Apps that aren't linked to a zone keep returning their normal
  `<PublicURL>/api/apps/<name>/...` URLs. Linking is per-install,
  per-consumer-app.
- **URL minting is a separate call.** Consumer apps (storage,
  media-studio, …) call `cdn_url_for(zone_id, origin_path)` at URL-
  build time. The cdn sidecar isn't in the request path — only at
  URL-mint time.

## What v0.1 covers (and doesn't)

| Capability | v0.1 | Comes later |
|---|---|---|
| Custom hostname → apteva-server origin | yes (local mode) | — |
| TLS via certs app | yes (when certs is installed) | — |
| DNS via domains app | yes (when domains is installed) | — |
| Edge caching | no | Mode A (provider) deferred; Mode B in v0.3 |
| Self-hosted edge nodes (via instances) | no | v0.3 |
| Multi-region origins | no | v0.4 |
| Provider-signed URLs (Cloudflare / Bunny token auth) | no | v0.5+ |
| Cache purge | n/a (no cache) | v0.3 |

There is no third-party CDN provider in the v0.1 request path. The
apteva-server is always the origin; the only thing the zone does is
let the request arrive on a custom hostname.

## Deps + which are optional

- **routes** is *required* — without it, apteva-server's HostRouter
  has nothing to dispatch on. cdn refuses to install without it.
- **domains** is *optional* — when unbound, cdn skips the DNS write
  leg (`dns_status="skipped"`). The operator wires DNS by hand —
  registrar UI, `/etc/hosts`, or a private resolver.
- **certs** is *optional* — when unbound, the zone has to serve over
  plain HTTP. Pass `allow_http:true` on `cdn_zone_create` to make
  this explicit (the cert leg is skipped, and the route is registered
  with `allow_http=true` so apteva-server doesn't 301 to HTTPS).

This makes the "install cdn + routes only, try locally" path cheap.

## Local dev (no registrar, no TLS)

The fastest way to feel cdn end-to-end without a domain or a cert:

1. Install `routes` and `cdn` (skip `domains` and `certs`).
2. Set cdn's `server_public_host` install config to `127.0.0.1`
   (only used when DNS gets written; harmless when `skip_dns:true`).
3. Add to `/etc/hosts`:
   ```
   127.0.0.1  files.local.test
   ```
4. Create a zone with the local-dev flags:
   ```
   cdn_zone_create(
     hostname:   "files.local.test",
     origin_url: "http://127.0.0.1:8080",     # storage's sidecar
     skip_dns:   true,                         # /etc/hosts owns DNS
     allow_http: true                          # no cert, no redirect
   )
   ```
5. `curl http://files.local.test/files/<id>/content` proxies through
   the HostRouter to storage.
6. Linking storage: set storage's `cdn_zone_id` to the returned zone
   id. `files_upload(..., visibility:"public")` now mints
   `http://files.local.test/files/<id>/content` for public files.

`cdn_url_for` honours the zone's `allow_http` flag, so consumer apps
get `http://` URLs for local-dev zones and `https://` URLs for
production zones — the storage call site doesn't need to know which
mode it's in.

## Creating a zone

```
cdn_zone_create(
  hostname:   "files.acme.com",
  origin_url: "http://127.0.0.1:8080"        // storage's sidecar URL
)
→ { zone: { id: 7, hostname, origin_url, status: "active", ... }, created: true }
```

Three legs run in sequence:

1. `domains.domain_records_set` — writes a DNS A record (or CNAME
   if configured) pointing the hostname at `server_public_host`
   (set in cdn's install config).
2. `certs.cert_issue` — kicks ACME for a TLS cert; async, the
   CertCache picks it up on its next 60-second refresh.
3. `routes.routes_register` — registers the host→target route so
   apteva-server's HostRouter reverse-proxies incoming requests.

Each leg's status lands on the zone row (`dns_status`,
`cert_status`, `route_status`). The overall `status` is `active` if
all three succeeded, else `error` with the failures in
`status_detail`. The local row exists regardless — re-running
`cdn_zone_create` for the same hostname returns it untouched.

## Linking a zone to a consumer app

The cdn app provides the zone; the consumer app stores its zone id
in its own install config. For storage:

1. `cdn_zone_create(hostname="files.acme.com", origin_url="http://127.0.0.1:8080")`
   → returns `zone.id = 7`.
2. Open storage's install settings, set `cdn_zone_id = 7`.
3. Storage's `absoluteContentURL` now mints public file URLs through
   the zone. Signed and private URLs continue to use the platform's
   PublicURL — only the public visibility tier flows through the
   zone.

The consumer-side wiring is per-app. Other consumer apps
(media-studio, podcast, …) follow the same pattern.

## Deleting a zone

```
cdn_zone_delete(id: 7)
→ { deleted: true, id: 7, hostname: "files.acme.com" }
```

Best-effort tear-down: unregisters the route, revokes the cert,
deletes the DNS record. Any leg that fails (e.g. registrar
unreachable) is logged but doesn't block local-row removal — the
operator can clean up by hand at the registrar / certs panel.

## When not to use CDN

- **Internal-only URLs.** Don't create a zone for paths only the
  agent ever reads — the platform's PublicURL already works.
- **One-off shares.** A signed URL via `files_get_url` (storage)
  is the right primitive for "let this person download this file
  for an hour."
- **You don't own the domain.** Zones need DNS write access, which
  needs a registrar connection (Porkbun / Namecheap) bound on the
  domains app for the apex.

## Failure modes worth knowing

- **`server_public_host not configured`** — the cdn install needs
  `server_public_host` set before any zone can be created. Set it
  to the IP or hostname clients should resolve to.
- **`apex CNAME isn't allowed`** — RFC says no CNAME at the apex.
  Use `record_type=A` with an IP for an apex hostname.
- **Cert pending for the first ~60 seconds.** ACME issuance is
  async; the first HTTPS request after `cdn_zone_create` may see
  a TLS handshake error. Wait one refresh tick.
- **Route registered but DNS not propagated.** `cdn_zone_create`
  returns immediately; the new hostname becomes browser-reachable
  only after recursive resolvers see the new record (typically
  seconds to minutes depending on previous TTL).
