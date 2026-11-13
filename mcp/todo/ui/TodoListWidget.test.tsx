import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
import Widget from "./TodoListWidget";
import TodoPanel from "./TodoPanel";
import { todoPanelSelection, widgetAPIURL, widgetPageLink, widgetPreferences, type WidgetProps } from "./widget";

const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, { window, document: window.document, HTMLElement: window.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });
const { createRoot } = await import("react-dom/client");
let root: Root | undefined;
afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.innerHTML = "";
  window.history.replaceState(null, "", "/");
  globalThis.fetch = originalFetch;
});

const task = { id: 5, title: "Publish website", priority: 1, due_at: "2026-11-20", rrule: "", list_id: 2, list_name: "Website launch" };
const page = { title: "Website launch", total: 15, items: [task], lists: [{ id: 2, name: "Website launch" }] };
type Call = { url: URL; init: RequestInit };
const calls: Call[] = [];
function stub(handler: (call: Call) => Response | Promise<Response>) {
  calls.length = 0;
  globalThis.fetch = (async (url: string, init: RequestInit = {}) => {
    const call = { url: new URL(String(url), "http://localhost"), init };
    calls.push(call);
    return handler(call);
  }) as typeof fetch;
}
const json = (value: unknown) => Response.json(value);
const domEvent = (type: string) => new window.Event(type, { bubbles: true, cancelable: true }) as unknown as Event;
const mount = async (props: WidgetProps = {}) => {
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<Widget appName="todo" projectId="project-a" installId={9} widgetSettings={{ target: "list:2" }} {...props} />);
  });
  return host;
};

test("host scope and list/group deep links survive navigation", () => {
  const props = { appName: "todo", projectId: "client/a b", installId: 9 };
  const url = new URL(widgetAPIURL(props, "/widget", { project_id: "wrong", install_id: "3" }), "http://localhost");
  expect(url.searchParams.get("project_id")).toBe("client/a b");
  expect(url.searchParams.get("install_id")).toBe("9");
  const link = new URL(widgetPageLink(props, "group:12"), "http://localhost");
  expect(link.pathname).toBe("/apps/todo/page");
  expect(link.searchParams.get("install_id")).toBe("9");
  expect(todoPanelSelection(link.search)).toEqual({ listId: null, groupId: 12 });
  expect(todoPanelSelection("?list_id=2&group_id=12")).toEqual({ listId: 2, groupId: null });
  expect(todoPanelSelection("?list_id=-1&group_id=nope")).toEqual({ listId: null, groupId: null });
  expect(widgetPreferences({ target: "all", sort: "random", max_todos: 999 })).toEqual({ target: "", sort: "due", limit: 20 });
});

test("shows the selection name, full remaining count and correctly scoped actions", async () => {
  stub(call => json(call.init.method === "POST" ? { status: "done" } : page));
  const host = await mount({ widgetSettings: { target: "list:2", sort: "priority", max_todos: 6 } });
  expect(host.textContent).toContain("Website launch");
  expect(host.textContent).toContain("15 remaining");
  expect(host.textContent).toContain("Showing 1 of 15");
  expect(calls[0].url.searchParams.get("sort")).toBe("priority");
  expect(calls[0].url.searchParams.get("limit")).toBe("6");
  await act(async () => (host.querySelector('[aria-label="Complete Publish website"]') as HTMLButtonElement).click());
  const post = calls.find(call => call.init.method === "POST")!;
  expect(post.url.pathname).toBe("/api/apps/todo/todos/5/complete");
  expect(post.url.searchParams.get("install_id")).toBe("9");
  expect(post.url.searchParams.get("project_id")).toBe("project-a");
  expect(host.textContent).toContain("Todo completed.");
  expect(calls.filter(call => !call.init.method).length).toBe(2);
});

test("group quick-add requires choosing a member list and creates there", async () => {
  stub(call => json(call.init.method === "POST" ? { status: "open" } : {
    ...page, title: "Personal", lists: [...page.lists, { id: 7, name: "Home" }],
  }));
  const host = await mount({ widgetSettings: { target: "group:3" } });
  const input = host.querySelector("input") as HTMLInputElement;
  expect(input.disabled).toBe(true);
  expect(host.textContent).toContain("Website launch");
  await act(async () => {
    const select = host.querySelector("select") as HTMLSelectElement;
    select.value = "7";
    select.dispatchEvent(domEvent("change"));
  });
  expect(input.disabled).toBe(false);
  // React's controlled input tracks value assignments; set through the native
  // setter to model an actual edit instead of changing its value tracker.
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!;
    setter.call(input, "Book plumber");
    input.dispatchEvent(domEvent("input"));
    input.dispatchEvent(domEvent("change"));
  });
  await act(async () => { host.querySelector("form")!.dispatchEvent(domEvent("submit")); });
  const post = calls.find(call => call.init.method === "POST");
  expect(post).toBeDefined();
  expect(post!.url.pathname).toBe("/api/apps/todo/todos");
  expect(JSON.parse(post!.init.body as string)).toEqual({ title: "Book plumber", list_id: 7, source: "human" });
  expect(input.value).toBe("");
});

