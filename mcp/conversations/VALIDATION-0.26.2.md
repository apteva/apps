# Conversations v0.26.2 validation — 2026-11-16

HTTP, HTTPS and protocol-relative links rendered in chat messages and Markdown
report sections now open a new tab with `noopener noreferrer`. Relative app
routes, fragments and email links retain their previous behavior. Sanitization
is shared by transcript and report rendering. No agent tools or skills changed.

- Four Playwright browser tests passed: dashboard chat, full panel, app-served
  external chat and packaged React chat. Each opens an actual preview tab and
  verifies the original chat URL and draft stay intact, `window.opener` is null,
  report links follow the same policy, unsafe URLs/event handlers remain removed,
  and relative/fragment/email links do not acquire a new-tab target.
- 142 Bun UI/client tests passed, with 2,044 assertions.
- Package build, public consumer TypeScript checks, app-served frontend build,
  and all four panel build/import checks passed.
- `GOWORK=off go test -race -tags=integration ./...` passed (126.606s).
- Application build and release artifact/version checks passed.
- App SDK v0.115.2 was verified as the latest release tag by ancestry from
  v0.115.1 and pinned for this release.

The native browser checks directly exercise this presentation behavior; no
additional model scenario is needed for a link target change.
