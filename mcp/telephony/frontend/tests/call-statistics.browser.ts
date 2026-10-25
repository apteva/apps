import { test, expect, type Page } from "@playwright/test";
const totals = {
  calls: 120,
  answered: 108,
  missed: 8,
  failed: 4,
  active: 2,
  observed: 100,
  problems: 8,
  pending: 0,
  talk_seconds: 32040,
  average_seconds: 296.7,
  problem_percent: 8,
  coverage_percent: 83.3333,
  sip_discard_ms: 12540,
  sip_concealed_ms: 4200,
  browser_discard_ms: 320,
  browser_concealed_ms: 6200,
  capture_discard_ms: 120,
  ingress_discard_ms: 280,
  playback_gap_ms: 1000,
  send_discard_ms: 1000,
  sip_missing_packets: 4,
  browser_lost_packets: 28,
  send_duration_ms: 100000,
  send_rate_calls: 80,
  send_discard_percent: 1,
  metric_calls: {
    sip: 80,
    browser: 95,
    webrtc: 90,
    capture: 95,
    ingress: 90,
    send: 100,
    playback_gap: 5,
  },
};
async function fixture(page: Page) {
  const requests: URL[] = [],
    mutations: string[] = [];
  await page.addInitScript(() => {
    navigator.mediaDevices.getUserMedia = () => {
      throw new Error("Statistics must not open microphone");
    };
  });
  await page.route("**/api/**", async (route) => {
    const u = new URL(route.request().url());
    if (route.request().method() !== "GET") mutations.push(u.pathname);
    if (u.pathname.endsWith("/events"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: ": fixture\n\n",
      });
    if (!u.pathname.endsWith("/audio-health"))
      return route.fulfill({
        json: { calls: [], recordings: [], numbers: [] },
      });
    if (u.searchParams.get("view") !== "statistics")
      return route.fulfill({
        json: {
          call: {
            call_id: u.searchParams.get("call_id"),
            active: true,
            status: "in-progress",
            peer_kind: "human",
          },
          generated_at: new Date().toISOString(),
          browser: {
            received_at: new Date().toISOString(),
            rtt_ms: 45,
            media_transport: "webrtc",
          },
          carrier_history: { records: [] },
        },
      });
    requests.push(u);
    const from = u.searchParams.get("from")!,
      until = u.searchParams.get("until")!,
      span = Date.parse(until) - Date.parse(from);
    const empty = u.searchParams.get("provider") === "empty";
    const current = empty
      ? {
          ...totals,
          calls: 0,
          observed: 0,
          problems: 0,
          problem_percent: null,
          coverage_percent: null,
        }
      : totals;
    const call = {
      call_id: "stats-call",
      status: "in-progress",
      direction: "inbound",
      provider: "twilio",
      from: "+1 202 555 0101",
      to: "+1 202 555 0102",
      started_at: new Date(Date.parse(from) + span / 2).toISOString(),
      adviser: "Adviser 1",
      observed: true,
      problem: true,
      talk_seconds: 240,
    };
    return route.fulfill({
      json: {
        totals: current,
        previous: { ...totals, calls: 100, problem_percent: 12 },
        buckets: Array.from({ length: 24 }, (_, i) => ({
          ...totals,
          from: new Date(Date.parse(from) + (i * span) / 24).toISOString(),
          until: new Date(
            Date.parse(from) + ((i + 1) * span) / 24,
          ).toISOString(),
          calls: 2 + (i % 9),
          observed: 1 + (i % 9),
          problems: i % 5 === 0 ? 1 : 0,
        })),
        breakdowns: Object.fromEntries(
          ["provider", "adviser", "number"].map((key) => [
            key,
            {
              items: [
                {
                  ...totals,
                  key:
                    key === "provider"
                      ? "twilio"
                      : key === "number"
                        ? "+12025550102"
                        : "principal-1",
                  label:
                    key === "provider"
                      ? "Twilio"
                      : key === "number"
                        ? "+1 202 555 0102"
                        : "Adviser 1",
                },
              ],
              total_groups: 1,
              omitted_groups: 0,
            },
          ]),
        ),
        calls: empty
          ? []
          : [
              {
                ...call,
                call_id: u.searchParams.get("cursor")
                  ? "next-stats-call"
                  : "stats-call",
              },
            ],
        matching_calls: empty
          ? 0
          : u.searchParams.has("segment_from")
            ? 6
            : 120,
        next_cursor: u.searchParams.get("cursor") ? "" : "next",
        from,
        until,
        previous_from: new Date(Date.parse(from) - span).toISOString(),
        generated_at: new Date().toISOString(),
        timezone: "Europe/Madrid",
        pending_reports: 0,
        query_ms: 12,
      },
    });
  });
  return { requests, mutations };
}
test("Statistics tab filters, histogram drilldown, per-call inspector and pagination", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const { requests, mutations } = await fixture(page);
  await page.goto("/?tab=statistics");
  await expect(
    page.getByRole("heading", { name: "Statistics", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("8.0%", { exact: true }).first()).toBeVisible();
  await page
    .getByLabel("Call activity histogram")
    .getByRole("button")
    .nth(5)
    .click();
  await expect
    .poll(() => requests.at(-1)?.searchParams.has("segment_from"))
    .toBe(true);
  await page
    .getByRole("button", { name: /202 555 0101.*202 555 0102/ })
    .click();
  await expect(
    page.getByRole("heading", { name: "Call diagnostics", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("45 ms", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Next calls", exact: true }).click();
  await expect
    .poll(() => requests.at(-1)?.searchParams.get("cursor"))
    .toBe("next");
  await page
    .getByRole("button", { name: "Previous calls", exact: true })
    .click();
  await expect
    .poll(() => requests.at(-1)?.searchParams.has("cursor"))
    .toBe(false);
  await page.getByLabel("Calls to inspect").selectOption("problems");
  await expect
    .poll(() => requests.at(-1)?.searchParams.get("focus"))
    .toBe("problems");
  await expect(page.getByText("8.0%", { exact: true }).first()).toBeVisible();
  await page.getByRole("button", { name: "Clear chart selection" }).click();
  await page.getByLabel("Period", { exact: true }).selectOption("custom");
  await page.getByLabel("Statistics from").fill("2026-10-24T00:00");
  await page.getByLabel("Statistics until").fill("2026-10-26T00:00");
  await page.getByLabel("Audio duration threshold").selectOption("1000");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect
    .poll(() => requests.at(-1)?.searchParams.get("threshold_ms"))
    .toBe("1000");
  expect(requests.at(-1)?.searchParams.get("from")).toBe(
    "2026-10-23T22:00:00.000Z",
  );
  expect(requests.at(-1)?.searchParams.get("until")).toBe(
    "2026-10-25T23:00:00.000Z",
  );
  await expect(page.getByText("8.0%", { exact: true }).first()).toBeVisible();
  await page.screenshot({
    path: "/private/tmp/telephony-statistics-wide.png",
    fullPage: true,
  });
  expect(errors).toEqual([]);
  expect(mutations).toEqual([]);
});
test("invalid dates do not request and permissions clear results", async ({
  page,
}) => {
  const { requests } = await fixture(page);
  await page.goto("/?tab=statistics");
  await expect(page.getByText("8.0%", { exact: true }).first()).toBeVisible();
  await page.getByLabel("Period", { exact: true }).selectOption("custom");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page.getByRole("alert")).toContainText("valid date range");
  const count = requests.length;
  expect(count).toBe(1);
  await page.route("**/audio-health?**", (route) =>
    route.fulfill({ status: 403, json: {} }),
  );
  await page.getByRole("button", { name: "Refresh statistics" }).click();
  await page.getByRole("button", { name: "Reset filters" }).click();
  await expect(page.getByRole("alert")).toContainText("Operator access");
  await expect(page.getByText("8.0%", { exact: true })).toHaveCount(0);
});
test("narrow layout, theme switching and hidden-tab polling remain bounded", async ({
  page,
}) => {
  const { requests } = await fixture(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?tab=statistics");
  await expect(
    page.getByRole("heading", { name: "Statistics", exact: true }),
  ).toBeVisible();
  await expect.poll(() => requests.length).toBe(1);
  await expect(page.getByText("8.0%", { exact: true }).first()).toBeVisible();
  for (const mode of ["dark", "light"]) {
    await page.evaluate(
      (mode) => (document.documentElement.dataset.mode = mode),
      mode,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: `/private/tmp/telephony-statistics-${mode}.png`,
      fullPage: true,
    });
  }
  await page.clock.install();
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => true,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.fastForward(65000);
  expect(requests.length).toBe(1);
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", {
      configurable: true,
      get: () => false,
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect.poll(() => requests.length).toBe(2);
});

test("empty and unobserved periods never claim perfect audio", async ({ page }) => {
 await fixture(page);
 await page.route("**/audio-health?**", async route => {
  const empty = new URL(route.request().url()).searchParams.get("direction") === "outbound";
  const unknown = {...totals, calls: empty ? 0 : 3, answered:0, active:0, observed:0, problems:0, talk_seconds:0, average_seconds:null, problem_percent:null, coverage_percent:empty ? null : 0, send_discard_percent:null, metric_calls:{}};
  await route.fulfill({json:{
    totals:unknown, previous:unknown, buckets:[],
    breakdowns:Object.fromEntries(["provider","adviser","number"].map(k=>[k,{items:[],total_groups:0,omitted_groups:0}])),
    calls:[], matching_calls:0, next_cursor:"", from:"2026-10-25T00:00:00Z",until:"2026-10-26T00:00:00Z",generated_at:"2026-10-26T00:00:00Z", timezone:"Europe/Madrid",pending_reports:0
  }});
 });
 await page.goto("/?tab=statistics");
 await expect(page.getByText("3 calls without telemetry",{exact:true})).toBeVisible();
 const rate = page.getByText("Calls with audio problems",{exact:true}).locator("..");
 await expect(rate).toContainText("—");
 await expect(page.getByText("SIP receive buffer discard",{exact:true}).locator("..")).toContainText("—");
 await page.getByLabel("Call direction").selectOption("outbound");
 await page.getByRole("button",{name:"Apply filters"}).click();
 await expect(page.getByText("No calls started in this period.",{exact:true})).toBeVisible();
 await expect(rate).toContainText("—");
});
