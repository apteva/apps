// Local-only simulator for exploring the real app-served frontend. No carrier APIs.
import { join } from "node:path";
const root = import.meta.dir;
const ui = join(root, "../../ui");
const build = await Bun.build({ entrypoints: [join(root, "main.ts")], target: "browser", format: "esm", minify: true });
if (!build.success) throw new AggregateError(build.logs);
const script = await build.outputs[0].text();
type DemoCall = { id: string; status: string; direction: string; peer_kind: string; from_number: string; to_number: string; token: string };
const calls = new Map<string, DemoCall>();
const intents = new Map<string, DemoCall>();
const sockets = new Map<string, Set<any>>();
function createCall(direction: string, to = "+15550100100", from = "+15550100112"): DemoCall {
  const call = { id: crypto.randomUUID(), status: direction === "inbound" ? "pending" : "ringing", direction, peer_kind: "human", from_number: from, to_number: to, token: crypto.randomUUID() };
  calls.set(call.id, call); return call;
}
const session = (call: DemoCall) => ({ call_id: call.id, media_url: `/api/apps/telephony/_install/42/softphone/media/${call.id}/${call.token}`, session_token: call.token });
const json = (value: unknown, status = 200) => Response.json(value, { status });
const server = Bun.serve<{ call: DemoCall; timer?: ReturnType<typeof setInterval> }>({
  hostname: "127.0.0.1", port: Number(process.env.PORT || 5397),
  async fetch(request, server) {
    const url = new URL(request.url), path = url.pathname;
    if (path === "/health") return new Response("ok");
    if (path === "/") return new Response(Bun.file(join(root, "index.html")), { headers: { "Content-Type": "text/html", "Cache-Control": "no-store" } });
    if (path === "/example.css") return new Response(Bun.file(join(root, "style.css")), { headers: { "Content-Type": "text/css" } });
    if (path === "/example.js") return new Response(script, { headers: { "Content-Type": "text/javascript" } });
    if (path === "/demo/incoming" && request.method === "POST") return json(createCall("inbound"));
    const prefix = "/api/apps/telephony";
    if (!path.startsWith(prefix + "/")) return new Response("Not found", { status: 404 });
    const appPath = path.slice(prefix.length);
    const media = appPath.match(/^\/_install\/42\/softphone\/media\/([^/]+)\/([^/]+)$/);
    if (media) {
      const call = calls.get(media[1]);
      if (!call || call.token !== media[2] || call.status === "completed") return new Response("Unavailable session", { status: 403 });
      if (server.upgrade(request, { data: { call } })) return;
      return new Response("WebSocket required", { status: 400 });
    }
    if (url.searchParams.get("project_id") !== "demo" || url.searchParams.get("install_id") !== "42") return new Response("Wrong demo scope", { status: 403 });
    if (appPath === "/ui/frontend.json") return new Response(Bun.file(join(ui, "frontend.json")), { headers: { "Content-Type": "application/json", "Cache-Control": "no-store" } });
    if (/^\/ui\/frontend\/client-[a-f0-9]{16}\.mjs$/.test(appPath)) return new Response(Bun.file(join(ui, appPath.slice(4))), { headers: { "Content-Type": "text/javascript", "Cache-Control": "public,max-age=31536000,immutable" } });
    if (appPath === "/calls") {
      let rows = [...calls.values()].reverse();
      if (url.searchParams.has("call_id")) rows = rows.filter(c => c.id === url.searchParams.get("call_id"));
      return json({ calls: rows.map(({ token, ...call }) => call) });
    }
    if (request.method !== "POST") return new Response("Not found", { status: 404 });
    const body = await request.json().catch(() => ({})) as any;
    if (appPath === "/softphone/place") {
      const previous = intents.get(body.idempotency_key);
      if (previous) return json(session(previous));
      const call = createCall("outbound", body.to, body.from);
      intents.set(body.idempotency_key, call); return json(session(call));
    }
    const id = appPath.startsWith("/calls/") ? appPath.split("/")[2] : appPath.split("/")[3];
    const call = calls.get(id);
    if (!call) return new Response("Unknown call", { status: 404 });
    if (appPath.startsWith("/softphone/answer/")) {
      if (call.status === "completed") return new Response("Call ended", { status: 409 });
      if (call.status === "pending") call.status = "answering";
      return json(session(call));
    }
    if (appPath.startsWith("/softphone/release/")) {
      if (body.session_token !== call.token) return new Response("Wrong session", { status: 403 });
      if (call.status === "answering" && !sockets.get(call.id)?.size) call.status = "pending";
      return json({ ok: true });
    }
    if (appPath.endsWith("/hangup")) {
      call.status = "completed";
      for (const socket of sockets.get(call.id) ?? []) { socket.send(JSON.stringify({ type: "call.ended" })); socket.close(); }
      return json({ ok: true });
    }
    return new Response("Not found", { status: 404 });
  },
  websocket: {
    open(socket) {
      const call = socket.data.call;
      for (const previous of sockets.get(call.id) ?? []) { previous.send(JSON.stringify({ type: "session.replaced" })); previous.close(); }
      const group = sockets.get(call.id) ?? new Set(); group.add(socket); sockets.set(call.id, group);
      call.status = "answered";
      socket.send(JSON.stringify({ type: "ready" }));
      socket.send(JSON.stringify({ type: "peer.connected" }));
      let tick = 0;
      socket.data.timer = setInterval(() => {
        // A quiet periodic tone tests speaker playback; microphone frames stay local and are discarded.
        const pcm = new Int16Array(480);
        if (tick % 100 < 12) for (let i = 0; i < pcm.length; i++) pcm[i] = Math.sin((tick * 480 + i) * 2 * Math.PI * 440 / 24000) * 900;
        tick++;
        socket.send(pcm.buffer);
      }, 20);
    },
    message(socket, data) {
      if (typeof data !== "string") return;
      try {
        const message = JSON.parse(data);
        if (message.type === "ping") socket.send(JSON.stringify({ type: "pong", nonce: message.nonce }));
        if (message.type === "dtmf") socket.send(JSON.stringify({ type: "dtmf.sent" }));
      } catch { /* ignore invalid simulator frames */ }
    },
    close(socket) { clearInterval(socket.data.timer); sockets.get(socket.data.call.id)?.delete(socket); },
  },
});
console.log(`Telephony example: ${server.url} (local simulator; no real calls)`);
