# Routes (v0.1)

Hostname-based routing for Apteva.

Owns the table mapping public hostnames to local backend targets. Apps
register routes; apteva-server reads them and reverse-proxies inbound
traffic. Optional — uninstall to disable hostname routing without
breaking the rest of the platform.

## Surfaces

- **4 MCP tools** — `routes_register`, `routes_unregister`,
  `routes_list`, `routes_get`.
- **REST surface** at `/api/apps/routes/api/routes/*` for the panel
  and apteva-server's cache.
- **Routes panel** at the install-settings slot — admin table, manual
  entries, owner-aware delete, cert column.
- **Events** — `routes.changed` on every register/unregister.
  Apteva-server subscribes for cache invalidation.

## How it's used

```
deploy.attach_domain(blog.example.com, port=7100)
  → domain_records_set (Domains app: writes the DNS record)
  → cert_issue          (Certs app: starts ACME via DNS-01)
  → routes_register     (this app: hostname → http://127.0.0.1:7100)
                         → emits routes.changed
                         → apteva-server cache picks up
                         → next request to blog.example.com proxied to :7100
```

## Schema

```
host_routes (
  id, hostname (unique), target,
  owner_install_id (0 = manual), owner_kind,
  cert_fqdn, allow_http,
  created_at, updated_at
)
```

`UNIQUE(hostname)` keeps two backends from claiming the same domain.
`owner_install_id` lets the orphan sweeper clean up routes when an
install is removed.

## Local development

```bash
cd mcp/routes
go build .
APTEVA_PROJECT_ID=test ./routes
curl http://localhost:8080/health
```

## Tests

```bash
go test ./...                  # tier 1 — schema + store + tool roundtrips
```
