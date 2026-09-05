import { test, expect } from "bun:test";
import { chromium } from "playwright";

const compiled = await Bun.build({
  entrypoints: [import.meta.dir + "/host.tsx"], target: "browser",
  define: {"process.env.NODE_ENV": '"production"'},
  plugins: [{name:"test-react", setup(builder) {
    builder.onResolve({filter:/^react(?:-dom)?(?:\/.*)?$/}, args => ({path:Bun.resolveSync(args.path, import.meta.dir)}));
  }}],
});
if (!compiled.success) throw new Error(compiled.logs.join("\n"));
const bundle = await compiled.outputs[0].text();
const deployment = (id: number, mobile = false) => ({id, name: id === 1 ? "Alpha" : "Bravo", target_kind:mobile?"ios":"service", framework:mobile?"ios":"blank", source_kind:"local", source_ref:"/fixture", build_backend:"local", build_backend_config_json:"{}", target_config_json:"{}", env_json:"{}", build_cmd:"", start_cmd:"", port_hint:0, domain:"", created_at:"2026-09-05", updated_at:"2026-09-05"});

for (const secondary of [false, true]) test(`selection is safe with delayed ${secondary ? "mobile signing" : "detail"} response`, async () => {
  let unblock!: () => void; let requested!: () => void;
  const gate = new Promise<void>(resolve => { unblock = resolve; });
  const started = new Promise<void>(resolve => { requested = resolve; });
  const posts: URL[] = [];
  const server = Bun.serve({hostname:"127.0.0.1",port:0, async fetch(req) {
    const url = new URL(req.url), path = url.pathname;
    if (path === "/") return new Response('<div id="root"></div><script type="module" src="/host.js"></script>', {headers:{"content-type":"text/html"}});
    if (path === "/host.js") return new Response(bundle, {headers:{"content-type":"text/javascript"}});
    if (req.method === "POST") { posts.push(url); return Response.json({build:{id:99}}); }
    if (path.endsWith("/deployments")) return Response.json({deployments:[deployment(1,secondary),deployment(2)]});
    if (path.endsWith("/1/mobile-signing") && secondary) { requested(); await gate; return Response.json({setups:[],identities:[]}); }
    if (path.endsWith("/store-config")) return Response.json({preflight:{ready:true,errors:0}});
    const match = path.match(/\/deployments\/(\d+)$/);
    if (match) {
      const id = Number(match[1]);
      if (id === 1 && !secondary) { requested(); await gate; }
      return Response.json({deployment:deployment(id,secondary && id===1), builds:[],releases:[],current_release:null,environments:[{id:1,name:"production"},{id:2,name:"staging"}]});
    }
    return Response.json({});
  }});
  const browser = await chromium.launch({headless:true, executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE});
  try {
    const page = await browser.newPage();
    const errors: string[] = []; page.on("pageerror", e => errors.push(e.message));
    // Simulate a transport that completes despite AbortController cancellation.
    await page.addInitScript(() => { const original = window.fetch; window.fetch = (input, init) => original(input, {...init, signal:undefined}); });
    await page.goto(server.url.href);
    await page.locator("aside li").filter({hasText:"Alpha"}).click();
    await started;
    await page.locator("aside li").filter({hasText:"Bravo"}).click();
    await page.getByRole("button", {name:"Build & Release",exact:true}).waitFor();
    unblock();
    // Wait for the late callback, not just the network send.
    await page.waitForTimeout(150);
    expect(await page.locator("main header").innerText()).toContain("Bravo");
    expect(await page.locator("main header").innerText()).not.toContain("Alpha");
    await page.getByLabel("Environment", {exact:true}).selectOption("staging");
    await page.getByRole("button", {name:"Build & Release",exact:true}).waitFor();
    await page.getByRole("button", {name:"Build & Release",exact:true}).click();
    await page.waitForTimeout(100);
    expect(posts).toHaveLength(1);
    expect(posts[0].pathname).toEndWith("/deployments/2/build");
    expect(posts[0].searchParams.get("environment")).toBe("staging");
    expect(errors).toEqual([]);
  } finally { unblock(); await browser.close(); server.stop(true); }
}, 15000);
