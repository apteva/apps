import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import SeoPanel from "./SeoPanel";

const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window, document: window.document, navigator: window.navigator,
  HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true,
});
const originalFetch = globalThis.fetch;
let root: Root;
let container: HTMLElement;
let requests: { tool: string; args: any; signal?: AbortSignal | null }[];
let delayed: ((tool: string, args: any) => Promise<any> | undefined) | undefined;
const domain = (id: number) => ({ id, host: `site${id}.test`, project_id: "p", created_at: "2026-01-01" });

beforeEach(() => {
  requests = [];
  delayed = undefined;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const path = new URL(String(input), "http://localhost").pathname;
    const { tool = path.split("/").pop(), args = {} } = JSON.parse(String(init?.body || "{}"));
    requests.push({ tool, args: { ...Object.fromEntries(new URL(String(input), "http://localhost").searchParams), ...args }, signal: init?.signal });
    const pending = delayed?.(tool, args);
    // Deliberately ignore abort in the mock to exercise post-response guards.
    const value = pending ? await pending : ({
      providers: { providers: ["dataforseo", "yepapi"], default: "dataforseo" },
      locations: { locations: [] }, domains_list: [domain(1), domain(2)],
      keywords_list: [{ id: 1, text: "seo", location_id: 1, search_engine: "google" }],
      entities_list: [], content_opportunities: { items: [] },
      "keyword-metric-jobs": { jobs: [] }, settings: { enabled: true, monthly_budget_usd: 5, daily_depth: 20, weekly_depth: 100 },
      domains_get: { domain: domain(args.id), metrics: { organic_traffic: args.id * 100 } },
      rankings_for_domain: [], backlink_movement: null, backlinks_list: [],
      keywords_get: { metrics: null }, rankings_for_keyword: [], "rank-tracking": { trackers: [] },
    } as Record<string, any>)[tool] ?? [];
    return new Response(JSON.stringify(value), { status: 200 });
  }) as typeof fetch;
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  globalThis.fetch = originalFetch;
});
async function mount() {
  await act(async () => { root.render(<SeoPanel appName="seo" installId={1} projectId="p" />); });
}
async function click(text: string) {
  const button = Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.trim() === text);
  expect(button).toBeDefined();
  await act(async () => { button!.click(); });
}

test("overview skips hidden detail fetches and loads scope data once", async () => {
  await mount();
  for (const tool of ["providers", "locations", "domains_list", "keyword-metric-jobs", "settings"]) {
    expect(requests.filter((r) => r.tool === tool)).toHaveLength(1);
  }
  expect(requests.some((r) => ["domains_get", "rankings_for_domain", "backlink_movement", "keywords_get", "rank-tracking"].includes(r.tool))).toBe(false);
  await click("Domains");
  expect(requests.filter((r) => r.tool === "domains_get")).toHaveLength(1);
  expect(container.textContent).toContain("100");
});

test("late domain response cannot overwrite the new selection", async () => {
  let resolveOld!: (value: any) => void;
  const old = new Promise((resolve) => { resolveOld = resolve; });
  delayed = (tool, args) => tool === "domains_get" && args.id === 1 ? old : undefined;
  await mount();
  await click("Domains");
  // Domain rows include their host plus descriptive text.
  const second = Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.includes("site2.test"));
  expect(second).toBeDefined();
  await act(async () => second!.click());
  expect(container.textContent).toContain("200");
  await act(async () => resolveOld({ metrics: { organic_traffic: 987654321 } }));
  expect(container.textContent).not.toContain("987,654,321");
  expect(container.textContent).toContain("200");
  expect(requests.find((r) => r.tool === "domains_get" && r.args.id === 1)?.signal?.aborted).toBe(true);
});

test("page SERP reads split more than 200 keyword IDs into valid batches", async () => {
  delayed = (tool) => tool === "rankings_for_domain"
    ? Promise.resolve(Array.from({ length: 201 }, (_, i) => ({
      id: i + 1, keyword_id: i + 1, rank_url: "https://site1.test/page", rank: 1, ts: 100,
    }))) : undefined;
  await mount();
  await click("Domains");
  const batches = requests.filter((r) => r.tool === "rankings_for_keywords");
  expect(batches.map((r) => r.args.keyword_ids.length)).toEqual([200, 1]);
});

test("changing engines does not reload provider settings or domain lists", async () => {
  await mount();
  await click("Keywords");
  const before = requests.length;
  await click("YouTube");
  const extra = requests.slice(before);
  expect(extra.some((r) => r.tool === "keywords_list" && r.args.search_engine === "youtube")).toBe(true);
  expect(extra.some((r) => ["providers", "locations", "domains_list", "settings"].includes(r.tool))).toBe(false);
});


test("domain refresh preserves its configured locale outside the loaded catalog", async () => {
  delayed = (tool) => {
    if (tool === "domains_list") return Promise.resolve([{ ...domain(1), default_location_id: 42 }]);
    if (tool === "locations") return Promise.resolve({ locations: [{
      id: 99, provider: "dataforseo", search_engine: "google", location_name: "Different country", language_code: "en",
    }] });
    return undefined;
  };
  await mount();
  await click("Domains");
  await click("Refresh metrics");
  expect(requests.find((r) => r.tool === "refresh")?.args.location_id).toBe("42");
});
