# CRM (v0.1)

Contacts store for Apteva agents and human teams.

## What's in v0.1

- **Contacts** with multi-value channels (email/phone/social/url),
  typed custom attributes with provenance, append-only activity log,
  soft-delete + merge.
- **10 MCP tools** the agent calls for CRUD + find-or-create + merge +
  activity logging.
- **REST surface** at `/api/apps/crm/*` for the dashboard panel.
- **Contacts panel** (vanilla HTML+JS) in `instance.tab` slot —
  filterable list + detail card with all structured fields.
- **Two install scopes**:
  - `project` — one install per Apteva project, physical isolation.
  - `global` — one install across all projects, isolation by
    `project_id` partition column. Agent calls and dashboard requests
    must supply the project explicitly when running global.

## What's deliberately deferred

- Companies as first-class entities (only `company TEXT` for now)
- Deals / pipeline / stages
- Inter-project teams (purely additive — `team_id` column + resolver)
- Bulk import / CSV
- Composio enrichment
- Email / call sync workers

## Local development

```bash
cd mcp/crm
go build .
APTEVA_PROJECT_ID=test ./crm     # smoke run; binds to :8080
curl http://localhost:8080/health
```

See `migrations/001_init.sql` for the full schema and `main.go`'s
`MCPTools()` for the tool surface.
