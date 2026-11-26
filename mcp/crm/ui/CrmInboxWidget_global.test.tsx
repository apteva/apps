import { afterAll, afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import CrmInboxWidget from "./CrmInboxWidget";
import { crmInboxAPIURL, crmInboxHeaderLink, crmInboxPageLink, crmInboxPreferences, crmInboxProjectColor, crmPanelInitialRoute, type InboxItem } from "./inbox";

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

const row = (id: number, projectID?: string, projectName?: string): InboxItem => ({
  id, contact_id: id, contact_name: `Person ${id}`, channel: "email",
  subject: `Subject ${id}`, status: "open", priority: "normal",
  last_activity_at: "2026-11-26T10:00:00Z", snippet: `Subject ${id}\n\nBody ${id}`,
  ...(projectID ? { project_id: projectID, project_name: projectName } : {}),
});

function serve(body: unknown) {
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    requests.push(new URL(String(input), "http://localhost"));
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
}

async function render(props: Parameters<typeof CrmInboxWidget>[0]) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host as unknown as Element);
  await act(async () => root!.render(<CrmInboxWidget {...props} />));
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  return host;
}

test("global scope requests scope=global and never sends the empty project", () => {
  const preferences = crmInboxPreferences({});
  const all = new URL(crmInboxAPIURL({ installId: 5, dashboardScope: "global" }, preferences), "http://x");
  expect(all.searchParams.get("scope")).toBe("global");
  expect(all.searchParams.has("project_id")).toBe(false);
  const one = new URL(crmInboxAPIURL({ installId: 5, dashboardScope: "global" }, preferences, "proj-b"), "http://x");
  expect(one.searchParams.get("project_id")).toBe("proj-b");
  const project = new URL(crmInboxAPIURL({ installId: 5, projectId: "proj-a", dashboardScope: "project" }, preferences, "proj-b"), "http://x");
  expect(project.searchParams.has("scope")).toBe(false);
  expect(project.searchParams.get("project_id")).toBe("proj-a");
});

test("links use the row's own project; the header link needs a picked project", () => {
  const props = { installId: 5, dashboardScope: "global" as const };
  const route = new URLSearchParams(crmInboxPageLink(props, row(9, "proj-b", "Beta")).split("?")[1]);
  expect(route.get("project_id")).toBe("proj-b");
  expect(crmPanelInitialRoute(route.toString()).conversationId).toBe(9);
  expect(crmInboxHeaderLink(props)).toBeNull();
  expect(crmInboxHeaderLink(props, "proj-a")).toContain("project_id=proj-a");
  expect(crmInboxHeaderLink({ installId: 5, projectId: "proj-a" })).toContain("project_id=proj-a");
});

test("renders rows from every project with a project filter in global scope", async () => {
  serve({
    inbox: [row(1, "proj-a", "Alpha"), row(2, "proj-b", "Beta")], total: 2,
    projects: [{ id: "proj-a", name: "Alpha" }, { id: "proj-b", name: "Beta" }],
  });
  const host = await render({ appName: "crm", installId: 5, dashboardScope: "global" });
  expect(requests[0].searchParams.get("scope")).toBe("global");
  expect(requests[0].searchParams.has("project_id")).toBe(false);
  expect(host.textContent).not.toContain("Select a project");
  expect(host.textContent).toContain("Alpha");
  expect(host.textContent).toContain("Beta");
  const links = [...host.querySelectorAll("a")].map(a => a.getAttribute("href") || "");
  expect(links.some(href => href.includes("conversation_id=1") && href.includes("project_id=proj-a"))).toBe(true);
  expect(links.some(href => href.includes("conversation_id=2") && href.includes("project_id=proj-b"))).toBe(true);
  expect(host.textContent).not.toContain("View inbox");

  const select = host.querySelector("select") as unknown as HTMLSelectElement;
  expect(select).not.toBeNull();
  expect([...select.options].map(o => o.textContent)).toEqual(["All projects", "Alpha", "Beta"]);
  await act(async () => {
    select.value = "proj-b";
    select.dispatchEvent(new browser.Event("change", { bubbles: true }) as unknown as Event);
  });
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
  const last = requests[requests.length - 1];
  expect(last.searchParams.get("scope")).toBe("global");
  expect(last.searchParams.get("project_id")).toBe("proj-b");
  expect(host.textContent).toContain("View inbox");
});

