const build = await Bun.build({
  entrypoints: [`${import.meta.dir}/entry.tsx`],
  target: "browser",
});
if (!build.success) throw new Error(build.logs.join("\n"));
Bun.serve({
  hostname: "127.0.0.1",
  port: 4178,
  fetch(req) {
    if (new URL(req.url).pathname === "/panel.js")
      return new Response(build.outputs[0], {
        headers: { "content-type": "application/javascript" },
      });
    return new Response(
      '<!doctype html><html><head><style>body{font:14px sans-serif}button{margin:4px}button:disabled{opacity:.4}input,select{display:block}header{font-weight:bold}</style></head><body><div id="root"></div><script type="module" src="/panel.js"></script></body></html>',
      { headers: { "content-type": "text/html" } },
    );
  },
});
