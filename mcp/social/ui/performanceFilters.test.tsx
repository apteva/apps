import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import Performance from "./SocialPerformanceWidget";
import {
  performanceDateError,
  performanceDefaults,
  performanceURL,
  sortPerformanceAccounts,
  type PerformanceOptions,
} from "./performanceFilters";
import type { PerformanceSummary } from "./homeWidgetData";

const browser = new Window({ url: "http://localhost:5280" });
const originalFetch = globalThis.fetch;
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
async function render(props: Record<string, unknown>) {
  globals();
  if (!root) {
    const host = browser.document.createElement("div");
    browser.document.body.append(host);
    root = createRoot(host);
  }
  await act(async () => root!.render(<Performance {...props} />));
}
async function select(label: string, value: string) {
  const element = browser.document.querySelector(
    `select[aria-label="${label}"]`,
  ) as HTMLSelectElement;
  expect(element).toBeTruthy();
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new browser.Event("change", { bubbles: true }));
  });
}
const options: PerformanceOptions = {
  demo_data: true,
  profiles: [1, 2, 3, 4].map((id) => ({ id, name: `Demo · Profile ${id}` })),
  platforms: ["facebook", "instagram", "tiktok", "youtube"],
  accounts: [],
};
for (let profile = 1; profile <= 4; profile++)
  for (const platform of options.platforms)
    options.accounts.push({
      id: options.accounts.length + 1,
      name: `Brand ${profile} ${platform}`,
      platform,
      profile_id: profile,
      profile_name: `Demo · Profile ${profile}`,
    });
function summary(accounts = options.accounts): PerformanceSummary {
  const metric = (value: number) => ({
    value,
    accounts: 1,
    days: 28,
    trend: [],
    change_percent: 12.5,
  });
  return {
    days: 28,
    start_date: "2026-10-17",
    end_date: "2026-11-13",
    updated_at: "2026-11-14T08:00:00Z",
    demo_data: true,
    accounts: accounts.map((account) => ({
      ...account,
      updated_at: "",
      metrics: {
        followers: metric(account.id * 100),
        views: metric(account.id * 200),
        impressions: metric(account.id * 400),
        interactions: metric(account.id * 20),
      },
    })),
    metrics: {
      followers: metric(13600),
      views: metric(27200),
      impressions: metric(54400),
      interactions: metric(2720),
    },
  };
}
function mockResponses(requests: URL[]) {
  globalThis.fetch = (async (input) => {
    const url = new URL(String(input), browser.location.origin);
    requests.push(url);
    if (url.pathname.endsWith("/options")) return Response.json(options);
    let accounts = options.accounts;
    if (url.searchParams.get("profile_id"))
      accounts = accounts.filter(
        (a) => a.profile_id === Number(url.searchParams.get("profile_id")),
      );
    if (url.searchParams.get("platforms"))
      accounts = accounts.filter(
        (a) => a.platform === url.searchParams.get("platforms"),
      );
    if (url.searchParams.get("account_ids"))
      accounts = accounts.filter((a) =>
        url.searchParams.get("account_ids")!.split(",").includes(String(a.id)),
      );
    return Response.json(summary(accounts));
  }) as typeof fetch;
}

test("performance shows all sixteen demo accounts and filters by named profile and platform", async () => {
  const requests: URL[] = [];
  mockResponses(requests);
  await render({
    projectId: "brand",
    installId: 34,
    widgetSize: "half",
    widgetSettings: { demo_data: true },
  });
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(16);
  expect(browser.document.body.textContent).toContain(
    "4 profiles, 16 synthetic accounts",
  );
  expect(
    browser.document.querySelectorAll('select[aria-label="Profile"] option'),
  ).toHaveLength(5);
  await select("Profile", "2");
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(4);
  await select("Platform", "instagram");
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(1);
  expect(browser.document.querySelector("tbody")?.textContent).toContain(
    "Brand 2 instagram",
  );
  const latest = requests
    .filter((url) => !url.pathname.endsWith("/options"))
    .at(-1)!;
  expect(latest.searchParams.get("profile_id")).toBe("2");
  expect(latest.searchParams.get("platforms")).toBe("instagram");
  expect(
    requests.every(
      (url) =>
        url.searchParams.get("project_id") === "brand" &&
        url.searchParams.get("install_id") === "34",
    ),
  ).toBe(true);
  expect(browser.document.querySelector("tbody a")).toBeNull();
});

