import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import FinancePanel from "./FinancePanel";

const originalFetch = globalThis.fetch;
let browser: Window;
let root: Root;
let container: HTMLElement;
let onEvent: (event: { install_id: number; topic: string }) => void;
let resolveSync: (response: Response) => void;
let resolveHistory: (response: Response) => void;
let calls: string[];
let historyRequests: number;

const json = (value: unknown) => Response.json(value);

beforeEach(() => {
  browser = new Window();
  Object.assign(globalThis, {
    window: browser,
    document: browser.document,
    HTMLElement: browser.HTMLElement,
    Event: browser.Event,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  (browser as any).__aptevaAppEvents = {
    subscribe: (_app: string, _project: string, callback: typeof onEvent) => {
      onEvent = callback;
      return () => {};
    },
  };
  container = browser.document.createElement("div") as unknown as HTMLElement;
  browser.document.body.appendChild(container as any);
  root = createRoot(container);
  calls = [];
  historyRequests = 0;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const path = new URL(String(input), "http://local.test").pathname;
    calls.push(path);
    if (path.endsWith("/brokerage/sync")) return new Promise<Response>(resolve => { resolveSync = resolve; });
    if (path.endsWith("/reports/net-worth")) {
      historyRequests++;
      if (historyRequests > 1) return new Promise<Response>(resolve => { resolveHistory = resolve; });
      return json({ series: "weekly", from: "2026-01-01", to: "2026-01-08", points: [
        { as_of: "2026-01-01", total: 10000 }, { as_of: "2026-01-08", total: 11000 },
      ] });
    }
    if (path.endsWith("/settings")) return json({ base_currency: "EUR" });
    if (path.endsWith("/accounts")) return json({ accounts: [] });
    if (path.endsWith("/holdings")) return json({ holdings: [] });
    if (path.endsWith("/txns")) return json({ transactions: [] });
    if (path.endsWith("/reports/allocation")) return json({ total: 11000, by_account_kind: [], top_instruments: [] });
    if (path.endsWith("/budgets/status")) return json({ budgets: [], period_start: "", period_end: "" });
    if (path.endsWith("/categories")) return json({ categories: [] });
    throw new Error(`Unexpected request: ${path}`);
  }) as typeof fetch;
});

afterEach(async () => {
  await act(async () => root.unmount());
  globalThis.fetch = originalFetch;
  await browser.happyDOM.close();
});

test("broker import keeps the current snapshot and refreshes once when it finishes", async () => {
  await act(async () => root.render(<FinancePanel appName="finance" projectId="p" installId={17} />));
  expect(container.textContent).toContain("€110.00");
  expect(container.textContent).toContain("View history values");
  const initialCalls = calls.length;

  await act(async () => {
    [...container.querySelectorAll("button")].find(button => button.textContent?.includes("Sync broker"))!.click();
  });
  for (let i = 0; i < 20; i++) onEvent({ install_id: 17, topic: "txn.created" });
  expect(calls.length).toBe(initialCalls + 1); // Only the sync request.
  expect(container.textContent).toContain("Showing the last loaded values");
  expect(container.textContent).toContain("€110.00");

  await act(async () => resolveSync(json({ orders: 20 })));
  expect(calls.length).toBe(initialCalls + 9); // Seven summary reads and one history read.
  expect(historyRequests).toBe(2);
  expect(container.textContent).not.toContain("Loading history");
  expect(container.textContent).toContain("Updating history");
  expect(container.textContent).toContain("View history values");
  await act(async () => resolveHistory(json({ series: "weekly", from: "2026-01-01", to: "2026-01-08", points: [
    { as_of: "2026-01-01", total: 10000 }, { as_of: "2026-01-08", total: 11000 },
  ] })));
});
