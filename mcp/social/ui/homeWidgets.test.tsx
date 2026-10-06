import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import PublishingCalendar from "./SocialPublishingCalendarWidget";
import Performance from "./SocialPerformanceWidget";
import { publishingWindow, socialPageLink, trendPath, widgetPreferences, widgetURL, type PerformanceSummary } from "./homeWidgetData";
import { socialNavigation } from "./socialNavigation";
import SocialPanel from "./SocialPanel";

const browser = new Window({ url: "http://localhost:3000" });
const originalFetch = globalThis.fetch;
let root: ReturnType<typeof createRoot> | undefined;
function globals() {
  Object.assign(globalThis, { window: browser, document: browser.document, navigator: browser.navigator, localStorage: browser.localStorage, HTMLElement: browser.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
}
afterEach(async () => {
  globals();
  if (root) await act(async () => root!.unmount());
  root = undefined;
  browser.document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
  browser.history.replaceState(null, "", "/");
  browser.localStorage.clear();
  await browser.happyDOM.abort();
});
async function render(component: React.ReactNode) {
  globals();
  if (!root) { const host = browser.document.createElement("div"); browser.document.body.append(host); root = createRoot(host); }
  await act(async () => root!.render(component));
}
const json = (value: unknown) => new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });

test("Home defaults, settings and links retain the selected install and scope", () => {
  expect(widgetPreferences({}).mode).toBe("upcoming");
  expect(widgetPreferences({ widgetSize: "full" }).mode).toBe("calendar");
  expect(widgetPreferences({ widgetSize: "full", widgetSettings: { view: "upcoming", days: "7", max_posts: 999 } })).toMatchObject({ mode: "upcoming", days: 7, maxPosts: 20 });
  const props = { projectId: "brand & studio", installId: 42, widgetSettings: { profile_id: 3, account_ids: "5,7" } };
  const url = new URL(widgetURL(props, "/widgets/performance", { days: 28 }), "http://localhost");
  expect(url.searchParams.get("project_id")).toBe(props.projectId);
  expect(url.searchParams.get("install_id")).toBe("42");
  expect(url.searchParams.get("account_ids")).toBe("5,7");
  const navigation = socialNavigation(new URL(socialPageLink(props, { post: 12, view: "calendar" }), "http://localhost").search);
  expect(navigation).toMatchObject({ profileID: 3, postID: 12, calendar: true, accountIDs: [5, 7] });
  expect(socialNavigation("?profile_id=0&compose=1&tab=metrics&range=90d")).toMatchObject({ profileID: null, compose: true, tab: "metrics", range: "90d" });
});

test("calendar bounds preserve local days and charts leave missing-data gaps", () => {
  const now = new Date(2026, 8, 30, 10);
  const week = publishingWindow(now, "calendar", 7, now);
  expect(week.days.length).toBe(7);
  expect(week.start.getDay()).toBe(1);
  expect(publishingWindow(now, "upcoming", 7, now).start).toBe(now);
  const path = trendPath([{ date: "1", value: 10, accounts: 1 }, { date: "2", value: null, accounts: 0 }, { date: "3", value: 20, accounts: 1 }]);
  expect(path.match(/M/g)?.length).toBe(2);
  expect(path).not.toContain("L");
});

test("publishing widget loads a private summary and changes to a full week", async () => {
  const requests: URL[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push(new URL(String(input), "http://localhost"));
    expect(init?.credentials).toBe("same-origin");
    return json({ posts: [], total: 0, attention: { failed: 2, approval: 1, overdue: 0 } });
  }) as typeof fetch;
  await render(<PublishingCalendar projectId="brand" installId={8} />);
  expect(browser.document.body.textContent).toContain("Social · Publishing calendar");
  expect(browser.document.body.textContent).toContain("2 failed or partial");
  expect(browser.document.body.textContent).toContain("No posts scheduled in this window");
  expect(requests[0].pathname).toBe("/api/apps/social/widgets/publishing-calendar");
  expect(requests[0].searchParams.get("mode")).toBe("upcoming");
  await render(<PublishingCalendar projectId="brand" installId={8} widgetSize="full" />);
  expect(requests.at(-1)?.searchParams.get("mode")).toBe("calendar");
  expect(browser.document.querySelector('button[aria-label="Next week"]')).toBeTruthy();
  expect(browser.document.body.textContent?.match(/No posts/g)?.length).toBe(7);
  const create = [...browser.document.querySelectorAll("a")].find(link => link.textContent?.includes("Create post"));
  expect(new URL(create!.href).pathname).toBe("/apps/social/page");
  expect(new URL(create!.href).searchParams.get("compose")).toBe("1");
});

