import { afterEach, expect, mock, test } from "bun:test";
import React from "react";
import { act, create } from "react-test-renderer";
const Container = ({ children, ...props }: any) => <div {...props}>{children}</div>;
mock.module("@apteva/ui-kit", () => ({ Card: Container, AppCardHeader: Container, CardHeader: Container, StatusDot: Container, StatusPill: Container, DataList: Container }));
const { default: BrowserViewCard } = await import("./BrowserViewCard");
const { scopeComputerURL, sessionURL, panelURL } = await import("./shared");
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
const originalFetch = globalThis.fetch, originalWindow = (globalThis as any).window;
let root: ReturnType<typeof create> | undefined;
let timers: Map<number, () => Promise<void>>;
let requests: { url: URL; init?: RequestInit }[];
let status: string;
function setup() {
  status = "active"; requests = []; timers = new Map(); let seq = 0;
  (globalThis as any).window = { location: { origin: "https://host.example" }, setInterval: (fn: () => Promise<void>) => { timers.set(++seq, fn); return seq; }, clearInterval: (id: number) => timers.delete(id) };
  globalThis.fetch = (async (input: any, init?: RequestInit) => {
    const url = new URL(String(input), window.location.origin); requests.push({ url, init });
    return url.pathname.endsWith("presentation")
      ? Response.json({ session: { session_id: "br_one", current_url: "https://example.com", backend: "local", status, width: 1600, height: 800 }, screenshot_url: "/api/apps/computer/sessions/br_one/screenshot?install_id=999" })
      : Response.json({ kind: "iframe", url: "/api/apps/computer/sessions/br_one/live?install_id=999", status });
  }) as typeof fetch;
}
afterEach(async () => { if (root) await act(async () => root!.unmount()); root = undefined; globalThis.fetch = originalFetch; (globalThis as any).window = originalWindow; });
const base = { session_id: "br_one", projectId: "p1", installId: 55 };
const viewport = () => root!.root.find(n => n.props["data-browser-viewport"] === true);
const iframe = () => root!.root.findByType("iframe");
async function poll() { await act(async () => { await Promise.all([...timers.values()].map(fn => fn())); }); }

test("trusted host identity scopes requests, returned media and the Open link", async () => {
  setup();
  await act(async () => { root = create(<BrowserViewCard {...base} />); });
  expect(requests.map(r => r.url.pathname.split("/").at(-1))).toEqual(["presentation", "stream"]);
  for (const { url, init } of requests) { expect(url.searchParams.get("install_id")).toBe("55"); expect(url.searchParams.get("project_id")).toBe("p1"); expect(init?.credentials).toBe("include"); }
  const live = new URL(iframe().props.src, window.location.origin);
  expect(live.searchParams.get("install_id")).toBe("55");
  const header = root!.root.find(n => n.props.action?.label === "Open");
  const link = new URL(header.props.action.href, window.location.origin);
  expect(link.pathname).toBe("/apps/computer/"); expect(link.searchParams.get("install_id")).toBe("55"); expect(link.searchParams.get("project_id")).toBe("p1");
  expect(viewport().props.style.height).toBe(360);
});

test("routing preserves external provider URLs and scopes aliases without leaking identity", () => {
  setup();
  const external = "https://browserbase.example/live?token=opaque#frame";
  expect(scopeComputerURL(external, "p1", 55)).toBe(external);
  expect(scopeComputerURL("data:image/png;base64,abc", "p1", 55)).toBe("data:image/png;base64,abc");
  expect(sessionURL("br/a", "stream", "p1", 55, "computer-alias")).toBe("/api/apps/computer-alias/sessions/br%2Fa/stream?project_id=p1&install_id=55");
  expect(panelURL("br/a", "p1", 55, "computer-alias")).toBe("/apps/computer-alias/?instance=br%2Fa&project_id=p1&install_id=55");
});

test("placement and resize preserve the iframe, aspect ratio and polling loop", async () => {
  setup(); await act(async () => { root = create(<BrowserViewCard {...base} />); });
  const frame = iframe(), signal = requests[0].init!.signal;
  await act(async () => root!.update(<BrowserViewCard {...base} presentation={{ mode: "side-panel", width: 402, height: 602, active: true }} />));
  expect(iframe()).toBe(frame); expect(iframe().props.style.width).toBe(400); expect(iframe().props.style.height).toBe(200);
  expect(viewport().props.style.height).toBe(600); expect(requests).toHaveLength(2); expect(signal?.aborted).toBe(false);
  await act(async () => root!.update(<BrowserViewCard {...base} presentation={{ mode: "floating", width: 1002, height: 302, active: true }} />));
  expect(iframe()).toBe(frame); expect(iframe().props.style.width).toBe(600); expect(iframe().props.style.height).toBe(300);
  await act(async () => root!.update(<BrowserViewCard {...base} presentation={{ mode: "floating", width: -5, height: NaN, active: true }} />));
  expect(Number.isFinite(iframe().props.style.height)).toBe(true); expect(iframe().props.style.width).toBe(0);
});

test("hidden views pause polling without closing, then resume to the saved final frame", async () => {
  setup(); await act(async () => { root = create(<BrowserViewCard {...base} />); });
  const frame = iframe(), signal = requests[0].init!.signal;
  await act(async () => root!.update(<BrowserViewCard {...base} presentation={{ mode: "side-panel", width: 400, height: 600, active: false }} />));
  expect(iframe()).toBe(frame); expect(signal?.aborted).toBe(true); expect(timers.size).toBe(0);
  await poll(); expect(requests).toHaveLength(2);
  status = "closed";
  await act(async () => root!.update(<BrowserViewCard {...base} presentation={{ mode: "floating", width: 400, height: 600, active: true }} />));
  expect(root!.root.findAllByType("iframe")).toHaveLength(0); expect(timers.size).toBe(0);
  const image = root!.root.findByType("img");
  expect(new URL(image.props.src, window.location.origin).searchParams.get("install_id")).toBe("55");
  const finalURL = image.props.src;
  await poll(); expect(image.props.src).toBe(finalURL);
  await act(async () => root!.unmount()); root = undefined;
  expect(requests.every(r => !r.init?.method || r.init.method === "GET")).toBe(true);
});

test("hidden initial views do not poll and old install responses cannot paint a new view", async () => {
  setup(); const fetchFixture = globalThis.fetch;
  let release!: (r: Response) => void;
  globalThis.fetch = (async (input: any, init?: RequestInit) => {
    const url = new URL(String(input), window.location.origin);
    if (url.searchParams.get("install_id") === "55" && url.pathname.endsWith("presentation")) return new Promise(r => { release = r; });
    return fetchFixture(input, init);
  }) as typeof fetch;
  await act(async () => { root = create(<BrowserViewCard {...base} presentation={{ mode: "floating", width: 400, height: 600, active: false }} />); });
  expect(requests).toHaveLength(0);
  await act(async () => root!.update(<BrowserViewCard {...base} />));
  await act(async () => root!.update(<BrowserViewCard {...base} installId={56} />));
  expect(new URL(iframe().props.src, window.location.origin).searchParams.get("install_id")).toBe("56");
  await act(async () => release(Response.json({ session: { status: "closed", current_url: "https://old.example" }, screenshot_url: "https://old.example/private.png" })));
  expect(root!.root.findAllByType("img")).toHaveLength(0);
  expect(new URL(iframe().props.src, window.location.origin).searchParams.get("install_id")).toBe("56");
});
