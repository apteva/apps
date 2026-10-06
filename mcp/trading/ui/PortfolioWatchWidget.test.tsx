import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, { isLive, pnlOf, sortPortfolios, type Portfolio } from "./PortfolioWatchWidget";

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

const portfolio = (over: Partial<Portfolio>): Portfolio => ({
  id: 1,
  name: "Paper desk",
  status: "active",
  mode: "paper",
  execution_environment: "simulation",
  live_armed: false,
  equity: 10000,
  cash: 5000,
  buying_power: 5000,
  day_pnl: 0,
  day_pnl_pct: 0,
  open_pnl: 0,
  open_pnl_pct: 0,
  realized_pnl: 0,
  total_pnl: 0,
  total_pnl_pct: 0,
  ...over,
});

const fixture = {
  portfolios: [
    portfolio({ id: 1, name: "Paper desk", equity: 90000, day_pnl: 120, day_pnl_pct: 1.2 }),
    portfolio({
      id: 2,
      name: "Live crypto",
      mode: "live",
      execution_environment: "broker_live",
      live_armed: true,
      broker_slug: "binance-trading",
      equity: 2000,
      cash: 400,
      day_pnl: -45.5,
      day_pnl_pct: -2.2,
      total_pnl: 300,
      total_pnl_pct: 17.6,
    }),
  ],
};

const render = async (settings: Record<string, unknown> = {}, revision = 0) => {
  const el = document.createElement("div");
  document.body.append(el);
  root = createRoot(el);
  await act(async () =>
    root.render(
      <Widget projectId="p1" appName="trading" eventRevision={revision} widgetSettings={settings} />,
    ),
  );
};

test("live desks sort above bigger paper desks and carry an armed badge", async () => {
  globalThis.fetch = (async () => Response.json(fixture)) as typeof fetch;
  await render();
  const names = [...document.querySelectorAll(".pw-name")].map((n) => n.textContent);
  // The live desk holds far less equity but must still lead: it is the one
  // that can lose real money unattended.
  expect(names[0]).toBe("Live crypto");
  expect(names[1]).toBe("Paper desk");

  const text = document.body.textContent || "";
  expect(text).toContain("Live · armed");
  expect(text).toContain("binance");
  expect(text).toContain("Armed live");
  // The live row is tinted, not merely labelled.
  expect(document.querySelector(".pw-row.live")).not.toBeNull();
});

test("a disarmed broker portfolio reads differently from an armed one", async () => {
  globalThis.fetch = (async () =>
    Response.json({
      portfolios: [
        portfolio({
          id: 3,
          name: "Live equities",
          mode: "live",
          execution_environment: "broker_live",
          live_armed: false,
          broker_slug: "alpaca-trading",
        }),
      ],
    })) as typeof fetch;
  await render();
  const text = document.body.textContent || "";
  expect(text).toContain("Live · disarmed");
  expect(text).not.toContain("Live · armed");
  // Nothing is armed, so the armed counter stays out of the header.
  expect(text).not.toContain("Armed live");
});

test("the P&L basis setting switches which number is shown", async () => {
  globalThis.fetch = (async () => Response.json(fixture)) as typeof fetch;
  await render({ pnl_basis: "total" });
  const text = document.body.textContent || "";
  expect(text).toContain("Total basis");
  expect(text).toContain("17.60%");
  expect(text).not.toContain("−2.20%");
});

test("filters can hide paper or live desks", async () => {
  globalThis.fetch = (async () => Response.json(fixture)) as typeof fetch;
  await render({ show_paper: false });
  const text = document.body.textContent || "";
  expect(text).toContain("Live crypto");
  expect(text).not.toContain("Paper desk");
});

test("a failed load keeps the last snapshot and offers a retry", async () => {
  let offline = false;
  globalThis.fetch = (async () =>
    offline ? new Response("offline", { status: 500 }) : Response.json(fixture)) as typeof fetch;
  await render({}, 0);
  expect(document.body.textContent).toContain("Live crypto");

  offline = true;
  await act(async () =>
    root.render(<Widget projectId="p1" appName="trading" eventRevision={1} widgetSettings={{}} />),
  );
  const text = document.body.textContent || "";
  expect(text).toContain("Portfolios unavailable (500)");
  expect(text).toContain("Showing the last received snapshot");
  expect(text).toContain("Live crypto");
});

test("sortPortfolios keeps live first under every sort key", () => {
  const rows = [
    portfolio({ id: 1, name: "Aaa paper", equity: 1e6, day_pnl_pct: 99, total_pnl_pct: 99 }),
    portfolio({
      id: 2,
      name: "Zzz live",
      execution_environment: "broker_live",
      equity: 1,
      day_pnl_pct: -99,
      total_pnl_pct: -99,
    }),
  ];
  for (const key of ["equity", "name", "day_pnl_pct", "total_pnl_pct"]) {
    expect(sortPortfolios(rows, key, "day")[0].name).toBe("Zzz live");
  }
});

test("pnlOf and isLive read the intended fields", () => {
  const p = portfolio({
    day_pnl: 1,
    day_pnl_pct: 0.1,
    open_pnl: 2,
    open_pnl_pct: 0.2,
    total_pnl: 3,
    total_pnl_pct: 0.3,
  });
  expect(pnlOf(p, "day").value).toBe(1);
  expect(pnlOf(p, "open").value).toBe(2);
  expect(pnlOf(p, "total").pct).toBe(0.3);
  expect(pnlOf(p, "nonsense").label).toBe("Day");
  expect(isLive(portfolio({ execution_environment: "broker_live" }))).toBe(true);
});
