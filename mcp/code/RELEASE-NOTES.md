# Apteva Code 0.14.10

Clarifies the live MCP tool descriptions, published manifest, and `/code`
skill so agents distinguish preview start/status/logs from workspace source
transfer and optional external Git auto-sync. Both enable and pause operations
change Git settings; neither starts, repairs, or verifies a preview. Enabling
Git auto-sync is only for a requested Git workflow with a Git-backed repository
and an origin-tracking branch. Saving or previewing native files needs no Git
remote, commit, native checkpoint, or auto-sync configuration.

Adds `scenarios/10-native-html-preview.yaml`: a real agent creates and edits
one HTML file in a blank repository, checks a real workspace preview live
twice, and verifies the saved source, disabled Git sync, unchanged initial
revision count, and dirty working tree. Git tools remain available and the
directive does not tell the agent which tools to choose. All assertions passed
with `openai-codex` / `gpt-6.1-sol` in 11 iterations, with no failed tools or Git
mutations. The temporary test preview and repository are removed afterward.

The manifest and runtime source pin match `code/v0.14.10`; the published app
SDK pin remains `v0.92.0`. No runtime handler behavior changes in this release.

---

# Apteva Code 0.14.9

Fixes static HTML previews in repositories created as `blank`: the detected
preview framework now selects the workspace runtime as well as its command.
Static, Node, and Next.js previews use the Bun profile; Go previews use Go.
Custom/blank commands retain source-based profile selection, and explicit
repository workspace image preferences are still forwarded unchanged.

Adds regression coverage for a blank repository containing only `index.html`,
framework/metadata mismatches, and a disposable Docker static preview that
serves HTML and relative assets with the default Bun profile image. The latter
runs with `CODE_TEST_DOCKER=1 go test -run TestStaticWorkspacePreviewDocker`.

Pins the Go app SDK to the current published `v0.92.0`. The separate Workspaces
exit-code formatting defect is not changed in this Code release.

---

# Apteva Code 0.14.8

Adds `code_import_file` to import one text or binary file into an existing
repository from a Core-rehydrated blob handle, binary envelope, or base64.
Exact bytes are preserved, including empty files. The destination path is
explicit; upload filenames are never used as paths.

Imports are create-only by default. Overwrites require the current destination
SHA-256; `create_only: true` always rejects an existing destination. Validation,
size limits, path protection, project isolation, and conditional atomic writes
keep failures non-destructive. Imports do not create Git commits or native
checkpoints. The exported `/code` skill and tool descriptions document the flow.

Unit and live MCP regression tests cover byte fidelity, empty files, malformed
inputs, unresolved handles, stale hashes, limits, traversal/symlinks, project
isolation, and concurrent creation. Manifest tests enforce the 73-tool surface
and the matching `code/v0.14.8` runtime source pin.

---

# Apteva Code 0.14.7

Clarifies the native-only editing flow in MCP tool descriptions and the `/code`
skill. A simple file write is already saved and does not require a Git commit or
native checkpoint. Agents are directed to verify `repos_git_status.git_backed`
before using Git mutation tools and not to retry Git commits for native-only
repositories.

---

# Apteva Code 0.14.6

Exports `how-to-use-code` through `provides.skills` with the `/code` command.
The playbook covers ZIP and Git imports, reviewed edits, workspace execution
and apply-back, optional native revisions, and building connected interfaces
with the published Apteva Web SDK. SDK examples cover authentication, scoped
app calls, CRM search/create/update, and loading another app's frontend.

The runtime source pin matches `code/v0.14.6`. Validation checks the skill's
manifest/file identity, available Code tools, and TypeScript examples against
the published SDK package.

Pins the sidecar to the current published Go app SDK, `v0.91.0`.

---

# Apteva Code 0.14.5

Corrects the runtime source ref that made 0.14.4 installations build 0.14.3
source. The published manifest and compiled MCP tool surface now come from the
same release, including `repos_import_zip` and reachable Git commit imports.

A regression test requires the manifest's source ref to match its version.
The ZIP preview/apply integration tests exercise the live compiled tool.

---

# Apteva Code 0.14.4

This patch release lets `repos_git_import` pin an import to any reachable Git
commit SHA as well as a branch or tag. Branch imports retain their local branch
and upstream tracking; tag and commit imports use detached HEAD so the working
tree is exactly the requested immutable revision.

Unknown or unreachable refs fail before Code creates a repository record, with
an actionable error that distinguishes the accepted ref forms.

It also adds reviewed ZIP import through MCP. Agents can preview an archive
against a new or existing repository, inspect additions and overwrites, then
apply the same staged bytes with `import_id` and `confirm=true`. Overlay imports
preserve files absent from the archive. Destination changes or repository
replacement after preview reject the apply without modifying files. ZIP import
does not require Git or create a native checkpoint automatically.

