# Conversations 0.21.0 validation

The release adds app-served frontend assets and a manifest consumed by Web SDK
0.7.0. The dashboard still uses its existing bundles and does not call apps.load.
The app frontend package is private; no Conversations npm version is published.

Before release:

- 140 Web SDK tests and strict TypeScript/build passed, including parallel downloads,
  immutable cache URLs, integrity checks, failed-load cleanup, isolated client/UI
  instances, reference-counted styles, cancellation and version changes.
- 25 Conversations frontend tests (117 assertions) passed.
- All four browser checks passed using published Web SDK 0.7.0: dashboard and
  external chat, inbox/report rendering, approvals and desktop/mobile geometry.
- All three production dashboard bundles passed host React import verification.
- All 12 Tier 3 workflows passed with real Codex on the 0.21.0 candidate in
  229.011 seconds, including reports, alerts, approvals and originating threads.
- A separate browser host loaded its client/UI/styles from the installed app using
  real application-user tokens, displayed real Codex replies, and confirmed that
  another visitor could not read the first visitor’s transcript. The dashboard
  wrapper passed against the same runtime using its session cookie.

Client/UI/CSS assets total approximately 55 KiB when gzip-compressed. This describes
asset size, not production page-load latency. The external loader allows the page
to render while it loads. The dashboard adds no frontend-manifest request.

The external example in frontend/example/main.tsx imports only React, React DOM
and @apteva/web-sdk. Its UI is supplied by the installed app. Runtime validation
uses a separate local platform and temporary agents, not production.
