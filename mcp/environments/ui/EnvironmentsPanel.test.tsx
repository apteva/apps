import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";

const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, navigator: window.navigator, HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
const { createRoot } = await import("react-dom/client");
const { default: Panel } = await import("./EnvironmentsPanel");
let root: ReturnType<typeof createRoot>;
const originalFetch = globalThis.fetch;
afterEach(async () => { if (root) await act(async () => root.unmount()); document.body.innerHTML = ""; globalThis.fetch = originalFetch; });
const catalog = { apps: [], connections: [], integrations: [], managed_mcps: [], agents: [], snapshots: [], web_fixtures: [], protocol_fixtures: [], realtime_providers: [] };
const spec = { app_install_ids: [], connection_ids: [], integration_bindings: [], network_mode: "block", integration_mode: "mock" };
const json = (value: any) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
const summaries = (items: any[], next_cursor: string | null = null) => ({ items, next_cursor, has_more: !!next_cursor });
const running = (id: string, name: string) => ({ id, name, desired_state: "running", active_run: { id: `run-${id}`, runtime_id: `rt-${id}`, status: "running" } });
function deferred() { let resolve!: (value: Response) => void; const promise = new Promise<Response>(r => resolve = r); return { promise, resolve }; }
async function mount(fetch: typeof globalThis.fetch) {
 globalThis.fetch = fetch;
 const host = document.createElement("div"); document.body.appendChild(host); root = createRoot(host);
 await act(async () => { root.render(<Panel appName="environments" installId={1} projectId="p" />); });
}
async function click(label: string) {
 const button = Array.from(document.querySelectorAll("button")).find(b => b.textContent?.startsWith(label));
 expect(button).toBeTruthy();
 await act(async () => button!.click());
}
test("only the selected environment loads detail; stopped selection clears runtime data", async () => {
 let catalogRequests = 0;
 const details: string[] = [];
 const a = running("a", "Alpha"); const b = { id: "b", name: "Beta", desired_state: "stopped" };
 await mount((async (input: any) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) { catalogRequests++; return json(catalog); }
  if (path.endsWith("/environments")) return json(summaries([a,b]));
  if (path.endsWith("/environments/a")) { details.push("a"); return json({ ...a, spec }); }
  if (path.endsWith("/environments/b")) { details.push("b"); return json({ ...b, spec }); }
  if (path.endsWith("/inspect")) return json({ runtime: { apps: [{ name: "Alpha-private-app" }], agents: [] } });
  throw new Error(path);
 }) as any);
 expect(details).toEqual([]);
 await click("Alpha"); await click("Alpha"); await click("Apps");
 expect(document.body.textContent).toContain("Alpha-private-app");
 await click("Beta"); await click("Apps");
 expect(document.body.textContent).not.toContain("Alpha-private-app");
 expect(document.body.textContent).toContain("No apps");
 expect(details).toEqual(["a","b"]);
 expect(catalogRequests).toBe(1);
});
test("a delayed detail response cannot overwrite the newly selected environment", async () => {
 const old = deferred();
 await mount((async (input: any) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) return json(catalog);
  if (path.endsWith("/environments")) return json(summaries([running("a","Alpha"),running("b","Beta")]));
  if (path.endsWith("/environments/a")) return old.promise;
  if (path.endsWith("/environments/b")) return json({ ...running("b","Beta"), spec });
  if (path.includes("run-b/inspect")) return json({ runtime: { apps: [{ name: "Beta-app" }], agents: [] } });
  throw new Error(path);
 }) as any);
 await click("Alpha"); await click("Beta"); await click("Apps");
 await act(async () => old.resolve(json({ ...running("a","Alpha"), spec })));
 expect(document.body.textContent).toContain("Beta-app");
 expect(document.body.textContent).not.toContain("Stale-alpha-app");
});
test("degraded reconciliation is visible and offers an explicit retry", async () => {
 let retried = false;
 const environment = { id: "env", name: "Broken", desired_state: "running", reconcile_status: "degraded", reconcile_failures: 5, reconcile_error: "seed failed" };
 await mount((async (input: any, init?: RequestInit) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) return json(catalog);
  if (path.endsWith("/environments/env/start") && init?.method === "POST") { retried = true; return json({}); }
  if (path.endsWith("/environments")) return json(summaries([environment]));
  if (path.endsWith("/environments/env")) return json({ ...environment, spec });
  throw new Error(path);
 }) as any);
 expect(document.body.textContent).toContain("degraded");
 await click("Broken");
 expect(document.body.textContent).toContain("Automatic reconciliation stopped after 5 failures.");
 expect(document.body.textContent).toContain("seed failed");
 await click("Retry");
 expect(retried).toBe(true);
});
test("pagination replaces the current summary page without loading details", async () => {
 const paths: string[] = [];
 await mount((async (input: any) => {
  const url = new URL(String(input), window.location.origin); paths.push(url.pathname + url.search);
  if (url.pathname.endsWith("/catalog")) return json(catalog);
  if (url.pathname.endsWith("/environments")) return json(url.searchParams.get("cursor") ? summaries([{ id:"b",name:"Beta" }]) : summaries([{id:"a",name:"Alpha"}],"opaque"));
  throw new Error(url.pathname);
 }) as any);
 expect(document.body.textContent).toContain("Alpha");
 await click("Next page");
 expect(document.body.textContent).toContain("Beta");
 expect(document.body.textContent).not.toContain("Alpha");
 expect(paths.some(path => path.includes("cursor=opaque"))).toBe(true);
 await click("Previous page");
 expect(document.body.textContent).toContain("Alpha");
 expect(paths.some(path => path.includes("/api/environments/"))).toBe(false);
});
test("a delayed inspection cannot overwrite the newly selected environment", async () => {
 const old = deferred();
 await mount((async (input: any) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) return json(catalog);
  if (path.endsWith("/environments")) return json(summaries([running("a","Alpha"),running("b","Beta")]));
  if (path.endsWith("/environments/a")) return json({ ...running("a","Alpha"), spec });
  if (path.endsWith("/environments/b")) return json({ ...running("b","Beta"), spec });
  if (path.includes("run-a/inspect")) return old.promise;
  if (path.includes("run-b/inspect")) return json({ runtime: { apps: [{ name: "Beta-app" }], agents: [] } });
  throw new Error(path);
 }) as any);
 await click("Alpha"); await click("Beta"); await click("Apps");
 await act(async () => old.resolve(json({ runtime: { apps: [{ name: "Stale-alpha-app" }] } })));
 expect(document.body.textContent).toContain("Beta-app");
 expect(document.body.textContent).not.toContain("Stale-alpha-app");
});