## Validation

Smart-HTTP regression coverage verifies tracking-branch, tag, full commit SHA,
and missing-ref imports against a real disposable Git remote. ZIP regression
coverage exercises preview/apply identity, stale destinations, invalid archives,
and both new-repository and overlay imports.

---

# Apteva Code 0.14.3

This patch release makes agent patching compatible with both unified diffs and
Codex-style `*** Begin Patch` envelopes. Structurally bounded hunks with
inaccurate counts are normalized safely, while malformed patches now report the
file, hunk header, line, and declared versus actual counts.

Dry-run previews still retain the exact submitted patch and expected source
hashes. Applying by `patch_id` therefore applies precisely the reviewed result,
and parser or context failures remain atomic and non-destructive.

## Validation

Regression coverage includes Codex modify/create/delete patches, valid unified
diffs, malformed Terra/Sol/Luna benchmark patterns, safe count normalization,
strict context rejection, multi-file atomicity, and preview/apply identity.

---

# Apteva Code 0.14.2

This patch release makes repository identities permanently monotonic. Deleting
the highest-numbered repository can no longer let SQLite reuse its identity for
a newly created repository, preventing stale deletion guards from blocking
commands and Workspaces operations on the replacement.

## Validation

Regression coverage verifies that stale operations remain rejected while a
replacement repository receives a new identity and remains usable. The full
unit suite, integration suite, `go vet`, and production build all pass.

---

# Apteva Code 0.14.1

This patch release fixes native checkpoints when Code is running with its
normal repository-locking file store. Checkpoints now complete without
recursively acquiring the write-held repository lock.

## Validation

The locked-store regression, full unit suite, integration suite, `go vet`, and
production build all pass.

---

# Apteva Code 0.14.0

This release makes version control native to Code. Every repository can now
have immutable revisions, branches, tags, safe restore, and exact-revision
exports without a Git installation or external provider. Git remains an
optional synchronization adapter.

## Changes

- Content-addressed native blobs, trees, revisions, and branch pointers stored
  outside the editable working tree.
- New native MCP tools for status, checkpoints, history, diffs, branches, tags,
  restore, and exact-revision ZIP export.
- New repositories and forks receive an initial native revision automatically;
  ordinary editing remains working-tree-first with no checkpoint ceremony.
- Git adapter startup is optional. Missing Git no longer prevents Code from
  mounting or serving native version control.

## Validation

Native revision, branch, tag, restore, export, invalid-ref, manifest, and full
Code test suites pass with the existing provider-neutral Git tests.

---

# Apteva Code 0.9.0

This reliability release addresses the 26 findings from the Code 0.8.2 audit.
It prevents lost concurrent edits and stale editor saves, makes multi-file
mutations recoverable, corrects Git status/selected commits, and preserves
workspace data when sync fails.

The app source is pinned to the immutable `code/v0.9.0` release tag and uses
app-sdk 0.75.0, including transactional migrations and protected-route auth fixes.

## Changes

- Transactional edits/imports/patches, conditional file saves, no-overwrite
  create/rename behavior, executable-mode preservation and strict unified diffs.
- Safe static previews, persisted scoped ingress ownership, reserved ports,
  cancellable startup/commands, slow-server readiness and recoverable deletion.
- Paginated issues/history, direct issue deep links, nested dependency tracking,
  working Go/Python/static starters and atomic repository metadata updates.
- Bounded file/archive/clone operations, streaming search, revision caches,
  resumable bounded logs and editor request ordering/large-file safeguards.
- One embedded manifest, shared backend services and extracted UI components.

## Upgrade notes

- Finite commands now default to isolated Workspaces. Bind Workspaces, or set
  `trusted_local_execution=true` explicitly for trusted local commands/dev scripts.
- Large MCP ZIP exports return an authenticated `download_url` instead of base64.
  Consumers must support both response forms.
- Fuzzy patches require `fuzzy=true`; malformed or unsupported diffs fail clearly.
- Public preview hostnames now include installation/project/repository identity.
  Check legacy public ingress routes when upgrading an existing installation.
- Migrations add import-history cascading deletion and persisted ingress ownership.

## Validation

192 Go tests/subtests passed with race detection and integration enabled,
including a disposable Git HTTP server covering clone through push. Nine
Chromium behavior tests, four UI unit tests, strict TypeScript, Go vet, Linux
amd64 compilation and production panel builds passed.

In three alternating local benchmark samples, repeated page reads were 5.3×
faster and search allocated about 92% fewer bytes. These are microbenchmarks,
not production latency guarantees.

Live Workspaces/Containers, Simulator, provider credentials and public ingress
were not exercised against a deployed installation. The complete remediation
and test mapping is in [AUDIT-FIXES.md](AUDIT-FIXES.md).
