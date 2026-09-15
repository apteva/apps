import { fixture } from "./fixtures";
const build = await Bun.build({
  entrypoints: [new URL("./preview.tsx", import.meta.url).pathname],
  target: "browser",
  format: "esm",
  define: { "process.env.NODE_ENV": '"development"' },
});
if (!build.success) throw new Error(build.logs.join("\n"));
const script = await build.outputs[0].text();
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
    return new Response(
      `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>A2A · Synthetic preview</title><style>
 :root{--bg:#0f1419;--bg-card:#1a2128;--bg-input:#131a21;--bg-hover:#222b35;--text:#e6edf3;--text-muted:#9eacbc;--border:#303b47;--accent:#68c5b4;--success:#6dcea8;--warn:#e6b65c;--error:#eb8181;--info:#8ab6ef}*{box-sizing:border-box}body{margin:0}#root{height:100dvh}button,input,select{font-family:inherit}</style></head><body><div id="root"></div><script type="module" src="/preview.js"></script></body></html>`,
      { headers: { "Content-Type": "text/html" } },
    );
  },
});
console.log("A2A synthetic preview ready on http://127.0.0.1:4196");
