import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import Triggers from "./Triggers";
const browser = new Window();
Object.assign(globalThis, {
  window: browser,
  document: browser.document,
  HTMLElement: browser.HTMLElement,
  Event: browser.Event,
  IS_REACT_ACT_ENVIRONMENT: true,
});
const { createRoot } = await import("react-dom/client");
const roots: ReturnType<typeof createRoot>[] = [];
afterEach(async () => {
  for (const r of roots.splice(0)) await act(async () => r.unmount());
  document.body.innerHTML = "";
});
test("preview is read only and real test runs require an explicit second action", async () => {
  const calls: { path: string; body: any }[] = [];
  const trigger = {
    id: "t1",
    revision: 1,
    status: "paused",
    config: {
      name: "Welcome",
      source_install_id: 41,
      topic: "customer.signed_up",
      filters: [],
      mappings: {},
    },
    sync_pending: false,
    subscription_enabled: false,
  };
  const api = async (path: string, method = "GET", body?: any) => {
    calls.push({ path, body });
    if (path.endsWith("/preview"))
      return { matched: true, parameters: { customer_id: "customer-1" } };
    if (path.endsWith("/test_run"))
      return { status: "started", run_id: "run-1" };
    if (path === "/trigger-sources")
      return {
        sources: [
          {
            install_id: 41,
            name: "Signup",
            events: [{ name: "customer.signed_up" }],
          },
        ],
      };
    return { triggers: [trigger] };
  };
  const div = document.createElement("div");
  document.body.appendChild(div);
  const root = createRoot(div);
  roots.push(root);
  await act(async () =>
    root.render(
      <Triggers
        assignment={{ id: "a1", status: "active" } as any}
        parameters={[]}
        api={api}
        processStatus="active"
      />,
    ),
  );
  const button = (name: string) =>
    [...div.querySelectorAll("button")].find((b) => b.textContent === name)!;
  const click = async (name: string) => {
    await act(async () => button(name).click());
  };
  await click("Event triggers");
  await click("Test trigger");
  expect(button("Start test run").disabled).toBe(true);
  await click("Preview only");
  expect(calls.filter((c) => c.path.endsWith("/test_run"))).toHaveLength(0);
  expect(button("Start test run").disabled).toBe(false);
  await click("Start test run");
  expect(calls.filter((c) => c.path.endsWith("/test_run"))).toHaveLength(1);
  expect(div.textContent).toContain("run-1");
});
test("paused assignments cannot activate listeners or start sample work", async () => {
  const t = {
    id: "t",
    revision: 1,
    status: "paused",
    config: { name: "Welcome", topic: "signup", source_install_id: 1 },
    sync_pending: false,
  };
  const div = document.createElement("div");
  document.body.appendChild(div);
  const root = createRoot(div);
  roots.push(root);
  await act(async () =>
    root.render(
      <Triggers
        assignment={{ id: "a", status: "paused" } as any}
        parameters={[]}
        processStatus="active"
        api={async () => ({ triggers: [t], sources: [] })}
      />,
    ),
  );
  await act(async () => {
    div.querySelector("button")!.click();
  });
  const activate = [...div.querySelectorAll("button")].find(
    (b) => b.textContent === "Activate",
  )!;
  expect(activate.disabled).toBe(true);
});

test("filter editor saves typed equality values and normalizes changed operators", async () => {
  let saved: any;
  const trigger = {
    id: "typed",
    revision: 1,
    status: "paused",
    config: {
      name: "Paid signup",
      source_install_id: 41,
      topic: "customer.signed_up",
      mappings: {},
      filters: [
        { path: "data.total", op: "eq", value: "42" },
        { path: "data.paid", op: "eq", value: "true" },
        { path: "data.name", op: "eq", value: false },
      ],
    },
  };
  const div = document.createElement("div");
  document.body.appendChild(div);
  const root = createRoot(div);
  roots.push(root);
  await act(async () =>
    root.render(
      <Triggers
        assignment={{ id: "a", status: "active" } as any}
        parameters={[]}
        processStatus="active"
        api={async (path, method, body) => {
          if (method === "PUT") saved = body;
          return {
            triggers: [trigger],
            sources: [{ install_id: 41, name: "Signups", events: [] }],
          };
        }}
      />,
    ),
  );
  const click = async (text: string) =>
    act(async () => {
      [...div.querySelectorAll("button")]
        .find((b) => b.textContent === text)!
        .click();
    });
  const select = async (label: string, index: number, value: string) =>
    act(async () => {
      const field = [...div.querySelectorAll("label")]
        .filter((l) => l.firstChild?.textContent?.trim() === label)
        [index].querySelector("select")!;
      field.value = value;
      field.dispatchEvent(new Event("change", { bubbles: true }));
    });
  await click("Event triggers");
  await click("Edit");
  await select("Value type", 0, "number");
  await select("Value type", 1, "boolean");
  await select("Condition", 2, "contains");
  await act(async () =>
    div
      .querySelector("form")!
      .dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
  );
  expect(saved.trigger.filters).toEqual([
    { path: "data.total", op: "eq", value: 42 },
    { path: "data.paid", op: "eq", value: true },
    { path: "data.name", op: "contains", value: "false" },
  ]);
});
