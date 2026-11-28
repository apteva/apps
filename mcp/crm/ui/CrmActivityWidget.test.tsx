import { afterAll, afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import CrmActivityWidget from "./CrmActivityWidget";
import { crmActivityAPIURL, crmActivityContactsLink, crmActivityDelta, crmActivityPreferences, type ActivityStats } from "./activity";

const previousGlobals = {
  window: globalThis.window, document: globalThis.document,
  HTMLElement: globalThis.HTMLElement, fetch: globalThis.fetch,
  IS_REACT_ACT_ENVIRONMENT: (globalThis as any).IS_REACT_ACT_ENVIRONMENT,
};
const browser = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window: browser, document: browser.document, HTMLElement: browser.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root | undefined;
let requests: URL[] = [];
afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  requests = [];
  document.body.innerHTML = "";
});
afterAll(() => Object.assign(globalThis, previousGlobals));

const zero = { new_contacts: 0, messages_received: 0, messages_sent: 0, send_failures: 0, conversations_opened: 0, conversations_closed: 0 };
function stats(over: Partial<ActivityStats> = {}): ActivityStats {
  return {
    period_days: 7, since: "2026-11-22T00:00:00Z", until: "2026-11-29T00:00:00Z", hide_automated: true,
    totals: { ...zero, new_contacts: 4, messages_received: 12, messages_sent: 5, send_failures: 2, conversations_opened: 3, conversations_closed: 1 },
    previous: { ...zero, new_contacts: 2, messages_received: 12, messages_sent: 10 },
    open_now: 9,
    daily: Array.from({ length: 7 }, (_, i) => ({ date: `2026-11-${22 + i}`, new_contacts: i === 6 ? 3 : 0, messages_received: i, messages_sent: 0 })),
    latest_contacts: [],
    ...over,
  };
}

function serve(body: unknown, status = 200) {
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    requests.push(new URL(String(input), "http://localhost"));
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
}

async function render(props: Parameters<typeof CrmActivityWidget>[0]) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host as unknown as Element);
  await act(async () => root!.render(<CrmActivityWidget {...props} />));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  return host;
}

const tile = (host: Element, key: string) => host.querySelector(`[data-stat="${key}"]`)?.textContent || "";

test("preferences default to 7 days, automated hidden, received chart", () => {
  expect(crmActivityPreferences({})).toEqual({ period: 7, hideAutomated: true, metric: "messages_received" });
  expect(crmActivityPreferences({ period: "30", hide_automated: false, default_metric: "new_contacts" })).toEqual({ period: 30, hideAutomated: false, metric: "new_contacts" });
  expect(crmActivityPreferences({ period: 14, default_metric: "bogus" })).toEqual({ period: 7, hideAutomated: true, metric: "messages_received" });
});

test("API URL follows the dashboard scope", () => {
  const prefs = crmActivityPreferences({ period: "90", hide_automated: false });
  const all = new URL(crmActivityAPIURL({ installId: 5, dashboardScope: "global" }, prefs), "http://x");
  expect(all.pathname).toBe("/api/apps/crm/stats");
  expect(all.searchParams.get("scope")).toBe("global");
  expect(all.searchParams.get("period")).toBe("90");
  expect(all.searchParams.has("hide_automated")).toBe(false);
  expect(all.searchParams.has("project_id")).toBe(false);
  expect(new URL(crmActivityAPIURL({ dashboardScope: "global" }, prefs, "proj-b"), "http://x").searchParams.get("project_id")).toBe("proj-b");
  const project = new URL(crmActivityAPIURL({ projectId: "proj-a" }, crmActivityPreferences({}), "proj-b"), "http://x");
  expect(project.searchParams.has("scope")).toBe(false);
  expect(project.searchParams.get("project_id")).toBe("proj-a");
  expect(project.searchParams.get("hide_automated")).toBe("1");
});

test("deltas compare with the previous period", () => {
  expect(crmActivityDelta(4, 2)).toEqual({ label: "+100% vs prev.", direction: "up" });
  expect(crmActivityDelta(5, 10)).toEqual({ label: "-50% vs prev.", direction: "down" });
  expect(crmActivityDelta(3, 0)).toEqual({ label: "+3 vs prev.", direction: "up" });
  expect(crmActivityDelta(0, 0).direction).toBe("flat");
  expect(crmActivityDelta(12, 12).label).toBe("Same as before");
});

