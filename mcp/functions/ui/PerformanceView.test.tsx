import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
import { PerformanceView, timingText } from "./PerformanceView";
import FunctionsPanel from "./FunctionsPanel";

const initialWindow = new Window();
Object.assign(globalThis, { window: initialWindow, document: initialWindow.document });
const { createRoot } = await import("react-dom/client");
let browser: Window;
let root: Root;
let container: HTMLElement;
let requests: { path: string; filters: Record<string, string> }[];
let selected: number[];

const functions = [{ id: 1, name: "busy" }, { id: 2, name: "slow" }];
const row = { function_id: 1, function_name: "busy", runtime: "node", calls: 100, completed: 99, running: 1, errors: 2, canceled: 1, error_rate: 2 / 99, calls_per_minute: 1.66, avg_duration_ms: 140, p95_duration_ms: 2300, max_duration_ms: 5000, avg_execution_ms: 105, p95_execution_ms: 1900, avg_queue_ms: 20, total_execution_ms: 10500 };
const report = { since: "2026-10-02T10:00:00Z", until: "2026-10-02T11:00:00Z", functions: [row], function_count: 1, has_more: false, totals: { calls: 100, completed: 99, running: 1, errors: 2, canceled: 1, total_execution_ms: 10500 } };
const call = { id: 77, function_id: 1, function_name: "busy", started_at: "2026-10-02T10:10:00Z", status: "ok", duration_ms: 2300, execution_ms: 1900, queue_ms: 200 };
const page = { since: report.since, until: report.until, invocations: [call], next_cursor: "page-2" };
type Api = <T,>(method: string, path: string, body?: unknown, extra?: Record<string, string>) => Promise<T>;
let api: Api;
const originalFetch = globalThis.fetch;

beforeEach(() => {
  browser = new Window();
  Object.assign(globalThis, { window: browser, document: browser.document, HTMLElement: browser.HTMLElement, Event: browser.Event, IS_REACT_ACT_ENVIRONMENT: true });
  container = browser.document.createElement("div") as unknown as HTMLElement;
  browser.document.body.appendChild(container as any);
  root = createRoot(container);
  requests = [];
  selected = [];
  api = async <T,>(_method: string, path: string, _body?: unknown, filters: Record<string, string> = {}) => {
    requests.push({ path, filters });
    return (path === "/performance" ? report : filters.cursor ? { ...page, invocations: [{ ...call, id: 76 }], next_cursor: "" } : page) as T;
  };
});

afterEach(async () => {
  await act(async () => root.unmount());
  globalThis.fetch = originalFetch;
  await browser.happyDOM.close();
});

async function render() {
  await act(async () => root.render(<PerformanceView api={api} functions={functions} onSelect={id => selected.push(id)} renderInvocation={id => <p>Invocation {id} timing and logs</p>} />));
}

async function click(label: string) {
  const button = [...container.querySelectorAll("button")].find(b => b.getAttribute("aria-label") === label || b.textContent === label);
  expect(button).toBeDefined();
  await act(async () => button!.click());
}

async function change(label: string, value: string) {
  const select = container.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!;
  await act(async () => { select.value = value; select.dispatchEvent(new browser.Event("change", { bubbles: true }) as any); });
}

test("ranking clearly shows timings and changes backend sort and period", async () => {
  await render();
  expect(container.textContent).toContain("1.90 s");
  expect(container.textContent).toContain("2.30 s");
  expect(requests[0]).toEqual({ path: "/performance", filters: { window: "24h", limit: "100", sort: "total_execution_ms" } });
  await change("Rank functions by", "calls");
  expect(requests.at(-1)?.filters.sort).toBe("calls");
  expect(container.querySelector('th[aria-sort="descending"]')?.textContent).toContain("Calls");
  await click("p95 execution");
  expect(requests.at(-1)?.filters.sort).toBe("p95_execution_ms");
  await change("Performance period", "7d");
  expect(requests.at(-1)?.filters.window).toBe("7d");
  await click("busy");
  expect(selected).toEqual([1]);
});

