import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
const root = resolve(import.meta.dir, "../../../.."), app = resolve(import.meta.dir, "../.."), scratch = await mkdtemp(join(tmpdir(), "tests-widget-host-"));
const apiPort = process.env.TESTS_WIDGET_FIXTURE_PORT || "4338", port = Number(process.env.TESTS_WIDGET_HOST_PORT || 4337);
const binary = join(scratch, "tests-fixture");
const build = Bun.spawn(["go", "test", "-c", "-o", binary], { cwd: app, env: { ...process.env, GOWORK: "off" }, stdout: "inherit", stderr: "inherit" });
if (await build.exited) throw new Error("Could not build local Tests fixture");
const backend = Bun.spawn([binary, "-test.run=^TestWidgetsBrowserFixture$", "-test.v"], { cwd: app, env: { ...process.env, TESTS_WIDGET_FIXTURE: "1", TESTS_WIDGET_FIXTURE_PORT: apiPort }, stdout: "inherit", stderr: "inherit" });
let stopped = false;
const stop = () => { if (!stopped) { stopped = true; backend.kill(); } process.exit(0); };
process.on("SIGTERM", stop); process.on("SIGINT", stop); process.on("exit", () => backend.kill());
for (let n = 0; n < 200; n++) {
  if (await fetch(`http://127.0.0.1:${apiPort}/__health`).then(r => r.ok).catch(() => false)) break;
  if (backend.exitCode !== null) throw new Error("Local Tests fixture exited");
  await Bun.sleep(100); if (n === 199) throw new Error("Local Tests fixture did not become ready");
}
const vendor = new Map<string, Uint8Array>();
for (const [name, pkg] of Object.entries({ react: "react", "react-dom": "react-dom", "react-dom-client": "react-dom/client", "react-jsx-runtime": "react/jsx-runtime" })) {
  if (process.env.TESTS_HOST_VENDOR) { vendor.set(name, new Uint8Array(await Bun.file(join(process.env.TESTS_HOST_VENDOR, name + ".mjs")).arrayBuffer())); continue; }
  const external = name === "react" ? [] : name === "react-dom-client" ? ["react", "react-dom"] : ["react"];
  const imported = await import(Bun.resolveSync(pkg, root));
  const names = Object.keys(imported.default || imported).filter(key => key !== "default" && /^[$A-Z_a-z][$\w]*$/.test(key));
  const entry = join(scratch, name + ".ts"); await writeFile(entry, `import * as ns from ${JSON.stringify(pkg)}; const value = ns.default || ns; export const {${names.join(",")}} = value; export default value;`);
  // Resolve React through the same workspace dependencies used by the host.
  const result = await Bun.build({ entrypoints: [entry], target: "browser", format: "esm", minify: true, external, define: { "process.env.NODE_ENV": '"production"' }, plugins: [{ name: "workspace-react", setup(build) { build.onResolve({ filter: /^(react|react-dom)(\/.*)?$/ }, args => external.includes(args.path) ? { path: args.path, external: true } : { path: Bun.resolveSync(args.path, root) }); } }] });
  if (!result.success) throw new AggregateError(result.logs, "Could not build host React"); vendor.set(name, new Uint8Array(await result.outputs[0].arrayBuffer()));
}
let delayEnvironment = "", delayMS = 0;
Bun.serve({ hostname: "127.0.0.1", port, async fetch(req) {
  const url = new URL(req.url), path = url.pathname;
  if (path === "/__delay") { delayEnvironment = url.searchParams.get("environment") || ""; delayMS = Number(url.searchParams.get("ms") || 0); return new Response("ok"); }
  if (path.startsWith("/__") || path.startsWith("/api/apps/tests/")) {
    if (path.includes("/dashboard/") && url.searchParams.get("environment") === delayEnvironment && delayMS) await Bun.sleep(delayMS);
    const target = `http://127.0.0.1:${apiPort}${path.replace("/api/apps/tests", "")}${url.search}`;
    return fetch(target, { method: req.method, headers: req.headers, body: ["GET", "HEAD"].includes(req.method) ? undefined : await req.arrayBuffer() });
  }
  const match = path.match(/^\/vendor\/(.+)\.mjs$/); if (match && vendor.has(match[1])) return new Response(new Uint8Array(vendor.get(match[1])!), { headers: { "Content-Type": "text/javascript" } });
  if (/^\/(TestsPanel|TestsOverviewWidget|TestsFailuresWidget)\.mjs$/.test(path)) return new Response(Bun.file(join(app, "ui", path.slice(1))), { headers: { "Content-Type": "text/javascript" } });
  if (path === "/host.css" && process.env.TESTS_HOST_CSS) return new Response(Bun.file(process.env.TESTS_HOST_CSS), { headers: { "Content-Type": "text/css" } });
  return new Response(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">${process.env.TESTS_HOST_CSS ? '<link rel="stylesheet" href="/host.css">' : ''}<style>
  :root{color-scheme:light;--color-bg-card:#fff;--color-bg-input:#f4f6fa;--color-bg-hover:#f7f8fb;--color-text:#172033;--color-text-dim:#64748b;--color-text-muted:#536179;--color-border:#dfe5ee;--color-accent:#5865d9}body{min-width:0;margin:0;padding:24px;background:#f2f4f8;color:var(--color-text);font:14px system-ui}h1{font-size:18px;margin:0 0 18px}#root{width:100%;min-width:0;max-width:100%}#widgets{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;max-width:1200px;margin:auto}.widget-frame{min-width:0;height:650px}body.dark{--color-bg-card:#151b27;--color-bg-input:#1b2433;--color-bg-hover:#1e293b;--color-text:#e7edf7;--color-text-dim:#93a4bb;--color-text-muted:#b4c1d2;--color-border:#303c4f;--color-accent:#a5adff;--color-green:#4ade80;--color-red:#fca5a5;--color-yellow:#fcd34d;background:#0b111b;color:#e7edf7}#panel{max-width:1200px;margin:auto}@media(max-width:700px){body{padding:12px}#widgets{grid-template-columns:1fr}}
  </style><script type="importmap">{"imports":{"react":"/vendor/react.mjs","react/jsx-runtime":"/vendor/react-jsx-runtime.mjs","react-dom":"/vendor/react-dom.mjs","react-dom/client":"/vendor/react-dom-client.mjs"}}</script></head><body><h1>Project overview</h1><div id="root"></div><script type="module">
  import React from 'react';import{createRoot}from'react-dom/client';import Overview from'/TestsOverviewWidget.mjs';import Failures from'/TestsFailuresWidget.mjs';import Panel from'/TestsPanel.mjs';
  const root=createRoot(document.querySelector('#root'));const q=new URLSearchParams(location.search);
  let props={projectId:q.get('project_id')||'project-a',installId:6,widgetId:'local-widget',widgetSize:q.get('size')||'half',eventRevision:0};
  window.renderWidgets=(patch={})=>{props={...props,...patch};root.render(React.createElement('div',{id:'widgets'},...(q.get('widget')==='failures'?[]:[React.createElement('div',{key:'overview',className:'widget-frame'},React.createElement(Overview,props))]),...(q.get('widget')==='overview'?[]:[React.createElement('div',{key:'failures',className:'widget-frame'},React.createElement(Failures,props))])))};
  if(location.pathname==='/apps/tests/page')root.render(React.createElement('div',{id:'panel'},React.createElement(Panel,props)));else window.renderWidgets();
  </script></body></html>`, { headers: { "Content-Type": "text/html" } });
} });
console.log(`Local Tests widget host: http://127.0.0.1:${port}`);
