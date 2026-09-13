import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import WorkPanel, { TaskComposer, TaskDetail } from "./Work";
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
async function render(el: any) {
  const div = document.createElement("div");
  document.body.append(div);
  const root = createRoot(div);
  roots.push(root);
  await act(async () => root.render(el));
  return div;
}
async function click(div: HTMLElement, text: string) {
  await act(async () => {
    const b = [...div.querySelectorAll("button")].find(
      (b) => b.textContent === text,
    );
    if (!b) throw new Error(`Missing ${text}`);
    b.click();
  });
}
const task = (patch: any = {}) => ({
  id: "task-1",
  project_id: "p",
  origin: "standalone",
  run_id: "",
  state: "waiting",
  revision: 3,
  executor: { kind: "human" },
  definition: {
    name: "Verify payment",
    instructions: "Check the receipt",
    expected_output: "Receipt reference",
    kind: "work",
    depends_on: [],
  },
  due_at: "",
  ...patch,
});
test("Work combines standalone and procedure tasks and filters by assignee", async () => {
  const paths: string[] = [];
  const div = await render(
    <WorkPanel
      api={async (path) => {
        paths.push(path);
        return {
          tasks: [
            task(),
            task({
              id: "step-1",
              origin: "process_step",
              run_id: "run-1",
              required: true,
              definition: { ...task().definition, name: "Draft post" },
            }),
          ],
          total: 2,
        };
      }}
      agents={[{ id: 7, name: "Writer" }]}
    />,
  );
  expect(div.textContent).toContain("Verify payment");
  expect(div.textContent).toContain("Draft post");
  expect(div.textContent).toContain("From process");
  const select = div.querySelector(
    'select[aria-label="Filter task assignee"]',
  )! as HTMLSelectElement;
  await act(async () => {
    select.value = "7";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  expect(paths.at(-1)).toContain("assignee=7");
  expect(paths.at(-1)).toContain("state=active");
});
test("task creation retries preserve the key and explicit run association", async () => {
  const bodies: any[] = [];
  const div = await render(
    <TaskComposer
      runID="run-1"
      agents={[]}
      api={async (_path, _method, body) => {
        bodies.push(body);
        if (bodies.length === 1) throw new Error("response lost");
        return { task: task() };
      }}
      onCreated={async () => {}}
      onClose={() => {}}
    />,
  );
  const form = div.querySelector("form")!;
  await act(async () =>
    form.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    ),
  );
  expect(div.textContent).toContain("response lost");
  await act(async () =>
    form.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    ),
  );
  expect(bodies).toHaveLength(2);
  expect(bodies[0].idempotency_key).toBe(bodies[1].idempotency_key);
  expect(bodies[0].run_id).toBe("run-1");
  expect(bodies[0].required).toBe(false);
  expect(bodies[0].executor).toEqual({ kind: "human" });
});
test("optional task remains actionable after a completed run and writes its revision", async () => {
  const writes: any[] = [];
  const detail = {
    task: task({
      origin: "attached",
      run_id: "run-1",
      required: false,
      output: "Receipt verified",
    }),
    run: { state: "completed" },
    can_update: true,
    can_manage: true,
    can_edit: true,
    can_reassign: false,
    history: [],
  };
  const div = await render(
    <TaskDetail
      id="task-1"
      agents={[]}
      api={async (_path, method, body) => {
        if (method === "PUT") writes.push(body);
        return detail;
      }}
      onClose={() => {}}
      onChanged={async () => {}}
    />,
  );
  await click(div, "Complete task");
  expect(writes[0]).toEqual({
    state: "completed",
    output: "Receipt verified",
    expected_revision: 3,
  });
  expect(
    (div.querySelector('[aria-label="Task assignee"]') as HTMLSelectElement)
      .disabled,
  ).toBe(true);
});
test("agent work cannot be completed from the operator panel", async () => {
  const div = await render(
    <TaskDetail
      id="task-1"
      agents={[{ id: 7, name: "Writer" }]}
      api={async () => ({
        task: task({ executor: { kind: "agent", agent_id: 7 } }),
        can_update: false,
        can_manage: true,
        can_edit: true,
        can_reassign: false,
        history: [],
      })}
      onClose={() => {}}
      onChanged={async () => {}}
    />,
  );
  expect(
    [...div.querySelectorAll("button")].some(
      (b) => b.textContent === "Complete task",
    ),
  ).toBe(false);
});
