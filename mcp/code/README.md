# Apteva Code (v0.1)

Repositories — code workspaces scoped to Apteva projects, with
first-class editing tools modelled on Claude Code.

## Surfaces

- **14 MCP tools** — repository lifecycle (`repos_list`, `repos_create`,
  `repos_get`, `repos_archive`, `repos_set_deploy_hints`) and the
  editing surface (`code_list_files`, `code_glob`, `code_grep`,
  `code_read_file`, `code_write_file`, `code_edit_file`,
  `code_multi_edit`, `code_rename_path`, `code_delete_file`).
- **REST mirror** at `/api/repos/*` for the SPA and curl debugging.
- **Templates** baked into the binary via `embed`: `blank`, `nextjs`.
  More land by dropping a directory under `templates/` and re-building.

## Editing semantics — modelled on Claude Code

- `code_read_file` returns content prefixed with `cat -n` line numbers,
  supports `offset` and `limit` for partial reads, and reports the
  total line count + a `truncated` flag.
- `code_edit_file` does exact-string replacement and **enforces
  uniqueness** — if `old_string` matches more than once, the call
  fails with the line numbers of the first few matches so the agent
  can disambiguate. `replace_all=true` skips the uniqueness check.
- `code_multi_edit` is **atomic**: if any operation fails the file
  isn't touched. Each edit applies to the state after the previous
  one — same semantics as Claude Code's MultiEdit.
- `code_grep` supports literal + regex modes, glob-scoped paths,
  before/after context, ignore-case. Skips binary files.

## Storage

v0.1 stores file bytes on local disk under
`/data/repos/<slug>/files/`, fronted by the `LocalFileStore`. The
`FileStore` interface is the single seam — v0.2 swaps in
`StorageAppFileStore` once the SDK gains cross-app RPC and Storage
adds `files_replace`. The editing engine and the MCP surface stay
unchanged.

Repository metadata lives in `code.db` (SQLite, migrations under
`migrations/`). Files are **not** shadowed in the DB — the FileStore
is the source of truth for content, the DB is for repos only.

## Local development

```bash
go build .
APTEVA_PROJECT_ID=test \
APTEVA_APP_TOKEN=dev-token \
CODE_REPOS_DIR=/tmp/code-repos \
DB_PATH=/tmp/code.db \
./code

# Smoke
curl -s http://localhost:8080/health
curl -s -H "Authorization: Bearer dev-token" \
     -X POST http://localhost:8080/api/repos \
     -d '{"name":"My Site","framework":"nextjs"}'
curl -s -H "Authorization: Bearer dev-token" \
     "http://localhost:8080/api/repos/my-site/tree"
```

## Tests

Three tiers, mirroring the convention CRM and Storage use.

```bash
go test ./...                       # tier 1, ~25ms — unit
go test -tags integration ./...     # tier 2, ~1.3s — real binary, real HTTP
apteva test ./scenarios/            # tier 3, ~3min — real LLM
```

**Tier 1 (unit).** Path normalisation, slug generation, repository
CRUD (project-scoping + slug uniqueness), the editing engine
(uniqueness, multi-edit atomicity, partial reads, glob, grep), the
embedded manifest's parse + handler agreement, template
materialisation. Runs without spawning a binary.

**Tier 2 (integration).** Builds the sidecar and talks to it over
HTTP via the SDK testkit: full repo lifecycle (create → tree → read
→ edit → grep → glob → multi-edit → REST tree), path-traversal
rejection, project-scope isolation between sidecars, and the
global-scope `_project_id` fallback. Catches SDK-wiring drift.

**Tier 3 (scenarios).** Five YAML scenarios under `scenarios/`
exercising create-from-template, write-and-read, unique-match edit,
grep-then-edit, and multi-edit refactor — each driven by a real LLM
through the apteva-server harness.

## Out of scope for v0.1

- Cross-app delegation to Storage (waits on SDK app-to-app RPC)
- Git layer (commits, branches, diffs)
- Real-time multi-cursor editing
- LSP / autocomplete
- In-browser execution / terminals
- Read-receipt enforcement on `code_write_file` (designed; deferred)
- `code_grep` content cache (cold-grep is fine for repos up to a
  few thousand files; FTS5 cache lands in v0.2)

## Path to deploy (v0.3)

The future Apteva Deploy app reads:

1. `GET /api/repos/<slug>` for metadata
2. `GET /api/repos/<slug>/export` for a zip
3. Builds via Kaniko, pushes to the registry, calls the orchestrator
4. `PATCH /api/repos/<slug>` to record `deploy_service` +
   `last_deployed_at`

Every field that pipeline needs is captured at create time via the
`.apteva/repo.json` template payload.
