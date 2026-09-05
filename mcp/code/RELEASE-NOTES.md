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
