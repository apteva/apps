const built = await Bun.build({
  entrypoints: [import.meta.dir + "/entry.tsx"],
  target: "browser",
  define: { "process.env.NODE_ENV": '"production"' },
  minify: true,
});
if (!built.success) throw new Error(String(built.logs));
const script = await built.outputs[0].text();
Bun.serve({
  hostname: "127.0.0.1",
  port: 18185,
  fetch(r) {
    const p = new URL(r.url).pathname;
    if (p === "/app.js")
      return new Response(script, {
        headers: { "Content-Type": "text/javascript" },
      });
    return new Response(
      '<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"/><style>html,body,#root{height:100%;margin:0;background:#0b1016;color:#cbd5e1;font-family:Arial}#root>div{height:100%;display:flex;flex-direction:column}button,input,select{font:inherit}header{display:flex;align-items:center;gap:8px;padding:8px}header h1{font-size:15px}.design-main{min-height:600px}.design-viewport{height:100%;min-height:500px}aside{overflow:auto}section{padding:12px}</style></head><body><div id="root"></div><script type="module" src="/app.js"></script></body></html>',
      { headers: { "Content-Type": "text/html" } },
    );
  },
});
console.log("Design browser fixture on :18185");
