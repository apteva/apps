# Conversations v0.26.3 validation — 2026-11-17

Numbered and bulleted list markers in chat messages and Markdown report
sections now line up with the surrounding text. The list indent was a fixed
`1.1rem` with markers drawn outside it, so a `1. ` marker (about 1.8em wide in a
monospace theme) hung left of the paragraph edge. The indent is now
`max(1.1rem, 2ch)` for bullets, `max(1.1rem, 3ch)` for numbered lists and
`max(1.1rem, 4ch)` for numbered lists with ten or more items. Stylesheet only:
no TypeScript, Go, agent tools or skills changed, and the app-served React
bundle is byte-identical to v0.26.2.

- Browser measurement with the compiled stylesheet and a 20px monospace font:
  before, list text started 17.6px in and its marker hung about 18px left of the
  paragraph edge; after, `1.`, `10.` and bullet markers align with the
  paragraph edge.
- 178 Playwright browser tests passed across dashboard, panel, app-served and
  packaged hosts.
- 142 Bun UI/client tests passed, with 2,044 assertions.
- Package build, public consumer TypeScript checks, app-served frontend build,
  and all four panel build/import checks passed.
- `GOWORK=off go mod tidy` left go.mod/go.sum unchanged; `go vet` clean;
  `GOWORK=off go test -race -tags=integration ./...` passed (144.229s).
- App SDK stays pinned at v0.115.2. v0.116.0 exists and is left for its own
  release.
