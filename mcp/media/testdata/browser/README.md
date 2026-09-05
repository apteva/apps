# Browser regression checks

Run from `mcp/media` with Bun, FFmpeg and Playwright available:

```sh
bun ui/build.ts
bun testdata/browser/run.ts
```

The harness starts a temporary local server, uses the real React components and
dashboard CSS/vendor bundles, and mocks Media/Storage HTTP responses. It creates
a short synthetic video with FFmpeg and checks actual browser playback. No live
account, provider call, uploaded file, or deployment is used.

Optional environment variables:

- `MEDIA_TEST_PLAYWRIGHT`: absolute path to Playwright's `index.mjs` when it is not
  installed in the usual module search path.
- `MEDIA_DASHBOARD_VENDOR_DIR`: dashboard build's `dist/vendor` directory. The
  default expects a sibling dashboard project in the workspace. CSS is read from
  its parent `dist` directory.
- `MEDIA_TEST_CHROME`: browser executable; otherwise Playwright's browser is used.

Checks cover 150-item pagination, selected-detail refresh after an event, install
routing, real video playback, Smart Crop preview and render range consistency,
Low/Medium/High quality selection, explicit Legacy defaults for new renders, and missing/failed/changed-file transcript states.
Uncaught browser errors fail the run. Screenshots are written under `/tmp` with
the `media-browser-` prefix. HTTP/provider mocks are not a live gateway isolation
test or a remote execution-host benchmark.
