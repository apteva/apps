import { afterEach, beforeEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import type { Root } from "react-dom/client";
import { fixture, tasks, connections } from "./fixtures";
const initialBrowser = new Window();
Object.assign(globalThis, {
  window: initialBrowser,
  document: initialBrowser.document,
});
const { createRoot } = await import("react-dom/client");
import A2APanel from "./A2APanel";
let root: Root, container: HTMLElement, browser: Window;
const originalFetch = globalThis.fetch;
const json = (data: unknown) =>
  new Response(JSON.stringify(data), {
    headers: { "Content-Type": "application/json" },
  });
const button = (text: string) =>
  [...container.querySelectorAll("button")].find((b) =>
    b.textContent?.includes(text),
  )!;
async function render(project = "p") {
  await act(async () =>
    root.render(<A2APanel projectId={project} appName="a2a" installId={1} />),
  );
}
async function click(text: string) {
  await act(async () => button(text).click());
}
async function setInput(label: string, value: string) {
  const input = [...container.querySelectorAll("input")].find(
    (i) =>
      i.getAttribute("aria-label") === label ||
      i.closest("label")?.textContent?.startsWith(label),
  )!;
  expect(input).toBeDefined();
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      browser.HTMLInputElement.prototype,
      "value",
    )!.set!.call(input, value);
    input.dispatchEvent(new browser.Event("input", { bubbles: true }) as any);
  });
}
beforeEach(() => {
  browser = new Window();
  Object.assign(globalThis, {
    window: browser,
    document: browser.document,
    HTMLElement: browser.HTMLElement,
    Event: browser.Event,
    MouseEvent: browser.MouseEvent,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  (browser as any).__aptevaAppEvents = { subscribe: () => () => {} };
  container = browser.document.createElement("div") as unknown as HTMLElement;
  browser.document.body.appendChild(container as any);
  root = createRoot(container);
  globalThis.fetch = (async (input: any) =>
    json(fixture(String(input)))) as typeof fetch;
});
afterEach(async () => {
  await act(async () => root.unmount());
  globalThis.fetch = originalFetch;
  await browser.happyDOM.close();
});

test("overview, directory, network map and connections navigate without losing state", async () => {
  await render();
  expect(container.textContent).toContain("128 exchanges in total");
  await click("Agents");
  expect(container.textContent).toContain("source-analysis");
  await click("Map");
  expect(container.textContent).toContain("Lines show configured connections");
  await click("Partner workspace");
  expect(container.textContent).toContain("financial-analysis");
  expect(container.textContent).not.toContain("copywriting");
  await click("Connections");
  expect(container.textContent).toContain("Bearer token configured");
  await click("Overview");
  expect(container.textContent).toContain("Recent exchanges");
});

test("an older message request cannot overwrite the newly selected exchange", async () => {
  let resolveOld!: (response: Response) => void;
  globalThis.fetch = (async (input: any) =>
    String(input).includes("/tasks/128/messages")
      ? new Promise<Response>((resolve) => {
          resolveOld = resolve;
        })
      : json(fixture(String(input)))) as typeof fetch;
  await render();
  await click("Exchanges");
  await click("OperationsResearch");
  await click("ResearchStrategy");
  expect(container.textContent).toContain("maximum budget");
  await act(async () =>
    resolveOld(
      json({
        task: tasks[0],
        messages: [
          {
            id: 1,
            from_agent_id: 41,
            body: "STALE MESSAGE",
            created_at: tasks[0].created_at,
          },
        ],
      }),
    ),
  );
  expect(container.textContent).not.toContain("STALE MESSAGE");
});

test("message failures are actionable and are not rendered as empty history", async () => {
  globalThis.fetch = (async (input: any) =>
    String(input).includes("/messages")
      ? new Response("temporarily unavailable", { status: 503 })
      : json(fixture(String(input)))) as typeof fetch;
  await render();
  await click("Exchanges");
  await click("OperationsResearch");
  expect(container.textContent).toContain("Could not load this exchange");
  expect(container.textContent).not.toContain("No messages recorded");
  expect(button("Retry")).toBeDefined();
});

test("node setup keeps inbound grants empty and does not submit until review", async () => {
  let submitted: any;
  globalThis.fetch = (async (input: any, options: any) => {
    if (options?.method === "POST") {
      submitted = JSON.parse(options.body);
      return json({ connection: connections[0] });
    }
    return json(fixture(String(input)));
  }) as typeof fetch;
  await render();
  await click("Connect");
  await click("Apteva installation");
  await click("Continue");
  expect(container.textContent).toContain("Empty grants deny access");
  await setInput("Connection ID", "new-node");
  await setInput("A2A base URL", "https://node.example/api/apps/a2a");
  await setInput("Pairing token", "secret");
  expect(submitted).toBeUndefined();
  await click("Continue");
  expect(container.textContent).toContain("No inbound access");
  await click("Save connection");
  expect(submitted.discover_agents).toEqual([]);
  expect(submitted.invoke_agents).toEqual([]);
});

test("artifacts and local thread links are available; remote agent IDs are not linked", async () => {
  await render();
  await click("Exchanges");
  await click("OperationsWeather intelligence");
  expect(container.textContent).toContain("Event weather brief");
  expect(
    container.querySelector('a[href="https://weather.example/forecast.pdf"]'),
  ).not.toBeNull();
  expect(container.querySelector('a[href^="/agents/0"]')).toBeNull();
  await click("OperationsResearch");
  expect(
    container.querySelector('a[href="/agents/42?thread=launch-plan"]'),
  ).not.toBeNull();
});

test("project switch resets selected exchange and hides the previous project's result", async () => {
  await render();
  await click("Exchanges");
  await click("OperationsResearch");
  await render("other");
  expect(
    container.querySelector('[role="tab"][aria-selected="true"]')?.textContent,
  ).toBe("Overview");
  expect(container.textContent).not.toContain("I’m reviewing delivery records");
});

test("connection checks report only observed discovery and preserve failures", async () => {
  await render();
  await click("Connections");
  expect(container.textContent).toContain("Not checked");
  await click("Check connection");
  expect(container.textContent).toContain("Discovery verified");
  expect(container.textContent).toContain("Sending a task has not been tested");
  globalThis.fetch = (async (input: any) =>
    String(input).includes("/check")
      ? json({
          ok: false,
          message: "Discovery check failed.",
          checked_at: new Date().toISOString(),
          latency_ms: 20,
        })
      : json(fixture(String(input)))) as typeof fetch;
  await click("Check connection");
  expect(container.textContent).toContain("Check failed");
  expect(container.textContent).not.toContain("Discovery verified");
});

test("capability search narrows the directory and agent detail opens its exchanges", async () => {
  await render();
  await click("Agents");
  await setInput("Search agents", "forecasting");
  expect(container.querySelectorAll(".agent-card").length).toBe(2);
  await click("Finance");
  expect(container.querySelector('[role="dialog"]')).not.toBeNull();
  await click("View exchanges");
  expect(
    container
      .querySelector('[aria-label="Exchange connection"]')
      ?.getAttribute("value") ||
      (
        container.querySelector(
          '[aria-label="Exchange connection"]',
        ) as HTMLSelectElement
      ).value,
  ).toBe("partner");
  expect(container.querySelector('[role="dialog"]')).toBeNull();
});