test("project scope is unchanged", async () => {
  serve({ inbox: [row(3)], total: 1 });
  const host = await render({ appName: "crm", installId: 5, projectId: "proj-a", dashboardScope: "project" });
  expect(requests[0].searchParams.has("scope")).toBe(false);
  expect(requests[0].searchParams.get("project_id")).toBe("proj-a");
  expect(host.querySelector("select")).toBeNull();
  expect(host.textContent).toContain("View inbox");
  expect(host.textContent).toContain("Subject 3 · Body 3");
  const rowLink = [...host.querySelectorAll("a")].find(a => (a.getAttribute("href") || "").includes("conversation_id=3"));
  expect(rowLink?.getAttribute("href")).toContain("project_id=proj-a");
});

test("a project widget without a project still asks for one", async () => {
  serve({ inbox: [], total: 0 });
  const host = await render({ appName: "crm", installId: 5 });
  expect(host.textContent).toContain("Select a project");
  expect(requests.length).toBe(0);
});

test("project colour comes from widgetContext and falls back to neutral", () => {
  const projects = [{ id: "proj-a", name: "Alpha", color: "#10B981" }, { id: "proj-b", name: "Beta", color: "" }, { id: "proj-c", name: "Gamma", color: "red; background:url(x)" }];
  const props = { dashboardScope: "global" as const, widgetContext: { projects } };
  expect(crmInboxProjectColor(props, "proj-a")).toBe("#10B981");
  expect(crmInboxProjectColor(props, "proj-b")).toBe("");
  expect(crmInboxProjectColor(props, "proj-c")).toBe("");
  expect(crmInboxProjectColor(props, "proj-x")).toBe("");
  expect(crmInboxProjectColor(props, undefined)).toBe("");
  expect(crmInboxProjectColor({ dashboardScope: "global" }, "proj-a")).toBe("");
  expect(crmInboxProjectColor({ dashboardScope: "global", widgetContext: {} }, "proj-a")).toBe("");
});

function projectBadges(host: Element) {
  return [...host.querySelectorAll("span[title]")]
    .filter(el => el.textContent === "Alpha" || el.textContent === "Beta")
    .map(el => ({ name: el.textContent, color: el.getAttribute("data-project-color"), dot: (el.querySelector("span[aria-hidden]") as HTMLElement | null)?.style.backgroundColor || "" }));
}

test("rows show a colour dot next to the project name when the project has a colour", async () => {
  serve({ inbox: [row(1, "proj-a", "Alpha"), row(2, "proj-b", "Beta")], total: 2, projects: [{ id: "proj-a", name: "Alpha" }, { id: "proj-b", name: "Beta" }] });
  const host = await render({
    appName: "crm", installId: 5, dashboardScope: "global",
    widgetContext: { projects: [{ id: "proj-a", name: "Alpha", color: "#10b981" }, { id: "proj-b", name: "Beta", color: "" }] },
  });
  const badges = projectBadges(host);
  expect(badges.find(b => b.name === "Alpha")?.color).toBe("#10b981");
  expect(badges.find(b => b.name === "Alpha")?.dot).not.toBe("");
  expect(badges.find(b => b.name === "Beta")).toEqual({ name: "Beta", color: null, dot: "" });
});

test("rows keep the neutral badge when the dashboard sends no projects", async () => {
  serve({ inbox: [row(1, "proj-a", "Alpha")], total: 1, projects: [{ id: "proj-a", name: "Alpha" }] });
  const host = await render({ appName: "crm", installId: 5, dashboardScope: "global" });
  expect(projectBadges(host)).toEqual([{ name: "Alpha", color: null, dot: "" }]);
});