test("contacts link needs a project in global scope", () => {
  expect(crmActivityContactsLink({ dashboardScope: "global" })).toBeNull();
  expect(crmActivityContactsLink({ dashboardScope: "global" }, "proj-b")).toContain("project_id=proj-b");
  expect(crmActivityContactsLink({ projectId: "proj-a", installId: 5 })).toBe("/apps/crm/page?tab=contacts&project_id=proj-a&install_id=5");
});

test("project scope renders tiles, failures, chart and contacts", async () => {
  serve({ stats: stats({ latest_contacts: [{ id: 7, name: "Ana Lopez", email: "ana@x.test", source: "import", created_at: new Date().toISOString() }] }) });
  const host = await render({ appName: "crm", installId: 5, projectId: "proj-a" });
  expect(requests[0].searchParams.get("project_id")).toBe("proj-a");
  expect(host.querySelector("select")).toBeNull();
  expect(tile(host, "new_contacts")).toContain("4");
  expect(tile(host, "new_contacts")).toContain("+100% vs prev.");
  expect(tile(host, "messages_sent")).toContain("2 failed");
  expect(tile(host, "messages_received")).toContain("Same as before");
  expect(tile(host, "open_now")).toContain("9");
  expect(host.querySelectorAll("[data-day]").length).toBe(7);
  expect(host.textContent).toContain("21 received");
  expect(host.textContent).toContain("Ana Lopez");
  expect(host.textContent).toContain("ana@x.test · import");
  expect([...host.querySelectorAll("a")].some(a => (a.getAttribute("href") || "").includes("tab=contacts"))).toBe(true);
});

test("metric switch redraws the chart without refetching", async () => {
  serve({ stats: stats() });
  const host = await render({ appName: "crm", projectId: "proj-a" });
  const before = requests.length;
  const tab = [...host.querySelectorAll("button[role=tab]")].find(b => b.textContent === "New contacts") as unknown as HTMLButtonElement;
  await act(async () => tab.click());
  expect(host.textContent).toContain("3 new contacts");
  expect(tab.getAttribute("aria-selected")).toBe("true");
  expect(requests.length).toBe(before);
});

test("global scope adds every project, filters and shows colour dots", async () => {
  serve({
    stats: stats({ latest_contacts: [
      { id: 1, name: "Ana", created_at: "2026-11-28T09:00:00Z", project_id: "proj-a", project_name: "Alpha" },
      { id: 2, name: "Ben", created_at: "2026-11-27T09:00:00Z", project_id: "proj-b", project_name: "Beta" },
    ] }),
    projects: [{ id: "proj-a", name: "Alpha" }, { id: "proj-b", name: "Beta" }],
  });
  const host = await render({
    appName: "crm", installId: 5, dashboardScope: "global",
    widgetContext: { projects: [{ id: "proj-a", name: "Alpha", color: "#10b981" }] },
  });
  expect(requests[0].searchParams.get("scope")).toBe("global");
  expect(host.textContent).toContain("across projects");
  expect(host.textContent).not.toContain("Contacts →");
  const alpha = host.querySelector('span[title="Alpha"]');
  expect(alpha?.getAttribute("data-project-color")).toBe("#10b981");
  expect(host.querySelector('span[title="Beta"]')?.getAttribute("data-project-color")).toBeNull();

  const select = host.querySelector("select") as unknown as HTMLSelectElement;
  expect([...select.options].map(o => o.textContent)).toEqual(["All projects", "Alpha", "Beta"]);
  await act(async () => {
    select.value = "proj-b";
    select.dispatchEvent(new browser.Event("change", { bubbles: true }) as unknown as Event);
  });
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  expect(requests[requests.length - 1].searchParams.get("project_id")).toBe("proj-b");
  expect(host.textContent).toContain("Contacts →");
  expect(host.textContent).not.toContain("across projects");
});

test("errors show with a retry; a project widget without a project asks for one", async () => {
  serve({ error: "boom" }, 500);
  const host = await render({ appName: "crm", projectId: "proj-a" });
  expect(host.textContent).toContain("boom");
  expect(host.textContent).toContain("Retry");
  await act(async () => root?.unmount());
  root = undefined;
  requests = [];
  const empty = await render({ appName: "crm" });
  expect(empty.textContent).toContain("Select a project");
  expect(requests.length).toBe(0);
});
