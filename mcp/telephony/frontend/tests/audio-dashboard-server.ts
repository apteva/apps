const result = await Bun.build({
  entrypoints: [import.meta.dir + "/audio-dashboard-host.tsx"],
  target: "browser",
  format: "esm",
  minify: true,
});
if (!result.success) throw new AggregateError(result.logs);
const source = await result.outputs[0].text();
// Reuse the local dashboard theme for visual inspection; no dashboard edits.
import { dirname, join } from "node:path";
let cssPath = process.env.TELEPHONY_DASHBOARD_CSS || "";
if (!cssPath) {
  let directory = import.meta.dir;
  while (directory !== dirname(directory)) {
    const candidate = join(directory, "dashboard/dist/style.css");
    if (await Bun.file(candidate).exists()) {
      cssPath = candidate;
      break;
    }
    directory = dirname(directory);
  }
}

Bun.serve({
  hostname: "127.0.0.1",
  port: 5297,
  fetch(r) {
    const path = new URL(r.url).pathname;
    if (path === "/health") return new Response("ok");
    if (path === "/app.js")
      return new Response(source, {
        headers: { "Content-Type": "text/javascript" },
      });
    if (path === "/theme.css")
      return new Response(cssPath ? Bun.file(cssPath) : "", {
        headers: { "Content-Type": "text/css" },
      });
    return new Response(
      '<!doctype html><html lang="en"><head><title>Telephony audio health fixture</title><link rel="stylesheet" href="/theme.css"><style>html,body,#root{margin:0;height:100%;font-family:system-ui;background:#101319;color:#e4e8ee}select,input{min-height:36px}button:disabled{opacity:.4}</style></head><body><div id="root"></div><script type="module" src="/app.js"></script></body></html>',
      {
        headers: {
          "Content-Type": "text/html",
          "Content-Security-Policy":
            "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'",
        },
      },
    );
  },
});
