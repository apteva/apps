import { resolve } from "node:path";
const result = await Bun.build({
  entrypoints: [resolve(import.meta.dir, "browser-entry.tsx")],
  target: "browser",
  define: { "process.env.NODE_ENV": JSON.stringify("development") },
});
if (!result.success) throw new Error(result.logs.join("\n"));
Bun.serve({
  hostname: "127.0.0.1",
  port: 5394,
  fetch(request) {
    if (new URL(request.url).pathname === "/entry.js")
      return new Response(result.outputs[0], {
        headers: { "Content-Type": "text/javascript" },
      });
    return new Response(
      '<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><style>body{margin:0;background:#101217;font-family:system-ui;color:#eee}</style></head><body><div id="root"></div><script type="module" src="/entry.js"></script></body></html>',
      { headers: { "Content-Type": "text/html" } },
    );
  },
});