test("failed completion retains work; recurring completion refreshes instead of removing it", async () => {
  let fail = true;
  stub(call => call.init.method === "POST"
    ? fail ? new Response("Unable to complete", { status: 500 }) : json({ status: "open" })
    : json({ ...page, items: [{ ...task, rrule: "FREQ=DAILY" }] }));
  const host = await mount();
  const complete = () => (host.querySelector('[aria-label="Complete Publish website"]') as HTMLButtonElement).click();
  await act(async () => complete());
  expect(host.querySelector('[role="alert"]')?.textContent).toBe("Unable to complete");
  expect(host.textContent).toContain("Publish website");
  fail = false;
  await act(async () => complete());
  expect(host.textContent).toContain("Next occurrence scheduled.");
  expect(host.textContent).toContain("Publish website");
});

test("missing settings and project do not fetch unscoped todos", async () => {
  stub(() => json(page));
  const host = await mount({ projectId: undefined });
  expect(host.textContent).toContain("Select a project");
  expect(calls.length).toBe(0);
  await act(async () => root!.render(<Widget projectId="a" />));
  expect(host.textContent).toContain("Configure this widget");
  expect(calls.length).toBe(0);
});

test("unavailable selection shows retry and hides actions instead of broadening scope", async () => {
  stub(() => new Response("This list is unavailable", { status: 404 }));
  const host = await mount();
  expect(host.querySelector('[role="alert"]')?.textContent).toContain("This list is unavailable");
  expect(host.querySelector("input")).toBeNull();
  stub(() => json(page));
  await act(async () => (host.querySelector("button") as HTMLButtonElement).click());
  expect(host.textContent).toContain("15 remaining");
});

test("multiple copies keep their own selection and settings", async () => {
  stub(call => json({ ...page, title: call.url.searchParams.get("target") }));
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<><Widget projectId="a" installId={1} widgetSettings={{ target: "list:2" }} />
      <Widget projectId="a" installId={1} widgetSettings={{ target: "group:3", sort: "priority", max_todos: 3 }} /></>);
  });
  expect([...host.querySelectorAll("h2")].map(heading => heading.textContent)).toEqual(["list:2", "group:3"]);
  expect(calls.map(call => call.url.searchParams.get("target"))).toEqual(["list:2", "group:3"]);
  expect(calls[1].url.searchParams.get("limit")).toBe("3");
});

test("late responses cannot replace a new selection; host events refresh the current one", async () => {
  let resolveOld!: (value: Response) => void;
  stub(call => call.url.searchParams.get("target") === "list:2"
    ? new Promise(resolve => { resolveOld = resolve; }) : json({ ...page, title: "New area" }));
  const host = await mount();
  await act(async () => root!.render(<Widget projectId="b" installId={10} widgetSettings={{ target: "list:7" }} />));
  await act(async () => resolveOld(json(page)));
  expect(host.querySelector("h2")?.textContent).toBe("New area");
  const before = calls.length;
  await act(async () => root!.render(<Widget projectId="b" installId={10} eventRevision={1} widgetSettings={{ target: "list:7" }} />));
  expect(calls.length).toBe(before + 1);
  expect(calls.at(-1)!.url.searchParams.get("project_id")).toBe("b");
});

test("opening the full Todo page honors the widget list and installation", async () => {
  const url = widgetPageLink({ projectId: "project-a", installId: 9 }, "list:2");
  window.history.replaceState(null, "", url);
  Object.assign(window, { __aptevaAppEvents: { subscribe: () => () => {} } });
  stub(call => {
    switch (call.url.pathname) {
      case "/api/apps/todo/lists": return json([{ ...page.lists[0], archived: false, group_id: null, color: "" }]);
      case "/api/apps/todo/list_groups":
      case "/api/apps/todo/tags": return json([]);
      case "/api/apps/todo/summary": return json({ overdue: 0, today: 1, future: 0 });
      default: return json([{ ...task, notes: "", tags: [], status: "open", source: "human", created_at: "2026-11-13T10:00:00Z" }]);
    }
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<TodoPanel appName="todo" projectId="project-a" installId={9} />);
  });
  expect(host.textContent).toContain("Publish website");
  expect(calls.some(call => call.url.pathname === "/api/apps/todo/todos"
    && call.url.searchParams.get("view") === "all"
    && call.url.searchParams.get("list_id") === "2"
    && call.url.searchParams.get("install_id") === "9")).toBe(true);
});
