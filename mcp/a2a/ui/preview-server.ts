import { fixture } from "./fixtures";
const build = await Bun.build({
  entrypoints: [new URL("./preview.tsx", import.meta.url).pathname],
  target: "browser",
  format: "esm",
  define: { "process.env.NODE_ENV": '"development"' },
});
if (!build.success) throw new Error(build.logs.join("\n"));
const script = await build.outputs[0].text();
const hostTheme = await Bun.file(
  new URL("./host-theme.fixture.css", import.meta.url),
).text();
Bun.serve({
  hostname: "127.0.0.1",
  port: Number(process.env.A2A_PREVIEW_PORT || 4196),
  fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === "/preview.js")
      return new Response(script, {
        headers: { "Content-Type": "text/javascript" },
      });
    if (path.startsWith("/api/")) return Response.json(fixture(req.url));
    const params = new URL(req.url).searchParams;
    const theme = params.get("theme") === "clean" ? "clean" : "terminal";
    const mode = params.get("mode") === "light" ? "light" : "dark";
    return new Response(
      `<!doctype html><html data-theme="${theme}" data-mode="${mode}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>A2A · Synthetic preview</title><style>
 ${hostTheme}</style></head><body><div id="root"></div><script type="module" src="/preview.js"></script></body></html>`,
      { headers: { "Content-Type": "text/html" } },
    );
  },
});
console.log("A2A synthetic preview ready on http://127.0.0.1:4196");
