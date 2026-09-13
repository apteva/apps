// External host: only the shared SDK is bundled here. Telephony is loaded over HTTP.
const bundle = await Bun.build({ entrypoints: [import.meta.dir + "/host.ts"], target: "browser", format: "esm" });
if (!bundle.success) throw new AggregateError(bundle.logs);
const source = await bundle.outputs[0].text();
const panel = await Bun.build({ entrypoints: [import.meta.dir + "/panel-host.tsx"], target: "browser", format: "esm" });
if (!panel.success) throw new AggregateError(panel.logs);
const panelSource = await panel.outputs[0].text();
Bun.serve({
  hostname: "127.0.0.1", port: Number(process.env.TELEPHONY_HOST_PORT || 5295),
  async fetch(request, server) {
    const url = new URL(request.url), path = url.pathname;
    if (path === "/host.js") return new Response(source, { headers: { "Content-Type": "text/javascript" } });
    if (path === "/panel.js") return new Response(panelSource, { headers: { "Content-Type": "text/javascript" } });
    // Match the dashboard's execution policy. In particular blob AudioWorklets
    // are blocked by script-src even though worker-src permits blob workers.
    if (path === "/panel") return new Response('<!doctype html><title>Calls panel integration</title><style>html,body,#root{height:100%;margin:0}button{min-height:32px} [aria-label="Active call controls"]{border:1px solid #ddd;padding:8px} .hidden{display:none}</style><div id="root"></div><script type="module" src="/panel.js"></script>', { headers: { "Content-Type": "text/html", "Content-Security-Policy": "default-src 'self'; script-src 'self'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; media-src 'self' blob:; object-src 'none'" } });
    // Dashboard cookie transport -> controlled operator gateway. Also relay media
    // because the panel correctly resolves its WebSocket against this host.
    if (path.startsWith("/api/apps/telephony/")) {
      const gateway = new URL(process.env.TELEPHONY_TEST_GATEWAY!);
      const target = new URL(path + url.search, gateway);
      if (request.headers.get("upgrade")?.toLowerCase() === "websocket") {
        target.protocol = "ws:";
        const upstream = new WebSocket(target.href);
        upstream.binaryType = "arraybuffer";
        await new Promise<void>((resolve, reject) => { upstream.onopen = () => resolve(); upstream.onerror = () => reject(new Error("Upstream media failed")); });
        const queued: Array<string | ArrayBuffer> = [];
        upstream.onmessage = e => queued.push(e.data);
        if (server.upgrade(request, { data: { upstream, queued } })) return;
        upstream.close(); return new Response("Upgrade failed", { status: 400 });
      }
      target.searchParams.set("project_id", "telephony-tier2");
      target.searchParams.set("install_id", "42");
      const headers = new Headers(request.headers); headers.set("Authorization", "Bearer headless-browser-fixture"); headers.delete("host");
      return fetch(target, { method: request.method, headers, body: request.method === "GET" ? undefined : await request.arrayBuffer() });
    }
    if (path === "/health") return new Response("ok");
    return new Response('<!doctype html><title>Headless Telephony test host</title><button id="answer">Answer</button><script type="module" src="/host.js"></script>', { headers: { "Content-Type": "text/html" } });
  },
  websocket: {
    open(socket: any) {
      const { upstream, queued } = socket.data;
      upstream.onmessage = (event: MessageEvent) => socket.send(event.data);
      upstream.onclose = () => socket.close();
      for (const data of queued) socket.send(data);
    },
    message(socket: any, data: string | Buffer) { socket.data.upstream.send(data); },
    close(socket: any) { socket.data.upstream.close(); },
  },
});
