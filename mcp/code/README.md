# Apteva Code

Repositories — code workspaces scoped to Apteva projects, with
first-class editing tools modelled on Claude Code.

## Surfaces

- **55 MCP tools** — repository lifecycle (`repos_list`, `repos_create`,
  `repos_get`, `repos_archive`, `repos_set_deploy_hints`) and the
  editing surface (`code_list_files`, `code_glob`, `code_grep`,
  `code_read_file`, `code_read_excerpt`, `code_file_outline`,
  `code_write_file`, `code_apply_patch`, `code_edit_file`,
  `code_multi_edit`, `code_rename_path`, `code_delete_file`), plus
  templates, dev runs, GitHub import, export, and native issues.
- **REST mirror** at `/api/repos/*` for the SPA and curl debugging.
- **Templates** baked into the binary via `embed`: `blank`, `nextjs`.
  More land by dropping a directory under `templates/` and re-building.

## Editing semantics — modelled on Claude Code

- `code_read_file` returns content prefixed with `cat -n` line numbers,
  defaults to a small 200-line page, supports `offset` and `limit`,
  and reports `next_offset`, total line count, and a `truncated` flag.
- `code_read_excerpt` reads ranges, tails, or lines around a target
  without forcing agents to fetch a whole large file for examples.
- `code_file_outline` returns Markdown headings and common code
  declarations with line numbers so agents can orient before reading.
- `code_edit_file` does exact-string replacement and **enforces
  uniqueness** — if `old_string` matches more than once, the call
  fails with the line numbers of the first few matches so the agent
  can disambiguate. `replace_all=true` skips the uniqueness check.
- `code_multi_edit` is **atomic**: if any operation fails the file
  isn't touched. Each edit applies to the state after the previous
  one — same semantics as Claude Code's MultiEdit.
- `code_apply_patch` applies unified diffs across files and supports
  `dry_run=true`. Patch validation is all-or-nothing before writes.
- `code_grep` supports literal + regex modes, glob-scoped paths,
  before/after context, ignore-case, `matches_per_file`, and
  `output_mode`. It defaults to compact file paths; agents request
  `output_mode=content` only when they need matching lines.

## Storage

v0.1 stores file bytes on local disk under
`/data/repos/<slug>/files/`, fronted by the `LocalFileStore`. The
`FileStore` interface is the single seam — v0.2 swaps in
`StorageAppFileStore` once the SDK gains cross-app RPC and Storage
adds `files_replace`. The editing engine and the MCP surface stay
unchanged.

## Isolated command execution

`repos_run_command` keeps its existing local runner and adds
`runtime=workspace`. When the optional Workspaces >=0.5.0 dependency is
installed, Code creates or reuses one durable isolated environment per
repository, transfers a source-only snapshot, installs dependencies when the
dependency inputs change, and runs the requested finite command there.

Code remains the repository and Git authority. Use `repos_workspace_changes`
to preview formatter, generator, or other command-produced source changes. It
returns a workspace digest and reports whether Code changed concurrently. Pass
that exact digest to `repos_workspace_apply`; the apply is rejected if either
side changed after the preview. Git metadata, dependency caches, and common
build outputs are never transferred back. Commit and push only through Code's
Git tools after reviewing the applied files.

For monorepos, pass `workspace_paths` with editable doublestar globs and
`support_paths` with read-only build inputs. Code transfers only their union,
persists the scope with the linked workspace, detects concurrent changes within
that scope, and refuses to apply workspace modifications to support or
out-of-scope files. Changing a scope provisions a new workspace only after the
old workspace is confirmed free of unapplied source changes.

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
(uniqueness, multi-edit atomicity, partial reads, excerpts, outlines,
patch dry-runs/apply/reject behavior, glob, compact grep), the
embedded manifest's parse + handler agreement, template
materialisation. Runs without spawning a binary.

**Tier 2 (integration).** Builds the sidecar and talks to it over
HTTP via the SDK testkit: full repo lifecycle (create → tree → read
→ edit → grep → outline → excerpt → glob → multi-edit → patch →
REST tree), path-traversal
rejection, project-scope isolation between sidecars, and the
global-scope `_project_id` fallback. Catches SDK-wiring drift.

**Tier 3 (scenarios).** Six YAML scenarios under `scenarios/`
exercising create-from-template, write-and-read, unique-match edit,
grep-then-edit, multi-edit refactor, and the full Code → Workspaces → Code
source round trip — each driven by a real LLM through the apteva-server
harness. The workspace scenario recursively installs Containers and Workspaces,
binds Code explicitly, and destroys its durable execution workspace during
test cleanup.

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
