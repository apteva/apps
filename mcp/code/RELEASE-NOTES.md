# Apteva Code 0.15.0

Moves native version control to storage format 2. Every existing tool keeps
its arguments and output, and existing history keeps its revision IDs.

## Changes

- **Change index.** A checkpoint or status check only rereads files whose
  size, timestamp or mode changed. Files modified within 2 seconds of the
  previous index write are always rehashed, so fast same-size edits are never
  missed. A missing or damaged index only costs a full rehash.
- **Folder trees.** Each revision also records a tree object per folder.
  Unchanged folders are shared between revisions, and `repos_diff` between
  two new revisions skips them without reading their contents.
- **Branch-move log and `repos_undo`.** Every checkpoint, restore, branch or
  tag creation and undo is appended to a log per ref. `repos_undo` lists a
  branch's moves or moves it back to a revision from that list; for the
  checked-out branch the working tree is restored too, which requires no
  changes outside a revision. REST: `GET /api/repos/<slug>/version/moves`.
- **Safe concurrent checkpoints.** `repos_checkpoint` accepts
  `expected_head` and refuses with "native ref moved" (HTTP 409 on REST) if
  the branch moved since the caller looked. All native mutations now hold the
  repository lock for their whole operation; restore and its revision are
  recorded under one lock.
- **Garbage collection.** On mount, and at most once a day per repository,
  objects no branch, tag or logged move can reach are removed once older than
  14 days. Any unreadable reachable object aborts collection without deleting
  anything.
- **Fix: auto-sync deadlock.** Git auto-sync held the repository lock and then
  called the native checkpoint, which took the same lock again, hanging the
  repository whenever auto-sync had changes to save.

## Upgrade and rollback

The first start upgrades each repository in place: nothing is rewritten, a
`format` marker is written, and each branch and tag gets a first log entry
("migrate"). Format-1 revisions stay readable. New revisions still carry the
format-1 flat tree, so a rollback to 0.14.10 can read history written by
0.15.0. Moves made before the upgrade, or while rolled back to 0.14.10, are
not in the log, so `repos_undo` cannot target them; `repos_restore` still
can. A real-binary round trip (0.15.0 writes, 0.14.10 reads, diffs, exports
and checkpoints, 0.15.0 reads and undoes again) passed all checks.

## Validation

- Tier 1: 21 new tests covering migration from a hand-built 0.14 layout,
  rollback readability, index reuse and racily-clean rehashing, folder-tree
  sharing and integrity, file/folder swaps, `expected_head` under 12 concurrent
  writers, a linear log under concurrent edits and checkpoints, undo and its
  refusals, torn log lines, GC reachability and aborts, and the auto-sync lock
  regression. Full suite passes with `-race`.
- Tier 2: the real sidecar over MCP and REST (expected_head, 409, undo, redo,
  moves, folder-tree diff), and an in-place upgrade: a repository written by
  the binary is rewritten to the 0.14 layout, the sidecar restarted on the
  same database, and history, tags, export, restore and undo verified.
- Tier 3: `scenarios/11-native-undo.yaml`, a live agent that saves two
  revisions and moves the branch back with `repos_undo`. Passed all 15
  assertions with `opencode-go` / `kimi-k2.7-code` in 9 iterations on a
  clean spawned server: two checkpoints, one undo, no restore, no Git, no
  failed calls; file, clean status, history and move log verified over REST.

---

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
