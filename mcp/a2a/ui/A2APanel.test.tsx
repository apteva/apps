import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
const initialBrowser = new Window();
Object.assign(globalThis, { window: initialBrowser, document: initialBrowser.document });
const { createRoot } = await import("react-dom/client");
import A2APanel from "./A2APanel";

let root: Root;
let container: HTMLElement;
let browser: Window;
const originalFetch = globalThis.fetch;
const tasks = [1, 2].map(id => ({ id, kind: "ask", status: "working", from_agent_id: 41,
  from_agent_name: `Requester ${id}`, to_agent_id: 42, to_agent_name: "Responder",
  created_at: "2026-09-07T00:00:00Z", updated_at: "2026-09-07T00:00:00Z" }));
const json = (data: unknown) => new Response(JSON.stringify(data), { headers: { "Content-Type": "application/json" } });
const messages = (body: string) => ({ messages: [{ id: 1, task_id: 1, from_agent_id: 41, body, created_at: "2026-09-07T00:00:00Z" }] });
const button = (text: string) => [...container.querySelectorAll("button")].find(b => b.textContent?.includes(text))!;
async function render() {
  await act(async () => { root.render(<A2APanel projectId="p" appName="a2a" installId={1} />); });
}
async function click(text: string) {
  await act(async () => { button(text).click(); });
}

beforeEach(() => {
  browser = new Window();
  Object.assign(globalThis, { window: browser, document: browser.document,
    HTMLElement: browser.HTMLElement, Event: browser.Event, MouseEvent: browser.MouseEvent,
    IS_REACT_ACT_ENVIRONMENT: true });
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  container = browser.document.createElement("div") as unknown as HTMLElement;
  browser.document.body.appendChild(container as any);
  root = createRoot(container);
  globalThis.fetch = (async (input: any) => {
    const url = String(input);
    if (url.includes("/connections")) return json({ connections: [] });
    if (url.includes("/messages")) return json(messages("Initial message"));
    return json({ tasks });
  }) as typeof fetch;
});

afterEach(async () => {
  await act(async () => root.unmount());
  globalThis.fetch = originalFetch;
  await browser.happyDOM.close();
});

test("Tasks and Connections can be switched without a hook-order crash", async () => {
  await render();
  await click("Connections");
  expect(container.textContent).toContain("Add connection");
  await click("Tasks");
  expect(container.textContent).toContain("Requester 1");
  await click("Connections");
  expect(container.textContent).toContain("Add connection");
});

test("an older message request cannot overwrite the newly selected task", async () => {
  let resolveOld!: (response: Response) => void;
  globalThis.fetch = (async (input: any) => {
    const url = String(input);
    if (url.includes("/tasks/1/messages")) return new Promise<Response>(resolve => { resolveOld = resolve; });
    if (url.includes("/tasks/2/messages")) return json(messages("Second task answer"));
    return json({ tasks });
  }) as typeof fetch;
  await render();
  await click("Requester 2");
  expect(container.textContent).toContain("Second task answer");
  await act(async () => resolveOld(json(messages("Stale first task answer"))));
  expect(container.textContent).toContain("Second task answer");
  expect(container.textContent).not.toContain("Stale first task answer");
});

test("node connections default to no inbound grants and explain wildcard scope", async () => {
  let submitted: any;
  globalThis.fetch = (async (_input: any, options: any) => {
    if (options?.method === "POST") { submitted = JSON.parse(options.body); return json({ connection: { name: "Node" } }); }
    return json({ tasks, connections: [] });
  }) as typeof fetch;
  await render(); await click("Connections");
  const select = container.querySelector("select")!;
  await act(async () => { select.value = "node"; select.dispatchEvent(new browser.Event("change", { bubbles: true }) as any); });
  expect(container.textContent).toContain("Empty grants deny access");
  // Set inputs via React's native change path in the DOM implementation.
  const setInput = async (placeholder: string, value: string) => {
    const input = [...container.querySelectorAll("input")].find(i => i.placeholder === placeholder)!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(browser.HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, value);
      input.dispatchEvent(new browser.Event("input", { bubbles: true }) as any);
      input.dispatchEvent(new browser.Event("change", { bubbles: true }) as any);
    });
  };
  await setInput("Node id", "node");
  await setInput("https://node.example/api/apps/a2a", "https://node.example/api/apps/a2a");
  await setInput("Pairing token (required)", "secret");
  await click("Validate and add");
  expect(submitted).toBeDefined();
  expect(submitted.discover_agents).toEqual([]);
  expect(submitted.invoke_agents).toEqual([]);
});