test("account selection narrows rows and changing profile clears the incompatible selection", async () => {
  const requests: URL[] = [];
  mockResponses(requests);
  await render({ projectId: "brand", widgetSettings: { demo_data: true } });
  const check = browser.document.querySelector(
    'input[aria-label="Include Brand 1 facebook"]',
  ) as HTMLInputElement;
  await act(async () => check.click());
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(1);
  await select("Profile", "3");
  expect(browser.document.querySelectorAll("tbody tr")).toHaveLength(4);
  const latest = requests
    .filter((url) => !url.pathname.endsWith("/options"))
    .at(-1)!;
  expect(latest.searchParams.has("account_ids")).toBe(false);
});

test("date presets and custom UTC dates reach the summary endpoint without ambiguous days", async () => {
  const requests: URL[] = [];
  mockResponses(requests);
  await render({
    projectId: "brand",
    widgetSettings: {
      demo_data: true,
      start_date: "2026-11-01",
      end_date: "2026-11-10",
    },
  });
  let latest = requests
    .filter((url) => !url.pathname.endsWith("/options"))
    .at(-1)!;
  expect(latest.searchParams.get("start_date")).toBe("2026-11-01");
  expect(latest.searchParams.get("end_date")).toBe("2026-11-10");
  expect(latest.searchParams.has("days")).toBe(false);
  await select("Date range", "7");
  latest = requests.filter((url) => !url.pathname.endsWith("/options")).at(-1)!;
  expect(latest.searchParams.get("days")).toBe("7");
  expect(latest.searchParams.has("start_date")).toBe(false);
});

test("invalid custom dates show a validation error without loading an unfiltered summary", async () => {
  const requests: URL[] = [];
  mockResponses(requests);
  await render({
    projectId: "brand",
    widgetSettings: { start_date: "2026-11-10", end_date: "2026-11-01" },
  });
  expect(browser.document.body.textContent).toContain(
    "Choose a range of 1–366 days",
  );
  expect(requests.every((url) => url.pathname.endsWith("/options"))).toBe(true);
  expect(browser.document.querySelector("table")).toBeNull();
});

test("turning demo mode off discards synthetic filters and hides the old sample rows", async () => {
  const requests: URL[] = [];
  mockResponses(requests);
  await render({
    projectId: "brand",
    widgetSettings: { demo_data: true, profile_id: 2, account_ids: "5" },
  });
  globalThis.fetch = (async (input) => {
    const url = new URL(String(input), browser.location.origin);
    requests.push(url);
    return Response.json(
      url.pathname.endsWith("/options")
        ? { profiles: [], accounts: [], platforms: [] }
        : { ...summary([]), demo_data: false },
    );
  }) as typeof fetch;
  const toggle = browser.document.querySelector(
    'input[aria-label="Demo data"]',
  ) as HTMLInputElement;
  await act(async () => toggle.click());
  expect(browser.document.body.textContent).not.toContain("Brand 2");
  expect(browser.document.body.textContent).not.toContain(
    "16 synthetic accounts",
  );
  const latest = requests
    .filter((url) => !url.pathname.endsWith("/options"))
    .at(-1)!;
  expect(latest.searchParams.get("demo_data")).toBe("false");
  expect(latest.searchParams.has("profile_id")).toBe(false);
  expect(latest.searchParams.has("account_ids")).toBe(false);
});

test("account metrics sort numerically with missing values last and preserve real zero", () => {
  expect(
    performanceDefaults({ preview: true, widgetSettings: { demo_data: true } })
      .demo,
  ).toBe(false);
  const accounts = summary(options.accounts.slice(0, 3)).accounts;
  accounts[0].metrics.views.value = null;
  accounts[1].metrics.views.value = 0;
  accounts[2].metrics.views.value = 20;
  expect(
    sortPerformanceAccounts(accounts, "views", false, "views").map((a) => a.id),
  ).toEqual([2, 3, 1]);
  expect(
    sortPerformanceAccounts(accounts, "views", true, "views").map((a) => a.id),
  ).toEqual([3, 2, 1]);
  const defaults = performanceDefaults({
    widgetSettings: { days: 7, platform: "facebook" },
  });
  expect(performanceURL({ projectId: "p", installId: 34 }, defaults)).toContain(
    "platforms=facebook",
  );
  expect(
    performanceDateError(
      {
        ...defaults,
        period: "custom",
        startDate: "2026-11-01",
        endDate: "2026-11-14",
      },
      new Date("2026-11-14T12:00:00Z"),
    ),
  ).toContain("completed UTC days");
});
