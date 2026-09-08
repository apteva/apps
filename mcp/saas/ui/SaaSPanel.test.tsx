import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";

const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, navigator: window.navigator,
  HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
const { createRoot } = await import("react-dom/client");
const { default: SaaSPanel } = await import("./SaaSPanel");
const originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot>;
let host: HTMLElement;
let requests: URL[];
let delayed: ((url: URL) => Promise<Response> | undefined) | undefined;
const accounts = Array.from({ length: 51 }, (_, i) => ({ id: `a${i}`, slug: `account-${i}`,
  owner_email: `owner${i}@example.com`, customer_id: i + 1, plan_key: "free",
  status: i === 0 ? "past_due" : "active", created_at: "2026-09-01T00:00:00Z" }));
const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200 });
function page(url: URL) {
  const status = url.searchParams.get("status");
  const matches = accounts.filter((a) => !status || a.status === status);
  const offset = Number(url.searchParams.get("offset") || 0);
  const limit = Number(url.searchParams.get("limit") || 50);
  return { accounts: matches.slice(offset, offset + limit), total: matches.length,
    has_more: offset + limit < matches.length,
    status_counts: { active: matches.filter((a) => a.status === "active").length,
      past_due: matches.filter((a) => a.status === "past_due").length } };
}
beforeEach(() => {
  requests = []; delayed = undefined;
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  globalThis.fetch = (async (input: string | URL | Request) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname.endsWith("/plans")) return json({ plans: [{ key: "free", name: "Free", billing_mode: "free" }] });
    if (url.pathname.endsWith("/accounts")) {
      requests.push(url);
      return delayed?.(url) ?? json(page(url));
    }
    if (url.pathname.endsWith("/usage")) return json({ usage: [] });
    throw new Error(`Unexpected request ${url}`);
  }) as typeof fetch;
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove(); globalThis.fetch = originalFetch;
});
async function mount() { await act(async () => root.render(<SaaSPanel projectId="p" installId={1} />)); }
async function click(label: string) {
  const button = Array.from(host.querySelectorAll("button")).find((b) => b.textContent === label)!;
  expect(button).toBeDefined();
  await act(async () => button.click());
}
async function filterStatus(status: string) {
  const select = host.querySelector('[aria-label="Filter accounts by status"]') as HTMLSelectElement;
  await act(async () => { select.value = status; select.dispatchEvent(new window.Event("change", { bubbles: true })); });
}

test("all accounts remain reachable with accurate totals across pages", async () => {
  await mount();
  expect(host.textContent).toContain("51 accounts");
  expect(host.querySelector("nav")!.textContent).toContain("1–50 of 51");
  expect(host.querySelector("aside")!.textContent).not.toContain("owner50@example.com");
  await click("Next");
  expect(requests.at(-1)!.searchParams.get("offset")).toBe("50");
  expect(host.querySelector("aside")!.textContent).toContain("owner50@example.com");
  expect(host.querySelector("nav")!.textContent).toContain("51–51 of 51");
  // Counts cover the full filtered dataset, rather than the one-row page.
  const metrics = host.querySelector("main section")!;
  expect(metrics.textContent).toContain("50");
  await click("Previous");
  expect(requests.at(-1)!.searchParams.get("offset")).toBe("0");
  expect(host.querySelector("nav")!.textContent).toContain("1–50 of 51");
});

test("filtering resets pagination", async () => {
  await mount(); await click("Next"); await filterStatus("past_due");
  expect(requests.at(-1)!.searchParams.get("offset")).toBe("0");
  expect(host.querySelector("nav")!.textContent).toContain("1–1 of 1");
  expect(host.querySelector("aside")!.textContent).toContain("owner0@example.com");
});

test("a delayed page cannot overwrite a newer filtered view", async () => {
  await mount();
  let resolve!: (value: Response) => void;
  const pending = new Promise<Response>((r) => { resolve = r; });
  let oldURL: URL;
  delayed = (url) => { if (url.searchParams.get("offset") === "50") { oldURL = url; return pending; } };
  await click("Next");
  await filterStatus("past_due");
  await act(async () => resolve(json(page(oldURL!))));
  expect(host.querySelector("nav")!.textContent).toContain("1–1 of 1");
  expect(host.querySelector("aside")!.textContent).not.toContain("owner50@example.com");
});
