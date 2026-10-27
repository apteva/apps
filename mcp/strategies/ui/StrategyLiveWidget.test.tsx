import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, { isLive, metricOf, type Data, type Row } from "./StrategyLiveWidget";

const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
});

const row = (over: Partial<Row>): Row => ({
  strategy_id: 1,
  strategy_name: "Momentum",
  strategy_status: "active",
  portfolio_id: 10,
  portfolio_name: "Crypto Core",
  mode: "paper",
  execution_environment: "simulation",
  live_armed: false,
  assignment_status: "active",
  realized_pnl: 0,
  unrealized_pnl: 0,
  total_pnl: 0,
  execution_cost: 0,
  fees_paid: 0,
  notional_traded: 0,
  fill_count: 0,
  runs_completed: 0,
  runs_orders_submitted: 0,
  runs_failed: 0,
  shared_symbols: 0,
  ...over,
});

const fixture: Data = {
  strategies: [
    row({
      strategy_id: 1,
      strategy_name: "Momentum",
      execution_environment: "broker_live",
      mode: "live",
      live_armed: true,
      realized_pnl: 200,
      unrealized_pnl: 50,
      total_pnl: 250,
      fill_count: 3,
      shared_symbols: 1,
      positions: [
        { symbol: "ETH-USD", qty: 2, avg_cost: 50, mark: 75, market_value: 150, unrealized_pnl: 50 },
      ],
    }),
    row({
      strategy_id: 2,
      strategy_name: "Mean reversion",
      portfolio_name: "Paper desk",
      realized_pnl: -30,
      total_pnl: -30,
      runs_failed: 2,
      assignment_status: "unassigned",
      fill_count: 1,
    }),
  ],
  totals: { realized_pnl: 170, unrealized_pnl: 50, total_pnl: 220, execution_cost: 0 },
  count: 2,
};

const render = async (revision = 0) => {
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () =>
    root.render(<Widget projectId="p1" appName="trading" eventRevision={revision} widgetSettings={{ scope: "all" }} />),
  );
};

test("live money is labelled, armed is distinguished, and shared lots are disclosed", async () => {
  globalThis.fetch = (async () => Response.json(fixture)) as typeof fetch;
  await render();
  const text = document.body.textContent || "";

  expect(text).toContain("Momentum");
  expect(text).toContain("Mean reversion");
  // Real money must be called out, and an armed portfolio distinguished from
  // a disarmed one — this widget is glanceable, so the badge is the warning.
  expect(text).toContain("Live · armed");
  expect(text).toContain("Paper");
  // A strategy that shares a symbol with another book gets the convention note.
  expect(text).toContain("close against their own");
  // Failed runs surface without opening the row.
  expect(text).toContain("2 failed");
  expect(text).toContain("unassigned");
});

test("filters narrow to live or paper without refetching", async () => {
  let calls = 0;
  globalThis.fetch = (async () => {
    calls++;
    return Response.json(fixture);
  }) as typeof fetch;
  await render();
  expect(calls).toBe(1);

  const liveButton = [...document.querySelectorAll("button")].find((b) => b.textContent === "Live")!;
  await act(async () => liveButton.click());
  let text = document.body.textContent || "";
  expect(text).toContain("Momentum");
  expect(text).not.toContain("Mean reversion");

  const paperButton = [...document.querySelectorAll("button")].find((b) => b.textContent === "Paper")!;
  await act(async () => paperButton.click());
  text = document.body.textContent || "";
  expect(text).toContain("Mean reversion");
  expect(text).not.toContain("Crypto Core");
  // Filtering is client-side; the rollup is not re-fetched per tab.
  expect(calls).toBe(1);
});

test("expanding a row shows its own open lots and costs", async () => {
  globalThis.fetch = (async () => Response.json(fixture)) as typeof fetch;
  await render();
  const strategyRow = [...document.querySelectorAll("button.sl-row")][0] as HTMLElement;
  await act(async () => strategyRow.click());
  const text = document.body.textContent || "";
  expect(text).toContain("ETH-USD");
  expect(text).toContain("Realized");
  expect(text).toContain("Open");
});

test("a failed load keeps the last snapshot and offers a retry", async () => {
  let offline = false;
  globalThis.fetch = (async () =>
    offline ? new Response("offline", { status: 503 }) : Response.json(fixture)) as typeof fetch;
  await render(0);
  expect(document.body.textContent).toContain("Momentum");

  offline = true;
  await act(async () =>
    root.render(<Widget projectId="p1" appName="trading" eventRevision={1} widgetSettings={{ scope: "all" }} />),
  );
  const text = document.body.textContent || "";
  expect(text).toContain("Strategies unavailable (503)");
  // Stale numbers are still shown, but labelled as stale rather than dropped.
  expect(text).toContain("Showing the last received snapshot");
  expect(text).toContain("Momentum");
});

test("metricOf selects the configured headline and tones cost as negative", () => {
  const r = row({ realized_pnl: 10, unrealized_pnl: -4, total_pnl: 6, execution_cost: 3 });
  expect(metricOf(r, "realized_pnl").label).toBe("Realized");
  expect(metricOf(r, "unrealized_pnl").tone).toBe(-4);
  expect(metricOf(r, "total_pnl").value).toContain("6.00");
  // Cost is always a drag, so a large cost must read as bad, not as a gain.
  expect(metricOf(r, "execution_cost").tone).toBe(-3);
});

test("isLive keys off the execution environment, not the mode string", () => {
  expect(isLive(row({ execution_environment: "broker_live", mode: "paper" }))).toBe(true);
  expect(isLive(row({ execution_environment: "simulation", mode: "live" }))).toBe(false);
});
