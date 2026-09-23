import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import ExecutionTools, {
  callsForExecution,
  resolveToolSource,
  type ToolSource,
} from "./ExecutionTools";

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

const events = [
  {
    id: "result-two",
    type: "tool.result",
    time: "2026-09-23T10:00:04Z",
    data: { id: "two", execution_ids: ["step-a"], is_error: true },
  },
  {
    id: "call-other",
    type: "tool.call",
    time: "2026-09-23T10:00:02Z",
    data: {
      id: "other",
      name: "browser_click",
      reason: "Wrong step",
      execution_ids: ["step-b"],
    },
  },
  {
    id: "call-two",
    type: "tool.call",
    time: "2026-09-23T10:00:03Z",
    data: {
      id: "two",
      name: "slack_send",
      reason: "Notify the reviewer",
      execution_ids: ["step-a"],
    },
  },
  {
    id: "call-one",
    type: "tool.call",
    time: "2026-09-23T10:00:01Z",
    data: {
      id: "one",
      name: "crm_contact_get",
      reason: "Load the customer record",
      execution_ids: ["step-a"],
    },
  },
];

test("tool calls are isolated to one step execution and ordered", () => {
  const calls = callsForExecution(events, "step-a");
  expect(calls.map((call) => call.id)).toEqual(["one", "two"]);
  expect(calls.map((call) => call.reason)).toEqual([
    "Load the customer record",
    "Notify the reviewer",
  ]);
  expect(calls[1].failed).toBe(true);
});

test("tool sources prefer exact app tools and resolve integration prefixes", () => {
  const sources: ToolSource[] = [
    {
      key: "integration:crm",
      label: "CRM integration",
      icon: "/crm.svg",
      aliases: ["crm"],
    },
    {
      key: "app:records",
      label: "Records app",
      icon: "/records.svg",
      aliases: ["records"],
      tools: ["crm_contact_get"],
    },
  ];
  expect(resolveToolSource("crm_contact_get", sources)?.label).toBe("Records app");
  expect(resolveToolSource("crm_contact_update", sources)?.label).toBe(
    "CRM integration",
  );
});

test("expanded tool activity shows provider icon and reason", async () => {
  let requested = "";
  globalThis.fetch = (async (url: unknown) => {
    requested = String(url);
    return Response.json(events);
  }) as typeof fetch;
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root.render(
      <ExecutionTools
        agentID={7}
        threadID="worker-thread"
        executionID="step-a"
        sources={[
          {
            key: "app:crm",
            label: "CRM",
            icon: "/crm.svg",
            aliases: ["crm"],
          },
        ]}
      />,
    ),
  );
  const details = container.querySelector("details")!;
  await act(async () => {
    details.open = true;
    details.dispatchEvent(new window.Event("toggle") as unknown as Event);
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(requested).toContain("agent_id=7");
  expect(requested).toContain("thread_id=worker-thread");
  expect(requested).toContain("type=tool");
  expect(container.textContent).toContain("Load the customer record");
  expect(container.querySelector<HTMLImageElement>('img[src="/crm.svg"]')).toBeTruthy();
});
