import { afterEach, expect, test } from "bun:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { Window } from "happy-dom";
import Performance from "./SocialPerformanceWidget";
import { graphPath, performanceGraphSeries } from "./performanceGraphData";
import type { PerformanceSummary, WidgetMetric } from "./homeWidgetData";

const browser = new Window({ url: "http://localhost:5280" }),
  originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;
function globals() {
  Object.assign(globalThis, {
    window: browser,
    document: browser.document,
    navigator: browser.navigator,
    HTMLElement: browser.HTMLElement,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
}
afterEach(async () => {
  globals();
  if (root) await act(async () => root!.unmount());
  root = undefined;
  browser.document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
  await browser.happyDOM.abort();
});
const metric = (values: (number | null)[]): WidgetMetric => ({
  value: values.at(-1) ?? null,
  accounts: 1,
  trend: values.map((value, i) => ({
    date: `2026-11-0${i + 1}`,
    value,
    accounts: value === null ? 0 : 1,
  })),
});
function fixture(): PerformanceSummary {
  const accounts = Array.from({ length: 16 }, (_, i) => ({
    id: i + 1,
    name: `Account ${i + 1}`,
    platform: i % 2 ? "instagram" : "youtube",
    profile_id: i < 8 ? 1 : 2,
    updated_at: "",
    metrics: {
      followers: metric([100 + i, 110 + i, 120 + i]),
      views: metric([i, 10 + i, 20 + i]),
      impressions: metric([i * 2, 20 + i, 40 + i]),
      interactions: metric([0, 1, 2]),
    },
  }));
  return {
    accounts,
    metrics: {
      followers: metric([1720, 1880, 2040]),
      views: metric([120, 280, 440]),
      impressions: metric([240, 440, 760]),
      interactions: metric([0, 16, 32]),
    },
    days: 3,
    start_date: "2026-11-01",
    end_date: "2026-11-03",
    updated_at: "",
  };
}
async function render(
  settings: Record<string, unknown> = {},
  project = "test",
) {
  globals();
  if (!root) {
    const host = browser.document.createElement("div");
    browser.document.body.append(host);
    root = createRoot(host);
  }
  await act(async () =>
    root!.render(
      <Performance
        installId={34}
        projectId={project}
        widgetSettings={{ show_trends: true, ...settings }}
      />,
    ),
  );
}
async function select(label: string, value: string) {
  const e = browser.document.querySelector(
    `select[aria-label="${label}"]`,
  ) as HTMLSelectElement;
  await act(async () => {
    e.value = value;
    e.dispatchEvent(new browser.Event("change", { bubbles: true }));
  });
}
function mock(data = fixture()) {
  globalThis.fetch = (async (input) => {
    const url = new URL(String(input), browser.location.origin);
    if (url.pathname.endsWith("/options"))
      return Response.json({
        profiles: [
          { id: 1, name: "First" },
          { id: 2, name: "Second" },
        ],
        platforms: ["youtube", "instagram"],
        accounts: data.accounts,
      });
    const accountIDs = url.searchParams
      .get("account_ids")
      ?.split(",")
      .map(Number);
    const accounts = data.accounts.filter(
      (a) =>
        (!url.searchParams.get("profile_id") ||
          a.profile_id === Number(url.searchParams.get("profile_id"))) &&
        (!url.searchParams.get("platforms") ||
          a.platform === url.searchParams.get("platforms")) &&
        (!accountIDs || accountIDs.includes(a.id)),
    );
    return Response.json({ ...data, accounts });
  }) as typeof fetch;
}

test("graph modes use one shared scale, stable account colors, gaps and real zeros", () => {
  const data = fixture();
  data.accounts[0].metrics.views = metric([0, null, 20]);
  const by = performanceGraphSeries(data, "views", "by_account", 20);
  expect(by).toHaveLength(16);
  const first = by.find((s) => s.id === "1")!;
  expect(first.points.map((p) => p.value)).toEqual([0, null, 20]);
  expect(graphPath(first.points, 100, 50, 40)).toBe(
    "M0.00,50.00  M100.00,25.00",
  );
  expect(
    performanceGraphSeries(
      { ...data, accounts: [data.accounts[0]] },
      "views",
      "by_account",
      20,
    )[0].color,
  ).toBe(first.color);
  expect(
    performanceGraphSeries(data, "views", "by_account", 3).map((s) => s.id),
  ).toEqual(["16", "15", "14"]);
  expect(
    performanceGraphSeries(data, "views", "combined", 3)[0].points[2].value,
  ).toBe(440);
});

test("sixteen account lines, legend toggling, metric switching and keyboard hover show exact values", async () => {
  mock();
  await render({
    graph_mode: "by_account",
    visibility_metric: "followers",
    max_accounts: 2,
  });
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(
    16,
  );
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(2);
  expect(
    browser.document.querySelectorAll('[aria-label="Graph legend"] button'),
  ).toHaveLength(16);
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("Account 16: 135");
  const plot = browser.document.querySelector(
    '[aria-label="Followers history; use left and right arrows to choose a date"]',
  )!;
  await act(async () =>
    plot.dispatchEvent(
      new browser.KeyboardEvent("keydown", { key: "Home", bubbles: true }),
    ),
  );
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("2026-11-01");
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("Account 16: 115");
  const legend = browser.document.querySelector(
    '[aria-label="Graph legend"] button',
  ) as HTMLButtonElement;
  await act(async () => legend.click());
  expect(legend.getAttribute("aria-pressed")).toBe("false");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(
    15,
  );
  await select("Graph metric", "views");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(
    16,
  );
  await select("Graph mode", "combined");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(1);
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("Combined: 440");
});

test("graphs follow profile/platform/account/date scopes and project changes reset chart state", async () => {
  mock();
  await render({
    graph_mode: "by_account",
    visibility_metric: "followers",
    start_date: "2026-11-01",
    end_date: "2026-11-03",
  });
  await select("Profile", "1");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(8);
  await select("Platform", "youtube");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(4);
  const choose = browser.document.querySelector(
    '[aria-label="Select accounts"]',
  ) as HTMLButtonElement;
  await act(async () => choose.click());
  const one = browser.document.querySelector(
    '[aria-label="Include Account 1"]',
  ) as HTMLInputElement;
  await act(async () => one.click());
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(1);
  await select("Graph metric", "views");
  await select("Profile", "2");
  expect(
    (
      browser.document.querySelector(
        '[aria-label="Graph metric"]',
      ) as HTMLSelectElement
    ).value,
  ).toBe("views");
  await render(
    { graph_mode: "combined", visibility_metric: "followers" },
    "other",
  );
  expect(
    (
      browser.document.querySelector(
        '[aria-label="Graph mode"]',
      ) as HTMLSelectElement
    ).value,
  ).toBe("combined");
  expect(
    (
      browser.document.querySelector(
        '[aria-label="Graph metric"]',
      ) as HTMLSelectElement
    ).value,
  ).toBe("followers");
});

test("unknown values remain unavailable and plotted gaps are not joined", async () => {
  const data = fixture();
  for (const a of data.accounts) a.metrics.followers = metric([10, null, 0]);
  mock(data);
  await render({
    graph_mode: "by_account",
    visibility_metric: "followers",
    graph_max_accounts: 3,
  });
  const svg = browser.document.querySelector(
    'svg[aria-label="Followers by account history"]',
  )!;
  Object.defineProperty(svg, "getBoundingClientRect", {
    value: () => ({ left: 0, width: 600 }),
  });
  await act(async () =>
    svg.dispatchEvent(
      new browser.PointerEvent("pointermove", { clientX: 316, bubbles: true }),
    ),
  );
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("2026-11-02");
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("Unavailable");
  expect(browser.document.querySelectorAll("[data-series-id]")).toHaveLength(3);
  const plot = svg.parentElement!;
  await act(async () =>
    plot.dispatchEvent(
      new browser.KeyboardEvent("keydown", { key: "End", bubbles: true }),
    ),
  );
  expect(
    browser.document.querySelector('[aria-label="Graph hover values"]')
      ?.textContent,
  ).toContain("Account 1: 0");
});
