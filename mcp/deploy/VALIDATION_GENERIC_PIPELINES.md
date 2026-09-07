# Generic pipeline validation — 2026-09-07

Implemented and checked against Code `code/v0.10.0`, Deploy `deploy/v0.25.0`,
and the workspace's `integrations/src/apps/steamworks.json`. The SDK dependency
is pinned to v0.76.0, verified as a descendant of v0.75.0.

Passed:

- Complete Go suite with race detection and integration enabled (110.575 s).
- Final complete Go unit suite after follow-up fixes (10.691 s).
- Focused race tests after the final publisher/policy changes (7.378 s).
- Real Code sidecar → Deploy transfer with multiple chunks, interruption,
  subsequent repository edits, resume, checksum validation and subdirectory scope.
- Five UI tests, including three Chromium behavior tests; strict TypeScript;
  production panel build and host import verification.
- Six existing Steamworks connector tests, including publisher authentication,
  form POSTs, preserved HTTP 201 responses and HTTP error propagation.
- Go vet, Linux amd64 compilation, module tidy and whitespace validation.

New Go regressions exercise malformed/expired/oversized snapshots, persisted
transfer receipts, executable binary assets, command export/build/test stages,
artifact modification detection, path confinement, selected/revoked bindings,
policy approval and invalidation, upload reuse, uncertain publish outcomes,
missing cloud evidence and cancellation before promotion.

Store API responses in publisher tests are synthetic. No real SteamCMD upload,
Steam Mobile confirmation, Apple/Google store publication or engine SDK build was
performed. Commands, runner tools, Steam build-account authentication and publisher
connections must be configured for a real target. The Steam configuration example
contains placeholder IDs and an illustrative observation pointer; confirm the
provider response shape before enabling production policy rules.

No new release tag or production deployment was created as part of this change.
