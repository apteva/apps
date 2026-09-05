# Apteva Code

Code provides project-scoped source repositories, native Git, issues, templates,
editing and development previews through 55 MCP tools, REST routes and four
React panels. `apteva.yaml` is the single embedded manifest source.

## Source integrity

Local files are keyed by project and repository under the installation's data
directory (`CODE_REPOS_DIR` overrides it). Git metadata is separate from source.
SQLite stores metadata, issues, imports, workspace links and runtime ownership.
app-sdk applies migrations with foreign keys enabled.

Edits, patches, imports, Git mutations and workspace applies serialize through
a repository transaction lock. Finite local commands hold that lock while
running. Multi-file writes use a rollback journal; failed cleanup retains a
recovery path/record. Existing file modes survive edits, and imports/forks
preserve executable permissions. Rename and create-only operations reject
occupied paths, including dangling symlinks.

These are application-level transactions with rollback on reported errors,
not power-loss recovery or a multi-process filesystem transaction. Trusted dev
servers and external tools can write outside Code's locks. Use Workspaces and
the preview/apply workflow when concurrent source writers need isolation.

### Editing and patches

Raw file GET returns an ETag. PUT with `If-Match` rejects stale content with 409;
`If-None-Match: *` creates only when absent. Oversized PUT returns 413. MCP
`code_write_file` accepts `expected_sha256` and `create_only`. Unconditional
replacement remains available for existing clients; use preconditions when
replacing previously read content.

The editor preserves drafts on conflicts and external rename/delete, rejects
stale replies after navigation, and retains typing made during a save. File
URLs encode every segment. Highlighting stops above 200,000 characters, and the
read-only preview renders at most 500,000 characters.

Numbered file pages include SHA-256, totals and continuation offsets. Excerpts,
outlines, glob and streaming literal/regex grep are supported. A bounded page
cache validates inode, size, mtime and ctime. Tree summaries invalidate on Code
mutations and expire after 15 seconds for external process writes.

Unified patches validate counts, positions, quoted/spaced paths and EOF newline
markers. Unsupported binary/mode-only patches, renames and duplicate file
sections fail explicitly. Use the rename tool for renames. `fuzzy=true` opts
into relocation and reports `relocated_hunks`. Dry-run IDs retain expected
file hashes/absence and reject later drift. They expire after 30 minutes and
are bounded to 128 entries / 32 MiB of patch text.

## Git and templates

Native Git supports clone, status, diff, commit, history, branches, fetch,
fast-forward pull and push, including platform provider connections. Selected
commits preserve unrelated staged changes, including additions/deletions.
Failed commits restore the original index. Clone has admission/size limits;
Git subprocesses use the caller's cancellation context.

Starters include `blank`, `nextjs`, `static`, `go` and `python`. Missing starter
files fail creation. Embedded `go.mod.tmpl` materializes as `go.mod`, avoiding
Go's nested-module embed exclusion. Forks preserve source modes/framework and
exclude generated trees. Ambiguous global template slugs fail; MCP accepts
`from_project_id` to select the owner. Metadata updates validate every supplied
field before a single column-selective SQL update.

## Commands and Workspaces

`repos_run_command` defaults to `runtime=workspace`, using the optional bound
Workspaces app. Code transfers source, installs dependencies and runs a finite
command in a durable isolated environment. Fingerprints include nested manifests
and workspace configuration; plans cover JS, Go, Python and mixed projects.
Environment values are validated before provisioning and install side effects.

Code remains the source/Git authority. `repos_workspace_changes` previews edits
and returns a workspace digest; `repos_workspace_apply` requires that digest
and rejects concurrent changes on either side. Apply, commands and destroy
share repository admission. Sync errors preserve workspace links/data. A failed
status poll attempts cancellation; failed cancellation reports command and
workspace IDs for recovery instead of implying the command stopped.

For monorepos, `workspace_paths` selects editable doublestar globs and
`support_paths` selects build inputs whose modifications cannot be applied
back. Changing scope/image requires a clean prior workspace source state.
Git data, dependency caches and excluded outputs are not applied back.