test("slow-call drilldown, filters and pagination preserve scope", async () => {
  await render();
  await click("Slow calls for busy");
  expect(requests.at(-1)?.path).toBe("/invocations/slow");
  expect(requests.at(-1)?.filters.id).toBe("1");
  await click("Inspect invocation 77");
  expect(container.textContent).toContain("Invocation 77 timing and logs");
  await click("Load more slow calls");
  expect(requests.at(-1)?.filters.cursor).toBe("page-2");
  expect(requests.at(-1)?.filters.id).toBe("1");
  expect(container.textContent).toContain("#76");
  await change("Sort slow calls by", "queue_ms");
  await change("Minimum slow-call time", "1000");
  await change("Slow-call status", "timeout");
  expect(requests.at(-1)?.filters).toEqual({ window: "24h", limit: "100", id: "1", sort: "queue_ms", min_ms: "1000", status: "timeout" });
  expect(container.textContent).not.toContain("Invocation 77 timing and logs");
  const before = requests.length;
  await click("Refresh performance");
  expect(requests.length).toBe(before + 1);
  expect(requests.at(-1)?.filters.cursor).toBeUndefined();
});

test("old requests cannot overwrite a newly selected ranking", async () => {
  let resolveOld!: (value: unknown) => void;
  api = async <T,>(_method: string, _path: string, _body?: unknown, filters: Record<string, string> = {}) => {
    if (filters.sort === "total_execution_ms") return await new Promise<unknown>(resolve => { resolveOld = resolve; }) as T;
    return { ...report, functions: [{ ...row, function_name: "new-ranking" }] } as T;
  };
  await render();
  expect(container.textContent).toContain("Loading performance");
  await change("Rank functions by", "calls");
  expect(container.textContent).toContain("new-ranking");
  await act(async () => resolveOld(report));
  expect(container.textContent).toContain("new-ranking");
  expect(container.querySelector('table[aria-label="Function performance ranking"]')?.textContent).not.toContain("busy");
});

test("empty results and failures provide useful recovery controls", async () => {
  api = async <T,>() => ({ ...report, functions: [], totals: { ...report.totals, calls: 0 } }) as T;
  await render();
  expect(container.textContent).toContain("No recorded calls in this period");
  api = async () => { throw new Error("Report unavailable"); };
  await render();
  expect(container.querySelector('[role="alert"]')?.textContent).toContain("Report unavailable");
  expect(container.querySelector('table[aria-label="Function performance ranking"]')).toBeNull();
  api = async <T,>(_method: string, path: string) => (path === "/performance" ? report : { ...page, invocations: [], next_cursor: "" }) as T;
  await render();
  await click("Slow calls");
  expect(container.textContent).toContain("No completed calls match these filters");
});

test("timings distinguish missing measurements from measured zero", () => {
  expect(timingText(null)).toBe("—");
  expect(timingText(0)).toBe("0 ms");
  expect(timingText(1250)).toBe("1.25 s");
  expect(timingText(120000)).toBe("2.0 min");
});

test("management status filters do not hide functions from performance diagnostics", async () => {
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  globalThis.fetch = (async (input: any) => {
    const url = String(input);
    const rows = functions.map((f, i) => ({ ...f, runtime: "node", status: i === 0 ? "active" : "disabled", timeout_ms: 30000, max_memory_mb: 256, runtime_readiness: { state: "ready" } }));
    const value = url.includes("/performance") ? report : url.includes("/functions?") ? { functions: rows, next_cursor: "" } : {};
    return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  await act(async () => root.render(<FunctionsPanel projectId="p" installId={1} appName="functions" />));
  const select = container.querySelector<HTMLSelectElement>("header select")!;
  await act(async () => { select.value = "disabled"; select.dispatchEvent(new browser.Event("change", { bubbles: true }) as any); });
  const management = [...container.querySelectorAll("table")].at(-1)!;
  expect(management.textContent).toContain("slow");
  expect(management.textContent).not.toContain("busy");
  const performanceOptions = container.querySelector('select[aria-label="Performance function"]')!;
  expect(performanceOptions.textContent).toContain("busy");
  expect(performanceOptions.textContent).toContain("slow");
});
