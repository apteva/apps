# Post reporting validation

Validated locally on 2026-10-03, starting from apps `origin/main`
`23582284fda72d595a9f22b729920fd8de12a061` (Social v0.16.5).
Environment: Go 1.26.8, darwin/arm64; Bun 1.3.13. Go checks used `GOWORK=off`
and the published app-sdk v0.93.0 dependency, without a local SDK overlay.

## Results

| Check | Result |
| --- | --- |
| `GOWORK=off go test -count=1 -timeout 180s ./...` | PASS: 218 top-level tests, 255 test/subtest results, no skips or failures |
| `GOWORK=off go test -race -count=1 -timeout 600s ./...` | PASS: the same 255 results; no race reports, skips, or failures |
| `GOWORK=off go vet ./...` | PASS |
| `GOWORK=off go build -o /private/tmp/social-post-reporting .` | PASS |
| `bun test mcp/social/ui scripts/verify-social-artifacts.test.ts` | PASS: 39 tests, 140 assertions |
| TypeScript check of panel, charts, publishing calendar, and performance widget | PASS |
| `bun run scripts/build-panels.ts --app social` | PASS: all five UI entry points; all 11 bundle modules import against the host React surface |
| `bun run scripts/verify-social-artifacts.ts` | PASS: all 11 generated bundles match committed source inputs and behavior |
| `git diff --check` | PASS |

Go commands run from `mcp/social`; Bun commands run from the apps repo.
The TypeScript command was:

```sh
bunx --no-install tsc --noEmit --jsx react-jsx --module esnext \
  --moduleResolution bundler --target es2022 --skipLibCheck \
  --allowImportingTsExtensions mcp/social/ui/SocialPanel.tsx \
  mcp/social/ui/SocialCharts.tsx \
  mcp/social/ui/SocialPublishingCalendarWidget.tsx \
  mcp/social/ui/SocialPerformanceWidget.tsx
```

## New regression coverage

Nine top-level tests in `post_reporting_test.go` cover:

- Reproducing a project-wide 200-post window with 45 HGV posts and 60
  targets, while the profile has 74 stored posts. Both slug and ID filtering
  retrieve all 74 posts and reconcile their 89 targets with profile counts.
- Traversing 413 posts in three pages with no duplicates or omissions,
  including equal timestamps, stable ID ordering, and the final page.
- Retaining the default 50-post limit, MCP cap of 200, HTTP cap of 1000,
  and minimum-limit clamping. Beyond-end offsets return explicit empty pages.
- Using identical profile, status, project, and date filters for totals and
  results; inclusive start/exclusive end bounds; rejecting unknown and foreign
  profiles; ignoring spoofed project arguments in a trusted MCP context.
- Counting drafts without targets; distinguishing posts from targets;
  excluding cross-project records from profile counts; agreeing between
  `profile_list` and `profile_get`.
- Returning explicit zero counts and surfacing database failures as errors.
- Preserving correct counts after profile renames and no-op updates.
- Rejecting negative, fractional, malformed, infinite, and overflowing offsets.
- Advertising all filters and pagination fields in the MCP input schema.
- Running a real isolated Social sidecar: creating a profile and drafts via
  MCP, comparing paged HTTP and MCP responses, and confirming anonymous
  `/posts`, `/profiles`, `/accounts`, and `/accounts/start` requests return 401.

The existing suite also covers publishing, scheduling, retries, drafts,
providers, imports, metrics, inboxes, OAuth handoff validation, popup polling,
project isolation, and Home widgets. UI bundles have no changes.

## Scope and limits

Changes are confined to the Social app. No server or integrations source was
changed. No running instance or production data was accessed or modified.
The numbers above are synthetic regression fixtures, not a verification of
the reported live HGV records.

These are local checks. The repository's Social GitHub workflow was found in
`disabled_manually` state and its setting was preserved.

Offset pagination assumes the dataset remains unchanged between pages. Every
single response reads totals, posts, and targets from one database snapshot;
separate pages are separate snapshots. Counts describe currently stored
history and cannot prove lifetime non-use of unimported or deleted content.
