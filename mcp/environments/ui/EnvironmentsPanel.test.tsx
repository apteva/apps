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
test("switching to a stopped environment clears previous runtime data without refetching the catalog", async () => {
 let catalogRequests = 0;
 await mount((async (input: any) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) { catalogRequests++; return json(catalog); }
  if (path.endsWith("/environments")) return json([
   { id: "a", name: "Alpha", desired_state: "running", spec, active_run: { id: "run-a", runtime_id: "rt-a", status: "running" } },
   { id: "b", name: "Beta", desired_state: "stopped", spec }
  ]);
  if (path.endsWith("/inspect")) return json({ runtime: { apps: [{ name: "Alpha-private-app" }], agents: [] } });
  throw new Error(path);
 }) as any);
 await click("Alpha"); await click("Apps");
 expect(document.body.textContent).toContain("Alpha-private-app");
 await click("Beta"); await click("Apps");
 expect(document.body.textContent).not.toContain("Alpha-private-app");
 expect(document.body.textContent).toContain("No apps");
 expect(catalogRequests).toBe(1);
});
test("a delayed inspection cannot overwrite the newly selected environment", async () => {
 const old = deferred();
 await mount((async (input: any) => {
  const path = new URL(String(input), window.location.origin).pathname;
  if (path.endsWith("/catalog")) return json(catalog);
  if (path.endsWith("/environments")) return json(["a", "b"].map(id => ({ id, name: id === "a" ? "Alpha" : "Beta", desired_state: "running", spec, active_run: { id: `run-${id}`, runtime_id: `rt-${id}`, status: "running" } })));
  if (path.includes("run-a/inspect")) return old.promise;
  if (path.includes("run-b/inspect")) return json({ runtime: { apps: [{ name: "Beta-app" }], agents: [] } });
  throw new Error(path);
 }) as any);
 await click("Alpha"); await click("Beta"); await click("Apps");
 await act(async () => old.resolve(json({ runtime: { apps: [{ name: "Stale-alpha-app" }] } })));
 expect(document.body.textContent).toContain("Beta-app");
 expect(document.body.textContent).not.toContain("Stale-alpha-app");
});
