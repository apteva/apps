import { resolve } from "node:path";
const root = resolve("dist");
const server = Bun.serve({
  hostname: "127.0.0.1", port: 4173,
  async fetch(request) {
    const url = new URL(request.url);
    const path = resolve(root, `.${url.pathname === "/" ? "/index.html" : url.pathname}`);
    if (!path.startsWith(root + "/")) return new Response("Not found", { status: 404 });
    const file = Bun.file(path);
    if (!await file.exists()) return new Response("Not found", { status: 404 });
    return new Response(file);
  },
});
console.log(`Community preview: ${server.url}`);