test("performance shows coverage, unavailable metrics, and account rows on full width", async () => {
  const metric = { value: 0, accounts: 1, trend: [] };
  const missing = { value: null, accounts: 0, trend: [] };
  const response: PerformanceSummary = { days: 28, start_date: "2026-09-02", end_date: "2026-09-29", updated_at: "2026-09-30T08:00:00Z",
    accounts: [{ id: 7, name: "Studio", platform: "youtube", updated_at: "2026-09-30T08:00:00Z", metrics: { followers: metric, views: missing, interactions: missing } }],
    metrics: { followers: metric, views: missing, interactions: missing } };
  const requests: string[] = [];
  globalThis.fetch = (async input => { requests.push(String(input)); return json(response); }) as typeof fetch;
  await render(<Performance projectId="brand" widgetSize="full" />);
  expect(browser.document.body.textContent).toContain("Social · Performance");
  expect(browser.document.body.textContent).toContain("Unavailable");
  expect(browser.document.body.textContent).toContain("1/1 accounts");
  expect(browser.document.body.textContent).not.toContain("NaN");
  expect(browser.document.querySelector("table")?.textContent).toContain("Studio");
  expect(requests.every(path => path.includes("/widgets/performance"))).toBe(true);
  await render(<Performance projectId="brand" widgetSize="half" eventRevision={1} />);
  expect(requests.length).toBe(2);
  expect(browser.document.querySelector("table")).toBeNull();
});

test("switching projects hides prior data and rejects a late response", async () => {
  let finishOld: (response: Response) => void = () => {};
  globalThis.fetch = (async input => new URL(String(input), "http://localhost").searchParams.get("project_id") === "old" ? await new Promise<Response>(resolve => { finishOld = resolve; }) : json({ accounts: [], metrics: {}, days: 28 })) as typeof fetch;
  await render(<Performance projectId="old" />);
  await render(<Performance projectId="new" />);
  expect(browser.document.body.textContent).toContain("No connected accounts");
  await act(async () => finishOld(json({ accounts: [{ id: 1, name: "Old private account" }], metrics: {} })));
  expect(browser.document.body.textContent).not.toContain("Old private account");
  expect(browser.document.body.textContent).toContain("No connected accounts");
});

test("missing project and preview mode never perform a network request", async () => {
  globalThis.fetch = (async () => { throw new Error("Unexpected request"); }) as typeof fetch;
  await render(<Performance />);
  expect(browser.document.body.textContent).toContain("Select a project");
  await render(<Performance preview widgetSize="full" />);
  expect(browser.document.body.textContent).toContain("Studio");
});

test("calendar links open the weekly view and override a stored profile", async () => {
  browser.history.replaceState(null, "", "/apps/social/page?view=calendar&profile_id=0&account_ids=7");
  browser.localStorage.setItem("social.activeProfile.brand", "2");
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  const requests: URL[] = [];
  globalThis.fetch = (async input => {
    const url = new URL(String(input), browser.location.origin); requests.push(url);
    if (url.pathname.endsWith("/profiles")) return json({ profiles: [{ id: 2, name: "Stored profile" }] });
    if (url.pathname.endsWith("/accounts")) return json({ accounts: [{ id: 7, platform: "youtube", display_name: "Studio", status: "active" }] });
    return json({ posts: [], platforms: [], items: [] });
  }) as typeof fetch;
  await render(<SocialPanel appName="social" installId={42} projectId="brand" />);
  expect(browser.document.querySelector('select[aria-label="Filter posts by account"]')?.getAttribute("value") || (browser.document.querySelector('select[aria-label="Filter posts by account"]') as HTMLSelectElement)?.value).toBe("selected");
  const calendarRequest = requests.find(url => url.pathname.endsWith("/posts") && url.searchParams.has("from"));
  expect(calendarRequest).toBeTruthy();
  expect(calendarRequest!.searchParams.has("profile_id")).toBe(false);
  const duration = Date.parse(calendarRequest!.searchParams.get("to")!) - Date.parse(calendarRequest!.searchParams.get("from")!);
  expect(duration).toBe(7 * 86400000);
});

test("post links load details even when the post is outside the recent list", async () => {
  browser.history.replaceState(null, "", "/apps/social/page?post=123&profile_id=0");
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  globalThis.fetch = (async input => {
    const path = new URL(String(input), browser.location.origin).pathname;
    if (path.endsWith("/posts/123")) return json({ post: { id: 123, body: "Deep linked post", targets: [], media_storage_ids: [], status: "draft", created_at: "2026-09-30T10:00:00Z", revision: 1, approval_status: "not_requested", requested_mode: "draft", source: "local" } });
    return json({ posts: [], profiles: [], accounts: [], platforms: [], items: [] });
  }) as typeof fetch;
  await render(<SocialPanel appName="social" installId={42} projectId="brand" />);
  expect(browser.document.body.textContent).toContain("Deep linked post");
});

test("attention links load older failures and override the saved calendar view", async () => {
  browser.history.replaceState(null, "", "/apps/social/page?status=attention&profile_id=0");
  browser.localStorage.setItem("social.posts.view", "calendar");
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  globalThis.fetch = (async input => {
    const url = new URL(String(input), browser.location.origin);
    if (url.pathname.endsWith("/posts") && url.searchParams.get("status") === "failed") return json({ posts: [{ id: 1, body: "Older failed delivery", targets: [], media_storage_ids: [], status: "failed", created_at: "2026-01-01T10:00:00Z", revision: 1, source: "local" }] });
    return json({ posts: [], profiles: [], accounts: [], platforms: [], items: [] });
  }) as typeof fetch;
  await render(<SocialPanel appName="social" installId={42} projectId="brand" />);
  expect(browser.document.body.textContent).toContain("Older failed delivery");
  expect((browser.document.querySelector('select[aria-label="Filter posts by status"]') as HTMLSelectElement).value).toBe("attention");
});