`runtime=local` and executable local dev previews require installation config
`trusted_local_execution=true`. This grants the Code sidecar's OS authority;
a working directory is not a sandbox. Use Workspaces for untrusted code or
installations requiring repository isolation. Static previews execute no repo
scripts. Mobile previews delegate to the bound Simulator app.

## Preview lifecycle and issues

The supervisor reserves ports through startup, probes until ready or a deadline,
and supports Stop during dependency installation/startup. Public ingress has
an installation/project/repository-specific hostname and persisted ownership.
Stop/crash removes it; failed cleanup retains ownership and reserves the port.
The UI uses server preview URLs, avoiding browser-local URLs on remote hosts.

Runs have distinct log identities, retain three log files per repository, and
cap each at 16 MiB while preserving final output. SSE resumes with identity and
byte offsets, handles truncation, and the browser retains a 256 KiB tail.
Hard-delete cancels commands, stops previews, removes ingress/workspaces,
quarantines source/Git files, then deletes metadata. Failures preserve recovery
state. After restart, unverified live PIDs become `orphaned` for operator
inspection; Code never kills a PID based only on liveness. Simulator records
remain recoverable and failed shutdown RPCs do not mark them stopped.

Issue lists accept `offset` / `limit` (default 100, max 200), return `total`, and
use stable ID tie-breaking. Deep links fetch by repo/number independently of
list pagination. Detail history uses `history_offset`, up to 200 comments,
links and events per collection, and separate totals. Actor labels come from
SDK caller context or platform-forwarded identity, not body-supplied labels.

## Limits and archive contracts

| Setting | Default |
| --- | --- |
| `max_file_size_mb` / `CODE_MAX_FILE_BYTES` | 10 MiB |
| `max_repo_size_mb` / `CODE_MAX_REPO_BYTES` | 1 GiB |
| `CODE_IMPORT_MAX_COMPRESSED_BYTES` | 128 MiB |
| `CODE_IMPORT_MAX_FILE_BYTES` | 64 MiB decode cap; ordinary file cap still applies on write |
| `CODE_IMPORT_MAX_TOTAL_BYTES` | 256 MiB expanded source |
| `CODE_IMPORT_MAX_FILES` | 20,000 |
| `CODE_EXPORT_INLINE_BYTES` | 8 MiB compressed |
| `CODE_MAX_COMMANDS` | 4 |
| `CODE_DEV_STARTUP_TIMEOUT_SECONDS` | 120 seconds |
| `CODE_DEV_PORT_RANGE_START` / `CODE_DEV_PORT_RANGE_END` | 6100 / 6199 |

Byte environment settings override manifest MB settings. Repository growth is
budgeted; source walks prune generated directories before traversing them.
`CODE_SOURCE_EXCLUDE_DIRS` replaces exclusions with comma-separated directory
names; `.git` always remains excluded. Configure this when real source lives
under a normally generated directory such as `build`, `dist` or `vendor`.

HTTP ZIP exports stream. Small MCP exports return `zip_b64`; larger ones return
`inline:false`, `format:"zip"` and a relative authenticated gateway `download_url`
with project/install scope. Consumers must handle both forms and use existing
authorization to download; the URL is not publicly signed. ZIP import rejects
duplicate normalized paths and uses transactional writes.

## Build and test

Run Go commands in this module; run Bun commands at the apps repository root.
A sibling `../../../ui-kit` checkout supplies host UI types/browser fixtures.
Production bundles externalize React and ui-kit for the dashboard import map.

```sh
GOWORK=off go build .
GOWORK=off go test ./...
GOWORK=off go test -race -tags integration ./... -timeout 180s
GOWORK=off go vet ./...
GOWORK=off go test -run '^$' -bench '^BenchmarkAudit' -benchmem

# From the apps repository root:
bun install --frozen-lockfile
bun run typecheck:code
bun run test:code:ui
bunx playwright install chromium
bun run test:code:browser
bun run scripts/build-panels.ts --app code
```

Tests exercise audit regressions, concurrency, real local Git repositories and
remotes, production-equivalent database constraints, recovery/cancellation,
HTTP/MCP integration, platform test doubles and Chromium editor behavior.
The configured Apteva scenarios under `scenarios/` include a real
Code → Workspaces → Code round trip; run those separately before release.
Mock platform tests do not prove production provider compatibility.
