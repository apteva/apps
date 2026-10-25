const built = await Bun.build({
  entrypoints: [import.meta.dir + "/call-inspector-host.tsx"],
  target: "browser",
  format: "esm",
});
if (!built.success) throw new AggregateError(built.logs);
const js = await built.outputs[0].text();
const css = process.env.TELEPHONY_DASHBOARD_CSS;
Bun.serve({
  hostname: "127.0.0.1",
  port: 5298,
  fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === "/health") return new Response("ok");
    if (path === "/app.js")
      return new Response(js, {
        headers: { "content-type": "text/javascript" },
      });
    if (path === "/theme.css")
      return new Response(css ? Bun.file(css) : "", {
        headers: { "content-type": "text/css" },
      });
    return new Response(
      `<!doctype html><html lang="en" data-theme="clean" data-mode="dark"><head><title>Call inspector fixture</title><link rel="stylesheet" href="/theme.css"><style>html,body,#root{height:100%;margin:0;background:var(--bg,#111);color:var(--text,#eee);font-family:var(--font-sans,system-ui)}*{box-sizing:border-box}</style></head><body><div id="root"></div><script type="module" src="/app.js"></script></body></html>`,
      { headers: { "content-type": "text/html" } },
    );
  },
});
