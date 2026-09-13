import { resolve } from "node:path";
const result = await Bun.build({
  entrypoints: [resolve(import.meta.dir, "browser-entry.tsx")],
  target: "browser",
  define: { "process.env.NODE_ENV": JSON.stringify("development") },
});
if (!result.success) throw new Error(result.logs.join("\n"));
const streams = new Set<ReadableStreamDefaultController<Uint8Array>>();
const encoder = new TextEncoder();
let runs: any[] = [], reads = 0;
Bun.serve({
  hostname: "127.0.0.1",
  port: 5394,
  async fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === "/api/app-events/_all") {
      let own: ReadableStreamDefaultController<Uint8Array>;
      return new Response(new ReadableStream({ start(controller) {own=controller; streams.add(controller);controller.enqueue(encoder.encode(": connected\n\n"));}, cancel() {streams.delete(own);} }), {headers:{"Content-Type":"text/event-stream","Cache-Control":"no-cache"}});
    }
    if (path === "/fixture/runs") {
      if (request.method === "POST") { runs = await request.json() as any[]; return Response.json({ok:true}); }
      reads++; return Response.json({direct_runs:runs,runs:[]});
    }
    if (path === "/fixture/events") {
      const event = await request.json();
      for (const controller of streams) controller.enqueue(encoder.encode(`data: ${JSON.stringify(event)}\n\n`));
      return Response.json({ok:true});
    }
    if (path === "/fixture/disconnect") {for (const controller of streams) controller.close();streams.clear();return Response.json({ok:true});}
    if (path === "/fixture/stats") return Response.json({reads,streams:streams.size});
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
